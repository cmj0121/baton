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

// bootFixture builds a private $HOME and a socket path for a forked daemon. The
// socket goes in a shortDir rather than under t.TempDir, for the socket-path cap
// shortDir documents.
func bootFixture(t *testing.T) (home, sock string) {
	t.Helper()
	return t.TempDir(), filepath.Join(shortDir(t), "b.sock")
}

// deadScoreDir is bootFixture with the score directory pointed at a path that
// never answers: a FIFO standing where score-events.jsonl belongs, which blocks
// in the kernel forever with no writer. It is the instrument the dead case here
// is measured with, and it keeps the config key, the file name and the
// permissions in one place rather than spread through the test that uses them.
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
