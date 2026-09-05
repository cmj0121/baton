package main

import (
	"path/filepath"
	"testing"

	"github.com/cmj0121/baton/internal/config"
)

// TestHandWrittenDirsAreExpanded covers the two directories an operator writes
// into the config by hand and the daemon then acts on. Both were passed through
// raw while log-dir and score.dir beside them were expanded, and both land
// somewhere the field's own documentation says they will not:
//
//   - workdir "~/proj" is a LITERAL "~/proj" under whatever directory baton was
//     launched from, and that field promises a panel never inherits it.
//   - worktree-dir "trees" gets two anchors at once — git resolves it against the
//     repository, filepath.Abs resolves the recorded copy against the daemon's
//     cwd — so the record names a path that is not the tree, and the next sweep
//     drops the record of a worktree that is still on disk.
func TestHandWrittenDirsAreExpanded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	base := t.TempDir()
	t.Chdir(base)

	rc := reloadableSettings(config.Config{Panel: config.PanelDefaults{
		Workdir:     "~/proj",
		WorktreeDir: "trees",
	}})

	if want := filepath.Join(home, "proj"); rc.settings.DefaultDir != want {
		t.Errorf("workdir ~/proj resolved to %q, want %q — a tilde is the home directory, not a directory named '~'",
			rc.settings.DefaultDir, want)
	}
	if want := filepath.Join(base, "trees"); rc.settings.WorktreeDir != want {
		t.Errorf("worktree-dir trees resolved to %q, want %q — a relative base must be pinned before it reaches git",
			rc.settings.WorktreeDir, want)
	}
}

// TestAnAbsoluteConfigDirIsLeftAlone is the other half: expansion must not
// rewrite the ordinary case, which is what every operator with an absolute path
// in their config has.
func TestAnAbsoluteConfigDirIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	rc := reloadableSettings(config.Config{Panel: config.PanelDefaults{
		Workdir:     dir,
		WorktreeDir: dir,
	}})
	if rc.settings.DefaultDir != dir || rc.settings.WorktreeDir != dir {
		t.Errorf("absolute dirs came back as (%q, %q), want both %q",
			rc.settings.DefaultDir, rc.settings.WorktreeDir, dir)
	}
}

// TestUnsetDirsStayUnset keeps the two fallbacks reachable. An empty workdir is
// "the user's home" and an empty worktree-dir is "a sibling of the repo"; if
// expansion turned either into the daemon's cwd, every fleet without those keys
// would silently start inheriting the launch directory — the exact defect this
// change is fixing, arriving through the door it opened.
func TestUnsetDirsStayUnset(t *testing.T) {
	t.Chdir(t.TempDir())
	rc := reloadableSettings(config.Config{})
	if rc.settings.DefaultDir != "" || rc.settings.WorktreeDir != "" {
		t.Errorf("unset dirs came back as (%q, %q), want both empty",
			rc.settings.DefaultDir, rc.settings.WorktreeDir)
	}
}
