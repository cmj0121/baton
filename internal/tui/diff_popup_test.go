package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// sampleDiffFiles is a small, representative change set: a staged-only file, an
// unstaged-only file, and an untracked file, the last with a long body so the
// detail pane has something to scroll.
func sampleDiffFiles() []proto.DiffFile {
	long := strings.Builder{}
	long.WriteString("new file: big.txt\n")
	for i := 0; i < 60; i++ {
		long.WriteString("+line\n")
	}
	return []proto.DiffFile{
		{Path: "a.go", Index: "M", Staged: "diff --git a/a.go b/a.go\n@@ -1 +1,2 @@\n ctx\n+added\n"},
		{Path: "b.go", Work: "M", Unstaged: "diff --git a/b.go b/b.go\n@@ -1 +1 @@\n-old\n+new\n"},
		{Path: "big.txt", Index: "?", Work: "?", Unstaged: long.String()},
	}
}

func diffModel() model {
	return model{width: 120, height: 40, mode: modeDiff, diffFrom: modeDashboard, diffFiles: sampleDiffFiles(), diffTitle: "diff · agent"}
}

func TestOpenDiffPopup(t *testing.T) {
	m := model{width: 120, height: 40, mode: modeDashboard}.openDiffPopup("diff · agent", sampleDiffFiles())
	if m.mode != modeDiff {
		t.Fatalf("openDiffPopup should enter modeDiff, got %v", m.mode)
	}
	if m.diffFrom != modeDashboard || len(m.diffFiles) != 3 || m.diffCursor != 0 {
		t.Fatalf("popup state wrong: from=%v files=%d cursor=%d", m.diffFrom, len(m.diffFiles), m.diffCursor)
	}

	// An empty set never opens the popup.
	empty := model{mode: modeDashboard}.openDiffPopup("x", nil)
	if empty.mode != modeDashboard {
		t.Fatalf("an empty diff should not open the popup, mode=%v", empty.mode)
	}
}

func TestDiffPopupNavigateFiles(t *testing.T) {
	m := diffModel()
	m = press(m, "j")
	if m.diffCursor != 1 {
		t.Fatalf("j should move to file 1, got %d", m.diffCursor)
	}
	m = press(m, "j", "j") // clamps at the last file
	if m.diffCursor != 2 {
		t.Fatalf("cursor should clamp at the last file, got %d", m.diffCursor)
	}
	m = press(m, "k")
	if m.diffCursor != 1 {
		t.Fatalf("k should move back to file 1, got %d", m.diffCursor)
	}
}

func TestDiffPopupPaneAndScroll(t *testing.T) {
	m := diffModel()
	m = press(m, "j", "j") // select big.txt, whose detail is long
	if m.diffOnDetail {
		t.Fatal("focus should start on the file list")
	}
	// j on the file list does not scroll the detail.
	if m.diffScroll != 0 {
		t.Fatalf("detail should be at the top, got %d", m.diffScroll)
	}
	m = press(m, "tab")
	if !m.diffOnDetail {
		t.Fatal("tab should move focus to the detail pane")
	}
	m = press(m, "G") // jump to the bottom
	maxOff := len(m.diffDetailLines()) - m.diffViewportRows()
	if maxOff < 1 {
		t.Fatalf("test fixture should be scrollable, maxOff=%d", maxOff)
	}
	if m.diffScroll != maxOff {
		t.Fatalf("G should scroll to the bottom %d, got %d", maxOff, m.diffScroll)
	}
	m = press(m, "g") // back to the top
	if m.diffScroll != 0 {
		t.Fatalf("g should scroll to the top, got %d", m.diffScroll)
	}
	// Switching files resets the detail scroll.
	m = press(m, "tab", "k", "tab", "G") // move file selection, then scroll
	m = press(m, "tab")                  // back to file list
	m = press(m, "k")                    // change file
	if m.diffScroll != 0 {
		t.Fatalf("changing files should reset the detail scroll, got %d", m.diffScroll)
	}
}

func TestDiffPopupClose(t *testing.T) {
	m := diffModel()
	m = press(m, "esc")
	if m.mode != modeDashboard {
		t.Fatalf("esc should restore the originating view, got %v", m.mode)
	}
	if m.diffFiles != nil {
		t.Fatal("closing should drop the captured files")
	}
}

func TestDiffView(t *testing.T) {
	out := diffModel().diffView()
	for _, want := range []string{"D I F F", "a.go", "b.go", "big.txt", "staged"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff view missing %q", want)
		}
	}
	// A tiny terminal must still render without panicking.
	small := model{width: 30, height: 10, mode: modeDiff, diffFiles: sampleDiffFiles(), diffTitle: "diff"}
	if small.diffView() == "" {
		t.Fatal("diff view should render on a small terminal")
	}
}
