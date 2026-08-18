package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// inboxEpoch is the fixed "now" every inbox case works from, so an age and a
// snooze expiry are both exact numbers rather than something the wall clock
// drifts under.
var inboxEpoch = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// wire builds one wire panel for the queue: its id, its state, and how long ago
// it entered that state. Everything else a row reads has an option below.
func wire(id, state string, ago time.Duration) proto.Panel {
	return proto.Panel{
		ID:    id,
		Kind:  proto.KindAgent,
		Title: "panel " + id,
		State: state,
		Since: inboxEpoch.Add(-ago).Format(time.RFC3339Nano),
	}
}

// inboxModel is a cockpit sitting on a given fleet, with the inbox's settings at
// their defaults and no live client (so sends are no-ops unless a case wires one).
func inboxModel(panels ...proto.Panel) model {
	m := baseModel()
	m.now = inboxEpoch
	m.inboxDone = true
	m.observeWire(panels)
	m.fleet = mergeFleet(panels)
	return m
}

// openedInbox is inboxModel already in modeInbox.
func openedInbox(t *testing.T, panels ...proto.Panel) model {
	t.Helper()
	m := inboxModel(panels...)
	out, _ := m.openInbox()
	mm, ok := out.(model)
	if !ok {
		t.Fatal("openInbox returned something other than a model")
	}
	if mm.mode != modeInbox {
		t.Fatalf("mode = %v, want modeInbox", mm.mode)
	}
	return mm
}

// rowIDs is the queue's order, for asserting on it in one line.
func rowIDs(m model) []string {
	out := make([]string, len(m.inboxRows))
	for i, r := range m.inboxRows {
		out[i] = r.id
	}
	return out
}

func eqIDs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestInboxOrdersByBucketThenAge is the ordering rule, and the ordering rule is
// the feature: "answer me" must never be buried under "review me". A `done` panel
// that has been waiting an hour still sorts below an `attention` raised a minute
// ago, because only one of them is a question.
func TestInboxOrdersByBucketThenAge(t *testing.T) {
	failed := wire("4", "exited", 30*time.Minute)
	failed.ExitCode = 2
	m := openedInbox(t,
		wire("1", "done", time.Hour),
		wire("2", "attention", time.Minute),
		wire("3", "stuck", 20*time.Minute),
		failed,
		wire("5", "attention", 5*time.Minute),
		wire("6", "idle", time.Hour),    // never a row
		wire("7", "running", time.Hour), // nor this
	)

	if got := rowIDs(m); !eqIDs(got, "5", "2", "3", "4", "1") {
		t.Fatalf("queue order = %v, want attention(oldest first), stuck, failed, done", got)
	}
}

// TestCleanExitIsNotNews. `failed` is a rendering, not a state: the daemon reports
// the exit code and the cockpit draws the conclusion. A panel that exited zero
// finished, and finishing is not a reason to interrupt anybody.
func TestCleanExitIsNotNews(t *testing.T) {
	clean := wire("1", "exited", time.Minute)
	bad := wire("2", "exited", time.Minute)
	bad.ExitCode = 1

	if got := rowIDs(openedInbox(t, clean, bad)); !eqIDs(got, "2") {
		t.Fatalf("only the non-zero exit belongs in the queue, got %v", got)
	}
}

// TestInboxTiesBreakByPanelIDNumerically. Two panels that entered their state in
// the same instant must still land in the same order on every cockpit, and ids
// are decimals — so "10" comes after "9", not before it as a string compare would
// have it.
func TestInboxTiesBreakByPanelIDNumerically(t *testing.T) {
	m := openedInbox(t,
		wire("10", "attention", time.Minute),
		wire("9", "attention", time.Minute),
		wire("2", "attention", time.Minute),
	)
	if got := rowIDs(m); !eqIDs(got, "2", "9", "10") {
		t.Fatalf("a tie should break numerically by id, got %v", got)
	}
}

// TestInboxDoneOffRemovesTheBucket: settings.inbox-done false, and a finished
// agent simply never joins the queue. The rest of it is untouched.
func TestInboxDoneOffRemovesTheBucket(t *testing.T) {
	panels := []proto.Panel{wire("1", "done", time.Hour), wire("2", "attention", time.Minute)}

	m := inboxModel(panels...)
	m.inboxDone = false
	out, _ := m.openInbox()
	mm := out.(model)

	if got := rowIDs(mm); !eqIDs(got, "2") {
		t.Fatalf("inbox-done: false should keep finished agents out, got %v", got)
	}
}

// TestInboxDoneOffIsWhatThePrefsSay pins the config key to the model field, since
// a setting that loads into nothing is the failure nobody notices.
func TestInboxDoneOffIsWhatThePrefsSay(t *testing.T) {
	off := false
	if p := prefsFromConfig(config.Config{Settings: config.Settings{InboxDone: &off}}); p.inboxDone {
		t.Error("inbox-done: false should reach the prefs")
	}
	if def := prefsFromConfig(config.Config{}); !def.inboxDone {
		t.Error("an unset config should default the bucket on")
	}
}

// TestInboxSkipsAckedAndTheSingletons. An acknowledgement is fleet state, so a row
// another cockpit already cleared must not be re-offered here; the conductor and
// the global shell are infrastructure, and a queue with a permanent floor of two
// is a queue nobody believes.
func TestInboxSkipsAckedAndTheSingletons(t *testing.T) {
	done := wire("1", "attention", time.Minute)
	done.Acked = true
	cond := wire("2", "attention", time.Minute)
	cond.Conductor = true
	gs := wire("3", "attention", time.Minute)
	gs.GlobalShell = true

	if got := rowIDs(openedInbox(t, done, cond, gs, wire("4", "attention", time.Minute))); !eqIDs(got, "4") {
		t.Fatalf("acked and singleton panels must not be rows, got %v", got)
	}
}

// TestInboxOrderFreezesUnderTheCursor is the promise of §5.4, and the one a queue
// lives or dies by: the thing you are about to press x on must still be the thing
// you read. A snapshot arrives that WOULD re-sort the list — an older attention
// appears, an existing row's state changes — and the order does not move, nor does
// the cursor.
func TestInboxOrderFreezesUnderTheCursor(t *testing.T) {
	m := openedInbox(t,
		wire("1", "attention", time.Minute),
		wire("2", "attention", 2*time.Minute),
		wire("3", "done", time.Hour),
	)
	if got := rowIDs(m); !eqIDs(got, "2", "1", "3") {
		t.Fatalf("setup order = %v", got)
	}
	m = press(m, "j") // cursor on row 1 ("1")

	// A far older attention shows up. Sorted, it would take the top of the list and
	// push everything down a line, moving the row under the cursor.
	m.observeWire([]proto.Panel{
		wire("1", "attention", time.Minute),
		wire("2", "attention", 2*time.Minute),
		wire("3", "done", time.Hour),
		wire("4", "attention", 3*time.Hour),
	})

	if got := rowIDs(m); !eqIDs(got, "2", "1", "3", "4") {
		t.Fatalf("a new qualifier must append at the tail, got %v", got)
	}
	if m.inboxCursor != 1 || m.inboxRows[m.inboxCursor].id != "1" {
		t.Fatalf("the cursor should still be on row 1 (%q), got %d (%q)",
			"1", m.inboxCursor, m.inboxRows[m.inboxCursor].id)
	}
}

// TestInboxMarksStaleRatherThanYanking. A panel that wakes to running while you
// are three rows below it must not renumber the list. It greys out and says so,
// and the row keeps its place until r.
func TestInboxMarksStaleRatherThanYanking(t *testing.T) {
	m := openedInbox(t,
		wire("1", "attention", time.Minute),
		wire("2", "stuck", time.Minute),
		wire("3", "done", time.Minute),
	)
	m = press(m, "j", "j") // cursor on the last row

	m.observeWire([]proto.Panel{
		wire("1", "running", 0), // woke up: no longer qualifies
		wire("2", "stuck", time.Minute),
		wire("3", "done", time.Minute),
	})

	if got := rowIDs(m); !eqIDs(got, "1", "2", "3") {
		t.Fatalf("a row that stopped qualifying must stay put, got %v", got)
	}
	if !m.inboxRows[0].stale {
		t.Error("the woken panel's row should be marked stale")
	}
	if m.inboxCursor != 2 {
		t.Errorf("the cursor must not move, got %d", m.inboxCursor)
	}

	// A panel that vanishes entirely goes stale too — the row remembers what it
	// last showed rather than renumbering everything below it.
	m.observeWire([]proto.Panel{wire("2", "stuck", time.Minute), wire("3", "done", time.Minute)})
	if !m.inboxRows[0].stale || m.inboxRows[0].title != "panel 1" {
		t.Errorf("a closed panel's row should grey out in place, got %+v", m.inboxRows[0])
	}
}

// TestInboxRefreshReSortsAndReanchors: r is the explicit re-sort, and it puts the
// cursor back on the row it was on BY IDENTITY. Re-anchoring by index would be the
// one thing worse than re-sorting under the hand.
func TestInboxRefreshReSortsAndReanchors(t *testing.T) {
	m := openedInbox(t, wire("1", "attention", time.Minute), wire("2", "done", time.Minute))
	m = press(m, "j") // cursor on "2"

	m.observeWire([]proto.Panel{
		wire("1", "running", 0), // stale now
		wire("2", "done", time.Minute),
		wire("3", "attention", time.Hour), // appended at the tail while frozen
	})
	if got := rowIDs(m); !eqIDs(got, "1", "2", "3") {
		t.Fatalf("frozen order = %v", got)
	}

	m = press(m, "r")
	if got := rowIDs(m); !eqIDs(got, "3", "2") {
		t.Fatalf("r should re-sort and drop the stale row, got %v", got)
	}
	if m.inboxRows[m.inboxCursor].id != "2" {
		t.Errorf("the cursor should follow its row by id, got %q", m.inboxRows[m.inboxCursor].id)
	}
}

// TestInboxRefreshFallsBackToTheNearestRow. When the anchored row is one of the
// ones r just dropped, the cursor lands on the nearest surviving index rather than
// jumping to the top.
func TestInboxRefreshFallsBackToTheNearestRow(t *testing.T) {
	m := openedInbox(t,
		wire("1", "attention", 3*time.Minute),
		wire("2", "attention", 2*time.Minute),
		wire("3", "attention", time.Minute))
	m = press(m, "j", "j") // cursor on "3"

	m.observeWire([]proto.Panel{
		wire("1", "attention", 3*time.Minute),
		wire("2", "attention", 2*time.Minute),
		wire("3", "running", 0),
	})
	m = press(m, "r")

	if got := rowIDs(m); !eqIDs(got, "1", "2") {
		t.Fatalf("r should drop the stale row, got %v", got)
	}
	if m.inboxCursor != 1 {
		t.Errorf("the cursor should clamp to the last surviving row, got %d", m.inboxCursor)
	}
}

// TestInboxOpensFromEveryViewAndReturns. C-t a is an escape, so it is reached
// after the leader in every command-mode view and from inside a zoom — the view
// you land in after pressing enter on a row, which is exactly where wanting the
// queue back is most likely.
func TestInboxOpensFromEveryViewAndReturns(t *testing.T) {
	for _, from := range []mode{modeDashboard, modeKeyMap, modeHelp, modePanelConfig} {
		m := inboxModel(wire("1", "attention", time.Minute))
		m.mode = from
		m = press(m, "ctrl+t", "a")
		if m.mode != modeInbox {
			t.Fatalf("C-t a from mode %v did not open the inbox, got %v", from, m.mode)
		}
		if m = press(m, "esc"); m.mode != from {
			t.Errorf("esc should restore mode %v, got %v", from, m.mode)
		}
	}

	// …and from a zoom, whose armed-prefix handler is a switch of its own.
	z := inboxModel(wire("1", "attention", time.Minute))
	z.mode = modeZoom
	z.zoomID = "1"
	out, _ := z.handleZoomKey(key("ctrl+t"))
	z = out.(model)
	out, _ = z.handleZoomKey(key("a"))
	if z = out.(model); z.mode != modeInbox {
		t.Fatalf("C-t a in a zoom should open the inbox, got %v", z.mode)
	}
	if z = press(z, "esc"); z.mode != modeZoom {
		t.Errorf("esc should drop back into the zoom, got %v", z.mode)
	}
}

// TestInboxZoomNoAck is Decision 2 as a test. Opening
// something is not dealing with it: a queue that empties because you LOOKED at it
// is a queue you stop trusting, so the row must still be there when you come back.
func TestInboxZoomNoAck(t *testing.T) {
	c, cmds := recordingServer(t)
	m := inboxModel(wire("1", "done", time.Minute), wire("2", "attention", time.Minute))
	m.client = c
	out, _ := m.openInbox()
	m = out.(model)

	m = press(m, "enter")
	if m.mode != modeZoom {
		t.Fatalf("enter should zoom, got mode %v", m.mode)
	}
	waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.attach" })

	// Nothing acknowledged the row on the way out.
	for {
		select {
		case c := <-cmds:
			if c.Action == "panel.ack" {
				t.Fatal("zooming must not acknowledge the row")
			}
			continue
		default:
		}
		break
	}
	// …and re-opening the queue still holds it.
	m = press(m, "ctrl+t", "a")
	if got := rowIDs(m); !eqIDs(got, "2", "1") {
		t.Fatalf("the zoomed row should still be queued, got %v", got)
	}
}

// TestInboxEmptyQueueStillOpens. "Nothing needs you" is information; bouncing
// straight back with a status line looks like the key did not work.
func TestInboxEmptyQueueStillOpens(t *testing.T) {
	m := openedInbox(t, wire("1", "running", 0))
	if len(m.inboxRows) != 0 {
		t.Fatalf("expected an empty queue, got %v", rowIDs(m))
	}
	if m.status != "inbox: clear" {
		t.Errorf("status = %q, want \"inbox: clear\"", m.status)
	}
	if !strings.Contains(m.inboxView(), "nothing needs a human") {
		t.Error("an empty inbox should say so")
	}
}

// --- the tail pane ------------------------------------------------------------

// TestInboxTailOneAtATime. The detail pane is a per-keystroke door: at most
// one request may be outstanding, a reply for a row the cursor has left is cached
// rather than drawn, and a cached tail is reused so walking back up a queue you
// have already read costs nothing.
func TestInboxTailOneAtATime(t *testing.T) {
	c, cmds := recordingServer(t)
	m := inboxModel(wire("1", "attention", time.Minute), wire("2", "attention", 2*time.Minute))
	m.client = c
	out, _ := m.openInbox()
	m = out.(model)

	if m.inboxTailWant != m.inboxRows[0].id {
		t.Fatalf("opening should request the selected row's tail, got %q", m.inboxTailWant)
	}
	first := m.inboxRows[0].id
	waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.tail" && c.ID == first })

	// Moving while a request is in flight must not open a second one.
	m = press(m, "j")
	if m.inboxTailWant != first {
		t.Fatalf("a second request must wait for the first, want in-flight %q, got %q", first, m.inboxTailWant)
	}

	// The late reply for the row we left is cached, not rendered…
	m.applyTail(first, []byte("Apply this refactor? [y/N] "))
	if _, ok := m.inboxTails[first]; !ok {
		t.Error("a late reply should still be cached — the bytes are already paid for")
	}
	// …and settling it fires the request the cursor is now waiting on.
	second := m.inboxRows[1].id
	if m.inboxTailWant != second {
		t.Fatalf("the next request should fire immediately, got %q", m.inboxTailWant)
	}
	m.applyTail(second, []byte("still building\n"))
	if !strings.Contains(strings.Join(m.inboxDetailBlock(40, 6), "\n"), "still building") {
		t.Error("the selected row's tail should be what the pane shows")
	}

	// Walking back up costs nothing: the cached tail is shown and no request goes out.
	m = press(m, "k")
	if m.inboxTailWant != "" {
		t.Errorf("a cached tail should need no request, got %q in flight", m.inboxTailWant)
	}
	if !strings.Contains(strings.Join(m.inboxDetailBlock(40, 6), "\n"), "[y/N]") {
		t.Error("the cached tail should be shown instantly")
	}
}

// TestInboxTailCacheIsBounded. The cache exists to make a triage session free, not
// to hold every panel the cockpit has ever looked at.
func TestInboxTailCacheIsBounded(t *testing.T) {
	m := openedInbox(t, wire("1", "attention", time.Minute))
	for i := range inboxTailCache + 10 {
		m.applyTail(string(rune('a'+i%26))+strings.Repeat("!", i/26+1), []byte("x"))
	}
	if len(m.inboxTails) > inboxTailCache {
		t.Fatalf("the tail cache should hold at most %d, got %d", inboxTailCache, len(m.inboxTails))
	}
	if len(m.inboxTailOrder) != len(m.inboxTails) {
		t.Errorf("the eviction ring and the cache should agree: %d vs %d", len(m.inboxTailOrder), len(m.inboxTails))
	}
}

// TestInboxTailShowsTheDeclaredReason. When an agent said why in its own words,
// the pane leads with that; the tail is what a heuristic or a timer leaves you to
// read instead.
func TestInboxTailShowsTheDeclaredReason(t *testing.T) {
	p := wire("1", "attention", time.Minute)
	p.Reason = "which migration do I run first?"
	m := openedInbox(t, p)
	m.applyTail("1", []byte("waiting\n"))

	pane := strings.Join(m.inboxDetailBlock(60, 8), "\n")
	if !strings.Contains(pane, "which migration do I run first?") {
		t.Errorf("the agent's own words should lead the pane, got %q", pane)
	}
	// The server already scrubbed the reason on the way in (sanitizeReason), so the
	// cockpit renders it as-is — a second escaping pass would mangle a legitimate
	// one, and the text arriving here is safe by contract.
	if strings.Contains(pane, "\\x1b") {
		t.Error("the reason must not be escaped a second time")
	}
}

// TestInboxViewRows is a render smoke test over all four buckets
// plus a stale row and an open composer, so a width or padding mistake shows up as
// a failure rather than as a corrupted frame in front of a user.
func TestInboxViewRows(t *testing.T) {
	failed := wire("4", "exited", time.Minute)
	failed.ExitCode = 3
	m := openedInbox(t,
		wire("1", "attention", 90*time.Second),
		wire("2", "stuck", 3*time.Hour),
		wire("3", "done", 20*time.Second),
		failed,
	)
	m.inboxRows[1].stale = true
	m.applyTail("1", []byte("a\nb\nc\n"))

	for _, w := range []int{40, 120} {
		m.width = w
		if out := m.inboxView(); !strings.Contains(out, spaced("INBOX")) {
			t.Fatalf("width %d: the header should survive, got %q", w, out)
		}
	}
	// The composer's "no echo" hint has to survive every width the cockpit will
	// render at, not just a wide one. The pop-up clips per line, so a hint that
	// does not fit loses its tail — and the tail is the half that says how to
	// watch the reply land, which is the only actionable thing on the line.
	m.inboxComposing, m.inboxReply = true, "yes"
	for _, w := range []int{40, 120} {
		m.width = w
		out := m.inboxView()
		if !strings.Contains(out, "no echo") {
			t.Errorf("width %d: the composer should say plainly that no echo comes back", w)
		}
		if !strings.Contains(out, "enter zooms") {
			t.Errorf("width %d: the hint must not be cut off before it says how to watch it land", w)
		}
	}
	m.width = 120
	if !strings.Contains(m.View(), spaced("INBOX")) {
		t.Error("modeInbox should render through the cockpit's own View")
	}
}

// TestInboxUnknownAgeRendersADash. DESIGN §3.3 deliberately does not bump
// ProtocolVersion, so a new cockpit against an older daemon is a supported
// pairing — and that daemon stamps no Since. Subtracting a zero instant from now
// is 2562047h, which eats eight of a thirty-column row and, being the oldest
// thing the queue has ever seen, sorts to the very top. The first row a user
// reads would be nonsense.
func TestInboxUnknownAgeRendersADash(t *testing.T) {
	old := wire("1", "attention", time.Minute)
	old.Since = "" // an older daemon: it does not know the field exists
	m := openedInbox(t, old)

	row := strings.Join(m.inboxRowLines(30, 4), "\n")
	if strings.Contains(row, "2562047h") {
		t.Fatalf("an unknown age must not render as a number, got %q", row)
	}
	if !strings.Contains(row, "—") {
		t.Errorf("an unknown age should say so, got %q", row)
	}
	// …and the title still gets its columns, which is what the nonsense was
	// stealing.
	if !strings.Contains(row, "panel 1") {
		t.Errorf("the title should survive an unknown age, got %q", row)
	}
}

// TestInboxRetriesADroppedTail. The daemon's outbound send has a default arm and
// drops a frame when a client's write queue is full, on purpose, so one slow
// reader cannot stall the fleet. A single-flight gate with no expiry turns that
// one dropped frame into a permanently blank detail pane — for every row, for the
// rest of the session.
func TestInboxRetriesADroppedTail(t *testing.T) {
	c, cmds := recordingServer(t)
	m := inboxModel(wire("1", "attention", 2*time.Minute), wire("2", "attention", time.Minute))
	m.client = c
	out, _ := m.openInbox()
	m = out.(model)

	first := m.inboxRows[0].id
	waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.tail" && c.ID == first })
	if m.inboxTailWant != first {
		t.Fatalf("the first request should be in flight, got %q", m.inboxTailWant)
	}

	// The reply never arrives. Before the threshold the gate still holds — a
	// round trip is allowed to take a moment, and re-asking every keystroke would
	// be its own bug.
	m.now = m.now.Add(inboxTailStale - time.Second)
	m = press(m, "j")
	if m.inboxTailWant != first {
		t.Fatalf("the gate should still hold inside the threshold, got %q", m.inboxTailWant)
	}

	// Past it, the inbox asks again for whatever the cursor is on now.
	m.now = m.now.Add(2 * time.Second)
	m.wantTail()
	second := m.inboxRows[1].id
	if m.inboxTailWant != second {
		t.Fatalf("a dropped reply should be re-asked, got %q in flight", m.inboxTailWant)
	}
	waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.tail" && c.ID == second })

	// A snapshot fires the retry too: a reader staring at one row makes no cursor
	// moves, and their pane must still come back.
	m.now = m.now.Add(2 * inboxTailStale)
	m.observeWire([]proto.Panel{wire("1", "attention", 2*time.Minute), wire("2", "attention", time.Minute)})
	if m.inboxTailAt.Before(m.now) {
		t.Errorf("a snapshot should re-ask a stale request, last asked at %v (now %v)", m.inboxTailAt, m.now)
	}
}

// TestInboxLegendNeverLosesTheWayOut. The pop-up clips each line at its right
// edge, so an over-long legend loses its LAST cell — and the last cell is where
// the exit lives. The queue is an overlay a human enters to get work done and
// then leaves; a legend that fits at 120 columns and drops "esc close" at 60 is a
// legend that strands them.
//
// The width check is on inboxFooter rather than on the rendered box on purpose:
// the footer must arrive already fitted, not be cut to size by the frame around
// it. minWidth is the narrowest viewport the cockpit will render at all, so it is
// the real floor rather than a pessimistic one.
func TestInboxLegendNeverLosesTheWayOut(t *testing.T) {
	m := openedInbox(t, wire("1", "attention", time.Minute))

	for _, w := range []int{minWidth, 40, 120} {
		m.width = w
		foot := m.inboxFooter()
		if !strings.Contains(foot, "close") {
			t.Errorf("width %d: the legend must always say how to leave, got %q", w, foot)
		}
		for _, line := range strings.Split(foot, "\n") {
			if got := lipgloss.Width(line); got > m.popupWidth() {
				t.Errorf("width %d: legend line is %d cells, pop-up holds %d: %q",
					w, got, m.popupWidth(), line)
			}
		}
	}
}

// TestInboxTitleIsSanitised. A reason is scrubbed by the server; a TITLE is not —
// it is built from a command line and a directory and has had no such pass — so
// the queue column runs it through the cockpit's own strip like every other
// untrusted string it draws.
func TestInboxTitleIsSanitised(t *testing.T) {
	p := wire("1", "attention", time.Minute)
	p.Title = "agent\x1b]52;c;cGF5bG9hZA==\x07"
	m := openedInbox(t, p)

	if out := strings.Join(m.inboxRowLines(30, 4), "\n"); strings.Contains(out, "\x1b]52") {
		t.Errorf("an OSC 52 clipboard write must never reach the terminal, got %q", out)
	}
}

// TestInboxIgnoresPanelStatesItHasNoBusinessWith keeps the qualifying set honest:
// the queue is what needs a human, and a spawning or running panel does not.
func TestInboxIgnoresPanelStatesItHasNoBusinessWith(t *testing.T) {
	for _, state := range []string{"spawning", "running", "idle"} {
		if got := rowIDs(openedInbox(t, wire("1", state, time.Hour))); len(got) != 0 {
			t.Errorf("%s should never be a row, got %v", state, got)
		}
	}
	// …and an unknown state from a newer daemon under-claims to idle rather than
	// inventing a row, which is panel.ParseState's contract.
	if got := rowIDs(openedInbox(t, wire("1", "teleporting", time.Hour))); len(got) != 0 {
		t.Errorf("an unknown state should not become a row, got %v", got)
	}
}

// TestInboxRowRendersFailedNotExited. `failed` is not a lifecycle state — the
// daemon reports the exit code and the cockpit draws the conclusion — and the
// queue draws the same conclusion the card does.
func TestInboxRowRendersFailedNotExited(t *testing.T) {
	bad := inboxRow{state: panel.Exited, code: 1}
	if got := bad.info().label; got != "failed" {
		t.Errorf("a non-zero exit should render as failed, got %q", got)
	}
	ok := inboxRow{state: panel.Exited}
	if got := ok.info().label; got != "exited" {
		t.Errorf("a clean exit should render as exited, got %q", got)
	}
}

// TestCompactAgeReadsLikeTheDaemons keeps the queue's age column in step with the
// activity line a card shows, which is the same number from the other side.
func TestCompactAgeReadsLikeTheDaemons(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "0s"}, // a clock skew across the --remote hop, not a negative age
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m"},
		{3 * time.Hour, "3h"},
	}
	for _, tc := range cases {
		if got := compactAge(tc.d); got != tc.want {
			t.Errorf("compactAge(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// --- clearing a row -----------------------------------------------------------

// clientInbox is an open inbox wired to a recording server, so a case can assert
// exactly what travelled over the socket.
func clientInbox(t *testing.T, panels ...proto.Panel) (model, <-chan proto.Command) {
	t.Helper()
	c, cmds := recordingServer(t)
	m := inboxModel(panels...)
	m.client = c
	out, _ := m.openInbox()
	return out.(model), cmds
}

// TestInboxReplyAnswersAndAdvances is the issue's actual ask, in one test.
// Handling an item today costs navigate, enter, read, type, C-t d — a full screen
// swap that loses the fleet. Here it costs i, the answer, enter: the bytes reach
// the panel, the row is acknowledged in the same breath, and the CURSOR DOES NOT
// MOVE — so the next item is already under it.
func TestInboxReplyAnswersAndAdvances(t *testing.T) {
	m, cmds := clientInbox(t,
		wire("1", "attention", 2*time.Minute),
		wire("2", "attention", time.Minute),
	)
	if got := rowIDs(m); !eqIDs(got, "1", "2") {
		t.Fatalf("setup order = %v", got)
	}

	m = press(m, "i")
	if !m.inboxComposing {
		t.Fatal("i should open the composer")
	}
	m = press(m, "y", "e", "s")
	m = press(m, "enter")

	in := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.input" })
	if in.ID != "1" || string(in.Data) != "yes\n" {
		t.Errorf("panel.input = %q to %q, want \"yes\\n\" to \"1\"", in.Data, in.ID)
	}
	ack := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.ack" })
	if ack.ID != "1" || ack.Until != "" {
		t.Errorf("the reply IS the acknowledgement: got %+v", ack)
	}

	if m.inboxComposing {
		t.Error("the composer should close on send")
	}
	if got := rowIDs(m); !eqIDs(got, "2") {
		t.Fatalf("the answered row should leave the queue, got %v", got)
	}
	if m.inboxCursor != 0 || m.inboxRows[0].id != "2" {
		t.Errorf("the cursor should stay at index 0 with the next item under it, got %d", m.inboxCursor)
	}
	// The server's confirming broadcast has not arrived yet, so the panel still
	// qualifies. The row must NOT reappear at the bottom of the queue.
	m.observeWire([]proto.Panel{wire("1", "attention", 2*time.Minute), wire("2", "attention", time.Minute)})
	if got := rowIDs(m); !eqIDs(got, "2") {
		t.Fatalf("an optimistically cleared row must not come back, got %v", got)
	}
	// …and once the ack lands, the hold is released.
	acked := wire("1", "attention", 2*time.Minute)
	acked.Acked = true
	m.observeWire([]proto.Panel{acked, wire("2", "attention", time.Minute)})
	if m.inboxCleared["1"] {
		t.Error("a confirmed clear should stop being held down")
	}
}

// TestInboxClearRowLeavesAnOlderModelIntact. The receiver is a VALUE, so shifting
// the rows in place would write through the backing array that an earlier copy of
// the model still points at. bubbletea passes the model along linearly today,
// which makes it harmless today — and makes it exactly the kind of trap that is
// expensive to find on the day something holds a copy.
func TestInboxClearRowLeavesAnOlderModelIntact(t *testing.T) {
	m, _ := clientInbox(t,
		wire("1", "attention", 3*time.Minute),
		wire("2", "attention", 2*time.Minute),
		wire("3", "attention", time.Minute))
	before := rowIDs(m)

	_ = m.clearRow(0, time.Time{}, "") // the newer model is discarded on purpose

	if got := rowIDs(m); !eqIDs(got, before...) {
		t.Fatalf("clearing must not write through the older model's rows, got %v, want %v", got, before)
	}
}

// TestInboxClearRowDropsTheTailFromTheRing. The eviction ring has to be kept in
// step with the cache it evicts from: a dead id left behind occupies one of the
// thirty-two slots, can be enqueued a second time if the row comes back, and —
// worst — evicts the FRESH entry when it reaches the head. The cache would be
// quietly smaller than it is documented to be.
func TestInboxClearRowDropsTheTailFromTheRing(t *testing.T) {
	m, _ := clientInbox(t, wire("1", "attention", 2*time.Minute), wire("2", "attention", time.Minute))
	m.applyTail("1", []byte("first"))
	m.applyTail("2", []byte("second"))

	m = m.clearRow(0, time.Time{}, "") // clears row "1"

	for _, id := range m.inboxTailOrder {
		if id == "1" {
			t.Fatalf("a cleared row must leave the eviction ring, got %v", m.inboxTailOrder)
		}
	}
	if len(m.inboxTailOrder) != len(m.inboxTails) {
		t.Fatalf("the ring and the cache must agree: %v vs %d entries", m.inboxTailOrder, len(m.inboxTails))
	}

	// The same id caching again enqueues once, not twice.
	m.applyTail("1", []byte("again"))
	seen := 0
	for _, id := range m.inboxTailOrder {
		if id == "1" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("a re-cached id should sit in the ring exactly once, got %d in %v", seen, m.inboxTailOrder)
	}
}

// TestInboxEmptyReplyIsARealAnswer. Accepting a [y/N] default by pressing enter is
// the commonest confirmation there is, and refusing it would make it the one case
// the inbox cannot handle.
func TestInboxEmptyReplyIsARealAnswer(t *testing.T) {
	m, cmds := clientInbox(t, wire("1", "attention", time.Minute))

	press(m, "i", "enter")

	in := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.input" })
	if string(in.Data) != "\n" {
		t.Errorf("an empty reply should send a bare newline, got %q", in.Data)
	}
}

// TestInboxComposerOwnsTheKeyboard. A reply containing the letter x must not
// dismiss the row it is answering, and one containing r must not re-sort the queue
// out from under it.
func TestInboxComposerOwnsTheKeyboard(t *testing.T) {
	m, _ := clientInbox(t, wire("1", "attention", time.Minute), wire("2", "done", time.Minute))

	m = press(m, "i", "x", "r", "space", "j")
	if m.inboxReply != "xr j" {
		t.Errorf("every key should be text while composing, got %q", m.inboxReply)
	}
	if len(m.inboxRows) != 2 {
		t.Fatalf("no row should have been cleared, got %v", rowIDs(m))
	}

	// backspace and ctrl+u edit the line; esc leaves the row exactly where it was.
	m = press(m, "backspace")
	if m.inboxReply != "xr " {
		t.Errorf("backspace should delete one rune, got %q", m.inboxReply)
	}
	m = press(m, "ctrl+u")
	if m.inboxReply != "" {
		t.Errorf("ctrl+u should clear the line, got %q", m.inboxReply)
	}
	m = press(m, "esc")
	if m.inboxComposing || m.mode != modeInbox || len(m.inboxRows) != 2 {
		t.Errorf("esc should cancel the composer and keep the row, got composing=%v mode=%v rows=%v",
			m.inboxComposing, m.mode, rowIDs(m))
	}
}

// TestInboxReplyRefusedOnAnExitedPanel. A write to a dead PTY is a silent no-op
// server-side, so without this guard the reply would vanish with no feedback at
// all and the row would clear as if it had landed.
func TestInboxReplyRefusedOnAnExitedPanel(t *testing.T) {
	dead := wire("1", "exited", time.Minute)
	dead.ExitCode = 1
	m, _ := clientInbox(t, dead)

	m = press(m, "i")
	if m.inboxComposing {
		t.Fatal("an exited panel cannot be replied to")
	}
	if !strings.Contains(m.status, "cannot reply") {
		t.Errorf("the refusal should say so, got %q", m.status)
	}
	if len(m.inboxRows) != 1 {
		t.Error("a refused reply must not clear the row")
	}
}
