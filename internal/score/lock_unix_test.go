//go:build unix

package score

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSingleWriterPerDirectory is scope item 7 (#40, Page's note): two daemons
// on two sockets both default to $HOME/.baton, and an unenforced "run only one"
// would let their rewrites of score.md clobber each other silently. The claim is taken at
// Open and released at Close, so the second daemon is told plainly instead of
// corrupting the first one's view.
func TestSingleWriterPerDirectory(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir, Policy{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	second, err := Open(dir, Policy{})
	if err == nil {
		second.Close()
		t.Fatal("a second store opened the same directory; the claim is not enforced")
	}
	if !strings.Contains(err.Error(), "another baton daemon") {
		t.Errorf("error = %v, want it to name the conflict plainly", err)
	}

	// Close hands the directory over, which is what makes a restart work.
	first.Close()
	third, err := Open(dir, Policy{})
	if err != nil {
		t.Fatalf("reopen after Close: %v", err)
	}
	third.Close()
	third.Close() // idempotent
}

// A daemon that is KILLED never reaches Close, so the claim it holds is dropped
// by the kernel rather than by this package. That is the whole reason the claim
// is an flock on a live descriptor and not a pid file: the next start must not
// find a lock nobody is holding and refuse a fleet its memory forever — the boot
// failure that cannot heal, because the process that would clear the lock is the
// process that died.
//
// It is driven with a real process killed by SIGKILL rather than reasoned about, because
// the property belongs to the kernel and a same-process test would prove nothing
// about it.
func TestLockIsReleasedByAKilledHolder(t *testing.T) {
	if dir := os.Getenv("BATON_TEST_LOCK_DIR"); dir != "" {
		// The child: take the claim, say so, and wait to be killed.
		s, err := Open(dir, Policy{})
		if err != nil {
			os.Exit(2)
		}
		defer s.Close()
		fmt.Println("held")
		time.Sleep(time.Minute)
		os.Exit(3) // never reached; the parent kills this process
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestLockIsReleasedByAKilledHolder", "-test.timeout=2m")
	cmd.Env = append(os.Environ(), "BATON_TEST_LOCK_DIR="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// Wait for the child to say it has the claim, so the kill lands on a holder.
	br := bufio.NewReader(out)
	for {
		line, rerr := br.ReadString('\n')
		if rerr != nil {
			t.Fatalf("the child never took the claim: %v", rerr)
		}
		if strings.TrimSpace(line) == "held" {
			break
		}
	}

	if s, oerr := Open(dir, Policy{}); oerr == nil {
		s.Close()
		t.Fatal("opened a directory another live process holds; the claim is not enforced across processes")
	}

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill the holder: %v", err)
	}
	_ = cmd.Wait()

	s, err := Open(dir, Policy{})
	if err != nil {
		t.Fatalf("Open after the holder was killed: %v", err)
	}
	defer s.Close()
	// Open, and usable: a store that boots but cannot record is no recovery.
	if _, _, err := s.Submit("the fleet remembers after a kill", Provenance{Source: "user"}); err != nil {
		t.Fatalf("submit after recovering the claim: %v", err)
	}
}

// TestOpenReleasesTheClaimWhenItFails checks the failed-boot path: a store that
// could not read its own log must not leave the directory claimed, or the next
// start would be refused for a reason that no longer exists.
func TestOpenReleasesTheClaimWhenItFails(t *testing.T) {
	dir := t.TempDir()
	// The event log as a directory: replay's ReadFile fails with something other
	// than "not exist", so Open returns an error rather than an empty store.
	if err := os.Mkdir(filepath.Join(dir, scoreEvents), 0o700); err != nil {
		t.Fatalf("mkdir over the log: %v", err)
	}

	if s, err := Open(dir, Policy{}); err == nil {
		s.Close()
		t.Fatal("Open succeeded with an unreadable event log")
	}
	s, err := Open(dir, Policy{})
	if err == nil {
		s.Close()
		t.Fatal("Open succeeded on the second try, so the first failure was not the log")
	}
	if strings.Contains(err.Error(), "another baton daemon") {
		t.Fatal("a failed Open kept the directory claimed")
	}
}
