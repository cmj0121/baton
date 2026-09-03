//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/paths"
)

// This file asserts the ONE thing #60 changed: the daemon reads the operator's
// filesystem before it binds its socket, not after.
//
// It is asserted against a forked daemon on a real path that does not answer,
// because that is the only place the property is observable. In-process the
// question cannot be put — runServerOn is handed an already-bound listener, so
// the ordering it is about has already happened by the time a test can look.
//
// The dead path is a FIFO standing where score-events.jsonl belongs: opening one
// with no writer blocks in the kernel forever, which is what a hard mount whose
// server has gone away does and what no test can otherwise conjure. It is the
// same instrument R7's ops review measured the old behaviour with.
//
// BOTH DIRECTIONS, because a test that only proves the socket is withheld would
// pass just as well on a daemon that never binds at all:
//
//	dead score.dir    -> no socket for the whole window, and a client dialling
//	                     gets a connection error rather than a hang
//	healthy score.dir -> the socket is there and serving, promptly
//
// hangWindow is how long the dead case is watched, and pollGap how often.
//
// The window sits far under scoreOpenTimeout, because past that the daemon gives
// up on the store and binds ON PURPOSE — the bound doing its job, not the
// ordering failing. Under the ordering this replaced, the socket was there
// within 11-40 ms of the fork on this machine, so half a second is better than a
// tenfold margin on the behaviour it rules out, and every millisecond past that
// is suite time bought with nothing.
//
// It is deliberately NOT the healthy case's budget. This one is an upper watch —
// longer is safer and only slower — while that one is a deadline a real boot has
// to beat, where longer is safer and shorter flakes. They are different numbers
// wanting opposite things, and a single constant serving both was 65-76 ms of
// race-instrumented boot against a 500 ms deadline on whatever machine CI gives
// us. See TestAHealthyBootStillBindsPromptly for what that budget is instead.
const (
	hangWindow = 500 * time.Millisecond
	pollGap    = 10 * time.Millisecond
	// hangTries is hangWindow expressed the way waitFor counts, so the watch and
	// every other budget in this file are the same kind of thing.
	hangTries = int(hangWindow / pollGap)
	// wedgeTries is how long a forked daemon is given to reach the score open —
	// three seconds, which is the fork plus a config read and nothing else. It is
	// not startDaemon's budget: what it waits for is the daemon getting STUCK,
	// which is a state no launcher is waiting on.
	wedgeTries = 300
)

// TestTheSocketIsNotBoundWhileTheLoadHangs is the firing direction: with
// score.dir on a path that never answers, no socket exists while the daemon is
// stuck in it, and a client gets ECONNREFUSED instead of an accepted connection
// that never replies.
func TestTheSocketIsNotBoundWhileTheLoadHangs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon fork-exec in -short")
	}
	home, sock, scoreDir := deadScoreDir(t)

	child := forkDaemon(t, home, sock)
	waitForScoreOpen(t, child, scoreDir)

	// The socket FILE, not a dial: a unix dial cannot succeed without one, so the
	// stat is the earlier and stricter of the two. waitFor is what every other
	// budget in this file uses; here it is read the other way round, so a socket
	// appearing at any point in the window is the failure.
	if waitFor(func() bool { _, err := os.Stat(sock); return err == nil }, hangTries, pollGap) {
		t.Fatalf("a socket exists at %s while the daemon is still inside the load: "+
			"clients can connect to a daemon that is not serving", sock)
	}
	if !childRunning(child) {
		t.Fatal("the daemon exited during the window; it was meant to be hung in the load, " +
			"so this test proved nothing about the ordering")
	}
	// And what a client actually pays for it. alive is the daemon's own dial, so
	// this measures the same call `baton` makes: on an absent unix socket it is
	// ENOENT/ECONNREFUSED and returns at once — the point is that it RETURNS.
	start := time.Now()
	if alive(sock) {
		t.Fatal("dialling the socket of a daemon stuck in its load succeeded")
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("a client dialling the hung daemon took %s to fail; it should not wait at all", took)
	}
}

// TestAHealthyBootStillBindsPromptly is the silent direction, and the one that
// makes the firing direction mean anything: a test that only proved the socket
// is withheld would pass just as well on a daemon that never binds at all.
//
// The budget is startDaemon's OWN — daemonPollTries × daemonPollGap — rather
// than a number this file picks, because that is the one that decides something.
// Moving the load above the bind delays every boot by whatever the load costs,
// and the question that delay can fail is exactly the launcher's: is the socket
// there before `baton` gives up and tells the operator the server did not come
// up. Measured here at 21-33 ms plain and 64-76 ms under the race detector,
// against five seconds.
func TestAHealthyBootStillBindsPromptly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon fork-exec in -short")
	}
	home, sock := bootFixture(t)
	writeScoreConfig(t, home, filepath.Join(home, "memory"))

	child := forkDaemon(t, home, sock)
	start := time.Now()
	if !waitFor(func() bool { return alive(sock) }, daemonPollTries, daemonPollGap) {
		t.Fatalf("a healthy daemon did not serve within the %s startDaemon waits, so `baton` would "+
			"report that the server did not come up; child alive=%v",
			time.Duration(daemonPollTries)*daemonPollGap, childRunning(child))
	}
	// Logged rather than asserted: it is the measurement hangWindow's own doc
	// quotes, and the one number a reader of this file needs.
	t.Logf("healthy boot served after %s", time.Since(start))
}

// TestABootSaysWhatItIsAboutToRead is the question #60's reordering makes an
// operator ask, and the one the daemon could not answer.
//
// Reading the filesystem above the bind turned "a socket that is accepted and
// never served" into "no socket at all", which is the better failure — but what
// `baton` then says is that the server did not come up, see the log. That log
// was ZERO BYTES: nothing was written between claiming the session and the first
// unbounded read, at the default level and at -vv alike, so the operator was
// pointed at a file that named neither the path the daemon stopped on nor the
// fact that it had got as far as starting.
//
// The dead path here is the CONFIG file rather than the score log, because it is
// the first unbounded read and the one nothing said anything about. A FIFO with
// no writer blocks in the kernel forever, which is what a hard mount whose server
// has gone away does.
//
// The budget is startDaemon's own, for the reason TestAHealthyBootStillBindsPromptly
// uses it: a line that lands after `baton` has given up is a line the operator
// reads five seconds after being sent to look for it.
func TestABootSaysWhatItIsAboutToRead(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon fork-exec in -short")
	}
	home, sock := bootFixture(t)
	if err := os.MkdirAll(filepath.Join(home, ".baton"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, ".baton", "config")
	if err := syscall.Mkfifo(cfg, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	child := forkDaemon(t, home, sock)
	daemonLog := filepath.Join(filepath.Dir(sock), "daemon.log")
	read := func() string {
		b, _ := os.ReadFile(daemonLog)
		return string(b)
	}

	start := time.Now()
	if !waitFor(func() bool { return strings.Contains(read(), cfg) }, daemonPollTries, daemonPollGap) {
		t.Fatalf("the daemon wedged on %s and its log never named it within the %s `baton` waits "+
			"before telling the operator to read that log; child alive=%v, log holds:\n%s",
			cfg, time.Duration(daemonPollTries)*daemonPollGap, childRunning(child), read())
	}
	t.Logf("the boot named the file it was about to read after %s", time.Since(start))

	// It really is wedged there, so what the line named is what stopped it rather
	// than a line a healthy boot happened to write on its way past.
	if alive(sock) {
		t.Fatal("the daemon bound its socket; nothing was stuck, so the line proved nothing")
	}
	if !childRunning(child) {
		t.Fatal("the daemon exited rather than hanging in the read this test needs it to hang in")
	}
}

// TestTheNoShowMessageCarriesItsRecovery pins the sentence an operator is left
// holding when the daemon never binds. It is the only place either the state or
// the way out is written down: a grep over docs/ and the README finds neither.
func TestTheNoShowMessageCarriesItsRecovery(t *testing.T) {
	got := didNotComeUpReason("/tmp/somewhere/baton.log", false)
	for _, want := range []string{"/tmp/somewhere/baton.log", "--force"} {
		if !strings.Contains(got, want) {
			t.Errorf("the message %q does not name %q. An operator holding it has a daemon that is "+
				"still alive, still holding the session claim, and refusing every later `baton` — and "+
				"nothing else anywhere tells them how to clear it", got, want)
		}
	}
}

// TestTheForcedNoShowMessageDoesNotSendThemBackRoundAgain is the other direction,
// and it exists because the un-forced sentence read as advice when it was reached
// from --force itself: the operator was told to run the flag they had just run.
//
// The flag had worked. It stopped the old daemon and started a fresh one, which
// walked into the same read — so the message has to say that repeating it will
// not help, and name what will.
func TestTheForcedNoShowMessageDoesNotSendThemBackRoundAgain(t *testing.T) {
	got := didNotComeUpReason("/tmp/somewhere/baton.log", true)
	if !strings.Contains(got, "/tmp/somewhere/baton.log") {
		t.Errorf("the message %q does not name the log, which is where the wedged path is written", got)
	}
	if strings.Contains(got, "`baton --force`") {
		t.Errorf("the message %q tells an operator who just ran --force to run --force. It worked: "+
			"the old daemon was stopped and a fresh one started, and that one stopped in the same "+
			"place, so the path is what is left to fix", got)
	}
	if !strings.Contains(got, "same place") {
		t.Errorf("the message %q does not say the fresh daemon stopped where the old one did, which "+
			"is the fact that makes repeating the flag pointless", got)
	}
}

// TestAStillStartingDaemonIsNotReportedAsGone is #69's firing direction: a
// daemon that is alive and has not bound yet must be described as that, not as
// one that did not come up.
//
// The instrument is the same wedged daemon the rest of this file uses, and it
// stands in for the case the issue was filed on for a reason no test can
// otherwise reach: the two states are the same picture on disk. A daemon 6.5
// seconds into opening a 456 MB score log and a daemon hung forever on a dead
// mount both hold the session claim, both have no socket, and both leave `baton`
// at the end of its five seconds with nothing to tell them apart. What the
// message may therefore claim is only what is true of both — alive, working, and
// here is the line it is on — which is exactly what stopped being said when the
// launcher concluded "did not come up" from "no socket yet".
func TestAStillStartingDaemonIsNotReportedAsGone(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon fork-exec in -short")
	}
	home, sock, scoreDir := deadScoreDir(t)
	child := forkDaemon(t, home, sock)
	waitForScoreOpen(t, child, scoreDir)

	daemonLog := filepath.Join(filepath.Dir(sock), "daemon.log")
	got := startFailureReason(sock, daemonLog, false)
	t.Logf("what an operator gets, daemon alive:\n\t%s", got)

	if strings.Contains(got, "did not come up") {
		t.Errorf("the message %q says the server did not come up about a daemon that is holding the "+
			"session claim and reading a file. It is starting, and a `baton` a moment later may well "+
			"attach to it", got)
	}
	if !strings.Contains(got, "still starting") {
		t.Errorf("the message %q does not say the daemon is still starting, which is the one fact "+
			"that distinguishes it from nothing running", got)
	}
	// The line, not the log: it names what the daemon is on, and pointing at a
	// file is what the old message already did.
	if !strings.Contains(got, "boot: opening the fleet memory") {
		t.Errorf("the message %q does not quote the log line naming what the daemon is working on, "+
			"which is the whole of what it can offer over `see the log`", got)
	}
	if !childRunning(child) {
		t.Fatal("the daemon exited during the check; it was meant to be alive, so this proved nothing")
	}
}

// TestNothingRunningStillReportsThatItDidNotComeUp is the silent direction, and
// the one that makes the firing direction mean anything: a launcher that called
// every failure "still starting" would pass the test above while telling an
// operator whose daemon died on startup to sit and wait for it.
//
// The socket path is one nothing has ever run on, which is the same on-disk
// state as a daemon that exited: nothing holds the claim.
func TestNothingRunningStillReportsThatItDidNotComeUp(t *testing.T) {
	sock := filepath.Join(shortDir(t), "b.sock")
	got := startFailureReason(sock, "/tmp/somewhere/baton.log", false)
	t.Logf("what an operator gets, nothing running:\n\t%s", got)

	if want := didNotComeUpReason("/tmp/somewhere/baton.log", false); got != want {
		t.Errorf("with no daemon holding the session the message should be the unchanged one.\n got %q\nwant %q", got, want)
	}
}

// TestStillStartingFallsBackToTheLogPath covers the daemon that is alive with a
// log nothing can read: the sentence must still arrive, naming the file the way
// didNotComeUpReason does, rather than quoting an empty string at the operator.
func TestStillStartingFallsBackToTheLogPath(t *testing.T) {
	got := stillStartingReason("/tmp/somewhere/baton.log", "")
	if !strings.Contains(got, "/tmp/somewhere/baton.log") {
		t.Errorf("with no line to quote the message %q must name the log instead", got)
	}
	if strings.Contains(got, `""`) {
		t.Errorf("the message %q quotes an empty line at the operator", got)
	}
}

// TestLastLogLineReadsTheEndOfALargeLog pins the reader stillStartingReason
// quotes from, on a log longer than the window it reads: the line wanted is the
// last one, and a window that starts mid-line must not hand back the fragment it
// opens in.
func TestLastLogLineReadsTheEndOfALargeLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baton.log")
	var b strings.Builder
	for b.Len() < 4*logTailBytes {
		b.WriteString("2026-09-03 00:00:00 INF boot: reading the config path=/home/x/.baton/config\n")
	}
	want := "2026-09-03 00:00:01 INF boot: opening the fleet memory dir=/home/x/.baton within=10s"
	b.WriteString(want + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := lastLogLine(path); got != want {
		t.Errorf("lastLogLine = %q, want %q", got, want)
	}

	if got := lastLogLine(filepath.Join(t.TempDir(), "no-such.log")); got != "" {
		t.Errorf("a log that cannot be read should answer an empty line, got %q", got)
	}
	empty := filepath.Join(t.TempDir(), "empty.log")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := lastLogLine(empty); got != "" {
		t.Errorf("an empty log should answer an empty line, got %q", got)
	}
}

// bootFixture builds a private $HOME and a socket path for a forked daemon. The
// socket goes in a shortDir rather than under t.TempDir, for the socket-path cap
// shortDir documents.
func bootFixture(t *testing.T) (home, sock string) {
	t.Helper()
	return t.TempDir(), filepath.Join(shortDir(t), "b.sock")
}

// deadScoreDir is bootFixture with the score directory pointed at a path that
// never answers: a FIFO standing where score-events.jsonl belongs, which blocks
// in the kernel forever with no writer. It is the instrument both of the wedged
// cases here use, and building it twice by hand meant two places to keep the
// config key, the file name and the permissions in step.
func deadScoreDir(t *testing.T) (home, sock, scoreDir string) {
	t.Helper()
	home, sock = bootFixture(t)
	scoreDir = filepath.Join(home, "memory")
	if err := os.MkdirAll(scoreDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeScoreConfig(t, home, scoreDir)
	if err := syscall.Mkfifo(filepath.Join(scoreDir, "score-events.jsonl"), 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	return home, sock, scoreDir
}

// waitForScoreOpen blocks until the store's single-writer claim appears, which
// is proof the daemon really is inside the load. score.Open takes it before it
// reads the event log; without this a child that died on startup would pass a
// test that only asks whether a socket exists, by never binding anything.
func waitForScoreOpen(t *testing.T, child *exec.Cmd, scoreDir string) {
	t.Helper()
	lock := filepath.Join(scoreDir, "score.lock")
	if !waitFor(func() bool { _, err := os.Stat(lock); return err == nil }, wedgeTries, pollGap) {
		t.Fatalf("the daemon never reached the score open, so it was never stuck where this test "+
			"needs it; child alive=%v", childRunning(child))
	}
}

// forkDaemon re-execs the test binary as a real daemon child (TestMain's
// testDaemonChildEnv branch lands in runServer) and kills it on cleanup.
//
// It goes through exec directly rather than through startDaemon, because
// startDaemon waits for a socket that a daemon hung above the bind never binds:
// it would spend its whole five seconds and then report the failure the test
// means to cause. The test keeps the PID so it can always reap its own child,
// whatever the case under test does or does not do to it.
//
// The environment is built from nothing rather than from os.Environ(), so a
// BATON_SOCK or BATON_PLUGIN in the developer's own shell cannot reach the child.
func forkDaemon(t *testing.T, home, sock string) *exec.Cmd {
	t.Helper()
	child := exec.Command(os.Args[0])
	child.Env = []string{
		"HOME=" + home,
		"XDG_RUNTIME_DIR=" + home,
		"BATON_SOCK=" + sock,
		testDaemonChildEnv + "=1",
	}
	if err := child.Start(); err != nil {
		t.Fatalf("fork the daemon child: %v", err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	return child
}

// childRunning reports whether the forked daemon is still alive. Signal 0 is the
// existence check: it validates the target and delivers nothing.
func childRunning(child *exec.Cmd) bool {
	return child.Process.Signal(syscall.Signal(0)) == nil
}

// TestForceStopsADaemonStuckAboveTheBind is the other half of what moving the
// filesystem reads above net.Listen did. A daemon hung there is holding the
// session claim it took a few lines earlier, so every later baton loses
// claimSession to it and exits quietly; with no socket to dial, the force-stop
// that is supposed to clear it used to find nothing alive and signal nobody.
//
// It is asserted end to end on a real forked daemon rather than in-process,
// because the property is about a process that cannot exist in-process: one
// stuck in a syscall it will not return from, with no socket, no signal handler
// of its own, and nothing to reach it by but the pid it published on the way
// past. The three things measured are the three that failed:
//
//	stopDaemon      -> the stuck daemon is gone, rather than a quiet return
//	                   from a stop that signalled nothing
//	the PID file    -> tidied, so no later stop can read it again
//	the next daemon -> starts, which none could while the claim was held
func TestForceStopsADaemonStuckAboveTheBind(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon fork-exec in -short")
	}
	home, sock, scoreDir := deadScoreDir(t)
	fifo := filepath.Join(scoreDir, "score-events.jsonl")

	child := forkDaemon(t, home, sock)
	// Reaped here rather than only in forkDaemon's cleanup, so the test can tell
	// "still stuck" from "already gone" without asking signal 0: a killed child
	// nothing has waited on is a zombie, and signal 0 calls a zombie alive.
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()

	waitForScoreOpen(t, child, scoreDir)
	select {
	case err := <-exited:
		t.Fatalf("the daemon exited on its own (%v); nothing was stuck for the stop to prove anything about", err)
	default:
	}
	if alive(sock) {
		t.Fatal("the daemon bound its socket; this test is about the case where it never gets that far")
	}

	start := time.Now()
	if err := stopDaemon(sock); err != nil {
		t.Fatalf("stopDaemon on a daemon stuck above the bind: %v", err)
	}
	select {
	case <-exited:
		t.Logf("the stuck daemon was stopped in %s", time.Since(start))
	case <-time.After(2 * time.Second):
		t.Fatal("stopDaemon returned, but the daemon it was meant to stop is still running")
	}
	if _, err := os.Stat(paths.PidFile(sock)); !os.IsNotExist(err) {
		t.Fatalf("the stopped daemon's PID file should be gone, not left for a later stop to read: %v", err)
	}

	// And the fleet is usable again: with the dead path out of the way, a fresh
	// daemon takes the session the stuck one was holding and serves. This is the
	// half that stays broken when a force-stop only appears to work.
	if err := os.Remove(fifo); err != nil {
		t.Fatal(err)
	}
	next := forkDaemon(t, home, sock)
	if !waitFor(func() bool { return alive(sock) }, daemonPollTries, daemonPollGap) {
		t.Fatalf("no daemon could start after the stuck one was stopped; child alive=%v", childRunning(next))
	}
}
