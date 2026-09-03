package main

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
)

// logRotateAtBytes is how much $HOME/.baton/baton.log may hold before the line
// that crosses it rotates the file. One rotation is kept, so the pair is bounded
// by twice this and by nothing else — in particular not by how long the
// installation has been running, which is what #64 was filed on.
//
// It is deliberately an eighth of panellog.DefaultMaxMB, and the two numbers
// answer different questions. A PANEL LOG carries a child process's raw output:
// a runaway build can produce gigabytes in minutes, so that cap exists to stop a
// third party taking the machine (docs/LIMITS.md), and 64 MiB is the room a
// transcript needs to still be worth reading afterwards. THIS file carries only
// lines baton's own code writes, at a rate baton's own code sets, so its cap is
// not a containment question but a retention one: how much history an operator
// sent here by didNotComeUpReason should still find. And there is one of this
// file per installation against one transcript per panel, so the whole
// two-generation budget of the daemon's log is set below what a single panel is
// allowed to spend — see TestLogRotateAtBytesIsBoundedBothWays, which pins both
// ends of that against measured bytes rather than against this prose.
//
// It is a CONSTANT where the transcript's cap is a config key
// (config.Panel.LogMaxMB, "log-max-mb"), and that difference is a decision
// rather than an omission. setupLogger runs in main before any config.Load does
// — a config that will not load is reported INTO this file — so a cap taken from
// there would not exist yet at the moment it is wanted, on any of the three
// paths that read one. Reusing panel.log-max-mb would be worse than a new
// key: same word, different file, different writer, different failure. And
// nothing an operator configures changes what the daemon writes about itself,
// which is the thing log-max-mb exists to track. A sibling key stays available
// the day something asks for one.
const logRotateAtBytes = 8 << 20

// The rotation's two derived paths. The rotated generation is the log's own name
// plus ".1", as panellog's roll already spells it; the lock is what makes two
// processes rotating at the same instant produce one rotation. Like
// paths.LockFile, the lock file is never read and never removed — a lock whose
// file can be unlinked lets the next process lock a fresh inode and believe it
// won.
const (
	rotatedLogSuffix = ".1"
	rotateLockSuffix = ".lock"
)

// logRotator is the writer under the daemon's (and every CLI's) zerolog: it
// appends to the log file and rotates it when it has grown past a cap.
//
// It is a SECOND roller in this repo, and panellog.Sink is the first. What is
// borrowed is the idea and the counter: Sink.n carries the current generation's
// size so the size check is an integer compare rather than a stat per write, and
// that is exactly the property the daemon's log path needs too. What is NOT
// borrowed is Sink's mechanism, because Sink has one writer and this has many —
// every goroutine in the daemon that logs a line, plus every other `baton`
// process appending to the same file:
//
//   - Sink takes a mutex on every Write. Here the cap check is an atomic add and
//     the mutex below is taken only by the write that crosses the cap.
//   - Sink.rollLocked closes its file, removes the previous generation, renames,
//     and reopens. Two of those steps are unsafe for a file several writers
//     hold: the close would pull the descriptor out from under the goroutines
//     writing through it, and the remove would unlink the inode a concurrently
//     running `baton` is still appending to. Rename alone replaces the
//     destination atomically and leaves every open descriptor pointed at a file
//     that still has a name.
//   - Sink needs no cross-process claim, because two panels never share a path.
//     Every baton process shares this one.
//
// Sharing an implementation would therefore mean giving panellog a lock file per
// transcript and a swap it has no use for, or growing one roller with two modes.
// Both cost more than the fifteen lines they would save, so the two stay
// separate and this comment is the link between them.
//
// Safe for concurrent use.
type logRotator struct {
	path string
	max  int64

	// f is the current generation, replaced by a rotation rather than reopened in
	// place — and the OLD file is deliberately never closed.
	//
	// Closing it is what there is no safe moment for. A writer that has loaded the
	// pointer and not yet reached the write would get "file already closed" and
	// lose its line, and the only ways to exclude that are a lock on every write
	// or a spin waiting for writers to drain. Dropping the pointer instead hands
	// the problem to the runtime, which already solves it: the descriptor behind
	// an *os.File is closed when the File becomes unreachable, and a writer
	// holding it in a local IS a reference, so the close cannot happen under one.
	// The cost is at most one descriptor per rotation, for as long as it takes a
	// GC to notice — against one rotation per cap of bytes, which is days.
	//
	// The obvious alternative — keep one descriptor and dup2 the new file onto it
	// — was measured and is WRONG here. On darwin, dup2 onto a descriptor another
	// thread is writing through fails about one of those writes in a hundred with
	// EBADF: the kernel reserves the slot while it closes what was there. It is
	// still the right tool for stdout and stderr, which have no concurrent writer
	// in the daemon; see mirrorStdio.
	f atomic.Pointer[os.File]

	// n is what this process believes the current generation holds: the size read
	// once when the file was adopted, plus every byte written since. Nothing here
	// stats per line.
	//
	// Other processes append to the same file, so n undercounts. That direction is
	// the safe one — this process rotates a little late rather than a little
	// early — and it does not accumulate, because every rotation re-reads the size
	// from the file it just adopted. The processes doing the undercounted writing
	// are CLI invocations that write a line or two and exit; the daemon, which
	// writes the fleet's events, is counting its own.
	n atomic.Int64

	// mu serialises rotation within this process. It is taken only by the write
	// that crosses the cap, never by an ordinary line.
	mu sync.Mutex
	// mirror holds descriptor numbers that must be re-pointed at each new
	// generation — the daemon's stdout and stderr. See mirrorStdio.
	mirror []int
}

// openLogRotator opens the log in append mode and returns the writer over it,
// rotating first if the file is already past the cap.
//
// The open-time check is what bounds a log that only short-lived processes
// write: a `baton ctl` writes a line or two and exits, so it would never reach
// the cap from the inside. It is not enough on its own — the daemon runs for
// weeks and opens once, which is why Write carries the same check.
func openLogRotator(path string, max int64) (*logRotator, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", path, err)
	}
	r := &logRotator{path: path, max: max}
	r.f.Store(f)
	r.n.Store(sizeOf(f))
	if r.n.Load() >= r.max {
		r.roll()
	}
	return r, nil
}

// Write appends p to the log and rotates when the file has grown past the cap.
func (r *logRotator) Write(p []byte) (int, error) {
	n, err := r.f.Load().Write(p)
	if n > 0 && r.n.Add(int64(n)) >= r.max {
		r.roll()
	}
	return n, err
}

// mirrorStdio points this process's stdout and stderr at the log as well, and
// records them so every later rotation re-points them too.
//
// It is the daemon's, and only the daemon's. startDaemon hands the forked child
// the log file as its std streams so a panic lands somewhere readable; that
// descriptor is inherited and never reopened, so after the SECOND rotation the
// inode behind it has been replaced and the child's panic would go to a file
// with no name. Re-pointing them at each generation closes that.
//
// These two are moved with dup2 where the logger's own descriptor is not,
// because the hazard that rules dup2 out there does not exist here: nothing in
// baton writes to fd 1 or 2 in the daemon. What does is the Go runtime printing
// a panic, and a panic that lands in the microseconds a rotation holds is a
// panic whose text is lost — which is strictly better than every panic after the
// second rotation being lost, and is the whole of what this trades.
func (r *logRotator) mirrorStdio() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, fd := range []int{syscall.Stdout, syscall.Stderr} {
		if err := swapFD(r.f.Load(), fd); err != nil {
			return fmt.Errorf("point fd %d at %s: %w", fd, r.path, err)
		}
		r.mirror = append(r.mirror, fd)
	}
	return nil
}

// roll moves the log aside to "<path>.1" and points this process at a fresh one.
// It is called only by a write that has just crossed the cap.
//
// Everything a failure can reach here ends with the counter back at zero rather
// than left over the cap. The alternative is worse than the failure it reports:
// a counter that stays above the cap makes the NEXT line roll too, so a
// filesystem that will not rename turns the daemon's write path into a lock and
// a stat per line. Zero means "ask again in another cap's worth of bytes", which
// is a bound on the retries as well as on the file.
func (r *logRotator) roll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n.Load() < r.max {
		return // another goroutine in this process rolled while we waited
	}
	release, err := lockRotation(r.path + rotateLockSuffix)
	if err != nil {
		r.n.Store(0)
		return
	}
	defer release()

	// The SIZE decides, re-read under the claim, rather than the counter that got
	// us here. Two processes crossing the cap in the same instant both arrive
	// wanting a rotation; the one that waited would otherwise rename the winner's
	// empty new file over the winner's rotation and destroy the only copy of
	// everything the log held. Under the claim the loser sees a small file and
	// rotates nothing — it still adopts it below, which is the half it does need.
	if fi, serr := os.Stat(r.path); serr == nil && fi.Size() >= r.max {
		_ = os.Rename(r.path, r.path+rotatedLogSuffix)
	}
	nf, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		r.n.Store(0)
		return
	}
	size := sizeOf(nf)
	if size >= r.max {
		size = 0 // the rotation did not take; do not roll again on the next line
	}
	// Order matters: the counter is reset before the swap, so a writer that
	// crosses the cap in the same instant is counted against the new generation
	// rather than rolling it again on its first line.
	r.n.Store(size)
	r.f.Store(nf) // the previous generation is dropped, not closed — see the field
	for _, fd := range r.mirror {
		_ = swapFD(nf, fd)
	}
}

// lockRotation takes the cross-process claim on rotating this log, the same
// advisory-flock idiom claimSession uses for the fleet.
//
// It BLOCKS, where claimSession does not, because losing this race has no answer
// of its own. A caller that gave up here would still have to end up pointed at
// whichever file baton.log is once the winner is finished, and waiting is the
// only way to know when that is. What is held across the wait is a stat, a
// rename and an open, and the kernel drops an flock the moment its holder dies,
// so the wait cannot outlive a crash.
func lockRotation(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open rotation lock %s: %w", path, err)
	}
	if lerr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); lerr != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock rotation %s: %w", path, lerr)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// sizeOf is the file's size, or zero when it cannot be read. A size this cannot
// answer means the next rotation is decided a cap's worth of bytes later than it
// would have been, which is the harmless direction.
func sizeOf(f *os.File) int64 {
	fi, err := f.Stat()
	if err != nil {
		return 0
	}
	return fi.Size()
}
