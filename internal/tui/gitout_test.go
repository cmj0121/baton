package tui

import (
	"strings"
	"testing"
)

// gitOutText is a multi-line body long enough that the popup has to scroll.
func gitOutText() string {
	b := strings.Builder{}
	for i := 0; i < 60; i++ {
		b.WriteString("commit line\n")
	}
	return b.String()
}

func TestOpenGitOutPopup(t *testing.T) {
	m := model{width: 120, height: 40, mode: modeDashboard}.openGitOutPopup("git log · agent", gitOutText(), false)
	if m.mode != modeGitOut {
		t.Fatalf("openGitOutPopup should enter modeGitOut, got %v", m.mode)
	}
	if m.gitOutFrom != modeDashboard || len(m.gitOutLines) != 60 || m.gitOutScroll != 0 {
		t.Fatalf("popup state wrong: from=%v lines=%d scroll=%d", m.gitOutFrom, len(m.gitOutLines), m.gitOutScroll)
	}
}

// TestGitOutEmptyOutput checks a silent op (e.g. `git add -A`) still opens, with a
// placeholder line so the body is never blank.
func TestGitOutEmptyOutput(t *testing.T) {
	m := model{width: 120, height: 40, mode: modeDashboard}.openGitOutPopup("git stage · agent", "   \n", false)
	if m.mode != modeGitOut {
		t.Fatalf("an empty op should still open the popup, mode=%v", m.mode)
	}
	if len(m.gitOutLines) != 1 || !strings.Contains(m.gitOutLines[0], "no output") {
		t.Fatalf("empty output should show a placeholder, got %v", m.gitOutLines)
	}
}

func TestGitOutScrollAndClose(t *testing.T) {
	m := model{width: 120, height: 40, mode: modeGitOut, gitOutFrom: modeDashboard, gitOutLines: strings.Split(strings.TrimRight(gitOutText(), "\n"), "\n")}

	rows := m.gitOutViewportRows()
	maxOff := len(m.gitOutLines) - rows

	// j scrolls down a line; G jumps to the bottom; g back to the top.
	m = press(m, "j")
	if m.gitOutScroll != 1 {
		t.Fatalf("j should scroll one line, got %d", m.gitOutScroll)
	}
	m = press(m, "G")
	if m.gitOutScroll != maxOff {
		t.Fatalf("G should rest the last line at the bottom (off %d), got %d", maxOff, m.gitOutScroll)
	}
	m = press(m, "g")
	if m.gitOutScroll != 0 {
		t.Fatalf("g should return to the top, got %d", m.gitOutScroll)
	}

	// k never scrolls above the top.
	m = press(m, "k")
	if m.gitOutScroll != 0 {
		t.Fatalf("k at the top should stay at 0, got %d", m.gitOutScroll)
	}

	// esc closes and restores the prior view, dropping the captured text.
	m = press(m, "esc")
	if m.mode != modeDashboard {
		t.Fatalf("esc should restore the dashboard, got %v", m.mode)
	}
	if m.gitOutLines != nil {
		t.Fatalf("close should drop the captured text, got %v", m.gitOutLines)
	}
}

// TestGitOutViewRenders checks the view renders without panicking and shows the
// header and a failure does not stop it.
func TestGitOutViewRenders(t *testing.T) {
	m := model{width: 100, height: 30, mode: modeGitOut, gitOutTitle: "git push · agent", gitOutFailed: true,
		gitOutLines: []string{"! [rejected] main -> main (fetch first)"}}
	out := m.gitOutView()
	if !strings.Contains(out, "push · agent") || !strings.Contains(out, "rejected") {
		t.Fatalf("the view should show the header and the output, got:\n%s", out)
	}
}
