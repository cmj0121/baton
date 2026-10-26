package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/cmj0121/baton/internal/i18n"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// sentCommands records every command the model sends from here to the end of the
// test, in order. It reads the send hook rather than a socket, so "nothing was
// sent" is a fact about a slice and not a guess about how long to wait.
func sentCommands(t *testing.T) *[]proto.Command {
	t.Helper()
	var got []proto.Command
	sendHook = func(c proto.Command) { got = append(got, c) }
	t.Cleanup(func() { sendHook = nil })
	return &got
}

// peersModel is the dashboard on the grouped fleet with the cursor on a group.
func peersModel(t *testing.T, group string) model {
	t.Helper()
	m := baseModel()
	m.fleet = groupedFleet()
	m.cursorOnGroup(t, group)
	return m
}

// peersSnapshot is the fleet as the server reports it, with peers on for the
// named groups.
func peersSnapshot(fleet []panel.Panel, on ...string) proto.ServerMsg {
	sm := proto.ServerMsg{Type: "panels"}
	for _, p := range fleet {
		sm.Panels = append(sm.Panels, proto.Panel{
			ID: p.ID, Kind: p.Kind.String(), Title: p.Title, State: p.State.String(), Group: p.Group,
		})
	}
	for _, g := range on {
		sm.Groups = append(sm.Groups, proto.GroupView{Group: g, Peers: true})
	}
	return sm
}

// TestPeersEnableAsksOnce: switching peers on parks exactly one y/n — nothing is
// sent and nothing is drawn until it is answered — and a yes sends the one
// command and leaves no second question behind.
func TestPeersEnableAsksOnce(t *testing.T) {
	sent := sentCommands(t)
	m := peersModel(t, "api")

	m = press(m, keyPeers)
	if m.pendingPeers != "api" {
		t.Fatalf("switching peers on should wait on a y/n naming api, got %q", m.pendingPeers)
	}
	if !strings.Contains(m.status, `"api"`) || !strings.Contains(m.status, "(y/n)") {
		t.Fatalf("the prompt should name the group and ask y/n, got %q", m.status)
	}
	if len(*sent) != 0 {
		t.Fatalf("nothing may be sent before the answer, got %+v", *sent)
	}
	if m.peersOn("api") {
		t.Fatal("the mark must not light before the answer")
	}

	m = press(m, "y")
	if m.pendingPeers != "" {
		t.Fatalf("one yes should settle the question, still pending %q", m.pendingPeers)
	}
	if len(*sent) != 1 {
		t.Fatalf("a yes should send exactly one command, got %+v", *sent)
	}
	if got := (*sent)[0]; got.Action != "group.peers" || got.Group != "api" || !got.Enable {
		t.Fatalf("a yes should send group.peers api enable, got %+v", got)
	}
	if !strings.Contains(m.status, "peers on") || !strings.Contains(m.status, `"api"`) {
		t.Fatalf("the status line should say peers went on for api, got %q", m.status)
	}
}

// TestPeersEnableCancels: any answer but yes leaves the group closed and sends
// nothing — and the key that cancelled is spent on the cancel, so a w does not
// go on to open a close prompt.
func TestPeersEnableCancels(t *testing.T) {
	sent := sentCommands(t)
	m := peersModel(t, "api")

	m = press(m, keyPeers, keyClose)
	if m.pendingPeers != "" || m.peersOn("api") {
		t.Fatalf("a non-yes should leave peers off, pending=%q on=%v", m.pendingPeers, m.peersOn("api"))
	}
	if len(*sent) != 0 {
		t.Fatalf("a cancelled switch sends nothing, got %+v", *sent)
	}
	if m.pendingClose {
		t.Fatal("the cancelling key leaked into the dashboard and armed a close")
	}
	if m.status != "peers left off" {
		t.Fatalf("the status line should say peers stayed off, got %q", m.status)
	}
}

// TestPeersEnterDoesNotConfirm: Enter is the dashboard's "open this row" key, so
// it is not an answer to the question. It cancels like any other key and is
// spent doing so.
func TestPeersEnterDoesNotConfirm(t *testing.T) {
	sent := sentCommands(t)
	m := peersModel(t, "api")

	m = press(m, keyPeers, "enter")
	if len(*sent) != 0 {
		t.Fatalf("enter must not open the channel, got %+v", *sent)
	}
	if m.peersOn("api") || m.pendingPeers != "" {
		t.Fatalf("enter should cancel the question, on=%v pending=%q", m.peersOn("api"), m.pendingPeers)
	}
	if m.status != "peers left off" {
		t.Fatalf("enter should cancel with the cancel line, got %q", m.status)
	}
	if m.mode != modeDashboard {
		t.Fatalf("the cancelling enter must not go on to open the row, mode=%v", m.mode)
	}
}

// answersNothing presses y on a model whose peers question should already be
// withdrawn, and fails if the y still opened the group.
func answersNothing(t *testing.T, m model, sent *[]proto.Command, why string) {
	t.Helper()
	*sent = nil
	m = press(m, "y")
	for _, c := range *sent {
		if c.Action == "group.peers" {
			t.Fatalf("%s: a late y still sent %+v", why, c)
		}
	}
	if m.peersOn("api") {
		t.Fatalf("%s: a late y still lit the mark", why)
	}
}

// TestPeersPromptFadesWithItsStatus: the question lives as long as its prompt is
// on screen. Once the status line has faded back to rest there is nothing left
// to say yes to, and a y typed later is just a y.
func TestPeersPromptFadesWithItsStatus(t *testing.T) {
	sent := sentCommands(t)
	m := peersModel(t, "api")
	m = press(m, keyPeers)

	for i := 1; i <= statusTTL+1; i++ { // one tick to notice the new status, then its TTL
		next, _ := m.Update(tickMsg(time.Unix(int64(i), 0)))
		m = next.(model)
	}
	if strings.Contains(m.status, "(y/n)") {
		t.Fatalf("setup: the prompt should have faded by now, got %q", m.status)
	}
	if m.pendingPeers != "" {
		t.Fatalf("the question should go when its prompt fades, still pending %q", m.pendingPeers)
	}
	answersNothing(t, m, sent, "after the fade")
}

// TestPeersPromptReplacedByAnotherStatus: a status that takes the prompt's place
// takes the question with it, without waiting for a tick.
func TestPeersPromptReplacedByAnotherStatus(t *testing.T) {
	sent := sentCommands(t)
	m := peersModel(t, "api")
	m = press(m, keyPeers)

	m.applyEvent(proto.ServerMsg{Type: "notice", Notice: "build finished"})
	answersNothing(t, m, sent, "after another status replaced the prompt")
}

// TestPeersPromptDropsWhenGroupVanishes: a snapshot without the group withdraws
// the question about it — even while the prompt is still on screen.
func TestPeersPromptDropsWhenGroupVanishes(t *testing.T) {
	sent := sentCommands(t)
	m := peersModel(t, "api")
	m = press(m, keyPeers)

	var rest []panel.Panel
	for _, p := range groupedFleet() {
		if p.Group != "api" {
			rest = append(rest, p)
		}
	}
	m.applyEvent(peersSnapshot(rest))
	if m.pendingPeers != "" {
		t.Fatalf("a snapshot without the group should withdraw the question, still pending %q", m.pendingPeers)
	}
	// The prompt had not faded; it comes down with the question, so nothing on
	// screen still asks for a y or an n.
	if m.status != "peers left off" {
		t.Fatalf("a withdrawn question should take its prompt down, status %q", m.status)
	}
	answersNothing(t, m, sent, "after the group left the fleet")

	// A snapshot that still has the group leaves the question standing.
	m = peersModel(t, "api")
	m = press(m, keyPeers)
	m.applyEvent(peersSnapshot(groupedFleet()))
	if m.pendingPeers != "api" {
		t.Fatalf("a snapshot that still has the group must not withdraw the question, pending %q", m.pendingPeers)
	}

	// A group left with nothing but sub-groups is still a row, but it has no panel
	// of its own for a peers flag to reach — the server would refuse it — so the
	// question about it is withdrawn too.
	m = baseModel()
	m.fleet = nestedFleet()
	m.cursorOnGroup(t, "backend")
	m = press(m, keyPeers)
	if m.pendingPeers != "backend" {
		t.Fatalf("setup: backend has a direct panel and should be asked about, pending %q", m.pendingPeers)
	}
	m.applyEvent(peersSnapshot(nestedFleet()[1:])) // backend's one direct panel is gone
	if m.pendingPeers != "" || m.status != "peers left off" {
		t.Fatalf("a group with no direct panel left cannot be opened, pending=%q status=%q", m.pendingPeers, m.status)
	}

	// A prompt that had already been replaced is not overwritten on the way out.
	m = peersModel(t, "api")
	m = press(m, keyPeers)
	m.status = "build finished"
	m.applyEvent(peersSnapshot(rest))
	if m.status != "build finished" {
		t.Fatalf("withdrawing must not replace a status that is not the prompt, got %q", m.status)
	}
}

// TestPeersRefusesAGroupOfSubGroups: a group that holds only sub-groups has no
// panel of its own, so there is nothing to ask — the key says why and parks no
// question.
func TestPeersRefusesAGroupOfSubGroups(t *testing.T) {
	sent := sentCommands(t)
	m := baseModel()
	m.fleet = nestedFleet()[1:] // backend/api, backend/db and a lone panel
	m.cursorOnGroup(t, "backend")

	m = press(m, keyPeers)
	if m.pendingPeers != "" {
		t.Fatalf("a group of sub-groups should not be asked about, pending %q", m.pendingPeers)
	}
	if m.status != `no panel sits directly in "backend", so there are no peers to switch on` {
		t.Fatalf("the refusal should name the group and the reason, got %q", m.status)
	}
	answersNothing(t, m, sent, "after the refusal")
	if len(*sent) != 0 {
		t.Fatalf("a refused switch sends nothing, got %+v", *sent)
	}

	m.lang = i18n.ZhTW
	if m = press(m, keyPeers); m.status != `沒有面板直接放在 "backend" 裡,無從開啟互通` {
		t.Fatalf("the zh-TW refusal is wrong: %q", m.status)
	}
}

// TestPeersPromptDropsOnLeavingDashboard: the question belongs to the dashboard.
// Whatever takes the cockpit somewhere else — a key, or a snapshot that zooms a
// spawn — the question does not follow, and it is not waiting on the way back.
func TestPeersPromptDropsOnLeavingDashboard(t *testing.T) {
	sent := sentCommands(t)

	// Away and back with the prompt still inside its TTL: the y on return is the
	// first key handled, and it must not be an answer.
	m := peersModel(t, "api")
	m = press(m, keyPeers)
	m.mode = modeHelp
	m = press(m, "j")
	if m.pendingPeers != "" {
		t.Fatalf("a key handled outside the dashboard should withdraw the question, pending %q", m.pendingPeers)
	}
	m.mode = modeDashboard
	answersNothing(t, m, sent, "back on the dashboard")

	// A snapshot that lands while the view is elsewhere withdraws it with no key.
	m = peersModel(t, "api")
	m = press(m, keyPeers)
	m.mode = modeGroupZoom
	m.groupName = "api"
	m.applyEvent(peersSnapshot(groupedFleet()))
	if m.pendingPeers != "" {
		t.Fatalf("a snapshot outside the dashboard should withdraw the question, pending %q", m.pendingPeers)
	}
}

// TestPeersDisableDoesNotAsk: switching peers off closes a channel, so it goes
// at once.
func TestPeersDisableDoesNotAsk(t *testing.T) {
	sent := sentCommands(t)
	m := peersModel(t, "api")
	m.peerGroups = map[string]bool{"api": true, "db": true}

	m = press(m, keyPeers)
	if m.pendingPeers != "" {
		t.Fatalf("switching peers off must not ask, got a prompt for %q", m.pendingPeers)
	}
	if len(*sent) != 1 {
		t.Fatalf("switching off should send exactly one command, got %+v", *sent)
	}
	if got := (*sent)[0]; got.Action != "group.peers" || got.Group != "api" || got.Enable {
		t.Fatalf("switching off should send group.peers api with enable unset, got %+v", got)
	}
	if m.peersOn("api") {
		t.Fatal("the mark should go out at once")
	}
	if !m.peersOn("db") {
		t.Fatal("switching one group off must leave another group's flag alone")
	}
	if !strings.Contains(m.status, "peers off") || !strings.Contains(m.status, `"api"`) {
		t.Fatalf("the status line should say peers went off for api, got %q", m.status)
	}
}

// TestPeersFlipIsOptimisticThenConfirmed: the mark lights the moment the yes is
// given, and the snapshot that follows is what makes it true — it stays when the
// server agrees.
func TestPeersFlipIsOptimisticThenConfirmed(t *testing.T) {
	sentCommands(t)
	m := peersModel(t, "api")

	m = press(m, keyPeers, "y")
	if !m.peersOn("api") {
		t.Fatal("the mark should light before the server answers")
	}
	if !m.peersUnsettled {
		t.Fatal("a flip drawn ahead of the server should be recorded as unsettled")
	}

	m.applyEvent(peersSnapshot(groupedFleet(), "api"))
	if !m.peersOn("api") {
		t.Fatal("a confirming snapshot should leave the mark lit")
	}
	if m.peersUnsettled {
		t.Fatal("a snapshot settles the flip")
	}
}

// TestPeersRefusalIsUndoneBySnapshot: a refused switch surfaces like any other
// command error, and the snapshot it asks for takes the mark back. The server
// answers a refusal to one client and broadcasts nothing, so without the request
// the mark would stay lit on a group nobody can reach.
func TestPeersRefusalIsUndoneBySnapshot(t *testing.T) {
	sent := sentCommands(t)
	m := peersModel(t, "api")
	m = press(m, keyPeers, "y")
	*sent = nil

	m.applyEvent(proto.ServerMsg{Type: "error", Error: "peer: group has no direct member"})
	if m.status != "error: peer: group has no direct member" {
		t.Fatalf("a refusal should reach the status line as an error, got %q", m.status)
	}
	if len(*sent) != 1 || (*sent)[0].Action != "panel.list" {
		t.Fatalf("a refusal of an unsettled flip should ask for the fleet, got %+v", *sent)
	}

	m.applyEvent(peersSnapshot(groupedFleet()))
	if m.peersOn("api") {
		t.Fatal("the snapshot after a refusal should take the mark back")
	}

	// With nothing unsettled an error is only an error: no extra traffic.
	*sent = nil
	m.applyEvent(proto.ServerMsg{Type: "error", Error: "unrelated"})
	if len(*sent) != 0 {
		t.Fatalf("an error with no flip in flight should send nothing, got %+v", *sent)
	}
}

// TestPeersKeyNeedsAGroup: the switch is a decision about a group, so it is
// refused — with a reason, and with nothing sent — on a panel, on a lens bucket
// and outside the dashboard.
func TestPeersKeyNeedsAGroup(t *testing.T) {
	sent := sentCommands(t)

	m := baseModel()
	m.fleet = groupedFleet()
	m.cursorOnPanel(t, "2")
	m = press(m, keyPeers)
	if m.pendingPeers != "" || m.status != "select a group to switch peers on" {
		t.Fatalf("on a panel the key should hint and not ask, pending=%q status=%q", m.pendingPeers, m.status)
	}

	lm := lensModel()
	lm.mode, lm.lens = modeDashboard, lensState
	lm.cursorOnGroup(t, "running")
	lm = press(lm, keyPeers)
	if lm.pendingPeers != "" || !strings.Contains(lm.status, "is a view, not a work item") {
		t.Fatalf("a lens bucket is not a group to open, pending=%q status=%q", lm.pendingPeers, lm.status)
	}

	fm := baseModel()
	fm.fleet = groupedFleet()
	fm.cursor = len(fm.dashItems())
	fm = fm.togglePeers() // no row under the cursor at all
	if fm.pendingPeers != "" || fm.status != "select a group to switch peers on" {
		t.Fatalf("with no row selected the key should hint, pending=%q status=%q", fm.pendingPeers, fm.status)
	}

	zm := peersModel(t, "api")
	zm.mode = modeZoom
	zm = zm.togglePeers()
	if zm.pendingPeers != "" {
		t.Fatalf("outside the dashboard the switch should do nothing, pending=%q", zm.pendingPeers)
	}

	if len(*sent) != 0 {
		t.Fatalf("none of the refusals should send a command, got %+v", *sent)
	}
}

// TestPeersForGroups: only the groups the server reports with peers on are in
// the set, under their full path.
func TestPeersForGroups(t *testing.T) {
	got := peersForGroups([]proto.GroupView{
		{Group: "api", Peers: true},
		{Group: "db", Favourite: true},
		{Group: "backend/api", Peers: true},
	})
	if len(got) != 2 || !got["api"] || !got["backend/api"] {
		t.Fatalf("want exactly api and backend/api, got %v", got)
	}
	if peersForGroups(nil) != nil {
		t.Fatal("no groups means no set")
	}
}

// TestPeersOnIsExactPath: the flag on a group says nothing about the groups
// inside it, the group around it, or a panel in no group.
func TestPeersOnIsExactPath(t *testing.T) {
	m := baseModel()
	m.peerGroups = map[string]bool{"backend": true}
	for group, want := range map[string]bool{
		"backend": true, "backend/api": false, "back": false, "": false,
	} {
		if got := m.peersOn(group); got != want {
			t.Errorf("peersOn(%q) = %v, want %v", group, got, want)
		}
	}
	m.peerGroups = map[string]bool{"": true}
	if m.peersOn("") {
		t.Error("a panel in no group has no peers, whatever the set holds")
	}
}

// groupRow is the dashboard row for a group of the nested fleet, by path.
func groupRow(t *testing.T, m model, path string) dashItem {
	t.Helper()
	for _, it := range m.dashItems() {
		if it.kind == itemGroup && it.name == path {
			return it
		}
	}
	t.Fatalf("no dashboard row for group %q", path)
	return dashItem{}
}

// TestPeersMarkOnTreeRow: the collapsed tree row carries the mark for a peers-on
// group and for no other row — not its sub-group, not its member panels.
func TestPeersMarkOnTreeRow(t *testing.T) {
	m := baseModel()
	m.fleet = nestedFleet()
	backend := groupRow(t, m, "backend")
	backend.expanded = false
	api := dashItem{kind: itemGroup, name: "backend/api", depth: 1, members: nestedFleet()[1:3]}

	if strings.Contains(m.rowOf(backend), peersGlyph) {
		t.Fatal("a peers-off group row should carry no mark")
	}

	m.peerGroups = map[string]bool{"backend": true}
	if !strings.Contains(m.rowOf(backend), peersGlyph) {
		t.Fatal("a peers-on group row should carry the mark")
	}
	if strings.Contains(m.rowOf(api), peersGlyph) {
		t.Fatal("the flag on backend must not mark backend/api")
	}
	if strings.Contains(m.rowOfPanel(nestedFleet()[0]), peersGlyph) {
		t.Fatal("a member panel's tree row is not a group row and carries no mark")
	}

	// Under a lens the rows are buckets; one that shares a group's name is still
	// not that group.
	m.lens = lensState
	if strings.Contains(m.rowOf(dashItem{kind: itemGroup, name: "backend", members: nestedFleet()}), peersGlyph) {
		t.Fatal("a lens bucket named like a peers-on group must not carry its mark")
	}
}

// TestPeersGlyphIsTheDisclosureCell: on the tree row the peers glyph is not a
// column of its own but stands in the disclosure cell — ⇄ where a collapsed
// group shows ▸, and the expanded ▾ kept but drawn in the peers colour — so the
// lead is exactly as wide as a peers-off row's and the name stays whole.
func TestPeersGlyphIsTheDisclosureCell(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)

	m := baseModel()
	m.fleet = nestedFleet()
	open := groupRow(t, m, "backend")
	if !open.expanded {
		t.Fatal("the fixture's backend row should start expanded")
	}
	shut := open
	shut.expanded = false
	stateColour := states[groupState(open.members)].color
	paint := func(c lipgloss.TerminalColor, s string) string {
		return lipgloss.NewStyle().Foreground(c).Bold(true).Render(s)
	}

	offOpen, offShut := m.rowLead(open), m.rowLead(shut)
	if row := ansi.Strip(m.rowOf(open)); !strings.Contains(row, "▾ backend (") {
		t.Fatalf("a peers-off expanded row should show ▾, got %q", row)
	}
	if row := ansi.Strip(m.rowOf(shut)); !strings.Contains(row, "▸ backend (") {
		t.Fatalf("a peers-off collapsed row should show ▸, got %q", row)
	}
	if !strings.Contains(offOpen, paint(stateColour, "▾")) || !strings.Contains(offShut, paint(stateColour, "▸")) {
		t.Fatalf("a peers-off row should draw its triangle in the group's state colour: %q / %q", offOpen, offShut)
	}

	m.peerGroups = map[string]bool{"backend": true}
	for _, c := range []struct {
		name      string
		it        dashItem
		off, want string
		gone      string
	}{
		{"collapsed", shut, offShut, peersGlyph + " backend (", "▸"},
		{"expanded", open, offOpen, "▾ backend (", peersGlyph},
	} {
		lead := m.rowLead(c.it)
		if lipgloss.Width(lead) != lipgloss.Width(c.off) {
			t.Errorf("%s: a peers-on lead is %d cells, want %d (the glyph takes no extra column)",
				c.name, lipgloss.Width(lead), lipgloss.Width(c.off))
		}
		row := ansi.Strip(m.rowOf(c.it))
		if !strings.Contains(row, c.want) {
			t.Errorf("%s: want %q in the row, got %q", c.name, c.want, row)
		}
		if strings.Contains(row, c.gone) {
			t.Errorf("%s: the row should not show %q, got %q", c.name, c.gone, row)
		}
		glyph := strings.TrimSuffix(c.want, " backend (")
		if !strings.Contains(lead, paint(colCyan, glyph)) {
			t.Errorf("%s: the %s should be drawn in the peers colour, got %q", c.name, glyph, lead)
		}
	}
}

// cardLine is line n of a rendered card under its top border — 1 the head, 2
// the kind line — with its colour stripped.
func cardLine(t *testing.T, card string, n int) string {
	t.Helper()
	lines := strings.Split(ansi.Strip(card), "\n")
	if len(lines) < 4 {
		t.Fatalf("a card should have a border, a head and a kind line, got %q", card)
	}
	return lines[n]
}

// TestPeersMarkOnGroupCard: the group card carries the mark once, on its head
// beside the glyph — no word, and nothing on the kind line, which has no room
// for one — exactly when the group has peers on, and stays card-sized.
func TestPeersMarkOnGroupCard(t *testing.T) {
	m := baseModel()
	m.fleet = groupedFleet()
	api := dashItem{kind: itemGroup, name: "api", members: m.fleet}

	off := m.renderGroupCard(api, false)
	if strings.Contains(off, peersGlyph) {
		t.Fatal("a peers-off group card should carry no mark")
	}

	m.peerGroups = map[string]bool{"api": true}
	on := m.renderGroupCard(api, false)
	if head := cardLine(t, on, 1); !strings.Contains(head, peersGlyph+" ▣ api") {
		t.Fatalf("a peers-on group card should lead its head with the mark, got %q", head)
	}
	if strings.Count(ansi.Strip(on), peersGlyph) != 1 {
		t.Fatalf("the card should say it once, got:\n%s", ansi.Strip(on))
	}
	if kind := cardLine(t, on, 2); kind != cardLine(t, off, 2) {
		t.Fatalf("the kind line should not change with the flag: %q vs %q", kind, cardLine(t, off, 2))
	}
	if lipgloss.Height(on) != lipgloss.Height(off) || lipgloss.Width(on) != lipgloss.Width(off) {
		t.Fatalf("the mark changed the card's size: %dx%d, want %dx%d",
			lipgloss.Width(on), lipgloss.Height(on), lipgloss.Width(off), lipgloss.Height(off))
	}

	db := dashItem{kind: itemGroup, name: "db", members: m.fleet}
	if strings.Contains(m.renderGroupCard(db, false), peersGlyph) {
		t.Fatal("the flag on api must not mark the db card")
	}

	// A lens bucket that shares the group's name is not the group.
	m.lens = lensState
	if strings.Contains(m.renderGroupCard(api, false), peersGlyph) {
		t.Fatal("a lens bucket named like a peers-on group must not carry its mark")
	}
}

// splitTitle is the first line of the group split, stripped of colour.
func splitTitle(m model) string {
	return strings.SplitN(ansi.Strip(m.groupZoomView()), "\n", 2)[0]
}

// TestPeersMarkOnSplitTitle: the split's title carries the chip for the group on
// screen, and only for that exact group.
func TestPeersMarkOnSplitTitle(t *testing.T) {
	m := backendSplit("")
	if strings.Contains(splitTitle(m), peersGlyph) {
		t.Fatalf("a peers-off split title should not mention peers, got %q", splitTitle(m))
	}

	m.peerGroups = map[string]bool{"backend": true}
	if title := splitTitle(m); !strings.Contains(title, peersGlyph+" peers") {
		t.Fatalf("a peers-on split title should carry the chip, got %q", title)
	}

	m.peerGroups = map[string]bool{"backend/api": true}
	if title := splitTitle(m); strings.Contains(title, peersGlyph) {
		t.Fatalf("the flag on backend/api must not mark backend's title, got %q", title)
	}

	// Under a lens the title names a bucket, and a bucket has no flag.
	m.peerGroups = map[string]bool{"backend": true}
	m.lens = lensState
	if title := splitTitle(m); strings.Contains(title, peersGlyph) {
		t.Fatalf("a lens bucket named like a peers-on group must not carry its chip, got %q", title)
	}
}

// TestPeersMarkOnMemberTile: a member tile leads its head with the mark when the
// panel's own group has peers on — including while it is being typed into.
func TestPeersMarkOnMemberTile(t *testing.T) {
	m := backendSplit("")
	direct := panel.Panel{ID: "1", Kind: panel.Shell, Title: "backend · d", State: panel.Running, Group: "backend"}
	nested := panel.Panel{ID: "2", Kind: panel.Agent, Title: "api · a", State: panel.Running, Group: "backend/api"}
	head := func(p panel.Panel, focused bool) string {
		return strings.Split(ansi.Strip(m.renderTile(p, focused, 40, 4, 0)), "\n")[1] // under the top border
	}

	if strings.Contains(head(direct, false), peersGlyph) {
		t.Fatal("a tile in a peers-off group should carry no mark")
	}

	m.peerGroups = map[string]bool{"backend": true}
	if !strings.Contains(head(direct, false), peersGlyph) {
		t.Fatalf("a tile in a peers-on group should carry the mark, got %q", head(direct, false))
	}
	if strings.Contains(head(nested, false), peersGlyph) {
		t.Fatal("a panel of backend/api is not a peer in backend and carries no mark")
	}

	m.groupInteract = true
	if h := head(direct, true); !strings.Contains(h, peersGlyph) || !strings.Contains(h, "⌨") {
		t.Fatalf("the mark should survive the interact badge, got %q", h)
	}
}

// TestPeersMarkYieldsOnANarrowTile: a tile too narrow for the mark and a cell of
// title goes without it rather than wrapping its head — the tile never grows a
// row for it — and gets it back the moment there is a cell to spare.
func TestPeersMarkYieldsOnANarrowTile(t *testing.T) {
	m := backendSplit("")
	direct := panel.Panel{ID: "1", Kind: panel.Shell, Title: "backend · d", State: panel.Running, Group: "backend"}
	tile := func(cols int) string { return m.renderTile(direct, false, cols, 4, 0) }

	// The head is "⇄ ● " and then the title: four cells before the first letter.
	const fits = 5
	off := [2]string{tile(fits - 1), tile(fits)}

	m.peerGroups = map[string]bool{"backend": true}
	narrow, wide := tile(fits-1), tile(fits)
	if strings.Contains(narrow, peersGlyph) {
		t.Fatalf("a %d-cell tile has no room for the mark and should drop it", fits-1)
	}
	if lipgloss.Height(narrow) != lipgloss.Height(off[0]) {
		t.Fatalf("a narrow marked tile is %d rows, want %d", lipgloss.Height(narrow), lipgloss.Height(off[0]))
	}
	if !strings.Contains(wide, peersGlyph) {
		t.Fatalf("a %d-cell tile has room for the mark and one cell of title", fits)
	}
	if lipgloss.Height(wide) != lipgloss.Height(off[1]) || lipgloss.Width(wide) != lipgloss.Width(off[1]) {
		t.Fatalf("the mark changed the tile's size: %dx%d, want %dx%d",
			lipgloss.Width(wide), lipgloss.Height(wide), lipgloss.Width(off[1]), lipgloss.Height(off[1]))
	}
}

// TestPeersZoomStripFitsTheScreen: on a strip too narrow for the title, the
// word and the status together, the word goes first and then the title is cut —
// the strip is never wider than the screen and the glyph is never what is lost.
func TestPeersZoomStripFitsTheScreen(t *testing.T) {
	m := baseModel()
	m.fleet = nestedFleet()
	m.peerGroups = map[string]bool{"backend": true}
	m.mode, m.zoomID = modeZoom, "1"
	m.status = "local"

	// A CJK title, whose cells come in pairs, and a Latin one that can be cut to
	// the exact cell: the second is what catches a cut that is one cell short.
	for _, title := range []string{strings.Repeat("重構登入流程的代理人", 6), strings.Repeat("x", 120)} {
		m.zoomTitle = title
		for _, width := range []int{120, 81, 80, 60} {
			m.width = width
			strip := ansi.Strip(m.zoomFooter())
			if got := lipgloss.Width(strip); got > width {
				t.Errorf("at %d cells the strip is %d wide: %q", width, got, strip)
			}
			if !strings.Contains(strip, peersGlyph) {
				t.Errorf("at %d cells the strip lost the mark: %q", width, strip)
			}
			if strings.Contains(strip, peersGlyph+" peers") {
				t.Errorf("at %d cells there is no room for the word, got %q", width, strip)
			}
		}
	}

	// baton's own titles carry a "·", which the layout counts as one cell and the
	// runewidth ruler as two. Cut against the wrong one, the title stays too long.
	// Checked with peers on and off: the cut is the strip's, not the mark's.
	for _, title := range []string{
		strings.Repeat("api · a · ", 12),
		strings.Repeat("重構 · 登入 ─ ", 10),
	} {
		m.zoomTitle = title
		for _, on := range []bool{true, false} {
			m.peerGroups = map[string]bool{"backend": on}
			for _, width := range []int{30, 40, 60} {
				m.width = width
				strip := ansi.Strip(m.zoomFooter())
				if got := lipgloss.Width(strip); got > width {
					t.Errorf("peers=%v at %d cells the strip is %d wide: %q", on, width, got, strip)
				}
				if on != strings.Contains(strip, peersGlyph) {
					t.Errorf("peers=%v at %d cells the mark is wrong: %q", on, width, strip)
				}
			}
		}
	}
	m.peerGroups = map[string]bool{"backend": true}

	// With room for all three, the word and the whole status are both there.
	m.width, m.zoomTitle = 120, "backend · d"
	if strip := ansi.Strip(m.zoomFooter()); !strings.Contains(strip, peersGlyph+" peers") || !strings.Contains(strip, "● local") {
		t.Fatalf("a wide strip should carry the word and the status whole, got %q", strip)
	}
	// Narrowed a cell at a time: the word is never paid for out of the status. At
	// every width that still shows the word the status is whole, and the word does
	// go before the strip runs out.
	wordGone := false
	for m.width = 119; m.width >= 40; m.width-- {
		strip := ansi.Strip(m.zoomFooter())
		if !strings.Contains(strip, peersGlyph+" peers") {
			wordGone = true
			continue
		}
		if !strings.Contains(strip, "● local") {
			t.Fatalf("at %d cells the word cost the status its room: %q", m.width, strip)
		}
	}
	if !wordGone {
		t.Fatal("setup: the strip never got narrow enough to drop the word")
	}
}

// TestPeersMarkOnZoom: a zoomed panel of a peers-on group says so on the zoom's
// strip — the only chrome a zoom has — and no other zoom does.
func TestPeersMarkOnZoom(t *testing.T) {
	m := baseModel()
	m.fleet = nestedFleet()
	m.mode, m.zoomID, m.zoomTitle = modeZoom, "1", "backend · d"

	if strings.Contains(m.zoomFooter(), peersGlyph) {
		t.Fatal("a zoom in a peers-off group should carry no mark")
	}

	m.peerGroups = map[string]bool{"backend": true}
	if strip := ansi.Strip(m.zoomFooter()); !strings.Contains(strip, peersGlyph+" peers") {
		t.Fatalf("a zoom in a peers-on group should say so, got %q", strip)
	}

	m.zoomID = "2" // a panel of backend/api
	if strings.Contains(m.zoomFooter(), peersGlyph) {
		t.Fatal("a zoomed panel of backend/api is not a peer in backend")
	}
	m.zoomID = "5" // the lone panel
	if strings.Contains(m.zoomFooter(), peersGlyph) {
		t.Fatal("a zoomed panel in no group has no peers")
	}
}

// TestPeersSpeaksZhTW: the prompt, both outcomes and the chip are the zh-TW
// catalog's words, with the group's name still quoted in them.
func TestPeersSpeaksZhTW(t *testing.T) {
	sentCommands(t)
	m := peersModel(t, "api")
	m.lang = i18n.ZhTW

	m = press(m, keyPeers)
	if m.status != `要讓 "api" 裡的面板互相讀取與輸入嗎? (y/n)` {
		t.Fatalf("the zh-TW prompt is wrong: %q", m.status)
	}
	m = press(m, "y")
	if m.status != `已開啟 "api" 的互通` {
		t.Fatalf("the zh-TW on line is wrong: %q", m.status)
	}
	if chip := ansi.Strip(m.peersChip()); chip != peersGlyph+" 互通" {
		t.Fatalf("the zh-TW chip is wrong: %q", chip)
	}
	m = press(m, keyPeers)
	if m.status != `已關閉 "api" 的互通` {
		t.Fatalf("the zh-TW off line is wrong: %q", m.status)
	}
}

// TestPeersPromptDropsWhenMembersChange: the y agrees to the panels that were in
// the group when the question was asked, not to whatever the name holds by the
// time it is typed. Any client can file a panel in or out in between, so a
// snapshot with a different set of direct members withdraws the question — and a
// snapshot with the same set, in any order, leaves it standing.
func TestPeersPromptDropsWhenMembersChange(t *testing.T) {
	// A fleet with nobody asking for attention, so the only thing a snapshot can
	// say on the status line is what happened to the question.
	calm := func() []panel.Panel {
		fleet := groupedFleet()
		for i := range fleet {
			fleet[i].State = panel.Idle
		}
		return fleet
	}
	asked := func() model {
		m := baseModel()
		m.fleet = calm()
		m.cursorOnGroup(t, "api")
		return press(m, keyPeers)
	}
	regroup := func(change map[string]string) []panel.Panel {
		fleet := calm()
		for i := range fleet {
			if g, ok := change[fleet[i].ID]; ok {
				fleet[i].Group = g
			}
		}
		return fleet
	}
	for name, change := range map[string]map[string]string{
		"a panel filed into the group":  {"2": "api"},
		"a panel filed out of it":       {"3": ""},
		"one out and another in":        {"3": "db", "2": "api"},
		"a panel moved in from a group": {"4": "api"},
	} {
		sent := sentCommands(t)
		m := asked()
		if m.pendingPeers != "api" {
			t.Fatalf("%s: setup: api should be asked about, pending %q", name, m.pendingPeers)
		}

		m.applyEvent(peersSnapshot(regroup(change)))
		if m.pendingPeers != "" || m.status != "peers left off" {
			t.Fatalf("%s: the question should be withdrawn with its prompt, pending=%q status=%q", name, m.pendingPeers, m.status)
		}
		answersNothing(t, m, sent, name)
	}

	// The same members in another order are the same members: the question
	// stands, and its y opens the group.
	sent := sentCommands(t)
	m := asked()
	fleet := calm()
	slices.Reverse(fleet)
	m.applyEvent(peersSnapshot(fleet))
	if m.pendingPeers != "api" {
		t.Fatalf("a reordered fleet must not withdraw the question, pending %q", m.pendingPeers)
	}
	m = press(m, "y")
	if len(*sent) != 1 || (*sent)[0].Action != "group.peers" || (*sent)[0].Group != "api" || !(*sent)[0].Enable {
		t.Fatalf("the standing question's y should open api, sent %+v", *sent)
	}
}
