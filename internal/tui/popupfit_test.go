package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/i18n"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/signals"
)

// TestNoOverlayOutgrowsItsTerminal renders every floating pop-up at every height
// the cockpit claims to support and fails if the composed frame is taller than
// the terminal it was drawn for.
//
// Overflow is not a soft failure here, which is why this is a test and not a
// nicety. lipgloss.Place hands back content taller than the box it was given, so
// the rows past the bottom are simply lost — the pop-up's legend first, and then
// the footer under it. The page reads as though it has no way out.
//
// It is run in BOTH languages because the two are not the same height. A
// translated line that no longer fits the pop-up's width wraps onto a second row,
// and one extra row is all it takes.
//
// This is the test that would have caught the panel-config page: its reserved
// count said 12 while the page had grown a footer of 14, so on any terminal
// shorter than about 36 rows the box ran past the bottom of the screen and the
// legend went with it.
func TestNoOverlayOutgrowsItsTerminal(t *testing.T) {
	overlays := []struct {
		name string
		mut  func(*model)
	}{
		{"panel config", func(m *model) { m.mode, m.shellPath = modePanelConfig, "/bin/zsh" }},
		{"key map", func(m *model) { m.mode = modeKeyMap }},
		{"help", func(m *model) { m.mode, m.helpFrom = modeHelp, modeDashboard }},
		{"inbox", func(m *model) { m.mode = modeInbox }},
		{"queue", func(m *model) { m.mode = modeQueue }},
		{"issues", func(m *model) { m.mode = modeIssues }},
		{"proc tree", func(m *model) { m.mode = modeProcTree }},
		{"remote", func(m *model) { m.mode = modeRemote }},
		{"commands", func(m *model) { m.mode = modeCommand }},
		{"git menu", func(m *model) { m.mode = modeGit }},
		{"dir picker", func(m *model) { m.mode = modeDirPick }},
		{"fleet search", func(m *model) { m.mode = modeFleetSearch }},
		{"usage", func(m *model) { m.mode = modeUsage }},
		{"diff", func(m *model) { m.mode = modeDiff }},
		{"signals", func(m *model) {
			m.mode, m.signalScope, m.signalTargets = modeSignal, "shell #1", []string{"1"}
		}},
		{"agent picker", func(m *model) {
			m.mode = modeAgentPick
			m.agentList = []proto.AgentBackend{{Name: "claude", Command: "claude"}}
		}},
		{"input", func(m *model) { m.input = inputShellPath }},
		{"dashboard", func(m *model) {}}, // not a pop-up, and it must not overflow either
	}

	for _, o := range overlays {
		for _, lang := range []i18n.Lang{i18n.EN, i18n.ZhTW} {
			for h := minHeight; h <= 60; h++ {
				m := baseModel()
				m.lang, m.width, m.height = lang, 120, h
				m.fleet = []panel.Panel{{ID: "1", Title: "p1", State: panel.Attention}}
				o.mut(&m)
				frame := m.frame()
				if got := lipgloss.Height(frame); got > h {
					t.Errorf("%s (%s) at height %d renders %d rows", o.name, lang, h, got)
					break // one height per overlay per language is enough to report
				}
				// Fitting is not the same as being whole, and the difference is the
				// bug a person actually reports. A pop-up whose body is sized two rows
				// too tall gets cut to the screen by the clamp above — the frame is the
				// right height and the bottom of the box, legend and all, is gone. So
				// the closing border has to be on screen: it is the last row the box
				// draws, and nothing below it can survive if it did not.
				if o.name != "dashboard" && !strings.Contains(frame, "╰") {
					t.Errorf("%s (%s) at height %d lost the bottom of its box", o.name, lang, h)
					break
				}
			}
		}
	}
}

// TestPopupKeepsItsLastLine: when a pop-up cannot fit, what it loses is rows from
// the middle of its list and never the legend at the bottom.
//
// The legend is the row that says which key closes the thing. Losing it strands
// someone in an overlay they opened by accident, which is a worse outcome than
// not seeing the last two signals in a list they can scroll.
func TestPopupKeepsItsLastLine(t *testing.T) {
	m := baseModel()
	m.lang, m.width, m.height = i18n.ZhTW, 120, 20
	m.mode, m.signalScope, m.signalTargets = modeSignal, "shell #1", []string{"1"}

	view := ansi.Strip(m.signalPickerView())
	var lines []string
	for _, l := range strings.Split(view, "\n") {
		if l = strings.TrimSpace(strings.Trim(l, "│╭╮╰╯─ ")); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		t.Fatal("the picker rendered nothing")
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "esc") {
		t.Errorf("the bottom line should still be the legend, got %q", last)
	}
	if !strings.Contains(view, "…") {
		t.Error("a clipped pop-up should say so with an ellipsis")
	}
}

// TestScrollPanelReservesExactlyItsChrome pins the arithmetic every scrolling
// pop-up sizes its body with: the rows a panel reserves are the rows it actually
// draws around that body, no more and no fewer.
//
// It is checked against the RENDERING rather than restated, because restating it
// is what went wrong. The reserved figure used to be a constant per panel, and
// the panel-config page's said 12 with a comment reading "two hints" long after
// the page had four of them and a score-feedback line. Two rows too tall, every
// frame, and the only thing that showed was the legend leaving the screen.
//
// Reserved too LOW is the visible failure; too high is the quiet one — the body
// is sized smaller than its room, so the page scrolls when it did not need to.
// Equality catches both.
func TestScrollPanelReservesExactlyItsChrome(t *testing.T) {
	for _, p := range []scrollPanel{
		{title: "T", body: []string{"b1", "b2", "b3"}},
		{title: "T", head: []string{"tabs", ""}, body: []string{"b1", "b2"}, footer: []string{"", "legend"}},
		{title: "T", body: []string{"b"}, footer: []string{"", "a", "b", "c", "", "rule", "legend"}},
	} {
		m := baseModel()
		m.height = 0 // unsized: nothing is windowed, so the body renders whole
		drawn := lipgloss.Height(m.renderScrollPanel(p))
		if got, want := drawn-len(p.body), p.reservedRows(); got != want {
			t.Errorf("head=%d footer=%d: the panel draws %d rows of chrome but reserves %d",
				len(p.head), len(p.footer), got, want)
		}
	}
}

// TestWidgetsKeepTheirSize: walking around inside a floating widget must not
// change how tall it is.
//
// A box that grows and shrinks under the hand reads as the overlay closing and
// reopening rather than as a list you are moving through, and it takes the row
// your eye was resting on with it every time. The width has been pinned since
// the pop-ups were unified; this is the other half.
//
// Each case walks the widget the way a person does — tabs, a cursor, a confirm,
// a directory — and every step must render the same number of rows. What is NOT
// asserted is that two different widgets agree with each other: a settings page
// holding three rows has no business being as tall as a browser.
func TestWidgetsKeepTheirSize(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"a", "b", "c", "a/x", "a/y", "a/z"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name  string
		steps int
		walk  func(m *model, step int) string // render the widget at one step of the walk
	}{
		{"panel config tabs", len(panelCfgTabs), func(m *model, step int) string {
			m.mode, m.panelTab = modePanelConfig, step
			m.cursor, _ = m.panelTabRange()
			return m.panelConfigView()
		}},
		{"help tabs", 5, func(m *model, step int) string {
			m.mode, m.helpFrom, m.helpTab = modeHelp, modeDashboard, step
			return m.helpView()
		}},
		{"key map cursor", 6, func(m *model, step int) string {
			m.mode, m.cursor = modeKeyMap, step*4
			return m.keyMapView()
		}},
		{"signal picker cursor", len(signals.Choices) + 1, func(m *model, step int) string {
			m.mode, m.signalScope, m.signalCursor = modeSignal, "shell #1", step
			return m.signalPickerView()
		}},
		{"git menu confirm", 2, func(m *model, step int) string {
			m.mode = modeGit
			if step == 1 {
				m.gitConfirmOp, m.status = "push", "push shell #1? · (y/n)"
			}
			return m.gitPickerView()
		}},
		{"inbox filters", 5, func(m *model, step int) string {
			m.mode, m.inboxFilter = modeInbox, step-1
			m.fleet = []panel.Panel{{ID: "1", Title: "p1", State: panel.Attention}}
			m.inboxRows = m.sortedInboxRows()
			return m.inboxView()
		}},
		{"dir picker browsing", 3, func(m *model, step int) string {
			at := []string{dir, filepath.Join(dir, "a"), filepath.Join(dir, "b")}[step]
			m.mode, m.dirPickDir = modeDirPick, at
			ents, err := os.ReadDir(at)
			if err != nil {
				t.Fatal(err)
			}
			m.dirPickRows = m.dirRows(ents)
			return m.dirPickView()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := 0
			for step := 0; step < tc.steps; step++ {
				m := baseModel()
				m.lang, m.width, m.height = i18n.ZhTW, 120, 44
				got := lipgloss.Height(tc.walk(&m, step))
				if step == 0 {
					want = got
					continue
				}
				if got != want {
					t.Errorf("step %d renders %d rows, the first rendered %d", step, got, want)
				}
			}
		})
	}
}
