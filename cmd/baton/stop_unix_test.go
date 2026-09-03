//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/paths"
)

// This file pins the second gate stopUnboundDaemon passes before it signals:
// confirmBatonPid, which asks the pid what it is running.
//
// The claim is the first gate and is the right question — it is an flock the
// kernel drops when its holder dies, so a false there means the PID file beside
// it is garbage whatever process now holds that number. What it cannot answer is
// the other half: a new daemon takes the claim and THEN tidies its predecessor's
// PID file, so a --force landing inside that window sees a claim held beside a
// pid that is not the holder's. Measured at 154-690 µs, median 230 µs — 0 hits in
// 60 random-timing trials, about 5% with a spin loop. No ordering of two files
// closes it, because only the kernel knows who holds an flock and it will not
// say. Asking the process closes it, because the process will.
//
// BOTH DIRECTIONS, because a check that refused everything would pass the
// refusal test while leaving an operator with a wedged fleet and no way out:
//
//	a live pid that is not baton -> refused, with the executable named, and the
//	                                innocent process still running afterwards
//	a genuinely stuck daemon     -> still stopped (see
//	                                TestForceStopsADaemonStuckAboveTheBind)

// TestForceRefusesAPidThatIsNotThisBaton is the firing direction. The state is
// built rather than raced for: the test holds the session claim itself — flock
// is per open file description, so a probe opening the same file from this same
// process reads it as held, exactly as TestSessionClaimedTracksTheHolder relies
// on — and puts a live, unrelated pid in the PID file beside it. That is the
// window's picture on disk, held still.
func TestForceRefusesAPidThatIsNotThisBaton(t *testing.T) {
	sock := filepath.Join(shortDir(t), "b.sock")
	release, held, err := claimSession(paths.LockFile(sock))
	if err != nil || !held {
		t.Fatalf("claim = held %v, err %v; want held", held, err)
	}
	defer release()

	// /bin/sleep rather than anything this suite builds: the check compares the
	// program's name, so the innocent process has to be a genuinely different one.
	victim := exec.Command("/bin/sleep", "30")
	if err := victim.Start(); err != nil {
		t.Fatalf("start the innocent process: %v", err)
	}
	// Reaped through a channel rather than signal 0, for the reason
	// TestForceStopsADaemonStuckAboveTheBind gives: a killed child nothing has
	// waited on is a zombie, and signal 0 calls a zombie alive.
	exited := make(chan error, 1)
	go func() { exited <- victim.Wait() }()
	t.Cleanup(func() { _ = victim.Process.Kill() })

	if err := writePidFile(paths.PidFile(sock), victim.Process.Pid); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err = stopUnboundDaemon(sock)
	if err == nil {
		t.Fatalf("stopUnboundDaemon signalled pid %d, which is /bin/sleep and not a baton daemon",
			victim.Process.Pid)
	}
	t.Logf("what an operator gets after %s:\n\t%v", time.Since(start), err)
	if !strings.Contains(err.Error(), "sleep") {
		t.Errorf("the refusal %q does not name what the pid is actually running, which is the whole "+
			"of what an operator needs to see that the PID file is stale", err)
	}

	// And the process is untouched. A SIGTERM would have ended /bin/sleep at once,
	// and the wait that followed it would have cost daemonPollTries × daemonPollGap.
	select {
	case werr := <-exited:
		t.Fatalf("the innocent process was killed (%v); refusing means refusing to signal, not "+
			"signalling with a warning", werr)
	case <-time.After(250 * time.Millisecond):
	}
}

// TestConfirmBatonPidAcceptsThisProcess is the silent direction at its cheapest:
// the running test binary is the same program the check compares against, so a
// check that refused everything fails here without forking anything.
//
// The end-to-end half — a real daemon, stuck above the bind, still stopped
// through this gate — is TestForceStopsADaemonStuckAboveTheBind.
func TestConfirmBatonPidAcceptsThisProcess(t *testing.T) {
	if err := confirmBatonPid(os.Getpid()); err != nil {
		t.Fatalf("confirmBatonPid on this very process: %v. Every stop goes through this, so a check "+
			"that cannot recognise its own binary leaves no way to stop a wedged daemon at all", err)
	}
}

// TestConfirmBatonPidRefusesAPidThatIsGone pins the fail-closed direction on the
// error an exited daemon leaves: a PID file outlives the process it named, and
// the number in it is one the operating system will hand to something else.
func TestConfirmBatonPidRefusesAPidThatIsGone(t *testing.T) {
	gone := exec.Command("/bin/sleep", "0.01")
	if err := gone.Start(); err != nil {
		t.Fatal(err)
	}
	_ = gone.Wait()

	if err := confirmBatonPid(gone.Process.Pid); err == nil {
		t.Fatalf("confirmBatonPid accepted pid %d, which has exited", gone.Process.Pid)
	}
}

// TestProgramNameComparesWhatTheTwoSidesCanAgreeOn pins the one line that makes
// the comparison possible at all. Neither platform hands back the path
// os.Executable does: darwin resolves /var/folders to /private/var/folders on
// one side only, and Linux marks a binary replaced under a running process with
// a " (deleted)" suffix — which is the daemon an operator is most likely to be
// force-stopping, right after `make install`.
func TestProgramNameComparesWhatTheTwoSidesCanAgreeOn(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/usr/local/bin/baton", "baton"},
		{"/usr/local/bin/baton (deleted)", "baton"},
		{"/private/var/folders/x/T/go-build1/b001/baton.test", "baton.test"},
		{"/bin/sleep", "sleep"},
	} {
		if got := programName(tc.path); got != tc.want {
			t.Errorf("programName(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
