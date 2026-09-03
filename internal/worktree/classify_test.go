package worktree_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cmj0121/baton/internal/worktree"
)

// statusOf finds one path in a classification, failing if it is not there.
func statusOf(t *testing.T, entries []worktree.Entry, path string) worktree.Entry {
	t.Helper()
	for _, e := range entries {
		if e.Path == path {
			return e
		}
	}
	t.Fatalf("no entry for %q in %+v", path, entries)
	return worktree.Entry{}
}

// TestClassifyThreeWay is the classification in one pass: a tree a running panel
// works in is live, a tree only an exited panel names is a dead slot, and a tree
// nobody names is the orphan. All three are asserted together because the whole
// point is telling them APART — a classifier that answered "orphan" to
// everything would pass any test that only looked at the orphan.
func TestClassifyThreeWay(t *testing.T) {
	base := realTempDir(t)
	running := filepath.Join(base, "running")
	closed := filepath.Join(base, "closed")
	nobody := filepath.Join(base, "nobody")
	for _, d := range []string{running, closed, nobody} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	got := worktree.Classify(
		[]string{running, closed, nobody},
		[]worktree.Owner{{Dir: running}, {Dir: closed, Exited: true}},
	)

	if s := statusOf(t, got, running).Status; s != worktree.StatusLive {
		t.Errorf("a tree a running panel works in should be live, got %q", s)
	}
	if s := statusOf(t, got, closed).Status; s != worktree.StatusDeadSlot {
		t.Errorf("a tree only an exited panel names should be a dead slot, got %q", s)
	}
	if s := statusOf(t, got, nobody).Status; s != worktree.StatusOrphan {
		t.Errorf("a tree no panel names should be the orphan, got %q", s)
	}
}

// TestClassifyMatchesAnUnresolvedWorkdir is the assertion that keeps a sweep from
// deleting a live agent's tree, and the one this file exists for.
//
// The store records a path the way git reports it — symlinks resolved — while a
// panel's spawn spec keeps the spelling its caller passed. On macOS those differ
// for every tree under a temp dir (/var is a symlink to /private/var). Compare
// them as raw strings and the live panel's claim matches nothing, so its tree is
// classified an orphan and the sweep removes the ground from under a running
// agent.
//
// The test is built so it CAN fail: the owner is handed the path through the
// symlink while the stamped path is the resolved one, so it passes only if
// Classify canonicalises. Drop the canonical() call in Classify and this reports
// "orphan".
func TestClassifyMatchesAnUnresolvedWorkdir(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "tree"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	stamped, err := filepath.EvalSymlinks(filepath.Join(real, "tree"))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	viaLink := filepath.Join(link, "tree")
	if viaLink == stamped {
		t.Skip("the temp root is not reached through a symlink here, so the two spellings cannot differ")
	}

	got := worktree.Classify([]string{stamped}, []worktree.Owner{{Dir: viaLink}})
	if s := statusOf(t, got, stamped).Status; s != worktree.StatusLive {
		t.Fatalf("a live panel's tree spelled through a symlink must still be live, got %q — a sweep would delete it", s)
	}
}

// TestClassifyLiveBeatsDeadSlot pins the tie-break: two panels on one tree, one
// still running, is live whatever order they arrive in. The running agent is what
// matters, and an exited slot beside it must not downgrade the answer to
// something a sweep would later act on.
func TestClassifyLiveBeatsDeadSlot(t *testing.T) {
	tree := filepath.Join(realTempDir(t), "shared")
	for _, owners := range [][]worktree.Owner{
		{{Dir: tree, Exited: true}, {Dir: tree}},
		{{Dir: tree}, {Dir: tree, Exited: true}},
	} {
		got := worktree.Classify([]string{tree}, owners)
		if s := statusOf(t, got, tree).Status; s != worktree.StatusLive {
			t.Fatalf("owners %+v should classify as live, got %q", owners, s)
		}
	}
}

// TestClassifyReportsOnlyStampedPaths is the fence the whole feature rests on: a
// tree the operator made by hand is in no record, so it is in no result, however
// many panels point at it. Nothing a sweep reads can name it, so nothing a sweep
// does can reach it.
func TestClassifyReportsOnlyStampedPaths(t *testing.T) {
	base := realTempDir(t)
	stamped := filepath.Join(base, "baton-made")
	theirs := filepath.Join(base, "hand-made")

	got := worktree.Classify([]string{stamped}, []worktree.Owner{{Dir: theirs}})
	if len(got) != 1 || got[0].Path != stamped {
		t.Fatalf("only the stamped path should be classified, got %+v", got)
	}
	if got[0].Status != worktree.StatusOrphan {
		t.Fatalf("a panel in someone else's tree does not claim the stamped one, got %q", got[0].Status)
	}
}

// TestClassifyEmptyRecordClassifiesNothing is the fail-safe direction stated as an
// assertion: persistence off means no state file, which means no record, which
// means a sweep has NOTHING to act on. The catastrophic reading of the same
// situation — no record, so sweep everything — would show up here as entries
// appearing out of a set that has none.
func TestClassifyEmptyRecordClassifiesNothing(t *testing.T) {
	owners := []worktree.Owner{{Dir: realTempDir(t)}, {Dir: realTempDir(t), Exited: true}}
	for name, stamped := range map[string][]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			if got := worktree.Classify(stamped, owners); len(got) != 0 {
				t.Fatalf("no record must classify nothing, got %+v", got)
			}
		})
	}
}

// TestClassifyTracksWhetherTheTreeIsStillThere covers the case Remove's own doc
// calls ordinary: an operator deletes the directory outside baton, so a recorded
// path names nothing. It is still classified — the record is what the sweep
// reads — but Exists says the disk half is already done.
func TestClassifyTracksWhetherTheTreeIsStillThere(t *testing.T) {
	base := realTempDir(t)
	there := filepath.Join(base, "there")
	if err := os.MkdirAll(there, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	gone := filepath.Join(base, "gone")

	got := worktree.Classify([]string{there, gone}, nil)
	if !statusOf(t, got, there).Exists {
		t.Error("a tree still on disk should report Exists")
	}
	if statusOf(t, got, gone).Exists {
		t.Error("a tree already deleted should not report Exists")
	}
	if s := statusOf(t, got, gone).Status; s != worktree.StatusOrphan {
		t.Errorf("a recorded path nothing points at is an orphan whether or not it is there, got %q", s)
	}
}

// TestClassifyIgnoresAPanelWithNoWorkdir checks the empty Dir is skipped rather
// than resolved. filepath.Abs("") is the DAEMON's own working directory, so a
// panel with no workdir would otherwise stake a claim on wherever baton was
// started — and silently protect, or fail to protect, a tree by accident.
func TestClassifyIgnoresAPanelWithNoWorkdir(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	got := worktree.Classify([]string{real}, []worktree.Owner{{Dir: ""}})
	if s := statusOf(t, got, real).Status; s != worktree.StatusOrphan {
		t.Fatalf("a panel with no workdir claims nothing, got %q for the daemon's own cwd", s)
	}
}
