package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// wired builds a model whose wire snapshot and domain fleet are the SAME fleet,
// which is what the cockpit always holds in real life: applyEvent merges the
// snapshot into m.fleet and files the raw frame in m.inboxWire in the same
// breath. Every test below depends on that pairing, because the need count is
// counted off the wire and rendered off the fleet.
func wired(ps []proto.Panel) model {
	m := baseModel()
	m.inboxDone = true
	m.inboxWire = ps
	m.fleet = mergeFleet(ps)
	return m
}

// needFleet spans every reason a panel can earn a row plus every reason it
// cannot, across two groups and one lone panel.
func needFleet() []proto.Panel {
	return []proto.Panel{
		{ID: "1", Kind: "agent", Title: "api · asks", State: "attention", Group: "api"},
		{ID: "2", Kind: "agent", Title: "api · works", State: "running", Group: "api"},
		{ID: "3", Kind: "agent", Title: "api · wedged", State: "stuck", Group: "api"},
		{ID: "4", Kind: "shell", Title: "api · failed", State: "exited", ExitCode: 2, Group: "api"},
		{ID: "5", Kind: "shell", Title: "api · clean", State: "exited", Group: "api"},
		{ID: "6", Kind: "agent", Title: "api · finished", State: "done", Group: "api"},
		{ID: "7", Kind: "agent", Title: "db · quiet", State: "idle", Group: "db"},
		{ID: "8", Kind: "agent", Title: "db · asks", State: "attention", Group: "db"},
		{ID: "9", Kind: "shell", Title: "lone asks", State: "attention"},
	}
}

// queuedByGroup is what the INBOX would actually put in the queue, bucketed by
// top-level group. It is built by running the queue itself — sortedInboxRows —
// rather than by re-deriving the rule, so a test that passes proves the two
// screens agree rather than proving two copies of one predicate agree.
func queuedByGroup(m model) map[string]int {
	byID := make(map[string]proto.Panel, len(m.inboxWire))
	for _, p := range m.inboxWire {
		byID[p.ID] = p
	}
	out := map[string]int{}
	for _, r := range m.sortedInboxRows() {
		if g := byID[r.id].Group; g != "" {
			out[panel.GroupTop(g)]++
		}
	}
	return out
}

// assertNeedMatchesInbox is the contract: every group header's ◆N is exactly the
// number of rows the inbox would offer for that group.
func assertNeedMatchesInbox(t *testing.T, m model) {
	t.Helper()
	want := queuedByGroup(m)
	seen := map[string]bool{}
	for _, it := range m.dashItems() {
		if it.kind != itemGroup {
			continue
		}
		seen[it.name] = true
		if it.need != want[it.name] {
			t.Errorf("group %q: header says %d need you, the inbox would queue %d", it.name, it.need, want[it.name])
		}
	}
	for g, n := range want {
		if n > 0 && !seen[g] {
			t.Errorf("group %q has %d queued rows but no dashboard item", g, n)
		}
	}
}

// TestGroupNeedCountMatchesTheInbox: the header count and the queue are two
// renderings of one predicate. attention, stuck, a non-zero exit and (with
// inbox-done on) done all count; running, idle and a clean exit do not.
func TestGroupNeedCountMatchesTheInbox(t *testing.T) {
	m := wired(needFleet())
	assertNeedMatchesInbox(t, m)

	got := map[string]int{}
	for _, it := range m.dashItems() {
		if it.kind == itemGroup {
			got[it.name] = it.need
		}
	}
	if got["api"] != 4 { // asks + wedged + failed + finished
		t.Errorf("api need = %d, want 4", got["api"])
	}
	if got["db"] != 1 {
		t.Errorf("db need = %d, want 1", got["db"])
	}
}

// TestGroupNeedCountDropsTheAcknowledged. An ack is fleet state the domain model
// does not carry, so a count taken off m.fleet would keep re-flagging work
// somebody has already cleared. It is taken off the wire for exactly this.
func TestGroupNeedCountDropsTheAcknowledged(t *testing.T) {
	ps := needFleet()
	ps[0].Acked = true // api · asks, dismissed from the inbox
	m := wired(ps)
	assertNeedMatchesInbox(t, m)

	for _, it := range m.dashItems() {
		if it.kind == itemGroup && it.name == "api" && it.need != 3 {
			t.Fatalf("api need = %d after an ack, want 3", it.need)
		}
	}
}

// TestGroupNeedCountFollowsInboxDone: settings.inbox-done removes the "review me"
// bucket from the queue, and the header must lose it in the same move.
func TestGroupNeedCountFollowsInboxDone(t *testing.T) {
	m := wired(needFleet())
	m.inboxDone = false
	assertNeedMatchesInbox(t, m)

	for _, it := range m.dashItems() {
		if it.kind == itemGroup && it.name == "api" && it.need != 3 {
			t.Fatalf("api need = %d with inbox-done off, want 3", it.need)
		}
	}
}

// TestGroupNeedCountFoldsTheSubtree: the card folds a nested group's whole
// subtree, so the count has to fold with it — a panel asking for help two levels
// down is still work inside the top-level item.
func TestGroupNeedCountFoldsTheSubtree(t *testing.T) {
	m := wired([]proto.Panel{
		{ID: "1", Kind: "shell", Title: "backend d", State: "running", Group: "backend"},
		{ID: "2", Kind: "agent", Title: "api a", State: "attention", Group: "backend/api"},
		{ID: "3", Kind: "agent", Title: "db a", State: "stuck", Group: "backend/db"},
		{ID: "4", Kind: "agent", Title: "db b", State: "idle", Group: "backend/db"},
	})
	assertNeedMatchesInbox(t, m)

	// The tree draws the sub-groups too, so the assertion is about the backend ROW
	// rather than about the dashboard having exactly one of them: its need must
	// still fold the whole subtree, because a panel asking for help two levels down
	// is work inside this item however many rows now stand between them.
	var backend dashItem
	for _, it := range m.dashItems() {
		if it.kind == itemGroup && it.name == "backend" {
			backend = it
		}
	}
	if backend.kind != itemGroup || backend.need != 2 {
		t.Fatalf("expected the backend row to need 2, got %+v", backend)
	}
}

// TestNeedCountNeverReordersTheTree is the issue's explicit constraint: the
// dashboard is NOT re-sorted by need. The same fleet must project to the same
// items in the same order whether or not anything is asking for a human — the
// count annotates the shape, it does not rearrange it.
func TestNeedCountNeverReordersTheTree(t *testing.T) {
	calm := wired(needFleet())
	for i := range calm.inboxWire {
		calm.inboxWire[i].State = "running" // nothing needs anybody
	}
	calm.fleet = mergeFleet(calm.inboxWire)

	loud := wired(needFleet())

	// The fleets differ only in state, so the projection must be identical in
	// kind, name and membership — order included.
	a, b := calm.dashItems(), loud.dashItems()
	if len(a) != len(b) {
		t.Fatalf("item count changed with need: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].kind != b[i].kind || a[i].name != b[i].name || len(a[i].members) != len(b[i].members) {
			t.Fatalf("item %d moved: %+v vs %+v", i, a[i], b[i])
		}
		if a[i].kind == itemPanel && a[i].panel.ID != b[i].panel.ID {
			t.Fatalf("panel item %d moved: %s vs %s", i, a[i].panel.ID, b[i].panel.ID)
		}
	}
}

// TestNeedCountAbsentWithoutAWireSnapshot: a cockpit that has not yet seen a
// frame counts nothing.
func TestNeedCountAbsentWithoutAWireSnapshot(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	if got := m.needByGroup(); got != nil {
		t.Fatalf("needByGroup should be nil with no wire snapshot, got %v", got)
	}
	for _, it := range m.dashItems() {
		if it.need != 0 {
			t.Fatalf("item %q counted %d with no wire snapshot", it.title(), it.need)
		}
	}
}

// TestNeedCountCostsNothingOnACalmFleet asserts the claim needByGroup's own doc
// comment makes, rather than leaving it as prose: the map is built lazily, so a
// full snapshot in which nothing is asking for a human allocates zero times.
//
// This is the common path — it runs inside dashItems, which the dashboard
// evaluates several times per frame — so "no allocation at all" is the property
// worth pinning down, and a comment that says it without a test to hold it is the
// first thing a refactor quietly breaks.
func TestNeedCountCostsNothingOnACalmFleet(t *testing.T) {
	m := wired(needFleet())
	for i := range m.inboxWire {
		m.inboxWire[i].State = "running" // a full fleet, none of it waiting
	}
	if got := m.needByGroup(); got != nil {
		t.Fatalf("a calm fleet should build no map at all, got %v", got)
	}
	if got := testing.AllocsPerRun(100, func() { _ = m.needByGroup() }); got != 0 {
		t.Fatalf("needByGroup allocated %v times with nothing to count", got)
	}
}

// TestNeedChipRendersOnCardAndTree: the number has to be visible in BOTH
// dashboard layouts. The tree is the one a 50-panel fleet actually lives in, so
// a count that only reached the card grid would miss the case it exists for.
func TestNeedChipRendersOnCardAndTree(t *testing.T) {
	m := wired(needFleet())
	items := m.dashItems()

	var api dashItem
	for _, it := range items {
		if it.kind == itemGroup && it.name == "api" {
			api = it
		}
	}
	if card := m.rowOf(api); !strings.Contains(card, "◆4") {
		t.Errorf("group card is missing the need count:\n%s", card)
	}
	if prev := m.renderGroupPreview(api, 40); !strings.Contains(prev, "◆4") {
		t.Errorf("group preview is missing the need count:\n%s", prev)
	}
	if tree := m.renderRows(items, 0, len(items), len(items), testRowWidth); !strings.Contains(tree, "◆4") {
		t.Errorf("tree row is missing the need count:\n%s", tree)
	}
}

// TestNeedChipSilentWhenNothingWaits: a calm group shows a plain card, exactly as
// it does today. The chip is a signal, and a signal that is always on is noise.
func TestNeedChipSilentWhenNothingWaits(t *testing.T) {
	if got := needChip(0); got != "" {
		t.Errorf("needChip(0) = %q, want empty", got)
	}
	if got := needChip(-1); got != "" {
		t.Errorf("needChip(-1) = %q, want empty", got)
	}
	m := wired([]proto.Panel{
		{ID: "1", Kind: "agent", Title: "api a", State: "running", Group: "api"},
		{ID: "2", Kind: "agent", Title: "api b", State: "idle", Group: "api"},
	})
	items := m.dashItems()
	if strings.Contains(m.rowOf(items[0]), "◆") {
		t.Error("a calm group card should carry no need chip")
	}
}
