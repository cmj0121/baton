package gitdiff

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCollectDoesNotBlockOnAnUntrackedLinkToAFifo is the work-tree half of the
// blocking-open case, and the one an agent can reach without naming anything on
// the socket: it already runs in this directory, so two commands in its own shell
// are the whole setup.
//
// The link is load-bearing. `git status --porcelain` does not list a bare FIFO —
// it lists regular files, directories and SYMLINKS — so the FIFO alone never
// reaches renderUntracked, and a test that planted one would pass against the
// unguarded code. A symlink to it is listed, is followed by the open, and parks
// the daemon's diff on a writer that never arrives.
//
// The plain file beside it is the assertion: the diff still has to come back with
// the ordinary untracked file rendered, so what is proved is that the FIFO was
// stepped over rather than that Collect gave up.
func TestCollectDoesNotBlockOnAnUntrackedLinkToAFifo(t *testing.T) {
	requireGit(t)

	repo := initRepo(t)
	commitFile(t, repo, "a.txt", "hello\n")
	if err := syscall.Mkfifo(filepath.Join(repo, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if err := os.Symlink(filepath.Join(repo, "pipe"), filepath.Join(repo, "notes.txt")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "real.txt"), []byte("new work\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	type result struct {
		changes []FileChange
		err     error
	}
	got := make(chan result, 1)
	go func() {
		c, err := Collect(repo)
		got <- result{c, err}
	}()

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("Collect = %v, want the tree read past the link", r.err)
		}
		var real *FileChange
		for i := range r.changes {
			if r.changes[i].Path == "real.txt" {
				real = &r.changes[i]
			}
		}
		if real == nil {
			t.Fatalf("Collect returned %d changes with no real.txt among them", len(r.changes))
		}
		if !strings.Contains(real.Unstaged, "+new work") {
			t.Fatalf("real.txt rendered as %q, want its content as an added diff", real.Unstaged)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Collect did not return: the diff is blocked on an untracked link to a FIFO")
	}
}
