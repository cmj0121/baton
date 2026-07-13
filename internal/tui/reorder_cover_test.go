package tui

import (
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// TestReorderDashItemLastEdge keeps the last item put and reports "already last".
func TestReorderDashItemLastEdge(t *testing.T) {
	m := model{fleet: lone("A", "B", "C"), cursor: 2}
	m = m.reorderDashItem(+1)
	if m.status != "already last" {
		t.Fatalf("status = %q, want %q", m.status, "already last")
	}
}

// TestReorderEdgeStatus covers both nudge directions directly.
func TestReorderEdgeStatus(t *testing.T) {
	if got := reorderEdgeStatus(-1); got != "already first" {
		t.Fatalf("earlier edge = %q, want %q", got, "already first")
	}
	if got := reorderEdgeStatus(+1); got != "already last" {
		t.Fatalf("later edge = %q, want %q", got, "already last")
	}
}

// TestReorderDashItemOutOfRange: a cursor off the list is a silent no-op.
func TestReorderDashItemOutOfRange(t *testing.T) {
	m := model{fleet: lone("A", "B"), cursor: 9}
	got := m.reorderDashItem(-1)
	if got.status != "" {
		t.Fatalf("an out-of-range reorder should not set a status, got %q", got.status)
	}
}

// TestReorderGroupMemberNoFocus: with no focused member the reorder is a no-op.
func TestReorderGroupMemberNoFocus(t *testing.T) {
	m := model{fleet: []panel.Panel{{ID: "A", Group: "g"}}, mode: modeGroupZoom, groupName: "missing"}
	got := m.reorderGroupMember(-1)
	if got.status != "" {
		t.Fatalf("a no-focus reorder should be a silent no-op, got status %q", got.status)
	}
}
