package tui

import (
	"strings"
	"testing"
)

// TestGitMenuNavAndEnter exercises the cursor nav (↑↓ j/k) and enter running the
// highlighted row — enter on the top row (diff) opens the diff view.
func TestGitMenuNavAndEnter(t *testing.T) {
	m, _ := zoomedAgent(t)
	m = openGitMenu(t, m)

	nm, _ := m.handleGitKey("down")
	m = nm.(model)
	if m.gitCursor != 1 {
		t.Fatalf("down should move the cursor to 1, got %d", m.gitCursor)
	}
	nm, _ = m.handleGitKey("j")
	m = nm.(model)
	if m.gitCursor != 2 {
		t.Fatalf("j should move the cursor to 2, got %d", m.gitCursor)
	}
	nm, _ = m.handleGitKey("up")
	m = nm.(model)
	nm, _ = m.handleGitKey("k")
	m = nm.(model)
	if m.gitCursor != 0 {
		t.Fatalf("up then k should return to the top, got %d", m.gitCursor)
	}

	// enter on the top row (diff) closes the menu back to the zoom.
	nm, _ = m.handleGitKey("enter")
	m = nm.(model)
	if m.mode != modeZoom {
		t.Fatalf("enter on diff should close to the zoom, got mode=%v", m.mode)
	}
}

// TestGitMenuNavWrapsUp: ↑ from the top wraps to the last row.
func TestGitMenuNavWrapsUp(t *testing.T) {
	m, _ := zoomedAgent(t)
	m = openGitMenu(t, m)
	nm, _ := m.handleGitKey("up")
	m = nm.(model)
	if m.gitCursor != len(gitMenu)-1 {
		t.Fatalf("up from the top should wrap to the last row, got %d", m.gitCursor)
	}
}

// TestGitMenuStrayKeyIgnored: an unbound key never leaves the menu.
func TestGitMenuStrayKeyIgnored(t *testing.T) {
	m, _ := zoomedAgent(t)
	m = openGitMenu(t, m)
	nm, _ := m.handleGitKey("z")
	m = nm.(model)
	if m.mode != modeGit {
		t.Fatalf("a stray key should keep the menu open, got mode=%v", m.mode)
	}
}

// TestGitMenuDiffOpens: d reuses the diff path and returns to the zoom.
func TestGitMenuDiffOpens(t *testing.T) {
	m, _ := zoomedAgent(t)
	m = openGitMenu(t, m)
	nm, _ := m.handleGitKey("d")
	m = nm.(model)
	if m.mode != modeZoom {
		t.Fatalf("d should close the menu back to the zoom, got mode=%v", m.mode)
	}
}

// TestGitEphOps exercises the remaining immediate ephemeral ops (status, stage
// all, worktree-list). The test name is kept short so the recording server's unix
// socket path stays under the OS limit. Subtests are avoided for the same reason.
func TestGitEphOps(t *testing.T) {
	cases := []struct {
		key, op string
	}{
		{"s", "status"},
		{"a", "add"},
		{"W", "worktree-list"},
	}
	for _, tc := range cases {
		m, cmds := zoomedAgent(t)
		m = openGitMenu(t, m)
		nm, _ := m.handleGitKey(tc.key)
		m = nm.(model)
		waitCmd(t, cmds, isGit(tc.op))
		if m.mode != modeZoom {
			t.Fatalf("%s should close to the zoom, got mode=%v", tc.op, m.mode)
		}
	}
}

// TestGitEphBlocked: the menu refuses to open on a transient (diff/git output)
// view. Name kept short for the unix-socket path limit.
func TestGitEphBlocked(t *testing.T) {
	m, _ := zoomedAgent(t)
	m.zoomEphemeral = true
	nm, _ := m.openGitPicker()
	m = nm.(model)
	if m.mode == modeGit {
		t.Fatal("the git menu must not open on an ephemeral view")
	}
	if !strings.Contains(m.status, "not available") {
		t.Fatalf("expected a not-available hint, got %q", m.status)
	}
}

// TestGitBranchEmptyReopens: an empty branch name keeps the field open rather than
// firing an op with no name.
func TestGitBranchEmptyReopens(t *testing.T) {
	m, _ := zoomedAgent(t)
	m = openGitMenu(t, m)
	nm, _ := m.handleGitKey("b")
	m = nm.(model)
	nm, _ = m.commitGitBranch("")
	m = nm.(model)
	if m.input != inputGitBranch {
		t.Fatalf("an empty branch name should reopen the field, got input=%v", m.input)
	}
}

// TestGitWorktreeEmptyReopens: an empty worktree branch reopens the field.
func TestGitWorktreeEmptyReopens(t *testing.T) {
	m, _ := zoomedAgent(t)
	m = openGitMenu(t, m)
	nm, _ := m.handleGitKey("w")
	m = nm.(model)
	nm, _ = m.commitGitWorktree("")
	m = nm.(model)
	if m.input != inputGitWorktree {
		t.Fatalf("an empty worktree branch should reopen the field, got input=%v", m.input)
	}
}

// TestGitRemoveEmptyReopens: an empty path reopens the field.
func TestGitRemoveEmptyReopens(t *testing.T) {
	m, _ := zoomedAgent(t)
	m = openGitMenu(t, m)
	nm, _ := m.handleGitKey("x")
	m = nm.(model)
	nm, _ = m.commitGitRemove("")
	m = nm.(model)
	if m.input != inputGitRemove {
		t.Fatalf("an empty remove path should reopen the field, got input=%v", m.input)
	}
}

// TestRunGitConfirmedUnknown: an unrecognised parked op just returns to the zoom.
func TestRunGitConfirmedUnknown(t *testing.T) {
	m, _ := zoomedAgent(t)
	m = openGitMenu(t, m)
	nm, _ := m.runGitConfirmed("bogus")
	m = nm.(model)
	if m.mode != modeZoom {
		t.Fatalf("an unknown confirmed op should return to the zoom, got mode=%v", m.mode)
	}
}

// TestGitPickerViewRenders draws the menu, and separately the confirm variant.
func TestGitPickerViewRenders(t *testing.T) {
	m, _ := zoomedAgent(t)
	m = openGitMenu(t, m)
	out := m.gitPickerView()
	for _, want := range []string{"diff", "worktree", "claude"} {
		if !strings.Contains(out, want) {
			t.Fatalf("git view should contain %q, got:\n%s", want, out)
		}
	}

	// A parked confirm swaps the legend for a y/n prompt.
	nm, _ := m.handleGitKey("p")
	m = nm.(model)
	confirm := m.gitPickerView()
	if !strings.Contains(confirm, "confirm") {
		t.Fatalf("a parked confirm should render a confirm legend, got:\n%s", confirm)
	}
}
