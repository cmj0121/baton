package proctree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestCwdReadsTheProcessTable: the fallback has to be exact, since it is what
// answers for every shell that does not report its own directory.
func TestCwdReadsTheProcessTable(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a probe process here: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	time.Sleep(200 * time.Millisecond)

	got, err := Cwd(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Cwd: %v", err)
	}
	// The kernel answers with symlinks resolved (darwin's /tmp is /private/tmp),
	// so the comparison is made on the resolved form of what the test asked for.
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		want = dir
	}
	if got != want {
		t.Fatalf("Cwd = %q, want %q", got, want)
	}
}

// TestCwdRejectsANonProcess: a panel whose process has exited carries pid 0, and
// asking about it must fail rather than answer for something else.
func TestCwdRejectsANonProcess(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if got, err := Cwd(pid); err == nil {
			t.Errorf("Cwd(%d) = %q, want an error", pid, got)
		}
	}
	// A pid far past any live process: the lookup fails rather than guessing.
	if _, err := Cwd(1 << 30); err == nil {
		t.Error("Cwd of a nonexistent process should fail")
	}
}

// TestCwdOfSelf: the simplest end-to-end check that the plumbing reaches the real
// process table on this platform.
func TestCwdOfSelf(t *testing.T) {
	got, err := Cwd(os.Getpid())
	if err != nil {
		t.Skipf("the process table is unavailable here: %v", err)
	}
	wd, _ := os.Getwd()
	want, err := filepath.EvalSymlinks(wd)
	if err != nil {
		want = wd
	}
	if got != want {
		t.Fatalf("Cwd(self) = %q, want %q", got, want)
	}
}
