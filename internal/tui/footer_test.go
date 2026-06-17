package tui

import (
	"strings"
	"testing"

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
