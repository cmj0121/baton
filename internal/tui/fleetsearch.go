package tui

import (
	"fmt"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/proto"
)

// Fleet-wide search (modeFleetSearch). `/` on the dashboard opens a term prompt; on
// enter the term goes to the server (fleet.search), which scans every panel's
// retained output and replies with the matching lines. openFleetResults renders
// them grouped by panel; j/k (or n/N) walk the hits, enter zooms the hit's panel
// and re-runs the term there as a scrollback search, esc closes. It owns nothing
// server-side — the scan already ran and was reaped — so esc just drops the hits.
//
// This is the fleet counterpart of the two narrower searches: f filters the
// dashboard by title/group, C-t f searches one panel's scrollback; / greps the
// content of every panel at once.

// openFleetSearch opens the term prompt, seeded with the last term so repeating a
// fleet search is one keypress (like the scrollback search).
func (m model) openFleetSearch() model {
	m.input = inputFleetSearch
	m.inputBuf = m.fsQuery
	m.status = "fleet search · type a regexp · enter searches every panel · esc cancels"
	return m
}

// sendFleetSearch dispatches the term to the server and stashes it for the results
// view and the jump-to-hit handoff. A blank term clears rather than scans.
func (m model) sendFleetSearch(query string) (tea.Model, tea.Cmd) {
	query = strings.TrimSpace(query)
	if query == "" {
		m.status = "fleet search cleared"
		return m, nil
	}
	m.fsQuery = query
	m.sendf(proto.Command{Action: "fleet.search", Query: query})
	m.status = "fleet search · " + query + " …"
	return m, nil
}

// openFleetResults enters the results popup with the hits from a "search" reply,
// remembering the view to return to. No hits stays out of the popup and says so.
func (m model) openFleetResults(hits []proto.SearchHit) model {
	if len(hits) == 0 {
		m.status = fmt.Sprintf("fleet search · no match for %q", m.fsQuery)
		return m
	}
	m.fsFrom = m.mode
	m.mode = modeFleetSearch
	m.fsHits = hits
	m.fsCursor = 0
	m.status = m.fleetSearchStatus()
	return m
}

// closeFleetResults leaves the popup, restoring the view it opened over and
// dropping the captured hits so they cannot linger.
func (m model) closeFleetResults() (tea.Model, tea.Cmd) {
	m.mode = m.fsFrom
	m.fsHits = nil
	m.fsCursor = 0
	if m.mode == modeDashboard {
		m.status = "dashboard"
	}
	return m, nil
}

// handleFleetSearchKey drives the results popup: j/k and n/N walk the hits, home/end
// jump to the ends, enter opens the selected hit's panel, esc/q close.
func (m model) handleFleetSearchKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q":
		return m.closeFleetResults()
	case "enter":
		return m.jumpToHit()
	case "up", "k", "N":
		m.fsCursor = clampInt(m.fsCursor-1, 0, len(m.fsHits)-1)
	case "down", "j", "n":
		m.fsCursor = clampInt(m.fsCursor+1, 0, len(m.fsHits)-1)
	case "home", "g":
		m.fsCursor = 0
	case "end", "G":
		m.fsCursor = len(m.fsHits) - 1
	}
	m.status = m.fleetSearchStatus()
	return m, nil
}

// jumpToHit zooms the panel the selected hit is in and re-runs the term there as a
// scrollback search, so the view opens on the match. A panel that has since closed
// drops back to the results with a note.
func (m model) jumpToHit() (tea.Model, tea.Cmd) {
	if m.fsCursor < 0 || m.fsCursor >= len(m.fsHits) {
		return m, nil
	}
	h := m.fsHits[m.fsCursor]
	p, ok := m.fleetPanel(h.Panel)
	if !ok {
		m.status = "fleet search · that panel is gone"
		return m, nil
	}
	m.fsHits = nil
	m = m.zoomInto(p)
	m = m.clearSearch()        // drop any prior scrollback search before seeding the new one
	m.searchSeedPending = true // runSearch fires once the panel's replay lands (see panelOutputMsg)
	m.status = "zoomed · " + p.Title + " · search " + m.fsQuery
	return m, nil
}

// fleetSearchStatus is the footer line while walking the results.
func (m model) fleetSearchStatus() string {
	return fmt.Sprintf("fleet search %q · %d/%d · enter opens · esc closes", m.fsQuery, m.fsCursor+1, len(m.fsHits))
}

// fleetSearchView renders the results popup: the matching lines grouped under a
// header per panel, the selected hit carated and brightened, the matched term
// highlighted on every line, windowed around the cursor so a long result scrolls.
func (m model) fleetSearchView() string {
	if len(m.fsHits) == 0 {
		return configBox(mutedStyle.Render("no matches"))
	}
	rows, anchor := m.fleetSearchRows()
	visible := clampInt(m.height-12, 5, 40)
	shown, _ := windowAround(rows, anchor, visible)

	header := sectionStyle.Render(spaced("FLEET SEARCH")) + "  " +
		mutedStyle.Render(fmt.Sprintf("%q  ·  %d hit(s) in %d panel(s)", m.fsQuery, len(m.fsHits), countHitPanels(m.fsHits)))
	legendLine := legend("j/k", "move", "n/N", "walk", "enter", "open panel", "esc", "close")
	body := lipgloss.JoinVertical(lipgloss.Left, shown...)
	return configBox(lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", legendLine))
}

// fleetSearchRows builds the styled result rows — a header line when the panel
// changes, then one row per hit — and returns the display-row index of the selected
// hit so the view can window around it.
func (m model) fleetSearchRows() (rows []string, anchor int) {
	re, _ := compileSearch(m.fsQuery)
	width := m.fleetSearchWidth()
	lastPanel := ""
	for i, h := range m.fsHits {
		if h.Panel != lastPanel {
			lastPanel = h.Panel
			rows = append(rows, fleetHeaderRow(h, width))
		}
		selected := i == m.fsCursor
		if selected {
			anchor = len(rows)
		}
		rows = append(rows, fleetHitRow(h, re, width, selected))
	}
	return rows, anchor
}

// fleetSearchWidth is the popup's inner content width, bounded and never wider than
// the screen.
func (m model) fleetSearchWidth() int {
	return min(clampInt(m.width-16, 24, 140), max(1, m.width-8))
}

// fleetHeaderRow renders a panel-group header: a marker, the panel title, and its
// work item when grouped, in the highlight blue.
func fleetHeaderRow(h proto.SearchHit, width int) string {
	label := h.Title
	if h.Group != "" {
		label += "  ·  " + h.Group
	}
	return lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).Render(truncate("◈ "+label, max(1, width)))
}

// fleetHitRow renders one matched line: a caret when selected, the line text in ink
// (selected) or muted (the rest), with the matched term highlighted throughout.
func fleetHitRow(h proto.SearchHit, re *regexp.Regexp, width int, selected bool) string {
	caret, baseFg := "    ", colMuted
	if selected {
		caret = "  " + lipgloss.NewStyle().Foreground(colBrand).Bold(true).Render("▸ ")
		baseFg = colInk
	}
	return caret + fleetHighlight(strings.TrimSpace(h.Text), re, max(1, width-4), baseFg, colBrandHi)
}

// fleetHighlight clips a plain line to width cells and renders it in baseFg with
// each match of re in hiFg. Segments are styled independently, so no program styling
// interleaves and the colours compose cleanly (unlike the scrollback search, which
// works over already-rendered rows). A nil re just colours the whole line.
func fleetHighlight(s string, re *regexp.Regexp, width int, baseFg, hiFg lipgloss.Color) string {
	s = clipCells(s, width)
	base := lipgloss.NewStyle().Foreground(baseFg)
	if re == nil {
		return base.Render(s)
	}
	hi := lipgloss.NewStyle().Foreground(hiFg).Bold(true)
	var b strings.Builder
	i := 0
	for _, loc := range re.FindAllStringIndex(s, -1) {
		if loc[0] == loc[1] { // zero-width match — nothing to wrap
			continue
		}
		b.WriteString(base.Render(s[i:loc[0]]))
		b.WriteString(hi.Render(s[loc[0]:loc[1]]))
		i = loc[1]
	}
	b.WriteString(base.Render(s[i:]))
	return b.String()
}

// countHitPanels counts the distinct panels a result set spans, for the header.
func countHitPanels(hits []proto.SearchHit) int {
	seen := make(map[string]struct{}, len(hits))
	for _, h := range hits {
		seen[h.Panel] = struct{}{}
	}
	return len(seen)
}
