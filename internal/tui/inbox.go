package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
// carried on a snapshot). The verbs it offers act WITHOUT leaving the overlay —
// i answers where you stand — and the one verb that does leave, enter, leaves
// deliberately because sometimes you really do have to look.
//
// Two rules shape everything below, and both are about trust rather than
// convenience:
//
//   - The order freezes while the cursor is in it (§5.4). A queue that re-sorts
//     under your hand is a queue where the thing you press x on is not the thing
//     you read, so an arriving snapshot may change what a row SAYS but never
//     where it IS. r is the explicit re-sort.
//   - Nothing leaves the queue by accident. A row is cleared only by a verb that
//     says so — never by merely opening the panel or zooming into it — because a
//     queue you can consume by looking at it is a queue you stop trusting.

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
	switch panel.ParseState(p.State) {
	case panel.Attention:
		return 0, true
	case panel.Stuck:
		return 1, true
	case panel.Exited:
		return 2, p.ExitCode != 0 // a clean exit is not news
	case panel.Done:
		return 3, wantDone
	}
	return 0, false
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
	m.status = "inbox: refreshed · " + m.inboxStatus()
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
	switch n := len(m.inboxRows); n {
	case 0:
		return "inbox: clear"
	case 1:
		return "inbox: 1 item"
	default:
		return fmt.Sprintf("inbox: %d items", n)
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

// clearRow is the one path every clearing verb takes — i sends text with it, the
// deferring verbs will send an until. It sends, removes the row, and leaves the
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
func (m model) handleInboxKey(key string, k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.inboxComposing {
		return m.handleInboxCompose(key, k)
	}
	switch key {
	case "esc", "q":
		return m.closeInbox()
	case "up", "k":
		m.inboxCursor = clampInt(m.inboxCursor-1, 0, len(m.inboxRows)-1)
		m.wantTail()
	case "down", "j":
		m.inboxCursor = clampInt(m.inboxCursor+1, 0, len(m.inboxRows)-1)
		m.wantTail()
	case "home", "g":
		m.inboxCursor = 0
		m.wantTail()
	case "end", "G":
		m.inboxCursor = max(0, len(m.inboxRows)-1)
		m.wantTail()
	case "r":
		return m.refreshInbox()
	case "enter":
		return m.zoomInboxRow()
	case "i":
		return m.startInboxReply()
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
		m.status = "cannot reply to an exited panel"
		return m, nil
	}
	if r.stale {
		m.status = "that row is stale — r refreshes the queue"
		return m, nil
	}
	m.inboxComposing, m.inboxReply = true, ""
	m.status = "reply to " + truncate(sanitizeText(r.title), 24)
	return m, nil
}

// handleInboxCompose is the composer's keyboard. Deliberately tiny: runes append,
// backspace deletes one, ctrl+u clears the line, esc/ctrl+c cancel and leave the
// row exactly where it was, enter sends. No other key is consulted.
func (m model) handleInboxCompose(key string, k tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	if k.Type == tea.KeyRunes {
		m.inboxReply += string(k.Runes)
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
	m.status = "replied to " + truncate(sanitizeText(r.title), 20) + " · enter zooms to watch it land"
	return m.afterClear()
}

// --- the view -----------------------------------------------------------------

// inboxViewportRows is the overlay's body height. It is deliberately the diff
// popup's: the two are the same shape, and a queue that sized differently would
// jump when you moved between them.
func (m model) inboxViewportRows() int { return m.diffViewportRows() }

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
	if len(m.inboxRows) == 0 {
		return m.popupBox(lipgloss.JoinVertical(lipgloss.Left,
			sectionStyle.Render(spaced("INBOX")),
			"",
			mutedStyle.Render("nothing needs a human right now"),
			"",
			legend("esc", "close"),
		))
	}
	listW, tailW, rows := m.inboxLayout()
	left := padBlock(m.inboxRowLines(listW, rows), rows, listW)
	right := padBlock(m.inboxDetailBlock(tailW, rows), rows, tailW)
	sepStyle := lipgloss.NewStyle().Foreground(colFaint)
	sep := make([]string, rows)
	for i := range sep {
		sep[i] = sepStyle.Render(" │ ")
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.JoinVertical(lipgloss.Left, left...),
		lipgloss.JoinVertical(lipgloss.Left, sep...),
		lipgloss.JoinVertical(lipgloss.Left, right...),
	)
	header := sectionStyle.Render(spaced("INBOX")) + "  " +
		mutedStyle.Render(fmt.Sprintf("%d of %d", m.inboxCursor+1, len(m.inboxRows)))
	content := lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", m.inboxFooter())
	return m.popupBox(content)
}

// inboxRowLines builds the queue column: the state LED, the panel's title, and its
// age. The cursor row is carated and brightened; a stale row is drawn in the faint
// colour with its glyph dropped, so "this is no longer what it said" is visible
// without the row moving.
func (m model) inboxRowLines(width, rows int) []string {
	all := make([]string, len(m.inboxRows))
	for i, r := range m.inboxRows {
		info := r.info()
		caret, fg := "  ", colMuted
		if i == m.inboxCursor {
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
		all[i] = lipgloss.NewStyle().Width(width).Render(
			caret + led + " " + lipgloss.NewStyle().Foreground(fg).Render(name) +
				strings.Repeat(" ", pad) + lipgloss.NewStyle().Foreground(colFaint).Render(age))
	}
	shown, _ := windowAround(all, m.inboxCursor, rows)
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
	body := []string{mutedStyle.Render("waiting for the tail…")}
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
		head := lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).Render("reply ▸ ") + field
		return lipgloss.JoinVertical(lipgloss.Left, head,
			fitLegend(m.popupWidth(), "enter", "send", "esc", "cancel", "no echo", "enter zooms"))
	}
	return fitLegend(m.popupWidth(),
		"j/k", "move", "i", "reply", "enter", "zoom", "r", "re-sort", "esc", "close")
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
