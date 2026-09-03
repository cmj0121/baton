package worktree_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/cmj0121/baton/internal/worktree"
)

// newStore opens a store on a fresh temp file that does not yet exist.
func newStore(t *testing.T) (*worktree.Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.worktrees.json")
	return worktree.New(p), p
}

// realTempDir is t.TempDir() with its symlinks resolved, so a path built under it
// is already in the form the store records. On macOS /var/folders is a symlink to
// /private/var/folders, and which of the two spellings a test typed is not the
// assertion any of these tests is making — TestAddResolvesSymlinks is.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return dir
}

// TestAddThenRead is the whole contract in one pass: a path that was added is in
// the set, and a path nobody added — the operator's own `git worktree add` — is
// not. The second half is what makes the assertion falsifiable; a store that
// reported every path would pass the first half alone.
func TestAddThenRead(t *testing.T) {
	s, path := newStore(t)
	mine := filepath.Join(realTempDir(t), "baton-tree")
	theirs := filepath.Join(realTempDir(t), "hand-made")

	if got := s.Paths(); len(got) != 0 {
		t.Fatalf("a store with no file should be empty, got %v", got)
	}
	if err := s.Add(mine); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got := s.Paths()
	if !slices.Contains(got, mine) {
		t.Fatalf("the added tree %q should be in the set %v", mine, got)
	}
	if slices.Contains(got, theirs) {
		t.Fatalf("a tree nobody added must not be in the set %v", got)
	}
	if s.Path() != path {
		t.Fatalf("Path() = %q, want %q", s.Path(), path)
	}
}

// TestAddPersistsAcrossStores checks the set really is on disk: a second store
// opened on the same file sees what the first one wrote, which is what a daemon
// restart does.
func TestAddPersistsAcrossStores(t *testing.T) {
	s, path := newStore(t)
	tree := filepath.Join(realTempDir(t), "tree")
	if err := s.Add(tree); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if got := worktree.New(path).Paths(); !slices.Contains(got, tree) {
		t.Fatalf("a reopened store should see %q, got %v", tree, got)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("the record is machine-owned; %q is reachable by group or other (%04o)", path, perm)
	}
}

// TestAddIsIdempotentAndAbsolute checks a repeated Add does not duplicate, and
// that a relative path — one that cannot be resolved either — is still stored
// absolute rather than as it arrived.
func TestAddIsIdempotentAndAbsolute(t *testing.T) {
	s, _ := newStore(t)
	tree := filepath.Join(t.TempDir(), "tree")

	for range 3 {
		if err := s.Add(tree); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if got := s.Paths(); len(got) != 1 {
		t.Fatalf("three adds of one tree should leave one entry, got %v", got)
	}

	if err := s.Add("relative/tree"); err != nil {
		t.Fatalf("Add relative: %v", err)
	}
	for _, p := range s.Paths() {
		if !filepath.IsAbs(p) {
			t.Fatalf("every recorded path should be absolute, got %q in %v", p, s.Paths())
		}
	}
}

// TestAddResolvesSymlinks is why Add does more than filepath.Abs: git reports a
// worktree with its symlinks resolved (a macOS /var/... tree comes back as
// /private/var/...), so a record kept in the unresolved form would never match
// the list an orphan sweep compares it against. Two spellings of one tree are
// therefore also one entry, not two.
func TestAddResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "tree"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	s, _ := newStore(t)
	if err := s.Add(filepath.Join(link, "tree")); err != nil {
		t.Fatalf("Add via the link: %v", err)
	}
	if err := s.Add(filepath.Join(real, "tree")); err != nil {
		t.Fatalf("Add via the real path: %v", err)
	}

	want, err := filepath.EvalSymlinks(filepath.Join(real, "tree"))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	got := s.Paths()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("two spellings of one tree should record once as %q, got %v", want, got)
	}
}

// TestRemoveTakesAPathBackOut is the other half of the set's meaning: it names
// the trees baton owns now, not every tree it ever opened. Removing a path that
// was never in it is a no-op rather than an error.
func TestRemoveTakesAPathBackOut(t *testing.T) {
	s, _ := newStore(t)
	mine := filepath.Join(realTempDir(t), "mine")
	other := filepath.Join(realTempDir(t), "other")

	if err := s.Add(mine); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Remove(other); err != nil {
		t.Fatalf("removing a path that was never added should be a no-op: %v", err)
	}
	if got := s.Paths(); !slices.Contains(got, mine) {
		t.Fatalf("removing someone else's path must not disturb the set, got %v", got)
	}

	if err := s.Remove(mine); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := s.Paths(); slices.Contains(got, mine) {
		t.Fatalf("the removed tree %q should be gone from %v", mine, got)
	}
}

// TestRemoveMatchesATreeAlreadyGone is the case Remove is actually called in: git
// has just deleted the directory, so the path can no longer be resolved whole. It
// still has to match the entry recorded while the tree stood — otherwise every
// removal would leak an entry on any host where the temp dir is a symlink, which
// macOS's /var → /private/var is.
func TestRemoveMatchesATreeAlreadyGone(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "tree"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	viaLink := filepath.Join(link, "tree")

	s, _ := newStore(t)
	if err := s.Add(viaLink); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// git removes the tree, then baton un-records the path it gave git.
	if err := os.RemoveAll(filepath.Join(real, "tree")); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if err := s.Remove(viaLink); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := s.Paths(); len(got) != 0 {
		t.Fatalf("a tree removed from disk should still be un-recorded, got %v", got)
	}
}

// TestUnreadableFileReadsEmpty checks the fail-safe direction: garbage and a
// newer schema both read back as no paths, so a sweep that trusts the set
// retires nothing rather than something it should not.
func TestUnreadableFileReadsEmpty(t *testing.T) {
	for name, body := range map[string]string{
		"garbage":      "not json at all",
		"newer schema": `{"schema": 99, "paths": ["/tmp/tree"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "s.worktrees.json")
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := worktree.New(p).Paths(); len(got) != 0 {
				t.Fatalf("an unusable record should read empty, got %v", got)
			}
		})
	}
}

// TestAddReportsAWriteFailure checks a store whose file cannot be written says
// so, rather than silently dropping the tree it was handed.
func TestAddReportsAWriteFailure(t *testing.T) {
	s := worktree.New(filepath.Join(t.TempDir(), "no-such-dir", "s.worktrees.json"))
	if err := s.Add("/tmp/tree"); err == nil {
		t.Fatal("adding to an unwritable store should have failed")
	}
}

// TestConcurrentAdds checks two clients opening a worktree at the same moment
// both land in the set — the reason the store carries a lock at all.
func TestConcurrentAdds(t *testing.T) {
	s, path := newStore(t)
	base := t.TempDir()

	const n = 8
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Add(filepath.Join(base, string(rune('a'+i)))); err != nil {
				t.Errorf("Add: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := s.Paths(); len(got) != n {
		t.Fatalf("%d concurrent adds should leave %d entries, got %v", n, n, got)
	}
	if !slices.IsSorted(s.Paths()) {
		t.Fatalf("the persisted set should be sorted, got %v", s.Paths())
	}

	// The file itself carries the current schema, so a reader can tell what wrote it.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var r struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Schema != worktree.Schema {
		t.Fatalf("schema = %d, want %d", r.Schema, worktree.Schema)
	}
}
