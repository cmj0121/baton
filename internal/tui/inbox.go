package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// The attention inbox (modeInbox, opened with C-t a from any view).
//
// Finding the panels that need a human was never the hard part — the footer badge
// has always known the count. The cost is CLEARING them: today handling one item
// is navigate, enter, read, type, C-t d, a full screen swap that loses sight of
// the fleet, and twenty of those in a row is the actual bottleneck. This overlay
// exists to make that cost per item as close to one keystroke as an honest
// implementation allows.
//
// It borrows the diff popup's master-detail shape: the queue on the left, on the
// right the tail that raised the flag (pulled per row with panel.tail, never
// carried on a snapshot). Every verb it offers acts WITHOUT leaving the overlay —
// i answers, - defers, x dismisses — and the one verb that does leave, enter,
// leaves deliberately because sometimes you really do have to look.
//
// Two rules shape everything below, and both are about trust rather than
// convenience:
//
//   - The order freezes while the cursor is in it (§5.4). A queue that re-sorts
//     under your hand is a queue where the thing you press x on is not the thing
//     you read, so an arriving snapshot may change what a row SAYS but never
//     where it IS. r is the explicit re-sort.
//   - Nothing leaves the queue by accident. A `done` panel is cleared only by x
//     or i — never by merely opening it or zooming into it — because a queue you
//     can consume by looking at it is a queue you stop trusting.

// inboxListColWidth is the queue column's preferred width; like the diff popup's
// file column it shrinks on a narrow terminal so the tail pane keeps room.
const inboxListColWidth = 30

// inboxTailCache is how many pulled tails the cockpit keeps. Walking back UP a
// queue you have already read must cost nothing, and thirty rows is the size of
// the problem the inbox was built for — so the cache holds a full triage session
// and then some, at a kibibyte a row.
const inboxTailCache = 32

// inboxTailStale is how long a pulled tail may be outstanding before the inbox
// asks again. It is a recovery threshold, not a deadline: a reply that arrives
// after it is still cached and still shown. Three seconds is far longer than a
// unix-socket round trip and far shorter than a human's patience with a pane that
// will not say anything.
const inboxTailStale = 3 * time.Second

// defaultInboxSnooze is how long `-` defers a row when settings.inbox-snooze says
// nothing. Ten minutes is long enough that the row is genuinely out of the way for
// one pass down the queue, and short enough that "later" still means today.
const defaultInboxSnooze = 10 * time.Minute

// inboxRow is one line in the queue, and it is a VALUE rather than a pointer into
// the fleet on purpose: the row keeps its own copy of what it showed, so a panel
// that closes underneath the cursor greys out in place instead of renumbering
// every row below it.
type inboxRow struct {
	id     string
	title  string
	state  panel.State
	code   int       // exit status; a non-zero one on an exited panel renders as failed
	reason string    // the agent's own words. ALREADY SANITISED by the server — see below
	since  time.Time // when the panel entered this state; the queue sorts on it
	stale  bool      // no longer qualifies, or is gone: greyed, not yanked from under the cursor
}

// inboxQualifies reports whether a wire panel earns a row, and which bucket it
// lands in. The buckets are the ordering (§5.3) and the ordering is the feature:
// "answer me" must never be buried under "review me", so the four kinds sort
// separately rather than by age alone.
//
//	0  attention — the only bucket you can clear from here by TYPING. First.
//	1  stuck     — it should have finished and has not. Actionable by looking.
//	2  failed    — a hard fact, already over. Triage, not rescue.
//	3  done      — "review me", and only when settings.inbox-done says so.
//
// The two singletons are excluded for the same reason attentionBadge excludes
// them: the conductor and the global shell are infrastructure, always present,
// and a queue that always holds two rows is a queue with a permanent floor.
func inboxQualifies(p proto.Panel, wantDone bool) (int, bool) {
	if p.Acked || p.Conductor || p.GlobalShell {
		return 0, false
	}
	return bucketOf(panel.ParseState(p.State), p.ExitCode, wantDone)
}

// bucketOf is the state-to-bucket mapping on its own, so the wire panel and the
// frozen row cannot disagree about which bucket a thing is in. inboxQualifies
// asks it for a panel arriving from the fleet; inboxRow.bucket asks it for a row
// the queue already froze, and the filter (#94) reads the row's answer — a mask
// over what the operator is looking at, never a re-derivation from a fleet that
// has moved on underneath.
func bucketOf(st panel.State, code int, wantDone bool) (int, bool) {
	switch st {
	case panel.Attention:
		return inboxAttention, true
	case panel.Stuck:
		return inboxStuck, true
	case panel.Exited:
		return inboxFailed, code != 0 // a clean exit is not news
	case panel.Done:
		return inboxDoneBucket, wantDone
	}
	return 0, false
}

// The buckets, named. inboxFilterAll is the mask that hides nothing and is what
// every open starts on — see cycleInboxFilter for why it is not remembered.
const (
	inboxFilterAll  = -1
	inboxAttention  = 0
	inboxStuck      = 1
	inboxFailed     = 2
	inboxDoneBucket = 3
)

// inboxBucketNames are the filter's labels, indexed by bucket. They are the
// words ATTENTION.md uses, so the tab bar names what the docs name.
var inboxBucketNames = [...]string{"attention", "stuck", "failed", "done"}

// bucket is the row's own bucket, from the state it froze with. The second
// result is false for a row whose state no longer earns a place — a stale row
// that woke to running — which the filter shows under `all` and under nothing
// else: it is still on screen where the hand expects it, and it is not an
// example of any bucket the operator asked to see.
func (r inboxRow) bucket(wantDone bool) (int, bool) {
	return bucketOf(r.state, r.code, wantDone)
}

// rowOf projects a wire panel onto a queue row. Since is parsed here rather than
// carried as a string because the queue sorts on it and a rendered age cannot be
// ordered; an instant the daemon could not stamp parses to the zero time, which
// sorts oldest-first and is then broken by id, so the order stays total either way.
//
// Reason is copied through UNESCAPED, and that is deliberate. The server scrubs
// an agent-supplied reason on the way in (sanitizeReason in
// internal/server/attention.go: printable runes only, format characters and
// controls dropped, capped at 200 runes) precisely so that every frontend
// downstream is holding text that is already safe. Escaping it a second time here
// would mangle legitimate reasons — a path, a quoted flag — for no gain. The
// TITLE is a different matter: it is built from a command line and a directory
// and has had no such pass, so the renderer runs it through sanitizeText like
// every other untrusted string the cockpit draws.
func rowOf(p proto.Panel) inboxRow {
	since, _ := time.Parse(time.RFC3339Nano, p.Since)
	return inboxRow{
		id:     p.ID,
		title:  p.Title,
		state:  panel.ParseState(p.State),
		code:   p.ExitCode,
		reason: p.Reason,
		since:  since,
	}
}

// info is the row's presentation, with failed's override applied. It goes through
// stateInfoFor rather than the states map so an exited panel with a non-zero code
// draws as `failed` here exactly as it does on its card.
func (r inboxRow) info() stateInfo {
	return stateInfoFor(panel.Panel{State: r.state, ExitCode: r.code})
}

// sortedInboxRows builds the queue from scratch, in the §5.3 order. This runs on
// open and on r, and NOWHERE else — everything in between goes through
// reconcileInbox, which does not re-sort.
func (m model) sortedInboxRows() []inboxRow {
	var rows []inboxRow
	buckets := map[string]int{}
	for _, p := range m.inboxWire {
		b, ok := inboxQualifies(p, m.inboxDone)
		if !ok {
			continue
		}
		buckets[p.ID] = b
		rows = append(rows, rowOf(p))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if bi, bj := buckets[rows[i].id], buckets[rows[j].id]; bi != bj {
			return bi < bj
		}
		if !rows[i].since.Equal(rows[j].since) {
			return rows[i].since.Before(rows[j].since) // oldest first: it has waited longest
		}
		return lessPanelID(rows[i].id, rows[j].id)
	})
	return rows
}

// lessPanelID breaks a tie between two rows of the same age. Panel ids are
// decimals handed out by the server's sequence, so they are compared as numbers —
// "10" after "9", not before it. An id that is not a decimal (an ephemeral
// "diff:…" slot) falls back to a string compare, which keeps the order total
// rather than arbitrary: every cockpit must draw the same queue.
func lessPanelID(a, b string) bool {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	if aerr == nil && berr == nil {
		return ai < bi
	}
	if aerr == nil != (berr == nil) {
		return aerr == nil // numeric ids sort ahead of the odd ones
	}
	return a < b
}

// openInbox enters modeInbox, remembering the view to return to. It re-sorts, so
// this is one of the two places (with refreshInbox) the order is allowed to move.
//
// An empty queue still opens. "C-t a" answering with a screen that says nothing
// needs you is information; bouncing straight back with a status line looks like
// the key did not work.
func (m model) openInbox() (tea.Model, tea.Cmd) {
	if m.mode != modeInbox {
		m.inboxFrom = m.mode
	}
	m.mode = modeInbox
	m.inboxRows = m.sortedInboxRows()
	m.inboxCursor = 0
	m.inboxFilter = inboxFilterAll // never remembered; see model.inboxFilter
	m.inboxCleared = nil
	m.inboxComposing, m.inboxReply = false, ""
	m.status = m.inboxStatus()
	m.wantTail()
	return m, nil
}

// refreshInbox is r: re-sort from scratch, drop the stale rows, and put the cursor
// back on the id it was on — or, when that row was one of the ones just dropped,
// on the nearest surviving index. Re-anchoring by IDENTITY rather than by number
// is the whole point: the list you asked to be reordered is the one case where
// the cursor must chase the row rather than the row chase the cursor.
func (m model) refreshInbox() (tea.Model, tea.Cmd) {
	anchor := ""
	if r, ok := m.inboxSelected(); ok {
		anchor = r.id
	}
	at := m.inboxCursor
	m.inboxRows = m.sortedInboxRows()
	m.inboxCleared = nil
	m.inboxCursor = clampInt(at, 0, len(m.inboxRows)-1)
	for i, r := range m.inboxRows {
		if r.id == anchor {
			m.inboxCursor = i
			break
		}
	}
	m.status = m.tr("inbox.status.refreshed", "inbox: refreshed") + " · " + m.inboxStatus()
	m.wantTail()
	return m, nil
}

// closeInbox restores the view the overlay was opened over and drops everything it
// was holding — the frozen order, the composer, the pulled tails. Nothing here is
// worth keeping: the queue is rebuilt from the fleet next time, and a cached tail
// from ten minutes ago would be a lie.
func (m model) closeInbox() (tea.Model, tea.Cmd) {
	m.mode = m.inboxFrom
	m.inboxRows = nil
	m.inboxCleared = nil
	m.inboxCursor = 0
	m.inboxFilter = inboxFilterAll
	m.inboxComposing, m.inboxReply = false, ""
	m.inboxTails, m.inboxTailOrder, m.inboxTailWant = nil, nil, ""
	if m.mode == modeDashboard {
		m.status = "dashboard"
	}
	return m, nil
}

// inboxSelected is the row under the cursor.
func (m model) inboxSelected() (inboxRow, bool) {
	if m.inboxCursor < 0 || m.inboxCursor >= len(m.inboxRows) {
		return inboxRow{}, false
	}
	return m.inboxRows[m.inboxCursor], true
}

// inboxStatus is the one-line summary the status bar carries while the overlay is
// open: how much is left to clear.
func (m model) inboxStatus() string {
	// The MASKED count, not the unfiltered total: the status line describes the
	// list in front of the operator, and "12 items" beside three visible rows is
	// the status bar disagreeing with the screen.
	n := len(m.inboxVisible())
	if m.inboxFilter != inboxFilterAll {
		return fmt.Sprintf(m.tr("inbox.status.count", "inbox: %d %s"), n, m.inboxFilterText(m.inboxFilter))
	}
	switch n {
	case 0:
		return m.tr("inbox.status.clear", "inbox: clear")
	case 1:
		return m.tr("inbox.status.one", "inbox: 1 item")
	default:
		return fmt.Sprintf(m.tr("inbox.status.items", "inbox: %d items"), n)
	}
}

// reconcileInbox folds a fresh fleet into the OPEN inbox without re-sorting it.
//
// While the cursor is in the list an arriving snapshot may change what a row says
// but never where it is: rows update in place, a panel that newly qualifies is
// appended at the tail where it cannot jump above the selection, and a panel that
// stopped qualifying — it woke to running, someone else cleared it, its process
// died — is marked stale and greyed rather than pulled out from under the hand
// about to press x on it. The remote view learned this rule the hard way, and so
// did fleetsearch.
//
// A row this cockpit already cleared is NOT re-appended while the server's
// confirming broadcast is still in flight. The removal is optimistic (§4.5) and
// the panel keeps qualifying for the few milliseconds until the ack lands, so
// without inboxCleared the row you just dismissed would reappear at the bottom of
// the queue — the single most confusing thing a queue can do.
func (m *model) reconcileInbox() {
	live := make(map[string]proto.Panel, len(m.inboxWire))
	for _, p := range m.inboxWire {
		live[p.ID] = p
	}
	held := make(map[string]bool, len(m.inboxRows))
	for i := range m.inboxRows {
		r := &m.inboxRows[i]
		held[r.id] = true
		p, ok := live[r.id]
		if !ok {
			r.stale = true // the panel is gone; the row stays put and says so
			continue
		}
		_, qualifies := inboxQualifies(p, m.inboxDone)
		row := rowOf(p)
		row.stale = !qualifies
		*r = row
	}
	for _, p := range m.inboxWire {
		if held[p.ID] || m.inboxCleared[p.ID] {
			continue
		}
		if _, ok := inboxQualifies(p, m.inboxDone); ok {
			m.inboxRows = append(m.inboxRows, rowOf(p))
		}
	}
	// A cleared row the server has now confirmed (or that stopped qualifying for
	// any other reason) no longer needs holding down.
	for id := range m.inboxCleared {
		if p, ok := live[id]; !ok {
			delete(m.inboxCleared, id)
		} else if _, qualifies := inboxQualifies(p, m.inboxDone); !qualifies {
			delete(m.inboxCleared, id)
		}
	}
	m.inboxCursor = clampInt(m.inboxCursor, 0, len(m.inboxRows)-1)
	*m = m.snapInboxCursor() // a snapshot may have changed the selected row's bucket
	// A snapshot is the one event that keeps arriving while the human sits still,
	// so it is also where a tail request the daemon dropped gets asked again —
	// without it the retry above would be waiting on a cursor move that a reader
	// staring at one row is not about to make.
	m.wantTail()
}

// --- the tail pane ------------------------------------------------------------

// wantTail asks for the selected row's tail, keeping at most ONE request in
// flight. A cached tail is shown instantly and costs nothing, which is what makes
// walking back up a queue you have already read free.
//
// The single-flight gate EXPIRES, and that is not belt-and-braces. The daemon's
// outbound send is a non-blocking select with a default arm: a client whose write
// queue is momentarily full has the frame dropped on the floor, deliberately, so
// that one slow reader cannot stall the fleet's broadcasts. A gate with no timeout
// turns that one dropped frame into a permanent one — inboxTailWant never clears,
// no further request is ever sent, and every row's detail pane reads "waiting for
// the tail…" for the rest of the session. Re-asking after inboxTailStale costs at
// most one extra kibibyte on a reply that was merely slow, and buys back the pane.
func (m *model) wantTail() {
	r, ok := m.inboxSelected()
	if !ok || m.inboxTails[r.id] != nil {
		return
	}
	if m.inboxTailWant != "" && m.now.Sub(m.inboxTailAt) < inboxTailStale {
		return
	}
	m.inboxTailWant = r.id
	m.inboxTailAt = m.now
	m.sendf(proto.Command{Action: "panel.tail", ID: r.id})
}

// applyTail files a "tail" reply. A reply for a row the cursor has since left is
// cached, not rendered — the human moved on, but they may well move back, and the
// bytes are already paid for. Once the outstanding request is settled the next one
// fires immediately, so a fast walk down the queue ends with the row you stopped
// on, not the row you passed through.
func (m *model) applyTail(id string, data []byte) {
	if m.inboxTails == nil {
		m.inboxTails = map[string][]byte{}
	}
	if _, seen := m.inboxTails[id]; !seen {
		m.inboxTailOrder = append(m.inboxTailOrder, id)
		for len(m.inboxTailOrder) > inboxTailCache {
			delete(m.inboxTails, m.inboxTailOrder[0])
			m.inboxTailOrder = m.inboxTailOrder[1:]
		}
	}
	if data == nil {
		data = []byte{} // a nil would read as "never fetched" and loop the request
	}
	m.inboxTails[id] = data
	if m.inboxTailWant == id {
		m.inboxTailWant = ""
	}
	m.wantTail()
}

// --- clearing a row -----------------------------------------------------------

// clearRow is the one path all three clearing verbs take: i (with text), - (with
// an until), and x (with neither). It sends, removes the row, and leaves the
// cursor at the same INDEX — because the slice shrank underneath it, that index
// now holds the next item, which is the whole ergonomic claim of this feature.
//
// The removal is local and optimistic; the server's ack broadcast is the
// confirmation, and reconcileInbox holds the id down until it arrives. This is the
// pattern cycleGroupLayout already uses: apply locally, let the next snapshot
// reconcile the guess.
func (m model) clearRow(idx int, until time.Time, sendText string) model {
	if idx < 0 || idx >= len(m.inboxRows) {
		return m
	}
	row := m.inboxRows[idx]
	if sendText != "" {
		// The submit sequence is "\n", matching the server's own defaultSubmit. An
		// EMPTY reply still sends a bare newline — accepting a [y/N] default is a
		// real answer, and refusing it would make the commonest confirmation in the
		// queue the one case the inbox cannot handle.
		m.sendf(proto.Command{Action: "panel.input", ID: row.id, Data: []byte(sendText)})
	}
	ack := proto.Command{Action: "panel.ack", ID: row.id}
	if !until.IsZero() {
		ack.Until = until.Format(time.RFC3339Nano)
	}
	m.sendf(ack)

	if m.inboxCleared == nil {
		m.inboxCleared = map[string]bool{}
	}
	m.inboxCleared[row.id] = true
	// A fresh slice, not an in-place append. The receiver is a value, so an
	// in-place shift would write through the shared backing array and corrupt the
	// rows an earlier copy of the model still points at. bubbletea hands the model
	// along linearly today, which makes that harmless today — and makes it exactly
	// the kind of trap that is expensive to find the day something holds a copy.
	rows := make([]inboxRow, 0, len(m.inboxRows)-1)
	rows = append(rows, m.inboxRows[:idx]...)
	rows = append(rows, m.inboxRows[idx+1:]...)
	m.inboxRows = rows
	m.inboxCursor = clampInt(idx, 0, len(m.inboxRows)-1)
	// The row that slid into this index may be in a bucket the mask hides, which
	// would leave the caret on something that is not drawn — and the next x on a
	// row the operator cannot see.
	m = m.snapInboxCursor()
	// The eviction ring is kept in step with the cache it evicts from. Dropping
	// the tail without dropping its id leaves a dead entry occupying one of the
	// thirty-two slots, lets the same id be enqueued twice if the row comes back,
	// and — worst of the three — makes the stale entry evict the FRESH one when it
	// reaches the head. The cache would quietly be smaller than it is documented
	// to be, in a way nothing would ever report.
	m.inboxTailOrder = withoutID(m.inboxTailOrder, row.id)
	delete(m.inboxTails, row.id)
	m.wantTail()
	return m
}

// withoutID returns ids with every occurrence of drop removed, in a fresh slice
// for the same aliasing reason clearRow builds one.
func withoutID(ids []string, drop string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != drop {
			out = append(out, id)
		}
	}
	return out
}

// --- keys ---------------------------------------------------------------------

// handleInboxKey drives the overlay. The composer, when open, owns the keyboard
// outright: the queue's own verbs are inert while you are typing, so a reply
// containing the letter x cannot dismiss the row you are answering.
func (m model) handleInboxKey(key string, k tea.Key) (tea.Model, tea.Cmd) {
	if m.inboxComposing {
		return m.handleInboxCompose(key, k)
	}
	switch key {
	case "esc", "q":
		return m.closeInbox()
	case "up", "k":
		m = m.moveInboxCursor(-1)
		m.wantTail()
	case "down", "j":
		m = m.moveInboxCursor(1)
		m.wantTail()
	case "home", "g":
		m = m.inboxEdgeCursor(false)
		m.wantTail()
	case "end", "G":
		m = m.inboxEdgeCursor(true)
		m.wantTail()
	case "tab":
		// Only outside the composer: handleInboxCompose owns the keyboard
		// outright while `i` is open, so the row being answered cannot vanish
		// under the reply field. The branch at the top of this function is what
		// makes that true, and this case is unreachable from there.
		return m.cycleInboxFilter(1)
	case "shift+tab":
		return m.cycleInboxFilter(-1)
	case "r":
		return m.refreshInbox()
	case "enter":
		return m.zoomInboxRow()
	case "i":
		return m.startInboxReply()
	case "-":
		return m.snoozeInboxRow()
	case "x":
		return m.dismissInboxRow()
	}
	return m, nil
}

// zoomInboxRow is the one verb that leaves the overlay, and it deliberately does
// NOT acknowledge the row. Opening something is not the same as dealing with it:
// a queue that empties because you looked at it is a queue you stop trusting, so
// zooming out and back leaves the item exactly where it was, waiting for x or i.
func (m model) zoomInboxRow() (tea.Model, tea.Cmd) {
	r, ok := m.inboxSelected()
	if !ok {
		return m, nil
	}
	p, live := m.fleetPanel(r.id)
	if !live {
		m.status = "inbox: " + truncate(sanitizeText(r.title), 24) + " is gone"
		return m, nil
	}
	out, _ := m.closeInbox()
	mm, _ := out.(model)
	var cmd tea.Cmd
	if mm.emu != nil && mm.zoomID != p.ID {
		// The inbox can be opened OVER a zoom (C-t a reaches it from anywhere), so
		// leaving it for a different panel has to tear the old zoom down — its
		// emulator and the reader goroutine feeding the old PTY would otherwise
		// outlive the view that owned them.
		det, c := mm.zoomDetach()
		mm, cmd = det.(model), c
	}
	return mm.zoomInto(p), cmd
}

// snoozeInboxRow defers the row. The cockpit computes the absolute instant from
// its OWN settings.inbox-snooze and sends that, rather than sending a duration and
// letting the daemon apply a policy: two cockpits with different snooze settings
// then each get what they configured, without the server holding a per-client one.
func (m model) snoozeInboxRow() (tea.Model, tea.Cmd) {
	r, ok := m.inboxSelected()
	if !ok {
		return m, nil
	}
	m = m.clearRow(m.inboxCursor, m.now.Add(m.effSnooze()), "")
	m.status = "snoozed " + truncate(sanitizeText(r.title), 20) + " · " + compactAge(m.effSnooze())
	return m.afterClear()
}

// dismissInboxRow drops the row until the panel next SPEAKS. Not until its state
// changes: a dismissed `done` that came back as `stuck` ten minutes later, on a
// timer the human did nothing to cause, is exactly the resurrection dismiss exists
// to prevent. Output is the panel doing something; a timer expiring is not.
func (m model) dismissInboxRow() (tea.Model, tea.Cmd) {
	r, ok := m.inboxSelected()
	if !ok {
		return m, nil
	}
	m = m.clearRow(m.inboxCursor, time.Time{}, "")
	m.status = m.tr("inbox.dismissed", "dismissed") + " " + truncate(sanitizeText(r.title), 24)
	return m.afterClear()
}

// afterClear closes the overlay once the last row is gone. Holding an empty box
// open would make the human press esc to be told nothing is left; closing says it
// and puts them back where they were in one move.
func (m model) afterClear() (tea.Model, tea.Cmd) {
	if len(m.inboxRows) > 0 {
		return m, nil
	}
	cleared := m.status
	out, cmd := m.closeInbox()
	mm, _ := out.(model)
	mm.status = cleared + " · inbox: clear"
	return mm, cmd
}

// effSnooze is how long `-` defers a row: settings.inbox-snooze, or the built-in
// default for a model that has not applied any prefs (the first frame, and tests).
func (m model) effSnooze() time.Duration {
	if m.inboxSnooze <= 0 {
		return defaultInboxSnooze
	}
	return m.inboxSnooze
}

// --- the composer -------------------------------------------------------------

// startInboxReply opens the single-line composer on the selected row.
//
// It is the inbox's own, not the shared m.input overlay: that one draws full-width
// over the whole view and would hide the queue it is answering, which is the one
// thing this feature exists to keep on screen.
//
// An exited panel is refused here rather than server-side, and it has to be:
// ptymgr's write on a dead id is a silent no-op, so without this guard the reply
// would vanish with no feedback at all and the row would clear as if it had landed.
func (m model) startInboxReply() (tea.Model, tea.Cmd) {
	r, ok := m.inboxSelected()
	if !ok {
		return m, nil
	}
	if r.state == panel.Exited {
		m.status = m.tr("inbox.no-reply-exited", "cannot reply to an exited panel — x dismisses it")
		return m, nil
	}
	if r.stale {
		m.status = m.tr("inbox.stale-row", "that row is stale — r refreshes the queue")
		return m, nil
	}
	m.inboxComposing, m.inboxReply = true, ""
	m.status = fmt.Sprintf(m.tr("inbox.replying-to", "reply to %s"), truncate(sanitizeText(r.title), 24))
	return m, nil
}

// handleInboxCompose is the composer's keyboard. Deliberately tiny: runes append,
// backspace deletes one, ctrl+u clears the line, esc/ctrl+c cancel and leave the
// row exactly where it was, enter sends. No other key is consulted.
func (m model) handleInboxCompose(key string, k tea.Key) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "ctrl+c":
		m.inboxComposing, m.inboxReply = false, ""
		m.status = m.inboxStatus()
		return m, nil
	case "ctrl+u":
		m.inboxReply = ""
		return m, nil
	case "backspace":
		if r := []rune(m.inboxReply); len(r) > 0 {
			m.inboxReply = string(r[:len(r)-1])
		}
		return m, nil
	case "enter":
		return m.sendInboxReply()
	case " ", "space":
		m.inboxReply += " "
		return m, nil
	}
	if k.Text != "" {
		m.inboxReply += k.Text
	}
	return m, nil
}

// sendInboxReply writes the line into the panel's PTY and acknowledges the row in
// the same breath — the reply IS the acknowledgement.
//
// It never attaches, and that is the design's most visible compromise: the human
// sees what they typed and nothing back. Attaching to reply would stream a whole
// panel's output into a client that is not rendering it, cost a replay flush and a
// repaint per row, and turn clearing twenty rows into twenty attach/detach cycles —
// which is precisely the cost the inbox exists to remove. The honest way to watch a
// program respond is still one keystroke away: enter zooms. The status line says so
// rather than leaving it to be discovered.
func (m model) sendInboxReply() (tea.Model, tea.Cmd) {
	r, ok := m.inboxSelected()
	if !ok {
		m.inboxComposing, m.inboxReply = false, ""
		return m, nil
	}
	text := m.inboxReply
	m.inboxComposing, m.inboxReply = false, ""
	m = m.clearRow(m.inboxCursor, time.Time{}, text+"\n")
	m.status = fmt.Sprintf(m.tr("inbox.replied-to", "replied to %s · enter zooms to watch it land"), truncate(sanitizeText(r.title), 20))
	return m.afterClear()
}

// --- the view -----------------------------------------------------------------

// inboxViewportRows is the overlay's body height. It is deliberately the diff
// popup's: the two are the same shape, and a queue that sized differently would
// jump when you moved between them.
// inboxFixedChrome is every row of the overlay that is not the body and not the
// footer: popupBoxAt's border and padding (2 + 2), the INBOX header, the filter
// tab bar, and the two blanks inboxView puts either side of the body.
//
// The tab bar counts as exactly one row and always will: popupBoxAt CLIPS each
// line to the popup width rather than wrapping it, so a bar too wide for a
// narrow terminal loses its right-hand stops and not its height. The footer is
// the only part that can grow, which is why it alone is measured.
const inboxFixedChrome = 4 + 1 + 1 + 1 + 1

// inboxViewportRows is the popup's body height — the rows the queue column and
// the tail pane each show.
//
// It sizes from the LEFTOVER under the banner, through panelVisibleRows, which
// is what help, the key map and panel-config already do (40e493f fixed the same
// class of bug for `?`). The old shape asked diffViewportRows, whose
// `m.height-14` counts neither the banner nor this overlay's own chrome: the
// composed frame came out twenty rows of chrome around a body sized for
// fourteen, so it was SIX ROWS TALLER THAN THE TERMINAL at every height — 46
// rows at 120x40 — and the terminal dropped the bottom edge of the box. The
// golden frame had been recording those 46 rows all along.
//
// The footer is MEASURED rather than counted, because it is the part that
// moves: fitLegend wraps on a narrow terminal, and the composer swaps the two
// legend rows for a reply field and a legend of its own. A constant here would
// be right until someone adds a cell to a legend — which #94 does, since `tab`
// needs one — and the bottom edge would go again with nothing to say why.
func (m model) inboxViewportRows() int {
	return m.panelVisibleRows(inboxFixedChrome + lipgloss.Height(m.inboxFooter()))
}

// inboxLayout sizes the two columns within the popup — a queue column that
// shrinks on a narrow terminal and a tail pane taking the rest, with 3 cells for
// the " │ " hairline.
func (m model) inboxLayout() (listW, tailW, rows int) {
	rows = m.inboxViewportRows()
	innerW := m.popupWidth()
	listW = max(min(inboxListColWidth, innerW-14), 8)
	tailW = max(innerW-listW-3, 10)
	return
}

// inboxView renders the master-detail overlay: the queue (left) and the tail that
// raised the selected row's flag (right).
func (m model) inboxView() string {
	listW, tailW, rows := m.inboxLayout()
	vis := m.inboxVisible()

	var body string
	if len(vis) == 0 {
		body = m.inboxEmptyBody(listW+tailW+3, rows)
	} else {
		sepStyle := lipgloss.NewStyle().Foreground(colFaint)
		sep := make([]string, rows)
		for i := range sep {
			sep[i] = sepStyle.Render(" │ ")
		}
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.JoinVertical(lipgloss.Left, padBlock(m.inboxRowLines(listW, rows), rows, listW)...),
			lipgloss.JoinVertical(lipgloss.Left, sep...),
			lipgloss.JoinVertical(lipgloss.Left, padBlock(m.inboxDetailBlock(tailW, rows), rows, tailW)...),
		)
	}

	at := 0
	for n, i := range vis {
		if i == m.inboxCursor {
			at = n
			break
		}
	}
	header := sectionStyle.Render(spaced(m.tr("inbox.title", "INBOX"))) + "  " +
		mutedStyle.Render(fmt.Sprintf(m.tr("inbox.of", "%d of %d"), min(at+1, len(vis)), len(vis)))
	content := lipgloss.JoinVertical(lipgloss.Left,
		header, m.inboxTabBar(), "", body, "", m.inboxFooter())
	return m.popupBox(content)
}

// inboxEmptyBody is the body when the mask shows nothing — and it is the SAME
// HEIGHT as a body full of rows, because the overlay must not change size under
// a key that only changes what is listed. Tabbing past an empty bucket used to
// collapse the box and the next stop bounced it open again, which reads as the
// overlay closing and reopening rather than as a list with nothing in it.
//
// The message is on the first line rather than centred: the eye is already at
// the top of the list after the tab bar, and a line that moves with the
// terminal's height is one more thing that is not where it was last time.
//
// What it says depends on whether the QUEUE is empty or only this bucket is.
// The two are different facts and the second must never wear the first's words:
// other buckets still hold rows, and "nothing needs a human right now" is a
// sentence the operator would act on.
func (m model) inboxEmptyBody(width, rows int) string {
	msg := m.tr("inbox.empty", "nothing needs a human right now")
	if len(m.inboxRows) > 0 {
		msg = fmt.Sprintf(m.tr("inbox.empty.bucket", "no %s panels"), m.inboxFilterText(m.inboxFilter))
	}
	lines := make([]string, rows)
	for i := range lines {
		lines[i] = lipgloss.NewStyle().Width(width).Render("")
	}
	lines[0] = lipgloss.NewStyle().Width(width).Render(mutedStyle.Render(msg))
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// inboxRowLines builds the queue column: the state LED, the panel's title, and its
// age. The cursor row is carated and brightened; a stale row is drawn in the faint
// colour with its glyph dropped, so "this is no longer what it said" is visible
// without the row moving.
func (m model) inboxRowLines(width, rows int) []string {
	vis := m.inboxVisible()
	all := make([]string, len(vis))
	anchor := 0
	for n, i := range vis {
		r := m.inboxRows[i]
		info := r.info()
		caret, fg := "  ", colMuted
		if i == m.inboxCursor {
			anchor = n
			caret, fg = lipgloss.NewStyle().Foreground(colBrand).Bold(true).Render("▸ "), colInk
		}
		led := lipgloss.NewStyle().Foreground(info.color).Render(info.led)
		// An age is only shown when there is one to show. A daemon older than
		// proto.Panel.Since stamps nothing — DESIGN §3.3 deliberately does not bump
		// ProtocolVersion, so a new cockpit against an old daemon is a SUPPORTED
		// pairing, not a bug report — and subtracting a zero instant from now yields
		// 2562047h: eight columns of nonsense that squeezes the title out of a
		// thirty-column row, on a row that sorts first in its bucket for being the
		// oldest thing the queue has ever seen.
		age := "—"
		if !r.since.IsZero() {
			age = compactAge(m.now.Sub(r.since))
		}
		if r.stale {
			led, fg, age = lipgloss.NewStyle().Foreground(colFaint).Render("·"), colFaint, "stale"
		}
		ageW := lipgloss.Width(age) // cells, not bytes: an em dash is three bytes and one cell
		name := truncate(sanitizeText(r.title), max(1, width-6-ageW))
		pad := max(1, width-4-lipgloss.Width(name)-ageW)
		all[n] = lipgloss.NewStyle().Width(width).Render(
			caret + led + " " + lipgloss.NewStyle().Foreground(fg).Render(name) +
				strings.Repeat(" ", pad) + lipgloss.NewStyle().Foreground(colFaint).Render(age))
	}
	shown, _ := windowAround(all, anchor, rows)
	return shown
}

// inboxDetailBlock builds the tail pane: the agent's declared reason on a ▸ line
// when one stands, then the trailing output, bottom-aligned.
//
// Bottom-aligned because the question is always the LAST thing a program printed —
// aligning to the top would show the setup and cut off the ask. The bytes are raw
// PTY output, so they go through sanitizeText like every other stream the cockpit
// draws outside an emulator.
func (m model) inboxDetailBlock(width, rows int) []string {
	r, ok := m.inboxSelected()
	if !ok {
		return nil
	}
	var head []string
	if r.reason != "" {
		// Already sanitised by the server; see rowOf. Escaping it again would mangle
		// a legitimate reason.
		head = append(head, lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).
			Width(width).Render(truncate("▸ "+r.reason, width)))
	}
	tail, pulled := m.inboxTails[r.id]
	body := []string{mutedStyle.Render(m.tr("inbox.waiting", "waiting for the tail…"))}
	switch {
	case pulled && len(tail) == 0:
		body = []string{mutedStyle.Render("(no output retained)")}
	case pulled:
		body = nil
		for _, l := range strings.Split(strings.TrimRight(string(tail), "\r\n"), "\n") {
			body = append(body, lipgloss.NewStyle().Foreground(colInk).Width(width).
				Render(clipVisible(sanitizeText(strings.TrimRight(l, "\r")), width)))
		}
	}
	room := max(1, rows-len(head))
	if len(body) > room {
		body = body[len(body)-room:] // keep the END: the ask is the last line
	}
	out := append(head, make([]string, room-len(body))...) // blank filler pushes the tail to the bottom
	blank := lipgloss.NewStyle().Width(width).Render("")
	for i := len(head); i < len(out); i++ {
		out[i] = blank
	}
	return append(out, body...)
}

// fitLegend packs key/label pairs onto as few lines as the pop-up's content width
// allows, in order, wrapping only where a line would overflow.
//
// It exists because a clipped legend does not lose a random word. popupBoxAt cuts
// each line at the right edge, so what goes is always the LAST cell — and by every
// convention this cockpit follows, the last cell is where the way out lives. A
// legend that runs one column too long is therefore a legend that says how to
// move, how to reply, how to defer, and nothing about how to leave. Spending a row
// of screen is cheap and reversible; stranding somebody in an overlay with no
// visible exit is neither.
//
// A single pair too wide for the width still goes on its own line and clips, which
// is the only case with no better answer available.
func fitLegend(width int, pairs ...string) string {
	var lines []string
	var cur []string
	for i := 0; i+1 < len(pairs); i += 2 {
		next := append(append([]string{}, cur...), pairs[i], pairs[i+1])
		if len(cur) > 0 && lipgloss.Width(legend(next...)) > width {
			lines = append(lines, legend(cur...))
			cur = []string{pairs[i], pairs[i+1]}
			continue
		}
		cur = next
	}
	if len(cur) > 0 {
		lines = append(lines, legend(cur...))
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// inboxFooter is the legend, or the composer when one is open. The composer
// REPLACES the legend rather than sitting beside it, so opening it costs the queue
// no rows.
//
// The "no echo" notice is folded INTO the composer's legend rather than given a
// prose line of its own, for two reasons that point the same way. A fixed row of
// screen per item is a real cost in a queue whose whole selling point is speed;
// and as a sentence it was 80 cells wide, so every terminal under 88 columns cut
// it at "…enter zooms to wa" — losing precisely the half that tells you how to
// watch your reply land. It splits onto a second line only when the pop-up is too
// narrow to fold it, where spending a row beats losing the instruction — the same
// rule fitLegend applies to the resting legend below.
func (m model) inboxFooter() string {
	if m.inboxComposing {
		field := lipgloss.NewStyle().Foreground(colInk).Render(m.inboxReply + "▏")
		head := lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).Render(m.tr("inbox.reply", "reply")+" ▸ ") + field
		return lipgloss.JoinVertical(lipgloss.Left, head,
			fitLegend(m.popupWidth(), "enter", m.tr("legend.send", "send"), "esc", m.tr("legend.cancel", "cancel"),
				m.tr("inbox.no-echo", "no echo"), m.tr("inbox.enter-zooms", "enter zooms")))
	}
	// Two groups, not one line: navigation and the way out, then the verbs that
	// clear a row. Each group is packed to the width on its own, so the grouping
	// survives a wide terminal and nothing is lost on a narrow one.
	w := m.popupWidth()
	return lipgloss.JoinVertical(lipgloss.Left,
		fitLegend(w, "j/k", m.tr("legend.move", "move"), "tab", m.tr("legend.filter", "filter"),
			"enter", m.tr("legend.zoom", "zoom"), "r", m.tr("legend.re-sort", "re-sort"), "esc", m.tr("legend.close", "close")),
		fitLegend(w, "i", m.tr("inbox.reply", "reply"), "-", m.tr("inbox.snooze", "snooze"), "x", m.tr("inbox.dismiss", "dismiss")),
	)
}

// compactAge renders a short, single-unit age: seconds under a minute, then
// minutes, then hours. It mirrors the daemon's own compactDur so the queue's age
// column and a card's activity line read alike; the two live in different packages
// and neither is worth exporting for six lines.
func compactAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// parseSnooze reads settings.inbox-snooze. An unparseable or non-positive value
// falls back to the default rather than disabling the verb: "-" with a broken
// config should still defer the row, since a snooze that silently did nothing
// would look like a dropped keystroke.
func parseSnooze(s string) time.Duration {
	if s == "" {
		return defaultInboxSnooze
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return defaultInboxSnooze
	}
	return d
}

// observeWire files the newest fleet snapshot exactly as it came off the wire, and
// folds it into the inbox when one is open.
//
// Both snapshot paths call it, because the queue's inputs arrive on both: an
// acknowledgement lands as a structural "panels" broadcast, while a panel climbing
// the quiet ladder into `done` or `stuck` lands as a "telemetry" refresh. A queue
// fed by only one of them would be right about half the fleet.
func (m *model) observeWire(panels []proto.Panel) {
	m.inboxWire = panels
	if m.mode == modeInbox {
		m.reconcileInbox()
	}
}

// --- the bucket filter (#94) --------------------------------------------------

// inboxStops is the filter cycle, in order: all, then each bucket. `done` is not
// in it when settings.inbox-done is false, because then the bucket does not
// exist — that is the one exception to showing every stop.
//
// Empty buckets are NOT skipped. A cycle whose next stop depends on the fleet is
// the same disorientation a re-sorting queue is: the names stay put and an empty
// one says so, rather than tab landing somewhere different each time.
func (m model) inboxStops() []int {
	stops := []int{inboxFilterAll, inboxAttention, inboxStuck, inboxFailed}
	if m.inboxDone {
		stops = append(stops, inboxDoneBucket)
	}
	return stops
}

// inboxVisible is the indexes of inboxRows the current filter shows, in queue
// order. It reads each row's OWN frozen bucket, so filtering never calls
// sortedInboxRows and the order the operator is looking at cannot move.
func (m model) inboxVisible() []int {
	out := make([]int, 0, len(m.inboxRows))
	for i, r := range m.inboxRows {
		if m.inboxFilter == inboxFilterAll {
			out = append(out, i)
			continue
		}
		if b, ok := r.bucket(m.inboxDone); ok && b == m.inboxFilter {
			out = append(out, i)
		}
	}
	return out
}

// inboxCounts is how many rows each bucket holds, for the tab bar. The count is
// what makes tab worth pressing or not worth pressing without pressing it.
func (m model) inboxCounts() map[int]int {
	counts := map[int]int{inboxFilterAll: len(m.inboxRows)}
	for _, r := range m.inboxRows {
		if b, ok := r.bucket(m.inboxDone); ok {
			counts[b]++
		}
	}
	return counts
}

// inboxFilterName is the filter's word, for the status line and the empty state.
// inboxFilterText is a bucket's name as the tab bar and the status line say it.
//
// The buckets are named for the states they hold, so they are translated by the
// state's own key rather than by four more of their own: "attention" means one
// thing in this cockpit, and a tab spelling it differently from the chip above it
// would be describing a different queue.
func (m model) inboxFilterText(f int) string {
	name := inboxFilterName(f)
	if name == "all" {
		return m.tr("inbox.all", "all")
	}
	return m.tr("state."+name, name)
}

func inboxFilterName(f int) string {
	if f == inboxFilterAll || f < 0 || f >= len(inboxBucketNames) {
		return "all"
	}
	return inboxBucketNames[f]
}

// cycleInboxFilter moves the mask one stop in dir, wrapping. The cursor then
// lands on a row the new mask actually shows: the one it was on when that is
// still visible, else the first visible row — so tab never leaves the caret
// pointing at something the operator cannot see.
func (m model) cycleInboxFilter(dir int) (tea.Model, tea.Cmd) {
	stops := m.inboxStops()
	at := 0
	for i, s := range stops {
		if s == m.inboxFilter {
			at = i
			break
		}
	}
	m.inboxFilter = stops[((at+dir)%len(stops)+len(stops))%len(stops)]

	held := ""
	if r, ok := m.inboxSelected(); ok {
		held = r.id
	}
	vis := m.inboxVisible()
	switch {
	case len(vis) == 0:
		// Nothing to point at. The overlay stays open and says so — see
		// inboxView's empty state, and afterClear for why an empty FILTER is not
		// an empty queue.
		m.inboxCursor = 0
	default:
		m.inboxCursor = vis[0]
		for _, i := range vis {
			if m.inboxRows[i].id == held {
				m.inboxCursor = i
				break
			}
		}
	}
	m.status = m.inboxStatus()
	m.wantTail()
	return m, nil
}

// moveInboxCursor walks the VISIBLE rows by delta, clamped at both ends. It
// walks the mask rather than the underlying slice so j and k never stop on a
// row that is not drawn.
func (m model) moveInboxCursor(delta int) model {
	vis := m.inboxVisible()
	if len(vis) == 0 {
		return m
	}
	at := 0
	for i, idx := range vis {
		if idx == m.inboxCursor {
			at = i
			break
		}
	}
	m.inboxCursor = vis[clampInt(at+delta, 0, len(vis)-1)]
	return m
}

// inboxEdgeCursor puts the cursor on the first or last VISIBLE row (g / G).
func (m model) inboxEdgeCursor(last bool) model {
	vis := m.inboxVisible()
	if len(vis) == 0 {
		return m
	}
	if last {
		m.inboxCursor = vis[len(vis)-1]
		return m
	}
	m.inboxCursor = vis[0]
	return m
}

// inboxTabBar is the filter's header row: the open name lit, its neighbours
// visible, and a count beside each. It is a HEADER row and not a third legend
// line — the legend is the verbs, and the filter is which list those verbs act
// on, so it belongs above the fold rather than below it (and fitLegend already
// wraps without help).
func (m model) inboxTabBar() string {
	counts := m.inboxCounts()
	parts := make([]string, 0, 5)
	for _, s := range m.inboxStops() {
		label := fmt.Sprintf("%s %d", m.inboxFilterText(s), counts[s])
		if s == m.inboxFilter {
			parts = append(parts, tabHotStyle.Render(label))
			continue
		}
		parts = append(parts, tabStyle.Render(label))
	}
	return strings.Join(parts, mutedStyle.Render(" · "))
}

// snapInboxCursor moves the cursor onto a row the current mask shows: the next
// visible one at or after where it is, else the last visible one. It is a no-op
// with no filter, and with a filter that hides everything — the latter is the
// empty state, which has nothing to point at by definition.
func (m model) snapInboxCursor() model {
	vis := m.inboxVisible()
	if len(vis) == 0 {
		return m
	}
	for _, i := range vis {
		if i >= m.inboxCursor {
			m.inboxCursor = i
			return m
		}
	}
	m.inboxCursor = vis[len(vis)-1]
	return m
}
