package paths

import (
	"os"
	"path/filepath"
	"testing"
)

// conductorstamp_test.go covers #100: the boot-time sweep removed a legacy
// workspace and left its boot stamp behind, forever. Measured on a real
// machine: 6 workspaces, 28 stamps, 22 orphans.
//
// The stamp's own doc says it PAIRS with a workspace and
// RemoveConductorWorkspace takes both — the sweep was the one path that took
// only the directory, and the list it walks filters stamps out, so it could not
// see them even in principle.

// stampFixture lays out a base directory with a live workspace, a stale one,
// and an orphaned stamp whose workspace is already gone.
func stampFixture(t *testing.T) (base, live string) {
	t.Helper()
	base = t.TempDir()
	live = filepath.Join(base, "conductor-live")
	for _, dir := range []string{live, filepath.Join(base, "conductor-stale")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(ConductorStampFile(dir), []byte("1"), 0o600); err != nil {
			t.Fatalf("stamp %s: %v", dir, err)
		}
	}
	// An orphan from a previous sweep: the stamp with no workspace.
	if err := os.WriteFile(filepath.Join(base, "conductor-orphan.boot"), []byte("1"), 0o600); err != nil {
		t.Fatalf("orphan stamp: %v", err)
	}
	return base, live
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return err == nil
}

// TestRemovingAWorkspaceTakesItsStamp is the pairing, asserted directly.
func TestRemovingAWorkspaceTakesItsStamp(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "conductor-x")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConductorStampFile(ws), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveConductorWorkspace(ws); err != nil {
		t.Fatalf("RemoveConductorWorkspace: %v", err)
	}
	if exists(t, ws) {
		t.Error("the workspace survived")
	}
	if exists(t, ConductorStampFile(ws)) {
		t.Error("the stamp was left behind")
	}
}

// TestRemovingAStamplessWorkspaceIsNotAnError covers the workspace written by
// an older build, or one whose stamp never landed. A sweep that treated this as
// a failure would log a warning the operator can do nothing about — and, if the
// caller stops on error, would abandon the rest of the list.
func TestRemovingAStamplessWorkspaceIsNotAnError(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "conductor-nostamp")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RemoveConductorWorkspace(ws); err != nil {
		t.Errorf("a workspace with no stamp should remove cleanly, got %v", err)
	}
}

// TestOrphanedStampsAreCollected is the half the issue is actually about: the
// stamps already on disk are not in the workspace list, so stopping the leak
// does not clear what has leaked.
func TestOrphanedStampsAreCollected(t *testing.T) {
	base, live := stampFixture(t)

	got := LegacyConductorLeaks(base, live)

	want := map[string]bool{
		filepath.Join(base, "conductor-stale"):       true,
		filepath.Join(base, "conductor-orphan.boot"): true,
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("the sweep would remove %q, which is not a leak", p)
		}
		delete(want, p)
	}
	for p := range want {
		t.Errorf("the sweep leaves %q behind", p)
	}
}

// TestTheLiveWorkspaceAndItsStampAreNeverSwept is the assertion that matters
// most. The stamp is what decides whether a workspace belongs to this host
// boot, so a sweep that took the live one would make the running conductor's
// own directory look foreign on the next start.
func TestTheLiveWorkspaceAndItsStampAreNeverSwept(t *testing.T) {
	base, live := stampFixture(t)

	for _, p := range LegacyConductorLeaks(base, live) {
		if p == live {
			t.Error("the sweep would remove the LIVE conductor workspace")
		}
		if p == ConductorStampFile(live) {
			t.Error("the sweep would remove the live workspace's boot stamp, orphaning the running conductor")
		}
	}
}

// TestRemoveConductorLeakTakesEitherShape pins that the sweep's one call
// handles both entries AllConductorLeaks returns. Passing a stamp to
// RemoveConductorWorkspace works today only because RemoveAll deletes a plain
// file and the stamp-of-a-stamp is tolerated as missing — two coincidences,
// neither of which is what that name says.
func TestRemoveConductorLeakTakesEitherShape(t *testing.T) {
	base, live := stampFixture(t)

	for _, leak := range LegacyConductorLeaks(base, live) {
		if err := RemoveConductorLeak(leak); err != nil {
			t.Errorf("RemoveConductorLeak(%s): %v", leak, err)
		}
		if exists(t, leak) {
			t.Errorf("%s survived its own removal", leak)
		}
	}
	// The stale workspace's stamp went with the directory, without being listed.
	if exists(t, ConductorStampFile(filepath.Join(base, "conductor-stale"))) {
		t.Error("a swept workspace left its stamp behind")
	}
	// …and the live pair is untouched.
	if !exists(t, live) || !exists(t, ConductorStampFile(live)) {
		t.Error("the live workspace or its stamp was swept")
	}
}
