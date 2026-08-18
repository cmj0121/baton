package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// TestShortPathKeepsTheTail: every worktree under one repo shares a prefix and
// differs at the end, so a card that has to drop something drops the front.
func TestShortPathKeepsTheTail(t *testing.T) {
	if got := shortPath("/a/b/c", 40); got != "/a/b/c" {
		t.Errorf("a path that fits should be untouched, got %q", got)
	}
	got := shortPath("/very/long/prefix/that/will/not/fit/api-worktree", 20)
	if !strings.Contains(got, "worktree") && !strings.HasPrefix(got, "…/") && !strings.HasPrefix(got, "api-") {
		t.Errorf("shortPath dropped the identifying tail: %q", got)
	}
	if len([]rune(got)) > 20 {
		t.Errorf("shortPath = %q, wider than the 20 it was given", got)
	}
	// A single component with nowhere left to cut is truncated rather than lost.
	if got := shortPath("/"+strings.Repeat("x", 40), 10); len([]rune(got)) > 10 {
		t.Errorf("a long single component was not clamped: %q", got)
	}
}

// TestShortPathAbbreviatesHome: "~" is what a user calls their home directory.
func TestShortPathAbbreviatesHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory on this host")
	}
	if got := shortPath(home, 40); got != "~" {
		t.Errorf("shortPath(home) = %q, want ~", got)
	}
	if got := shortPath(filepath.Join(home, "work"), 40); got != "~/work" {
		t.Errorf("shortPath = %q, want ~/work", got)
	}
}

// TestPanelCardShowsTheDirectory: fifty panels called "shell #1"…"#50" are
// indistinguishable; the path is what tells them apart.
func TestPanelCardShowsTheDirectory(t *testing.T) {
	m := model{width: 200, height: 30}
	with := m.rowOfPanel(panel.Panel{ID: "p1", Kind: panel.Shell, Title: "shell #1", Cwd: "/srv/api-worktree"})
	if !strings.Contains(with, "api-work") {
		t.Errorf("the card should carry the directory:\n%s", with)
	}
	// A panel with no known directory renders exactly as it did before.
	without := m.rowOfPanel(panel.Panel{ID: "p1", Kind: panel.Shell, Title: "shell #1"})
	if strings.Contains(without, "…") {
		t.Errorf("an unknown directory should add nothing:\n%s", without)
	}
	if lines := strings.Count(with, "\n"); lines != strings.Count(without, "\n") {
		t.Errorf("the card must stay the same height: %d vs %d", lines, strings.Count(without, "\n"))
	}
}

// TestCwdSourceFollowsTheFocus: "here" means the panel the cockpit is pointed at
// — the zoomed one, the focused member of a split, or the selected card.
func TestCwdSourceFollowsTheFocus(t *testing.T) {
	m := model{
		width: 200, height: 30,
		fleet: []panel.Panel{
			{ID: "p1", Kind: panel.Shell, Title: "one", Cwd: "/one"},
			{ID: "p2", Kind: panel.Shell, Title: "two", Cwd: "/two"},
		},
	}

	m.cursor = 1
	if p, ok := m.cwdSource(); !ok || p.ID != "p2" {
		t.Errorf("dashboard: got %+v/%v, want the selected card", p, ok)
	}

	m.mode, m.zoomID = modeZoom, "p1"
	if p, ok := m.cwdSource(); !ok || p.ID != "p1" {
		t.Errorf("zoom: got %+v/%v, want the zoomed panel", p, ok)
	}

	empty := model{width: 200, height: 30}
	if _, ok := empty.cwdSource(); ok {
		t.Error("an empty dashboard has no here to open in")
	}
}

// TestSpawnPanelHereFallsBackLoudly: a panel whose directory is unknown opens in
// the default workdir and says so, rather than opening somewhere unintended in
// silence.
func TestSpawnPanelHereFallsBackLoudly(t *testing.T) {
	m := model{
		width: 200, height: 30,
		fleet: []panel.Panel{{ID: "p1", Kind: panel.Shell, Title: "one"}},
	}
	got := m.spawnPanelHere()
	if !strings.Contains(got.status, "not known") {
		t.Fatalf("status = %q, want it to say the directory is unknown", got.status)
	}

	m.fleet[0].Cwd = "/srv/app"
	if got := m.spawnPanelHere(); !strings.Contains(got.status, "/srv/app") {
		t.Fatalf("status = %q, want the directory it is spawning in", got.status)
	}
}
