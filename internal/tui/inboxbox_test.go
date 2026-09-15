package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/proto"
)

// inboxbox_test.go covers #94's first half: the inbox overlay draws a rounded
// box, and the bottom edge of that box has to be on the screen.
//
// The golden frames are taken at 120x40, which is tall enough that a clipped
// bottom is easy to miss unless you are looking for it. These assert the glyph
// at heights an operator actually has — a split terminal, a laptop with a
// browser above it — which is where the leftover under the banner runs out.

// shortInbox is an opened inbox on a terminal of the given height.
func shortInbox(t *testing.T, height int, panels ...proto.Panel) model {
	t.Helper()
	m := openedInbox(t, panels...)
	m.width, m.height = 120, height
	return m
}

// bottomBorderVisible reports whether the popup's closing edge survived Place.
// It looks for the rounded corner rather than the horizontal rule, because a
// clipped box still shows plenty of "─" from its own separators.
func bottomBorderVisible(view string) bool {
	return strings.Contains(view, "╰")
}

// TestInboxBoxClosesAtShortHeights is the reported bug. The overlay is a
// rounded popupBox and its "╰───╯" line does not land on the screen: the body
// is sized from height alone, while the overlay sits UNDER the banner, so the
// frame overflows the leftover and Place clips the first thing past the bottom.
//
// This is the same class 40e493f fixed for `?`. Help, the key map and
// panel-config size through panelVisibleRows + overlayStack; the inbox does not.
func TestInboxBoxClosesAtShortHeights(t *testing.T) {
	failed := wire("4", "exited", time.Minute)
	failed.ExitCode = 3
	rows := []proto.Panel{
		wire("1", "attention", 90*time.Second),
		wire("2", "stuck", 3*time.Hour),
		wire("3", "done", 20*time.Second),
		failed,
	}

	for _, height := range []int{24, 28, 32, 36, 40} {
		m := shortInbox(t, height, rows...)
		view := m.View().Content
		if !bottomBorderVisible(view) {
			t.Errorf("at height %d the inbox box has no bottom edge:\n%s", height, view)
		}
		if got := strings.Count(view, "\n") + 1; got > height {
			t.Errorf("at height %d the frame is %d rows — it overflows the terminal", height, got)
		}
	}
}

// TestInboxBoxClosesWhileComposing is the case with the most chrome: the
// composer replaces the two legend rows with two more of its own, and on a
// narrow terminal those wrap. A legend that wraps to a third line without the
// body shrinking clips the box again, which is why fitLegend's wrapping has to
// be measured rather than hoped.
func TestInboxBoxClosesWhileComposing(t *testing.T) {
	for _, height := range []int{24, 30, 40} {
		m := shortInbox(t, height, wire("1", "attention", 90*time.Second))
		m.inboxComposing, m.inboxReply = true, "yes"
		view := m.View().Content
		if !bottomBorderVisible(view) {
			t.Errorf("at height %d the composing inbox has no bottom edge:\n%s", height, view)
		}
		if got := strings.Count(view, "\n") + 1; got > height {
			t.Errorf("at height %d the composing frame is %d rows — it overflows", height, got)
		}
	}
}

// TestInboxBoxIsTheSameSizeInEveryState is the size constancy the overlay owes
// the operator: tab changes WHAT is listed, never how big the thing listing it
// is. An empty bucket used to collapse the box and the next stop bounced it
// open again, which reads as the overlay closing and reopening.
//
// It asserts the rendered height of the popup itself rather than the whole
// frame, so a change in the banner cannot make this pass by accident.
func TestInboxBoxIsTheSameSizeInEveryState(t *testing.T) {
	pinRender(t)
	failed := wire("4", "exited", time.Minute)
	failed.ExitCode = 3
	m := openedInbox(t,
		wire("1", "attention", 90*time.Second),
		wire("2", "stuck", 3*time.Hour),
		wire("3", "done", 20*time.Second),
		failed,
	)
	m.inboxDone = true
	m.width, m.height = 120, 40

	want := lipgloss.Height(m.inboxView())
	for _, f := range m.inboxStops() {
		m.inboxFilter = f
		m = m.snapInboxCursor()
		if got := lipgloss.Height(m.inboxView()); got != want {
			t.Errorf("the %q filter draws a %d-row box, want %d — tab must not resize the overlay",
				inboxFilterName(f), got, want)
		}
	}

	// A bucket that is genuinely empty is the case that used to collapse.
	only := openedInbox(t, wire("1", "attention", 90*time.Second))
	only.width, only.height = 120, 40
	full := lipgloss.Height(only.inboxView())
	only.inboxFilter = inboxFailed
	if got := lipgloss.Height(only.inboxView()); got != full {
		t.Errorf("an empty bucket draws a %d-row box, want %d", got, full)
	}

	// …and so did an empty queue.
	empty := openedInbox(t)
	empty.width, empty.height = 120, 40
	if got := lipgloss.Height(empty.inboxView()); got != want {
		t.Errorf("an empty queue draws a %d-row box, want %d", got, want)
	}
}

// TestInboxEmptyStatesSayWhichEmptinessItIs keeps the two apart. A filter with
// nothing in it while other buckets hold rows must not claim the queue is
// clear — that is a sentence the operator would act on.
func TestInboxEmptyStatesSayWhichEmptinessItIs(t *testing.T) {
	pinRender(t)
	empty := openedInbox(t)
	empty.width, empty.height = 120, 40
	if v := empty.inboxView(); !strings.Contains(v, "nothing needs a human right now") {
		t.Errorf("an empty QUEUE does not say so:\n%s", v)
	}

	filtered := openedInbox(t, wire("1", "attention", 90*time.Second))
	filtered.width, filtered.height = 120, 40
	filtered.inboxFilter = inboxFailed
	v := filtered.inboxView()
	if !strings.Contains(v, "no failed panels") {
		t.Errorf("an empty FILTER does not name the bucket:\n%s", v)
	}
	if strings.Contains(v, "nothing needs a human right now") {
		t.Error("an empty filter claims the whole queue is clear, which is a lie")
	}
}
