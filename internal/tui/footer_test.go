package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

func TestMemLabel(t *testing.T) {
	const g = 1 << 30
	cases := []struct {
		used, total uint64
		want        string
	}{
		{9*g + g/5, 16 * g, "9.2/16G"},
		{512 * (1 << 20), 2 * g, "0.5/2G"},
		{0, 0, "0.0/0B"},
		{700, 1000, "700.0/1000B"},
	}
	for _, c := range cases {
		if got := memLabel(c.used, c.total); got != c.want {
			t.Errorf("memLabel(%d, %d) = %q, want %q", c.used, c.total, got, c.want)
		}
	}
}

func TestStatsStrip(t *testing.T) {
	// No sample yet: the strip is blank so the footer never shows 0/0.
	if s := (model{}).statsStrip(); s != "" {
		t.Fatalf("statsStrip with no sample should be empty, got %q", s)
	}

	m := model{cpuPct: 18, memUsed: 9 << 30, memTotal: 16 << 30}
	strip := m.statsStrip()
	for _, want := range []string{"CPU", "18%", "MEM", "9.0/16G"} {
		if !strings.Contains(strip, want) {
			t.Errorf("statsStrip missing %q in %q", want, strip)
		}
	}

	// And it shows up in the rendered footer.
	m.width = 120
	if !strings.Contains(m.footer(), "CPU") {
		t.Fatal("footer should include the CPU/MEM stats segment")
	}
}

func TestStatsEventPopulatesFooter(t *testing.T) {
	// A "stats" event from the server (the backend measures the host) fills the
	// footer fields.
	m := model{width: 120}
	m.applyEvent(proto.ServerMsg{Type: "stats", CPU: 42, MemUsed: 4 << 30, MemTotal: 8 << 30})
	if m.cpuPct != 42 || m.memUsed != 4<<30 || m.memTotal != 8<<30 {
		t.Fatalf("stats event not applied: cpu=%v used=%v total=%v", m.cpuPct, m.memUsed, m.memTotal)
	}
	if got := m.footer(); !strings.Contains(got, "42%") || !strings.Contains(got, "4.0/8G") {
		t.Fatalf("footer should show the backend stats, got %q", got)
	}
}

func TestFooterClipsLongStatus(t *testing.T) {
	// A long status must not overflow onto a second line and swallow the bar:
	// the footer stays exactly one row, the full terminal width.
	m := model{width: 80, endpoint: "local"}
	m.status = strings.Repeat("grouped a very long work item name ", 5)
	foot := m.footer()
	if strings.Contains(foot, "\n") {
		t.Fatalf("footer should be a single line, got %d lines", strings.Count(foot, "\n")+1)
	}
	if w := lipgloss.Width(foot); w != 80 {
		t.Fatalf("footer should fill the width exactly, got %d want 80", w)
	}
}

func TestStatusFadesAfterIdle(t *testing.T) {
	m := model{endpoint: "local"}
	m.status = "grouped 2 panel(s)"

	// It survives a few ticks, then settles back to the resting line.
	for i := 0; i < statusTTL+1; i++ {
		m.ageStatus()
	}
	if m.status != m.restingStatus() {
		t.Fatalf("status should fade to the resting line, got %q", m.status)
	}

	// Errors are sticky — they do not fade.
	m.status = "error: boom"
	for i := 0; i < statusTTL+2; i++ {
		m.ageStatus()
	}
	if m.status != "error: boom" {
		t.Fatalf("an error status should persist, got %q", m.status)
	}
}

// TestFooterFillsWidth pins the invariant the user cares about: the footer is a
// single, full-width coloured strip in every state and at every width — never
// short (leaving an uncoloured tail) and never wrapped onto a second line.
func TestFooterFillsWidth(t *testing.T) {
	states := []struct {
		name string
		mut  func(*model)
	}{
		{"plain", func(m *model) {}},
		{"with-stats", func(m *model) { m.cpuPct = 18; m.memUsed = 9 << 30; m.memTotal = 16 << 30 }},
		{"prefix-armed", func(m *model) { m.prefix = true }},
		{"attention", func(m *model) { m.fleet = []panel.Panel{{State: panel.Attention}} }},
		{"error", func(m *model) { m.status = "error: boom" }},
		{"long-status", func(m *model) { m.status = strings.Repeat("grouped a long work item ", 6) }},
	}
	// The stats and clock are always shown, so the footer needs a realistic
	// terminal width to fit them — widths below ~60 are not a supported size.
	for _, w := range []int{60, 80, 120, 200} {
		for _, st := range states {
			m := model{width: w, height: 30, endpoint: "local", status: "dashboard"}
			st.mut(&m)
			foot := m.footer()
			if strings.Contains(foot, "\n") {
				t.Fatalf("%s @ w=%d: footer wrapped to %d lines", st.name, w, strings.Count(foot, "\n")+1)
			}
			if got := lipgloss.Width(foot); got != w {
				t.Fatalf("%s @ w=%d: footer width = %d, want %d (uncoloured tail)", st.name, w, got, w)
			}
		}
	}
}

// TestGroupZoomFooterFillsWidth holds the same invariant for the group split bar.
func TestGroupZoomFooterFillsWidth(t *testing.T) {
	for _, w := range []int{60, 80, 120, 200} {
		m := model{width: w, height: 30, groupName: "api", binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
		foot := m.groupZoomFooter()
		if strings.Contains(foot, "\n") {
			t.Fatalf("group footer @ w=%d wrapped to %d lines", w, strings.Count(foot, "\n")+1)
		}
		if got := lipgloss.Width(foot); got != w {
			t.Fatalf("group footer @ w=%d width = %d, want %d", w, got, w)
		}
	}
}
