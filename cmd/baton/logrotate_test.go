package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/panellog"
)

// writeLines pushes n numbered lines through w under the given prefix and
// returns what it wrote, so a caller can assert on the exact bytes.
func writeLines(t *testing.T, w *logRotator, prefix string, n int) []string {
	t.Helper()
	lines := make([]string, 0, n)
	for i := range n {
		line := fmt.Sprintf("%s%05d\n", prefix, i)
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write %s: %v", strings.TrimSpace(line), err)
		}
		lines = append(lines, line)
	}
	return lines
}

// readAll is the file's contents, or "" when it is not there.
func readAll(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestLogRotatorBoundsTheFileAndKeepsOneGeneration is the shape of #64: a log
// driven past the cap is bounded, and what it held before is still readable.
//
// The second pass is the half that matters. One rotation proves a rename
// happened; the SECOND proves the bound is "twice the cap" and not "the cap per
// rotation" — a roller that named each generation .1, .2, .3 would pass the
// first pass and grow forever, which is the defect this replaces.
func TestLogRotatorBoundsTheFileAndKeepsOneGeneration(t *testing.T) {
	const cap64 = 4096
	dir := t.TempDir()
	path := filepath.Join(dir, "baton.log")

	r, err := openLogRotator(path, cap64)
	if err != nil {
		t.Fatalf("openLogRotator: %v", err)
	}

	first := writeLines(t, r, "A", 1000) // ~7 KiB: past the cap
	if got := readAll(t, path+rotatedLogSuffix); got == "" {
		t.Fatal("nothing was rotated aside; the log grows for ever, which is #64")
	}
	second := writeLines(t, r, "B", 1000)

	live, rotated := readAll(t, path), readAll(t, path+rotatedLogSuffix)
	if int64(len(live)) >= cap64 {
		t.Errorf("the live log is %d bytes with a cap of %d: it is not bounded", len(live), cap64)
	}
	if total := int64(len(live) + len(rotated)); total > 2*cap64 {
		t.Errorf("the two generations hold %d bytes; the bound is twice the %d cap", total, cap64)
	}
	// The bound is one rotation, so no third file was ever made — a roller naming
	// generations .1, .2, .3 would pass every size check above and grow for ever.
	if got := readAll(t, path+rotatedLogSuffix+rotatedLogSuffix); got != "" {
		t.Errorf("a second rotated generation exists (%s.1.1); the bound is one", path)
	}
	// What the bound BUYS: the tail of what was written is still readable, in one
	// generation or the other.
	for _, want := range second[len(second)-3:] {
		if !strings.Contains(live+rotated, want) {
			t.Errorf("%q is in neither generation; a line was lost across a rotation", strings.TrimSpace(want))
		}
	}
	// What the bound COSTS, and the assertion that makes it a bound rather than a
	// claim: the oldest lines are gone from both files. A rotator that kept
	// everything would fail here.
	if strings.Contains(live+rotated, first[0]) {
		t.Errorf("%q survived %d bytes of later writing under a %d cap; nothing is being dropped",
			strings.TrimSpace(first[0]), len(strings.Join(second, "")), cap64)
	}
}

// TestLogRotatorLeavesALogUnderTheCapAlone is the other direction, and it is not
// a formality: a rotator that rolled on every write would satisfy every "is it
// bounded" test in this file.
func TestLogRotatorLeavesALogUnderTheCapAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baton.log")

	r, err := openLogRotator(path, 1<<20)
	if err != nil {
		t.Fatalf("openLogRotator: %v", err)
	}
	want := writeLines(t, r, "A", 100)

	if got := readAll(t, path+rotatedLogSuffix); got != "" {
		t.Fatalf("a %d-byte log under a 1 MiB cap was rotated", len(strings.Join(want, "")))
	}
	if got := readAll(t, path); got != strings.Join(want, "") {
		t.Fatalf("the log does not hold what was written to it:\n%s", got)
	}
	if _, err := os.Stat(path + rotateLockSuffix); err == nil {
		t.Error("a log that never rotated made a rotation lock file")
	}
}

// TestLogRotatorRotatesWhatItFindsAtOpen covers the growth no write path can
// reach: a `baton ctl` writes a line or two and exits, so a log filled by short
// runs alone would never be rotated by the process that crossed the cap.
func TestLogRotatorRotatesWhatItFindsAtOpen(t *testing.T) {
	const cap64 = 4096
	dir := t.TempDir()
	path := filepath.Join(dir, "baton.log")

	seed := strings.Repeat("seeded-before-this-process-existed\n", 200) // > 4 KiB
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := openLogRotator(path, cap64); err != nil {
		t.Fatalf("openLogRotator: %v", err)
	}

	if got := readAll(t, path); got != "" {
		t.Errorf("the log was not rotated at open; it still holds %d bytes", len(got))
	}
	if got := readAll(t, path+rotatedLogSuffix); got != seed {
		t.Errorf("the rotated generation is not what the file held (%d of %d bytes)", len(got), len(seed))
	}
}

// TestLogRotatorSwapsGenerationsUnderLiveWriters is the in-process half of the
// multi-writer story, and it is a regression test for a design that was tried
// and measured wrong rather than a precaution.
//
// The daemon logs from every goroutine it has, so a rotation always lands under
// writers that are mid-line. Keeping ONE descriptor and dup2-ing each new
// generation onto it is the obvious way to do that with no lock on the write
// path, and it does not work: on darwin, dup2 onto a descriptor another thread
// is writing through fails about one of those writes in a hundred with EBADF,
// because the kernel reserves the slot while it closes what was there. Under
// that design this test reports failed writes in the thousands.
//
// The shape is what makes it bite. The writers must be WRITING while the swap
// happens, not queued behind it, so the rotations are driven from a goroutine of
// their own rather than by the cap — a version where the writers themselves
// crossed the cap spent its time blocked on the rotation mutex and caught the
// broken design roughly once in twenty-four thousand writes.
func TestLogRotatorSwapsGenerationsUnderLiveWriters(t *testing.T) {
	const (
		cap64   = 4096
		writers = 8
		rolls   = 400
		warmup  = 5000
	)
	dir := t.TempDir()
	path := filepath.Join(dir, "baton.log")

	r, err := openLogRotator(path, cap64)
	if err != nil {
		t.Fatalf("openLogRotator: %v", err)
	}

	stop := make(chan struct{})
	var failed, wrote atomic.Int64
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				line := fmt.Sprintf("w%d-%05d\n", w, i%100000)
				n, werr := r.Write([]byte(line))
				if werr != nil || n != len(line) {
					if failed.Add(1) == 1 {
						t.Errorf("write %q: wrote %d of %d bytes: %v", strings.TrimSpace(line), n, len(line), werr)
					}
					continue
				}
				wrote.Add(1)
			}
		}()
	}
	// Let the writers get going first: rotating a file nobody is writing to
	// proves nothing, and the check below says so rather than trusting it.
	for wrote.Load() < warmup {
		runtime.Gosched()
	}
	// Force the rotations rather than waiting for the cap, so they land at the
	// rate the swap has to survive rather than at the rate the writers reach.
	before := wrote.Load()
	for range rolls {
		r.n.Store(r.max)
		r.roll()
	}
	during := wrote.Load() - before
	close(stop)
	wg.Wait()

	if got := failed.Load(); got != 0 {
		t.Errorf("%d writes failed or came up short across %d rotations, out of %d that did not",
			got, rolls, wrote.Load())
	}
	// Guard against the test going vacuous: the exposure is writes that overlap a
	// swap, so writers that had stopped would make every assertion above free.
	if during < rolls/8 {
		t.Fatalf("only %d writes landed across %d rotations; the writers were not writing through them",
			during, rolls)
	}
	if readAll(t, path+rotatedLogSuffix) == "" {
		t.Fatal("no rotation happened; this proved nothing about writing across one")
	}
	// Every line that survived is a whole line. A rotation that truncated, or one
	// writer landing inside another's bytes, shows up here and nowhere else.
	both := readAll(t, path+rotatedLogSuffix) + readAll(t, path)
	for _, line := range strings.Split(strings.TrimSuffix(both, "\n"), "\n") {
		if !wellFormedWriterLine(line) {
			t.Fatalf("a torn line survived the rotations: %q", line)
		}
	}
}

// wellFormedWriterLine reports whether a line is exactly what one of the writers
// above emits: "w<digit>-<five digits>".
func wellFormedWriterLine(line string) bool {
	var w, i int
	n, err := fmt.Sscanf(line, "w%d-%d", &w, &i)
	return err == nil && n == 2 && line == fmt.Sprintf("w%d-%05d", w, i)
}

// TestSetupLoggerRotatesAtTheDefaultCap drives the real logger — the real
// constant, the real zerolog.ConsoleWriter over the real sink — past the cap,
// because everything above tests the rotator with a cap of its own choosing and
// none of it would notice the console writer being handed a file the rotation
// cannot reach through.
func TestSetupLoggerRotatesAtTheDefaultCap(t *testing.T) {
	saved := log.Logger
	savedLevel := zerolog.GlobalLevel()
	t.Cleanup(func() { log.Logger = saved; zerolog.SetGlobalLevel(savedLevel) })

	dir := t.TempDir()
	path := filepath.Join(dir, "baton.log")
	if _, err := setupLogger(0, path); err != nil {
		t.Fatalf("setupLogger: %v", err)
	}

	// 8 KiB of payload a line, so the 8 MiB cap is a thousand lines rather than
	// eighty thousand. The last line is the one that must be readable afterwards.
	payload := strings.Repeat("x", 8<<10)
	lines := int(logRotateAtBytes/int64(len(payload))) + 64
	for i := range lines {
		log.Info().Int("i", i).Str("payload", payload).Msg("fleet event")
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the log: %v", err)
	}
	if fi.Size() >= logRotateAtBytes {
		t.Errorf("baton.log is %d bytes with a cap of %d: the real logger is not bounded", fi.Size(), logRotateAtBytes)
	}
	rotated, err := os.Stat(path + rotatedLogSuffix)
	if err != nil {
		t.Fatalf("no rotated generation beside a log driven past the cap: %v", err)
	}
	if total := fi.Size() + rotated.Size(); total > 2*logRotateAtBytes {
		t.Errorf("the pair holds %d bytes; the bound is twice the %d cap", total, logRotateAtBytes)
	}
	if !strings.Contains(readAll(t, path), fmt.Sprintf(`i=%d`, lines-1)) {
		t.Error("the last line written is not in the live log")
	}
}

// TestLogRotateAtBytesIsBoundedBothWays pins the CONSTANT, which every test
// above deliberately cannot: each of them builds its fixture from the cap it was
// handed, so all of them prove a relationship and none of them would notice the
// default moving to 64 KiB or to a gigabyte.
//
// TOO SMALL costs the history an operator was sent here for. didNotComeUpReason
// points at this file by name and nothing else in the repo does, so a cap the
// ordinary running of a fleet crosses means the answer has already been rotated
// away by the time it is read. The floor is measured twice, off real bytes
// rather than off a rate nobody has reproduced: what one REAL daemon session
// costs this file, and what one REAL line through the logger setupLogger builds
// costs it.
//
// TOO LARGE costs disk nobody asked for. A panel transcript is opted into per
// panel, named in the cockpit, and carries a child process's raw output — a
// runaway build, gigabytes in minutes — which is why panellog gives it 64 MiB.
// baton.log is none of those things: one per installation, written whether
// anyone wanted it or not, never shown a size. So the ceiling is stated against
// that number rather than as a byte count of its own — the unattended file's
// whole two-generation budget stays inside half of what one attended file may
// spend.
func TestLogRotateAtBytesIsBoundedBothWays(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the daemon fork-exec the session measurement needs")
	}

	perSession := measureDaemonSessionBytes(t)
	perLine := measureLogLineBytes(t)

	// A thousand daemon lifetimes: every boot, claim, bind and shutdown since the
	// installation was made, for an operator who restarts several times a day for
	// a year. Restarts alone must not be what churns this file.
	const sessions = 1000
	if got := int64(sessions) * perSession; got > logRotateAtBytes {
		t.Errorf("a daemon session writes %d bytes, so %d of them is %d — over the %d cap: "+
			"restarting the fleet would rotate the log by itself",
			perSession, sessions, got, logRotateAtBytes)
	}
	// Fifty thousand fleet events, at what an event really costs through
	// zerolog.ConsoleWriter. This is the floor that moves when the LINE moves: a
	// logger reworked to write fatter lines shrinks the window without touching
	// the constant, and this is the assertion that says so.
	const events = 50_000
	if got := int64(events) * perLine; got > logRotateAtBytes {
		t.Errorf("a fleet event costs %d bytes, so %d of them is %d — over the %d cap: "+
			"the log no longer holds a fleet's recent history",
			perLine, events, got, logRotateAtBytes)
	}
	if budget := panellog.MaxBytes(0) / 2; 2*logRotateAtBytes > budget {
		t.Errorf("the two generations of baton.log may reach %d bytes, past the %d that is half of "+
			"one panel transcript's budget: an unattended per-installation file must not cost more "+
			"than an opted-into per-panel one", 2*logRotateAtBytes, budget)
	}
}

// measureDaemonSessionBytes is what one real daemon costs this log: a fork-exec'd
// child that claims the session, binds, serves and shuts down, at the default
// verbosity, writing through the same setupLogger every baton process uses.
func measureDaemonSessionBytes(t *testing.T) int64 {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	t.Setenv(testDaemonChildEnv, "1")

	// Not t.TempDir(): a unix socket path is capped near 104 bytes and this test's
	// name is long enough to overflow it under the system temp root.
	rundir, err := os.MkdirTemp("", "batonlog")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rundir) })

	sock := filepath.Join(rundir, "d.sock")
	t.Setenv("BATON_SOCK", sock)
	// TestMain points the child's logger here, beside its socket.
	childLog := filepath.Join(rundir, "daemon.log")

	if err := startDaemon(0, filepath.Join(home, "parent.log"), "", false); err != nil {
		t.Fatalf("startDaemon: %v", err)
	}
	if err := stopDaemon(sock); err != nil {
		t.Fatalf("stopDaemon: %v", err)
	}
	fi, err := os.Stat(childLog)
	if err != nil {
		t.Fatalf("the daemon session wrote no log to measure: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatal("the daemon session's log is empty; there is nothing to derive a floor from")
	}
	return fi.Size()
}

// measureLogLineBytes is what one ordinary fleet event costs on disk, written
// through exactly the writer setupLogger builds over the sink.
func measureLogLineBytes(t *testing.T) int64 {
	t.Helper()
	path := filepath.Join(t.TempDir(), "one.log")
	sink, err := openLogRotator(path, 1<<30)
	if err != nil {
		t.Fatalf("openLogRotator: %v", err)
	}
	writer := zerolog.ConsoleWriter{Out: sink, NoColor: true, TimeFormat: "2006-01-02 15:04:05"}
	logger := zerolog.New(writer).With().Timestamp().Logger()
	logger.Info().Str("panel", "p3").Str("profile", "claude").Str("group", "api").Msg("panel spawned")

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatal("one Info line wrote nothing; the measurement is not measuring the logger")
	}
	return fi.Size()
}

// TestRotationsDoNotAccumulateDescriptors is the assertion behind the one soft
// spot in the design: a rotation DROPS the generation it replaces instead of
// closing it, because there is no moment at which closing it is safe under a
// writer that has already loaded it.
//
// That trade is only sound if the runtime really does take the descriptor back —
// an *os.File closes its own when it becomes unreachable — and "the runtime will
// get to it" is the kind of claim that deserves a number instead. A rotator that
// kept a reference to every generation would climb by one per rotation and be
// caught here; the measured answer over several hundred is no climb at all.
func TestRotationsDoNotAccumulateDescriptors(t *testing.T) {
	const (
		cap64 = 1024
		lines = 20000
		slack = 64 // room for whatever else the test binary opens meanwhile
	)
	dir := t.TempDir()
	path := filepath.Join(dir, "baton.log")

	r, err := openLogRotator(path, cap64)
	if err != nil {
		t.Fatalf("openLogRotator: %v", err)
	}
	before := openDescriptors()
	for i := range lines {
		if _, werr := fmt.Fprintf(r, "line-%06d-padding-padding-padding\n", i); werr != nil {
			t.Fatalf("write %d: %v", i, werr)
		}
	}
	rotations := lines * 35 / cap64
	got := descriptorsAfterCollection(before + slack)
	// The rotator has to still be alive for this to mean anything: collected, it
	// would take any generation it was holding with it and the count would come
	// back clean whatever the design did.
	runtime.KeepAlive(r)
	if got > before+slack {
		t.Errorf("this process still holds %d descriptors against %d before, after about %d "+
			"rotations and a collection: the generations are reachable, not garbage", got, before, rotations)
	}
}

// descriptorsAfterCollection is how many descriptors survive a collection,
// giving up once the count is down to target.
//
// The collection is forced rather than waited for, and that is the honest shape
// of the claim: the generations a rotation drops are GARBAGE, not leaks, so what
// bounds them is how long the runtime takes to notice rather than a number. In
// this test, run at hundreds of rotations a second, an unforced count came back
// at 141 of 683 — the daemon reaches one rotation per cap of bytes, so between
// two of them it has allocated its way through many collections.
func descriptorsAfterCollection(target int) int {
	// A dropped generation's descriptor is closed by os.File's FINALIZER, and a
	// finalizer needs two collections — one to find the file unreachable and queue
	// it, one after it has run — and runtime.GC does not wait for the queue to
	// drain, since finalizers run on a goroutine of their own.
	//
	// So this yields between collections rather than spinning on GC. Twenty tight
	// GCs queued the finalizers and outran them, which is why this test used to
	// pass only where the runtime was slow enough: under -race on darwin, and not
	// at all on Linux (#74).
	//
	// The budget is generous on purpose. What it separates is not fast from slow
	// but reclaimed from LEAKED: a generation that is still reachable never comes
	// back, however long this waits.
	deadline := time.Now().Add(10 * time.Second)
	open := openDescriptors()
	for open > target && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(5 * time.Millisecond)
		open = openDescriptors()
	}
	return open
}

// openDescriptors counts what this process has open. It asks the descriptors
// themselves rather than reading /dev/fd, which is not on every host the suite
// runs on.
func openDescriptors() int {
	var open int
	for fd := range 4096 {
		if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0); errno == 0 {
			open++
		}
	}
	return open
}
