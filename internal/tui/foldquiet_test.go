package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// The dashboard's quiet fold. The need counts it sits beside are in fold_test.go;
// `wired` and the model helpers are shared with it.

// quietWire builds a fleet of quiet panels (idle, and clean exits) plus busy ones,
// as the wire frame the cockpit would actually receive. Ids carry the index so a
// fold that keeps the wrong panel is named in the failure rather than counted.
func quietWire(quiet, busy int) []proto.Panel {
	ps := make([]proto.Panel, 0, quiet+busy)
	for i := 0; i < quiet; i++ {
		st := "idle"
		if i%3 == 2 {
			st = "exited" // a CLEAN exit is quiet too: nothing happened, nothing will
		}
		ps = append(ps, proto.Panel{ID: fmt.Sprintf("q%d", i), Kind: "shell", Title: fmt.Sprintf("quiet %d", i), State: st})
	}
	for i := 0; i < busy; i++ {
		ps = append(ps, proto.Panel{ID: fmt.Sprintf("b%d", i), Kind: "agent", Title: fmt.Sprintf("busy %d", i), State: "running"})
	}
	return ps
}

// folding splits a projection into the fold row and everything else, reporting
// where the row landed (-1 when nothing folded).
func folding(items []dashItem) (row dashItem, at int, rest []dashItem) {
	at = -1
	for i, it := range items {
		if it.kind == itemFold {
			row, at = it, i
			continue
		}
		rest = append(rest, it)
	}
	return row, at, rest
}

// TestFoldQuietThreshold: fold only once there are MORE quiet panels than
// settings.fold-quiet. At or below it the dashboard is what it always was, which
// is the property that lets a five-panel fleet never notice this feature exists.
func TestFoldQuietThreshold(t *testing.T) {
	for _, tc := range []struct {
		name       string
		quiet      int
		threshold  int
		wantFolded bool
	}{
		{"below", 5, 8, false},
		{"exactly at", 8, 8, false},
		{"one over", 9, 8, true},
		{"far over", 40, 8, true},
		{"tight threshold", 2, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := wired(quietWire(tc.quiet, 3))
			m.foldQuiet = tc.threshold
			items := m.dashItems()
			_, at, rest := folding(items)
			if tc.wantFolded != (at >= 0) {
				t.Fatalf("%d quiet at threshold %d: folded=%v, want %v", tc.quiet, tc.threshold, at >= 0, tc.wantFolded)
			}
			if !tc.wantFolded {
				if len(items) != tc.quiet+3 {
					t.Fatalf("nothing should fold: got %d items, want %d", len(items), tc.quiet+3)
				}
				return
			}
			if len(rest) != 3 {
				t.Fatalf("expected only the 3 busy panels left, got %d", len(rest))
			}
		})
	}
}

// TestFoldQuietZeroNeverFolds: settings.fold-quiet 0 switches the whole thing off,
// however big the fleet gets.
func TestFoldQuietZeroNeverFolds(t *testing.T) {
	m := wired(quietWire(50, 2))
	m.foldQuiet = 0
	items := m.dashItems()
	if _, at, _ := folding(items); at >= 0 {
		t.Fatal("fold-quiet: 0 must never fold")
	}
	if len(items) != 52 {
		t.Fatalf("got %d items, want all 52", len(items))
	}
}

// TestFoldQuietIsIdenticalBelowTheThreshold is the promise a small fleet is owed:
// with the feature configured and under the threshold, the projection is the same
// list, item for item, as with the feature switched off entirely.
func TestFoldQuietIsIdenticalBelowTheThreshold(t *testing.T) {
	off := wired(quietWire(6, 2))
	on := wired(quietWire(6, 2))
	on.foldQuiet = defaultFoldQuiet

	a, b := off.dashItems(), on.dashItems()
	if len(a) != len(b) {
		t.Fatalf("item count differs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].kind != b[i].kind || a[i].panel.ID != b[i].panel.ID {
			t.Fatalf("item %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// TestFoldRowSitsWhereTheFirstQuietPanelWas: the tree's shape survives the fold.
// The row takes the position of the first panel it swallowed and every other card
// keeps its neighbours, so nothing the fold does moves a card you were reading.
func TestFoldRowSitsWhereTheFirstQuietPanelWas(t *testing.T) {
	m := wired([]proto.Panel{
		{ID: "b0", Kind: "agent", Title: "busy 0", State: "running"},
		{ID: "q0", Kind: "shell", Title: "quiet 0", State: "idle"},
		{ID: "b1", Kind: "agent", Title: "busy 1", State: "running"},
		{ID: "q1", Kind: "shell", Title: "quiet 1", State: "idle"},
		{ID: "q2", Kind: "shell", Title: "quiet 2", State: "idle"},
		{ID: "b2", Kind: "agent", Title: "busy 2", State: "attention"},
	})
	m.foldQuiet = 1

	row, at, rest := folding(m.dashItems())
	if at != 1 {
		t.Fatalf("fold row is at %d, want 1 (where quiet 0 was)", at)
	}
	if row.quiet != 3 {
		t.Fatalf("fold row stands for %d panels, want 3", row.quiet)
	}
	got := make([]string, len(rest))
	for i, it := range rest {
		got[i] = it.panel.ID
	}
	if strings.Join(got, ",") != "b0,b1,b2" {
		t.Fatalf("the kept cards were reordered: %v", got)
	}
}

// TestFoldNeverSwallowsTheCursor. A fold that hides what you are pointing at is a
// bug, not a preference: the selection would land on a card you did not choose. The
// protected panel is carried as an IDENTITY captured before the snapshot, because
// the case worth protecting is exactly the one where a card goes quiet under the
// cursor — where an index names two different things on either side of the fold.
func TestFoldNeverSwallowsTheCursor(t *testing.T) {
	m := wired(quietWire(20, 1))
	m.foldQuiet = 4
	m.foldKeepID = "q7"

	_, at, rest := folding(m.dashItems())
	if at < 0 {
		t.Fatal("expected a fold")
	}
	for _, it := range rest {
		if it.kind == itemPanel && it.panel.ID == "q7" {
			return
		}
	}
	t.Fatal("the panel under the cursor was folded away")
}

// TestRestoreCursorArmsTheFoldKeep: the identity the fold protects is the one
// restoreCursor is already handed, so the two can never disagree about which card
// the cursor was on.
func TestRestoreCursorArmsTheFoldKeep(t *testing.T) {
	m := wired(quietWire(3, 1))
	m.restoreCursor(itemPanel, "q1", "", true)
	if m.foldKeepID != "q1" {
		t.Fatalf("foldKeepID = %q, want q1", m.foldKeepID)
	}
	m.restoreCursor(itemGroup, "", "api", true)
	if m.foldKeepID != "" {
		t.Fatalf("a group selection should protect no panel, got %q", m.foldKeepID)
	}
}

// TestFoldNeverSwallowsWhatYouSaidMatters: favourites, pins and a pending mark are
// the user having already said this one is worth a card. The fold does not argue.
func TestFoldNeverSwallowsWhatYouSaidMatters(t *testing.T) {
	ps := quietWire(20, 0)
	ps[1].Favourite = true
	ps[4].Pinned = true
	m := wired(ps)
	m.foldQuiet = 3
	m.marked = map[string]bool{"q9": true}

	_, at, rest := folding(m.dashItems())
	if at < 0 {
		t.Fatal("expected a fold")
	}
	kept := map[string]bool{}
	for _, it := range rest {
		kept[it.panel.ID] = true
	}
	for _, id := range []string{"q1", "q4", "q9"} {
		if !kept[id] {
			t.Errorf("%s should never be folded away", id)
		}
	}
}

// TestFoldKeepsGroupsAndFailures: a group is already one card and carries its own
// need count, and a non-zero exit is a failure waiting to be read — the inbox
// queues it for the same reason. Neither is quiet.
func TestFoldKeepsGroupsAndFailures(t *testing.T) {
	ps := append(quietWire(20, 0),
		proto.Panel{ID: "g1", Kind: "agent", Title: "api a", State: "idle", Group: "api"},
		proto.Panel{ID: "g2", Kind: "agent", Title: "api b", State: "idle", Group: "api"},
		proto.Panel{ID: "f1", Kind: "shell", Title: "broke", State: "exited", ExitCode: 1},
	)
	m := wired(ps)
	m.foldQuiet = 3

	_, at, rest := folding(m.dashItems())
	if at < 0 {
		t.Fatal("expected a fold")
	}
	var group, failed bool
	for _, it := range rest {
		if it.kind == itemGroup && it.name == "api" {
			group = true
		}
		if it.kind == itemPanel && it.panel.ID == "f1" {
			failed = true
		}
	}
	if !group {
		t.Error("a group card must never be folded away")
	}
	if !failed {
		t.Error("a non-zero exit is not quiet and must keep its card")
	}
}

// TestFoldExpandShowsExactlyTheFoldedMembers: expanding restores the projection to
// what it would be with the feature off, plus the row itself. Folded, never
// dropped — and every panel comes back where it was, not clustered under the row.
func TestFoldExpandShowsExactlyTheFoldedMembers(t *testing.T) {
	ps := quietWire(12, 3)
	unfolded := wired(ps)
	unfolded.foldQuiet = 0

	m := wired(ps)
	m.foldQuiet = 4
	m.foldOpen = map[string]bool{"": true}

	want := unfolded.dashItems()
	_, at, rest := folding(m.dashItems())
	if at < 0 {
		t.Fatal("the fold row must stay on screen while it is expanded")
	}
	if len(rest) != len(want) {
		t.Fatalf("expanded shows %d items, unfolded has %d", len(rest), len(want))
	}
	for i := range want {
		if rest[i].kind != want[i].kind || rest[i].panel.ID != want[i].panel.ID {
			t.Fatalf("item %d is not in its place: %q vs %q", i, rest[i].panel.ID, want[i].panel.ID)
		}
	}
}

// TestFoldRowCarriesNoPanels is the safety net under "the fold owns no verbs": the
// row answers no ids, so any bulk verb that forgot to ask acts on nothing at all
// rather than on every quiet panel at once.
func TestFoldRowCarriesNoPanels(t *testing.T) {
	m := wired(quietWire(12, 1))
	m.foldQuiet = 4
	row, at, _ := folding(m.dashItems())
	if at < 0 {
		t.Fatal("expected a fold")
	}
	if len(row.ids()) != 0 || len(row.members) != 0 {
		t.Fatalf("the fold row must cover no panels, got ids=%v members=%d", row.ids(), len(row.members))
	}
	if row.title() != "12 quiet" {
		t.Fatalf("fold row title = %q, want %q", row.title(), "12 quiet")
	}
}

// foldedModel is a dashboard sitting with the cursor on the quiet row.
func foldedModel(t *testing.T) model {
	t.Helper()
	m := wired(quietWire(12, 2))
	m.foldQuiet = 4
	_, at, _ := folding(m.dashItems())
	if at < 0 {
		t.Fatal("expected a fold")
	}
	m.cursor = at
	return m
}

// TestFoldRowRefusesEveryPanelVerb. A bulk close of "the quiet ones" reachable by
// one keystroke on a row whose contents you have deliberately not looked at is the
// destructive surprise this row is not allowed to be.
func TestFoldRowRefusesEveryPanelVerb(t *testing.T) {
	m := foldedModel(t)
	for _, act := range []action{actClose, actRespawn, actSignal, actMark, actFavourite,
		actRename, actUngroup, actAdd, actDispatch, actEnqueue, actDiff, actNewHere} {
		if !m.refusedOnFoldRow(act) {
			t.Errorf("action %v should be refused on the fold row", act)
		}
	}
	// Everything that does not act on the selection stays reachable from the row.
	for _, act := range []action{actNewPanel, actInbox, actHelp, actPurge, actFleetSearch, actQueue} {
		if m.refusedOnFoldRow(act) {
			t.Errorf("action %v is not a selection verb and must stay reachable", act)
		}
	}
	// And off the fold row every verb is itself again.
	m.cursor = len(m.dashItems()) - 1
	if m.refusedOnFoldRow(actClose) {
		t.Error("close is refused only ON the fold row")
	}
}

// TestFoldRowRefusalSaysWhatToDo: the status line names the way forward rather
// than just saying no, and the refused verb does nothing at all.
func TestFoldRowRefusalSaysWhatToDo(t *testing.T) {
	m := foldedModel(t)
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	mm, _ := out.(model)
	if mm.status != "expand the quiet group first" {
		t.Fatalf("status = %q, want the expand hint", mm.status)
	}
	if mm.pendingClose {
		t.Fatal("w on the fold row must not arm a close")
	}
}

// TestFoldRowTogglesOnEnterRightAndEsc: the row's only verbs, and all three of
// them do the same one thing.
func TestFoldRowTogglesOnEnterRightAndEsc(t *testing.T) {
	m := foldedModel(t)
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	mm, _ := out.(model)
	if !mm.foldOpen[""] {
		t.Fatal("enter should expand the quiet row")
	}
	out, _ = mm.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	mm, _ = out.(model)
	if mm.foldOpen[""] {
		t.Fatal("esc should fold the quiet row back up")
	}
	out, _ = mm.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	mm, _ = out.(model)
	if !mm.foldOpen[""] {
		t.Fatal("l should expand the quiet row")
	}
}

// TestFoldEscKeepsTheFilter: esc unwinds one layer at a time, so escaping out of an
// expanded fold does not also throw away the filter that got you there.
func TestFoldEscKeepsTheFilter(t *testing.T) {
	m := foldedModel(t)
	m.filter = "quiet"
	m.foldOpen = map[string]bool{"": true}
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	mm, _ := out.(model)
	if mm.foldOpen[""] {
		t.Fatal("esc should fold first")
	}
	if mm.filter != "quiet" {
		t.Fatalf("esc cleared the filter too: %q", mm.filter)
	}
}

// TestFoldEscFallsThroughWithNoRow: an expanded flag left standing over a fleet
// with nothing to fold must not swallow esc. Filter the dashboard down past the
// threshold and esc means what it always meant — clear the filter.
func TestFoldEscFallsThroughWithNoRow(t *testing.T) {
	m := foldedModel(t)
	m.foldOpen = map[string]bool{"": true}
	m.filter = "busy 0" // one match: no fold row on screen
	if m.foldRowShowing() {
		t.Fatal("the filtered dashboard should hold no fold row")
	}
	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	mm, _ := out.(model)
	if mm.filter != "" {
		t.Fatalf("esc should have cleared the filter, got %q", mm.filter)
	}
}

// TestFoldRowRenders in every place the dashboard can draw an item: the card grid,
// the tree row, and the preview pane. The preview matters most — without its own
// branch the row would render as a nameless, stateless panel.
func TestFoldRowRenders(t *testing.T) {
	m := foldedModel(t)
	items := m.dashItems()
	row := items[m.cursor]

	if card := m.renderItemCard(row, true); !strings.Contains(card, "12 quiet") || !strings.Contains(card, "▸") {
		t.Errorf("fold card does not read as a closed quiet row:\n%s", card)
	}
	if tree := m.renderTree(items, 0, len(items), len(items)); !strings.Contains(tree, "12 quiet") {
		t.Errorf("tree is missing the fold row:\n%s", tree)
	}
	if prev := m.renderPreview(items, 44); !strings.Contains(prev, "12 quiet") {
		t.Errorf("preview is missing the fold row:\n%s", prev)
	}
	m.foldOpen = map[string]bool{"": true}
	if card := m.renderItemCard(row, false); !strings.Contains(card, "▾") {
		t.Errorf("an expanded fold row should show the open glyph:\n%s", card)
	}
	// The whole dashboard still draws, folded and expanded, in both layouts.
	for _, exp := range []bool{false, true} {
		m.foldOpen = map[string]bool{"": exp}
		for _, w := range []int{120, 60} {
			m.width = w
			if got := m.dashboardView(); got == "" {
				t.Fatalf("dashboardView is empty with foldOpen=%v at width %d", exp, w)
			}
		}
	}
}

// rawItems is the projection BEFORE the fold — what foldQuietLevel is handed.
func rawItems(m model) []dashItem {
	off := m
	off.foldQuiet = 0
	return off.dashItems()
}

// TestFoldQuietAt50 is the fleet this unit exists for. The fold runs on every
// dashboard render, several times per frame, so the budget guards the one thing
// that would actually hurt: allocating per member. Two O(n) passes and one output
// slice is the whole cost, and neither pass grows with anything but the item count.
func TestFoldQuietAt50(t *testing.T) {
	m := wired(quietWire(45, 5))
	m.foldQuiet = defaultFoldQuiet
	raw := rawItems(m)

	if _, at, rest := folding(m.foldQuietLevel(raw, "")); at < 0 || len(rest) != 5 {
		t.Fatalf("45 quiet + 5 busy should leave 5 cards and one row, got %d cards at %d", len(rest), at)
	}
	const budget = 4 // measured at 1; the slack is for the slice growing differently elsewhere
	if got := testing.AllocsPerRun(100, func() { m.foldQuietLevel(raw, "") }); got > budget {
		t.Fatalf("folding 50 items took %v allocations, budget %d", got, budget)
	}
}

// TestFoldQuietPrefReachesTheModel: settings.fold-quiet has to travel config →
// prefs → model, or the knob is a comment. An explicit 0 must survive as 0 rather
// than falling back to the default, which is why the config field is a pointer.
func TestFoldQuietPrefReachesTheModel(t *testing.T) {
	if got := prefsFromConfig(config.Config{}).foldQuiet; got != defaultFoldQuiet {
		t.Fatalf("unset fold-quiet = %d, want the default %d", got, defaultFoldQuiet)
	}
	for _, want := range []int{0, 3, 40} {
		v := want
		p := prefsFromConfig(config.Config{Settings: config.Settings{FoldQuiet: &v}})
		if p.foldQuiet != want {
			t.Fatalf("fold-quiet %d came through as %d", want, p.foldQuiet)
		}
		if m := baseModel().applyPrefs(p); m.foldQuiet != want {
			t.Fatalf("fold-quiet %d reached the model as %d", want, m.foldQuiet)
		}
	}
}

// --- the cursor across a snapshot ---------------------------------------------

// TestFoldKeepsTheCursorAcrossASnapshot is the end-to-end version of the promise
// the fold makes about the cursor, and it is deliberately built so that the
// obvious near-miss fails it.
//
// The two halves of the mechanism — restoreCursor arming foldKeepID, and the fold
// honouring it — were each tested on their own and neither test could see the
// ORDER they happen in. Move the assignment to the end of restoreCursor and both
// still pass: the fold protects the card by the time anyone reads the list, just
// one frame after the cursor was already clamped somewhere else.
//
// So this drives two real snapshots and asserts on the CARD, not on a number, in a
// fleet arranged so the clamped answer and the correct answer differ: the quiet
// block sorts first (the fold row lands at index 0), two cards ahead of the
// selected one go idle in the same snapshot, and the selection therefore has to
// move from index 3 to index 1. A cursor left to clamp lands on index 2 — the card
// after it — which is exactly the silent one-off this test exists to catch.
func TestFoldKeepsTheCursorAcrossASnapshot(t *testing.T) {
	busy := func(id string) proto.Panel {
		return proto.Panel{ID: id, Kind: "agent", Title: "agent " + id, State: "running"}
	}
	first := append(quietWire(20, 0), busy("a"), busy("b"), busy("p"), busy("c"), busy("d"))

	m := baseModel()
	m.foldQuiet = 4
	m.applyEvent(proto.ServerMsg{Type: "panels", Panels: first})

	items := m.dashItems()
	m.cursor = -1
	for i, it := range items {
		if it.kind == itemPanel && it.panel.ID == "p" {
			m.cursor = i
		}
	}
	if m.cursor != 3 {
		t.Fatalf("expected the selected card at index 3 of %d items, got %d", len(items), m.cursor)
	}

	// a, b and p all go quiet in one snapshot: two cards ahead of the selection
	// leave the list, and the selection itself would leave it too if the fold were
	// allowed to have its way.
	second := append([]proto.Panel(nil), first...)
	for i := range second {
		switch second[i].ID {
		case "a", "b", "p":
			second[i].State = "idle"
		}
	}
	m.applyEvent(proto.ServerMsg{Type: "panels", Panels: second})

	it, ok := m.selectedItem()
	if !ok {
		t.Fatal("the cursor was left outside the list by the snapshot")
	}
	if it.kind != itemPanel || it.panel.ID != "p" {
		t.Fatalf("the cursor moved off the card it was on: now %q (kind %v)", it.title(), it.kind)
	}
}

// TestFoldRowIsIdentifiedByItsLevel: a fold row is "the quiet ones here", so the
// level it sits in is its identity and the cursor keeps its place across a
// snapshot. It pins no panel — it is not one, so there is nothing the fold could
// be asked to spare on its behalf.
//
// It used to report no identity at all, which was defensible while there was one
// fold row on the whole dashboard. With a fold per level that answer costs the
// cursor its place every time a snapshot lands while you are on one.
func TestFoldRowIsIdentifiedByItsLevel(t *testing.T) {
	m := foldedModel(t)
	kind, id, group, ok := m.selectedKey()
	if !ok || kind != itemFold || group != "" || id != "" {
		t.Fatalf("a top-level fold row should identify as its level, got kind=%v id=%q group=%q ok=%v", kind, id, group, ok)
	}

	at := m.cursor
	m.restoreCursor(kind, id, group, ok)
	if m.foldKeepID != "" {
		t.Fatalf("the fold row should pin no panel, got %q", m.foldKeepID)
	}
	if m.cursor != at {
		t.Fatalf("the cursor should stay on the fold row, moved %d -> %d", at, m.cursor)
	}
	if it, ok := m.selectedItem(); !ok || it.kind != itemFold {
		t.Fatalf("the fold row should still be selected, got %+v ok=%v", it, ok)
	}
}

// --- collapsing, and the cursor it takes with it ------------------------------

// TestCollapsingNeverStrandsTheCursor. Expand, walk down into the folded block,
// press esc: the list gets twelve items shorter under the cursor. Without a
// re-anchor the index is simply past the end — selectedItem reports nothing, the
// preview reads "no panel selected", and the highlight vanishes until you press an
// arrow key to bring it back.
func TestCollapsingNeverStrandsTheCursor(t *testing.T) {
	m := foldedModel(t)
	m = m.toggleFold() // expand
	if !m.foldOpen[""] {
		t.Fatal("expected the row to expand")
	}
	m.cursor = -1
	for i, it := range m.dashItems() { // the last of the panels the row had swallowed
		if it.kind == itemPanel && strings.HasPrefix(it.panel.ID, "q") {
			m.cursor = i
		}
	}
	if it, ok := m.selectedItem(); !ok || it.kind != itemPanel {
		t.Fatalf("expected to be on a folded panel, got ok=%v", ok)
	}

	m = m.toggleFold() // collapse
	items := m.dashItems()
	if m.cursor < 0 || m.cursor >= len(items) {
		t.Fatalf("cursor %d is outside the %d-item list", m.cursor, len(items))
	}
	it, ok := m.selectedItem()
	if !ok {
		t.Fatal("collapsing stranded the cursor outside the list")
	}
	if it.kind != itemFold {
		t.Fatalf("a card that went back into the fold should leave the cursor on the row, got %q", it.title())
	}
}

// TestCollapsingKeepsAVisibleCard: the re-anchor is by identity, so a card that
// survives the collapse keeps the cursor rather than losing it to the fold row.
func TestCollapsingKeepsAVisibleCard(t *testing.T) {
	m := foldedModel(t)
	m = m.toggleFold() // expand
	for i, it := range m.dashItems() {
		if it.kind == itemPanel && it.panel.ID == "b1" {
			m.cursor = i
		}
	}
	m = m.toggleFold() // collapse
	it, ok := m.selectedItem()
	if !ok || it.kind != itemPanel || it.panel.ID != "b1" {
		t.Fatalf("a card that survived the collapse should keep the cursor, got %+v (ok=%v)", it, ok)
	}
}

// --- reorder ------------------------------------------------------------------

// TestFoldRowRefusesReorder. shift+arrow is dispatched straight out of handleKey
// and never passes through lookupCmd, so the blanket refusal does not cover it —
// which is why it gets its own guard and its own test. Moving a row that stands for
// twelve panels would have to move all twelve.
func TestFoldRowRefusesReorder(t *testing.T) {
	c, cmds := recordingServer(t)
	m := foldedModel(t)
	m.client = c

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyShiftDown})
	mm, _ := out.(model)
	if mm.status != "expand the quiet group first" {
		t.Fatalf("status = %q, want the expand hint", mm.status)
	}
	select {
	case cmd := <-cmds:
		t.Fatalf("the fold row must send no reorder, got %+v", cmd)
	default:
	}
}

// TestMoveTargetStepsOverAnEmptyUnit is the other half of the same bug, and the
// half that bites a card the user CAN move. A unit covering no panels — the fold
// row is the only one — used to abort the reorder outright, so the card next to the
// row was told "already first" while sitting in the middle of the list. At fifty
// panels the row is routinely index 0, which made that the normal case.
func TestMoveTargetStepsOverAnEmptyUnit(t *testing.T) {
	fleet := []panel.Panel{{ID: "1"}, {ID: "2"}, {ID: "3"}}
	units := [][]string{{"1"}, nil, {"2"}, {"3"}} // index 1 stands in for the fold row

	block, index, ok := moveTarget(fleet, units, 0, 1) // "1" moves later, past the row
	if !ok || len(block) != 1 || block[0] != "1" {
		t.Fatalf("moving past an empty unit should work, got block=%v ok=%v", block, ok)
	}
	if index != 1 {
		t.Fatalf("index = %d, want 1 (just past where \"2\" ends)", index)
	}

	block, index, ok = moveTarget(fleet, units, 2, -1) // "2" moves earlier, past the row
	if !ok || len(block) != 1 || block[0] != "2" || index != 0 {
		t.Fatalf("moving earlier past an empty unit: block=%v index=%d ok=%v", block, index, ok)
	}

	// A genuine end still reports one. There is nothing above the row.
	if _, _, ok := moveTarget(fleet, [][]string{nil, {"1"}}, 1, -1); ok {
		t.Fatal("a card with only an empty unit above it is already first")
	}
}

// --- the doubled caret --------------------------------------------------------

// TestSelectedFoldRowShowsOneMarker: the cursor caret and the row's disclosure
// glyph are the same character, so a selected fold row read "▸ ▸ 12 quiet". The
// row is drawn in inverse video anyway; the caret is the half that goes.
func TestSelectedFoldRowShowsOneMarker(t *testing.T) {
	m := foldedModel(t)
	items := m.dashItems()
	tree := m.renderTree(items, 0, len(items), len(items))
	if n := strings.Count(tree, "▸"); n != 1 {
		t.Fatalf("the selected fold row should carry exactly one ▸, the tree has %d:\n%s", n, tree)
	}
	// And the disclosure glyph is the one that survived: expanding flips it.
	m.foldOpen = map[string]bool{"": true}
	items = m.dashItems()
	if tree := m.renderTree(items, 0, len(items), len(items)); !strings.Contains(tree, "▾") {
		t.Fatalf("an expanded row should show ▾ in the tree:\n%s", tree)
	}
}
