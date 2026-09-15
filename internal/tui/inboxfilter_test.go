package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/cmj0121/baton/internal/proto"
)

// inboxfilter_test.go covers #94's second half: tab cycles which bucket the
// queue shows.
//
// The rule the whole feature hangs on is that the filter is a DISPLAY MASK over
// the frozen order, never a re-sort. The order freezes while the cursor is in
// the inbox for a reason (ATTENTION.md § the order is the feature): a snapshot
// may change what a row says and must never pull it out from under the hand
// about to press x on it. A filter that re-derived the list would break that in
// the one place the operator is most committed.

// fourBuckets is one row in each bucket, in the order the queue sorts them.
func fourBuckets(t *testing.T) model {
	t.Helper()
	failed := wire("4", "exited", time.Minute)
	failed.ExitCode = 3
	m := openedInbox(t,
		wire("1", "attention", 90*time.Second),
		wire("2", "stuck", 3*time.Hour),
		wire("3", "done", 20*time.Second),
		failed,
	)
	m.inboxDone = true
	return m
}

// press sends one key to the open inbox.
func inboxPress(t *testing.T, m model, key string) model {
	t.Helper()
	out, _ := m.handleInboxKey(key, tea.Key{})
	mm, ok := out.(model)
	if !ok {
		t.Fatalf("handleInboxKey(%q) returned %T", key, out)
	}
	return mm
}

// visibleIDs is the ids the current mask shows, in the order they are drawn.
func visibleIDs(m model) []string {
	out := []string{}
	for _, i := range m.inboxVisible() {
		out = append(out, m.inboxRows[i].id)
	}
	return out
}

// TestInboxTabCycles walks the documented order and back round to the start.
// Empty buckets are not skipped — a cycle whose next stop depends on the fleet
// is the same disorientation a re-sorting queue is.
func TestInboxTabCycles(t *testing.T) {
	m := fourBuckets(t)
	if m.inboxFilter != inboxFilterAll {
		t.Fatalf("a fresh inbox opens on %d, want all", m.inboxFilter)
	}
	for _, want := range []int{inboxAttention, inboxStuck, inboxFailed, inboxDoneBucket, inboxFilterAll} {
		m = inboxPress(t, m, "tab")
		if m.inboxFilter != want {
			t.Fatalf("tab landed on %q, want %q", inboxFilterName(m.inboxFilter), inboxFilterName(want))
		}
	}
}

// TestInboxShiftTabCyclesBack is the other direction, the way ? already walks
// its purpose tabs.
func TestInboxShiftTabCyclesBack(t *testing.T) {
	m := fourBuckets(t)
	m = inboxPress(t, m, "shift+tab")
	if m.inboxFilter != inboxDoneBucket {
		t.Fatalf("shift+tab from all landed on %q, want done", inboxFilterName(m.inboxFilter))
	}
	m = inboxPress(t, m, "shift+tab")
	if m.inboxFilter != inboxFailed {
		t.Fatalf("shift+tab landed on %q, want failed", inboxFilterName(m.inboxFilter))
	}
}

// TestInboxDoneIsNotInTheCycleWhenOff is the one bucket that may not exist.
// settings.inbox-done false means a finished agent never joins the queue at
// all, so a stop for it would always be empty and would always be a lie.
func TestInboxDoneIsNotInTheCycleWhenOff(t *testing.T) {
	m := fourBuckets(t)
	m.inboxDone = false
	for i := 0; i < 8; i++ {
		m = inboxPress(t, m, "tab")
		if m.inboxFilter == inboxDoneBucket {
			t.Fatal("tab stopped on done while settings.inbox-done is off")
		}
	}
	if m.inboxFilter != inboxFilterAll {
		t.Errorf("four stops should return to all after 8 presses, got %q", inboxFilterName(m.inboxFilter))
	}
}

// TestInboxFilterIsAMaskNotASort is the constraint the whole feature hangs on,
// and it is written so that a re-derivation actually breaks it.
//
// Asserting that the ids are unchanged after a tab is not enough on its own: on
// a fleet that has not moved, sortedInboxRows returns exactly the frozen order,
// so a filter that re-sorted would pass. The fleet is therefore CHANGED first —
// a new attention panel that would sort to the FRONT — while the frozen queue
// keeps it out. A mask leaves the order alone; a re-sort pulls the new row in
// above the selection, which is precisely what freezing exists to prevent.
func TestInboxFilterIsAMaskNotASort(t *testing.T) {
	m := openedInbox(t,
		wire("2", "stuck", 3*time.Hour),
		wire("3", "done", 20*time.Second),
	)
	m.inboxDone = true

	// The fleet moves under the open inbox: a panel raises its hand, and it would
	// sort above everything the operator is currently looking at. reconcileInbox
	// appends it at the TAIL instead — that is the frozen order doing its job,
	// and it is what makes a re-sort observable from here on.
	arrived := []proto.Panel{
		wire("2", "stuck", 3*time.Hour),
		wire("3", "done", 20*time.Second),
		wire("9", "attention", time.Second),
	}
	m.observeWire(arrived)
	m.fleet = mergeFleet(arrived)
	before := rowIDs(m)
	if len(before) == 0 || before[len(before)-1] != "9" {
		t.Fatalf("the arrival did not land at the tail (%v); the fixture proves nothing", before)
	}
	if got := m.sortedInboxRows(); len(got) == 0 || got[0].id != "9" {
		t.Fatal("a re-sort would not move this queue, so this test cannot detect one")
	}

	m = inboxPress(t, m, "tab")

	if after := rowIDs(m); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("the frozen queue moved: %v then %v — filtering must not re-derive it", before, after)
	}
}

// TestInboxFilterShowsOnlyItsBucket is the plain behaviour, separate from the
// rule above so a failure says which of the two broke.
func TestInboxFilterShowsOnlyItsBucket(t *testing.T) {
	m := fourBuckets(t)
	for _, tc := range []struct {
		filter int
		want   string
	}{
		{inboxAttention, "1"},
		{inboxStuck, "2"},
		{inboxFailed, "4"},
		{inboxDoneBucket, "3"},
	} {
		m.inboxFilter = tc.filter
		if got := visibleIDs(m); len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s shows %v, want [%s]", inboxFilterName(tc.filter), got, tc.want)
		}
	}
	m.inboxFilter = inboxFilterAll
	if got := visibleIDs(m); len(got) != 4 {
		t.Errorf("all shows %v, want every row", got)
	}
}

// TestInboxFilterMovesTheCursorToAVisibleRow pins that tab never leaves the
// caret on something that is not drawn — and that x would therefore act on a
// row the operator cannot see.
func TestInboxFilterMovesTheCursorToAVisibleRow(t *testing.T) {
	m := fourBuckets(t)
	for i := 0; i < 5; i++ {
		m = inboxPress(t, m, "tab")
		vis := m.inboxVisible()
		if len(vis) == 0 {
			continue
		}
		found := false
		for _, idx := range vis {
			if idx == m.inboxCursor {
				found = true
			}
		}
		if !found {
			t.Fatalf("filter %q left the cursor at %d, which the mask hides",
				inboxFilterName(m.inboxFilter), m.inboxCursor)
		}
	}
}

// TestInboxJKWalkOnlyVisibleRows is the same rule for movement.
func TestInboxJKWalkOnlyVisibleRows(t *testing.T) {
	m := fourBuckets(t)
	m.inboxFilter = inboxAttention
	m = m.snapInboxCursor()
	start := m.inboxCursor

	m = inboxPress(t, m, "j")
	if m.inboxCursor != start {
		t.Errorf("j moved off the only visible row to %d", m.inboxCursor)
	}
	m = inboxPress(t, m, "k")
	if m.inboxCursor != start {
		t.Errorf("k moved off the only visible row to %d", m.inboxCursor)
	}
}

// TestInboxEmptyFilterStaysOpen is the empty state that is NOT an empty queue.
// afterClear closes the overlay when the last row is dismissed, which is right
// for "the queue is consumed" and wrong for "you asked to see failed and there
// are none" — other buckets still hold rows, and saying nothing needs a human
// would be a lie the operator would act on.
func TestInboxEmptyFilterStaysOpen(t *testing.T) {
	m := openedInbox(t, wire("1", "attention", 90*time.Second))
	m.inboxFilter = inboxFailed

	if m.mode != modeInbox {
		t.Fatal("an empty filter closed the overlay")
	}
	view := m.inboxView()
	if !strings.Contains(view, "no failed panels") {
		t.Errorf("the empty filter does not name the bucket:\n%s", view)
	}
	if strings.Contains(view, "nothing needs a human right now") {
		t.Error("an empty FILTER claims the whole queue is clear, which is a lie")
	}
	// The bar has to stay, or tab has nowhere legible to go.
	if !strings.Contains(view, "attention") {
		t.Error("the empty state dropped the tab bar")
	}
}

// TestInboxStatusFollowsTheMask pins the status line describing the list in
// front of the operator rather than the unfiltered total.
func TestInboxStatusFollowsTheMask(t *testing.T) {
	m := fourBuckets(t)
	if got := m.inboxStatus(); got != "inbox: 4 items" {
		t.Errorf("unfiltered status = %q", got)
	}
	m = inboxPress(t, m, "tab")
	m = inboxPress(t, m, "tab")
	m = inboxPress(t, m, "tab") // failed
	if got := m.inboxStatus(); got != "inbox: 1 failed" {
		t.Errorf("filtered status = %q, want it to name the mask", got)
	}
}

// TestInboxFilterResetsOnEveryOpen is the session-memory trap named in the
// issue: C-t a exists to clear what needs a human, and a remembered `done`
// filter would hide attention behind a key the operator did not press.
func TestInboxFilterResetsOnEveryOpen(t *testing.T) {
	m := fourBuckets(t)
	m = inboxPress(t, m, "tab")
	if m.inboxFilter == inboxFilterAll {
		t.Fatal("the filter did not move, so the reset proves nothing")
	}
	out, _ := m.closeInbox()
	closed := out.(model)
	again, _ := closed.openInbox()
	if got := again.(model).inboxFilter; got != inboxFilterAll {
		t.Errorf("the inbox reopened on %q, want all", inboxFilterName(got))
	}
}

// TestComposerOwnsTab is the fence the issue is explicit about: while `i` is
// open the composer owns the keyboard outright, so tab is not a filter change —
// the row being answered must not vanish under the reply field.
func TestComposerOwnsTab(t *testing.T) {
	m := fourBuckets(t)
	m.inboxComposing, m.inboxReply = true, "yes"
	before := m.inboxFilter

	m = inboxPress(t, m, "tab")
	if m.inboxFilter != before {
		t.Errorf("tab changed the filter to %q while the composer was open",
			inboxFilterName(m.inboxFilter))
	}
	if !m.inboxComposing {
		t.Error("tab closed the composer")
	}
}

// TestInboxTabBarShowsEveryStopWithCounts pins what makes tab worth pressing —
// or not worth pressing — without pressing it.
func TestInboxTabBarShowsEveryStopWithCounts(t *testing.T) {
	m := fourBuckets(t)
	bar := m.inboxTabBar()
	for _, want := range []string{"all 4", "attention 1", "stuck 1", "failed 1", "done 1"} {
		if !strings.Contains(bar, want) {
			t.Errorf("the tab bar is missing %q:\n%s", want, bar)
		}
	}
}

// TestInboxDismissUnderAFilterKeepsTheCursorVisible covers the row that slides
// into the cleared index: it may belong to a bucket the mask hides, and the
// next x would then act on something not drawn.
func TestInboxDismissUnderAFilterKeepsTheCursorVisible(t *testing.T) {
	extra := wire("5", "attention", 30*time.Second)
	m := openedInbox(t,
		wire("1", "attention", 90*time.Second),
		extra,
		wire("2", "stuck", 3*time.Hour),
	)
	m.inboxFilter = inboxAttention
	m = m.snapInboxCursor()

	out, _ := m.dismissInboxRow()
	mm := out.(model)
	if mm.mode != modeInbox {
		t.Fatal("dismissing one of several rows closed the overlay")
	}
	for _, idx := range mm.inboxVisible() {
		if idx == mm.inboxCursor {
			return
		}
	}
	t.Errorf("after a dismiss the cursor sits at %d, which the %q mask hides",
		mm.inboxCursor, inboxFilterName(mm.inboxFilter))
}
