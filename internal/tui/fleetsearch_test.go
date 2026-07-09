package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// TestFleetSearchOpensPrompt checks / on the dashboard opens the fleet-search term
// prompt, seeded with the last term.
func TestFleetSearchOpensPrompt(t *testing.T) {
	m := baseModel()
	m.fleet = []panel.Panel{{ID: "a1", Kind: panel.Agent, Title: "claude", State: panel.Running}}
	m.fsQuery = "panic"

	m = press(m, keyFleetSearch)
	if m.input != inputFleetSearch {
		t.Fatalf("/ should open the fleet-search prompt, got input %v", m.input)
	}
	if m.inputBuf != "panic" {
		t.Fatalf("the prompt should seed with the last term, got %q", m.inputBuf)
	}
}

// TestFleetSearchSendsQuery checks committing a term sends fleet.search with the
// term and stashes it for the results view.
func TestFleetSearchSendsQuery(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = []panel.Panel{{ID: "a1", Kind: panel.Agent, Title: "claude", State: panel.Running}}

	m = press(m, keyFleetSearch)
	m.inputBuf = "traceback"
	m = press(m, "enter")

	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "fleet.search" })
	if got.Query != "traceback" {
		t.Fatalf("fleet.search should carry the term, got %+v", got)
	}
	if m.fsQuery != "traceback" {
		t.Fatalf("the term should be stashed for the results view, got %q", m.fsQuery)
	}
}

// TestFleetSearchBlankClears checks a blank term sends nothing and clears.
func TestFleetSearchBlankClears(t *testing.T) {
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c

	m = press(m, keyFleetSearch)
	m.inputBuf = "   "
	m = press(m, "enter")

	select {
	case got := <-cmds:
		if got.Action == "fleet.search" {
			t.Fatalf("a blank term must not send fleet.search, got %+v", got)
		}
	default:
	}
	if m.status != "fleet search cleared" {
		t.Fatalf("expected the cleared status, got %q", m.status)
	}
}

// TestFleetSearchResultsOpen checks a "search" reply with hits opens the results
// popup, and j/k walk the hits.
func TestFleetSearchResultsOpen(t *testing.T) {
	m := baseModel()
	m.fsQuery = "err"
	m.applyEvent(proto.ServerMsg{Type: "search", Hits: []proto.SearchHit{
		{Panel: "a1", Title: "claude · auth", Group: "api", Text: "an error occurred"},
		{Panel: "a1", Title: "claude · auth", Group: "api", Text: "error: retrying"},
		{Panel: "b2", Title: "codex · db", Text: "no error path"},
	}})
	if m.mode != modeFleetSearch {
		t.Fatalf("a search reply with hits should open the results popup, got mode %v", m.mode)
	}
	if m.fsCursor != 0 {
		t.Fatalf("the results should open on the first hit, got %d", m.fsCursor)
	}

	m = press(m, "j")
	if m.fsCursor != 1 {
		t.Fatalf("j should move to the next hit, got %d", m.fsCursor)
	}
	m = press(m, "k")
	if m.fsCursor != 0 {
		t.Fatalf("k should move back, got %d", m.fsCursor)
	}
	// Clamp at the top.
	m = press(m, "k")
	if m.fsCursor != 0 {
		t.Fatalf("k at the top should clamp, got %d", m.fsCursor)
	}
	// The view renders without panicking, showing the panel a hit belongs to.
	if !strings.Contains(m.View(), "auth") {
		t.Fatal("the results view should render its panel-group header")
	}
}

// TestFleetSearchNoHits checks a "search" reply with no hits stays out of the popup
// and reports no match.
func TestFleetSearchNoHits(t *testing.T) {
	m := baseModel()
	m.fleet = []panel.Panel{{ID: "a1", Kind: panel.Agent, Title: "claude", State: panel.Running}}
	m.fsQuery = "ghost"
	m.applyEvent(proto.ServerMsg{Type: "search", Hits: nil})
	if m.mode == modeFleetSearch {
		t.Fatal("no hits must not open the results popup")
	}
	if !strings.Contains(m.status, "no match") {
		t.Fatalf("expected a no-match status, got %q", m.status)
	}
}

// TestFleetSearchJumpZooms checks enter on a hit zooms its panel and arms the
// scrollback-search seed so the zoom opens on the match.
func TestFleetSearchJumpZooms(t *testing.T) {
	c, _ := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = []panel.Panel{{ID: "b2", Kind: panel.Agent, Title: "codex · db", State: panel.Running}}
	m.fsQuery = "err"
	m.mode = modeFleetSearch
	m.fsHits = []proto.SearchHit{{Panel: "b2", Title: "codex · db", Text: "no error path"}}
	m.fsCursor = 0

	m = press(m, "enter")
	if m.mode != modeZoom {
		t.Fatalf("enter should zoom the hit's panel, got mode %v", m.mode)
	}
	if m.zoomID != "b2" {
		t.Fatalf("the wrong panel was zoomed: %q", m.zoomID)
	}
	if !m.searchSeedPending {
		t.Fatal("the scrollback-search seed should be armed for the zoom")
	}
	if m.fsHits != nil {
		t.Fatal("the results should be dropped after jumping")
	}
}

// TestFleetSearchJumpMissingPanel checks jumping to a hit whose panel has since
// closed reports it rather than crashing.
func TestFleetSearchJumpMissingPanel(t *testing.T) {
	m := baseModel()
	m.fsQuery = "err"
	m.mode = modeFleetSearch
	m.fsHits = []proto.SearchHit{{Panel: "gone", Title: "closed", Text: "err here"}}
	m.fsCursor = 0

	m = press(m, "enter")
	if m.mode != modeFleetSearch {
		t.Fatalf("a missing panel should keep the results open, got mode %v", m.mode)
	}
	if !strings.Contains(m.status, "gone") {
		t.Fatalf("expected a gone-panel note, got %q", m.status)
	}
}

// TestFleetSearchEscCloses checks esc restores the view the popup opened over.
func TestFleetSearchEscCloses(t *testing.T) {
	m := baseModel()
	m.fleet = []panel.Panel{{ID: "a1", Kind: panel.Agent, Title: "claude", State: panel.Running}}
	m.fsQuery = "err"
	m.applyEvent(proto.ServerMsg{Type: "search", Hits: []proto.SearchHit{{Panel: "a1", Title: "claude", Text: "err"}}})
	if m.mode != modeFleetSearch {
		t.Fatalf("setup: expected the results popup, got %v", m.mode)
	}
	m = press(m, "esc")
	if m.mode != modeDashboard {
		t.Fatalf("esc should restore the dashboard, got %v", m.mode)
	}
	if m.fsHits != nil {
		t.Fatal("esc should drop the captured hits")
	}
}

// TestCountHitPanels checks the distinct-panel count used in the header.
func TestCountHitPanels(t *testing.T) {
	hits := []proto.SearchHit{{Panel: "a"}, {Panel: "a"}, {Panel: "b"}}
	if got := countHitPanels(hits); got != 2 {
		t.Fatalf("countHitPanels = %d, want 2", got)
	}
}
