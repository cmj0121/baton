package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// dashKind tags a dashboard item: a lone panel, or a group folded into one card.
type dashKind int

const (
	itemPanel dashKind = iota // a single ungrouped panel
	itemGroup                 // a work item: many panels under one name

	// itemFold is the "▸ N quiet" row: the panels the dashboard has folded away
	// because nothing about them is asking for anything. It is a DISPLAY device
	// and owns no verbs beyond opening and closing itself — see foldQuietItems.
	itemFold
)

// defaultFoldQuiet is settings.fold-quiet's built-in: how many quiet panels the
// dashboard shows before it folds them into one row.
//
// Eight is chosen off the card grid rather than off a fleet size. It is about
// where the grid stops fitting on one screen at a normal terminal width, which is
// the point at which a quiet panel stops being context you take in at a glance and
// starts being something you scroll past. Below it the dashboard is byte-identical
// to what it was before this existed, which is the property the number is really
// picked to preserve: nobody running five panels should be able to tell the fold
// was added.
const defaultFoldQuiet = 8

// dashItem is one cursor-addressable cell on the dashboard. A group collapses
// all of its member panels into a single card; a lone panel stands on its own.
// The cursor indexes dashItems, not the flat fleet, so everything that acts on
// the selection resolves through here.
type dashItem struct {
	kind    dashKind
	panel   panel.Panel   // itemPanel: the panel itself
	name    string        // itemGroup: the work-item name
	members []panel.Panel // itemGroup: panels filed under name, in fleet order

	// need is how many of a group's members the attention inbox would queue right
	// now — the ◆N on the card head and the tree row. It is carried on the item
	// rather than recomputed by each renderer because the dashboard draws the same
	// group twice (card and preview, or tree row and preview) in one frame, and
	// counting it once per fold is what keeps a 50-panel render linear.
	need int

	// quiet is how many panels an itemFold row stands for. The panels themselves
	// are NOT carried here, and members is left empty on a fold row on purpose:
	// ids() reads members, every bulk verb reads ids(), and a fold row that
	// answered "these fifty" to `w` would be precisely the destructive surprise
	// this row is not allowed to be.
	quiet int

	// The tree coordinates. depth is how far the row is indented and parent is the
	// group path it sits directly inside ("" at the top level) — which is also a
	// fold row's identity, since a fold row is "the quiet ones at this level".
	// last closes the branch glyph (└─ rather than ├─).
	depth  int
	parent string
	last   bool

	// expanded is an itemGroup's open/shut state. It is a VIEW state and never a
	// selection one: members is the whole subtree either way, so every bulk verb
	// means the same thing on an open group as on a closed one. Getting that wrong
	// would be a `w` that closes twelve panels or one depending on a keypress
	// nobody thinks of as changing what is selected.
	expanded bool

	// node is the tree node an itemGroup row was built from, so flatten can recurse
	// into it without rebuilding the tree. Nil on every other kind.
	node *groupNode
}

// label is how a row NAMES itself on screen: the last path segment for a nested
// group, since its ancestors are already drawn above it as the rows it is indented
// under. name stays the full path everywhere else, because that is the identity
// and what every server op takes.
func (it dashItem) label() string {
	if it.kind == itemGroup {
		return panel.GroupLeaf(it.name)
	}
	return it.title()
}

// dashItems is the dashboard's cursor model: the fleet flattened into rows.
//
// It delegates to the tree projection (see tree.go), which is what makes nesting
// visible — a group row is one work item at one depth, not a whole subtree
// collapsed under its top-level name. Everything that acts on the selection still
// resolves through here, so there remains exactly one answer to "what is row N".
//
// The need counts ride along and deliberately do NOT change the order. A card
// moving under the cursor because a panel elsewhere started asking a question is
// the disorientation the inbox's frozen ordering exists to avoid, and it is worse
// here because the dashboard is where you are looking when it happens. The tree
// says WHERE the work is by annotating the shape, never by rearranging it.
func (m model) dashItems() []dashItem { return m.dashTree() }

// foldQuietLevel collapses the panels nothing is happening in into a single
// expandable row, once one LEVEL of the tree holds more of them than
// settings.fold-quiet. parent is the group path the level sits in ("" at the top),
// which is the fold row's identity — every level folds its own quiet panels, so a
// crowded work item tidies itself without the top level having to know.
//
// Folded, never dropped. The row expands in place and the panels come back exactly
// where they were, because a dashboard that silently stops showing you panels is a
// dashboard you have to keep double-checking against `baton ctl ls`, and then it
// has cost you more than it saved. It is also why the row is inserted at the
// position of the FIRST panel it swallowed rather than pushed to the end: the
// tree's shape is meant to survive the fold, so that the row above it and the row
// below it are the same two rows they were a moment ago.
//
// Cost: two passes over one level's rows and, when the fold fires, one slice. The
// first pass only counts, because the fold has to know whether it fires before it
// can decide where to put the row. Below the threshold — the case a small fleet is
// in permanently — the whole thing is one count and a return of the input.
func (m model) foldQuietLevel(level []dashItem, parent string) []dashItem {
	if m.foldQuiet <= 0 {
		return level // settings.fold-quiet: 0 — the dashboard shows everything it has
	}
	n := 0
	for i := range level {
		if m.foldable(level[i]) {
			n++
		}
	}
	if n <= m.foldQuiet {
		return level
	}
	depth := 0
	if len(level) > 0 {
		depth = level[0].depth
	}
	out := make([]dashItem, 0, len(level)+1)
	placed := false
	for _, it := range level {
		fold := m.foldable(it)
		if fold && !placed {
			out = append(out, dashItem{kind: itemFold, quiet: n, depth: depth, parent: parent})
			placed = true
		}
		if !fold || m.foldOpen[parent] {
			out = append(out, it)
		}
	}
	return out
}

// foldable reports whether one dashboard item is a candidate for the quiet fold.
//
// Quiet means the panel is idle, or exited cleanly — the two states that say
// nothing happened and nothing is going to. A non-zero exit is not quiet: it is a
// failure sitting there waiting to be read, and it has a row in the inbox for the
// same reason.
//
// Four things are never folded, and all four are the user having already said this
// one matters: a favourite, a pin, a member of a pending selection, and the card
// under the cursor. The first three are the same argument that floats favourites
// to the front of the dashboard. The last is the one that would be a bug rather
// than a preference — a fold that swallows what you are pointing at moves the
// selection somewhere you did not put it, which is the exact failure the tree
// refuses to re-sort in order to avoid.
//
// Groups are never folded either. A group is already one card, it already carries
// its own need count, and folding a whole work item away because its members
// happen to be resting would hide the count that says otherwise.
func (m model) foldable(it dashItem) bool {
	if it.kind != itemPanel {
		return false
	}
	p := it.panel
	if p.Favourite || p.Pinned || m.marked[p.ID] || p.ID == m.foldKeepID {
		return false
	}
	return p.State == panel.Idle || (p.State == panel.Exited && p.ExitCode == 0)
}

// toggleFold opens or closes the quiet row. One flag, because there is at most one
// fold row on the dashboard: the quiet panels are one set, not one set per group.
//
// Both directions move the list under the cursor — collapsing removes every folded
// row from it — so the cursor is re-anchored by IDENTITY afterwards rather than
// left on a number that now means something else, or nothing at all. The landing
// order is deliberate: the fold row first, then the card the cursor was on if it is
// still on screen. That way a card that just went back INTO the fold leaves the
// cursor on the row it went into, which is where that card now lives, instead of
// past the end of a list that just got twelve items shorter.
func (m model) toggleFold() model {
	was, had := m.selectedItem()
	// Which level's fold: the one the cursor is in. A fold row toggles itself; any
	// other row toggles the fold of the level it sits at, so `esc` from a panel
	// inside a work item folds that work item's quiet panels rather than the top
	// level's — the level the person is looking at is the one they mean.
	parent := ""
	if had {
		parent = was.parent
	}
	if m.foldOpen == nil {
		m.foldOpen = map[string]bool{}
	}
	m.foldOpen[parent] = !m.foldOpen[parent]
	if m.foldOpen[parent] {
		m.status = "quiet panels shown · esc folds them again"
	} else {
		m.status = "quiet panels folded"
	}
	if at := m.foldRowIndexAt(parent); at >= 0 {
		m.cursor = at
	}
	if had && was.kind != itemFold {
		m.cursorToItem(was) // a no-match leaves the cursor on the row, which is the honest answer
	}
	m.clampCursor()
	return m
}

// foldRowIndex is where the quiet row sits in the current projection, or -1 when
// nothing is folded.
//
// esc consults it (through foldRowShowing) before collapsing, so that an expanded
// flag left standing over a fleet with nothing to fold — filter it down to three
// panels and there is no row — does not swallow the keystroke and answer "quiet
// panels folded" to somebody who was trying to clear the filter. It walks the fold
// once, on a keypress rather than on a frame.
func (m model) foldRowIndex() int {
	if m.foldQuiet <= 0 {
		return -1
	}
	for i, it := range m.dashItems() {
		if it.kind == itemFold {
			return i
		}
	}
	return -1
}

// foldRowIndexAt is where one LEVEL's quiet row sits, or -1 when that level has
// nothing folded. The tree has a fold row per level, so a toggle has to land the
// cursor on the row it just operated rather than on whichever one comes first.
func (m model) foldRowIndexAt(parent string) int {
	if m.foldQuiet <= 0 {
		return -1
	}
	for i, it := range m.dashItems() {
		if it.kind == itemFold && it.parent == parent {
			return i
		}
	}
	return -1
}

// foldRowShowing reports whether a quiet row is actually on the dashboard.
func (m model) foldRowShowing() bool { return m.foldRowIndex() >= 0 }

// anyFoldOpen reports whether any level's quiet fold currently stands expanded —
// what esc consults before deciding it has a fold to collapse.
func (m model) anyFoldOpen() bool {
	for _, open := range m.foldOpen {
		if open {
			return true
		}
	}
	return false
}

// foldRowVerbs are the dashboard actions that act on whatever the cursor is
// resting on. Each of them is refused on a fold row.
//
// The fold row is a display device. Letting `w` close "the quiet ones" would be a
// bulk destructive action reachable by one keystroke on a row whose whole purpose
// is that you have not looked at its contents — which is the surprise the plan spun
// synchronized-input out to avoid, arrived at from the other direction. Expanding
// first costs one keystroke and puts the panels back under the verbs they already
// have, individually, where the confirmation prompt names what it is about to do.
//
// It is a block list rather than an allow list because the thing being protected is
// narrow and nameable: a verb that resolves through selectedItem. Everything else a
// dashboard key does — spawn, filter, help, the inbox, the process tree — is
// unaffected by where the cursor happens to be and stays reachable from the row.
var foldRowVerbs = map[action]bool{
	actClose: true, actRespawn: true, actSignal: true, actDiff: true,
	actDispatch: true, actEnqueue: true, actMark: true, actAdd: true,
	actUngroup: true, actRename: true, actFavourite: true, actNewHere: true,
}

// refusedOnFoldRow reports whether an action must be refused because the cursor is
// on the quiet row. The action is checked first so the common case — every other
// key — never pays for a dashItems fold.
func (m model) refusedOnFoldRow(a action) bool {
	if !foldRowVerbs[a] {
		return false
	}
	it, ok := m.selectedItem()
	return ok && it.kind == itemFold
}

// needByGroup tallies, per top-level group, how many of its panels the attention
// inbox would queue right now — the ◆N a group header carries.
//
// It asks inboxQualifies rather than restating what "needs you" means, and that is
// the whole point of the function. The dashboard's count and the queue C-t a opens
// are two renderings of one predicate; the moment they are two predicates, a header
// says a group has two panels waiting and the queue offers one, and the operator
// has to decide which of baton's own screens is lying. Reading the WIRE snapshot
// rather than m.fleet is part of the same argument: Acked — whether a human already
// dealt with a panel — is fleet state the domain model deliberately does not carry,
// so counting from m.fleet would keep re-flagging work somebody has already cleared.
//
// Lone panels are skipped because their own card already shows the state LED that
// would have earned them a row; a count beside it would say the same thing twice.
//
// Cost: one pass over the snapshot, one map read per group card, and — on the
// common path of a fleet where nothing is asking — no allocation at all, since the
// map is built only once something qualifies. That matters because this runs inside
// dashItems, which the dashboard evaluates several times per frame.
func (m model) needByGroup() map[string]int {
	var need map[string]int
	for _, p := range m.inboxWire {
		if p.Group == "" {
			continue
		}
		if _, ok := inboxQualifies(p, m.inboxDone); !ok {
			continue
		}
		if need == nil {
			need = make(map[string]int)
		}
		need[panel.GroupTop(p.Group)]++ // the card folds the subtree, so the count must too
	}
	return need
}

// itemFavourite reports whether a dashboard item is a favourite: a lone panel by
// its server-owned Favourite flag, a group by the snapshot's favGroups set.
func (m model) itemFavourite(it dashItem) bool {
	if it.kind == itemGroup {
		return m.favGroups[it.name]
	}
	return it.panel.Favourite
}

// childGroup is one immediate sub-group under a parent path: its full path and its
// whole subtree. The dashboard counts them (subGroupCount) and the split renders one
// descendable tile per child group.
type childGroup struct {
	path    string
	members []panel.Panel
}

// childGroupsOf folds panels into the immediate sub-groups directly under parent —
// one childGroup per distinct child segment, in first-appearance order, each with its
// whole subtree. The dashboard card (subGroupCount) and the split (childGroups) share
// it, so "the immediate sub-groups" is derived one way.
func childGroupsOf(panels []panel.Panel, parent string) []childGroup {
	at := map[string]int{}
	var out []childGroup
	for _, p := range panels {
		seg, ok := panel.GroupChildSegment(p.Group, parent)
		if !ok {
			continue
		}
		child := panel.GroupJoin(parent, seg)
		if i, seen := at[child]; seen {
			out[i].members = append(out[i].members, p)
			continue
		}
		at[child] = len(out)
		out = append(out, childGroup{path: child, members: []panel.Panel{p}})
	}
	return out
}

// subGroupCount is how many immediate sub-groups a top-level group holds, for the
// card's nested makeup — the same fold the split uses, counted.
func subGroupCount(members []panel.Panel, top string) int {
	return len(childGroupsOf(members, top))
}

// title is the label shown for an item on the dashboard.
func (it dashItem) title() string {
	switch it.kind {
	case itemGroup:
		return it.name
	case itemFold:
		return fmt.Sprintf("%d quiet", it.quiet)
	}
	return it.panel.Title
}

// closePrompt is the y/n confirmation line for closing an item with w. A group
// spells out that the close takes every member with it, so the count is never a
// surprise; a lone panel just names itself.
func (it dashItem) closePrompt() string {
	if it.kind == itemGroup {
		return fmt.Sprintf("close group %q and its %d panel(s)? (y/n)", it.name, len(it.members))
	}
	return "close " + it.title() + "? (y/n)"
}

// ids is the panel ids an item covers: one for a panel, every member for a group,
// and NONE for a fold row — see the quiet field. Every bulk verb reads this, so an
// empty answer is what makes "the fold owns no verbs" true even if a caller forgets
// to ask.
func (it dashItem) ids() []string {
	if it.kind == itemPanel {
		return []string{it.panel.ID}
	}
	ids := make([]string, len(it.members))
	for i, p := range it.members {
		ids[i] = p.ID
	}
	return ids
}

// selectedItem resolves the cursor to its dashboard item, reporting false when
// the dashboard is empty or the cursor is out of range.
func (m model) selectedItem() (dashItem, bool) {
	items := m.dashItems()
	if m.cursor < 0 || m.cursor >= len(items) {
		return dashItem{}, false
	}
	return items[m.cursor], true
}

// stateRank orders lifecycle states by how loudly they call for attention, so a
// group can roll up to the most urgent state among its members.
//
// done and stuck slot ABOVE running: both want a human and running does not, and
// a card that reads "running" while one of its members has been wedged for ten
// minutes is exactly the burial this ranking exists to prevent. Every relative
// order that existed before is preserved.
var stateRank = map[panel.State]int{
	panel.Attention: 7,
	panel.Stuck:     6,
	panel.Done:      5,
	panel.Running:   4,
	panel.Spawning:  3,
	panel.Idle:      2,
	panel.Exited:    1,
}

// groupState rolls a group's members up to one representative state: the most
// urgent member wins, so a group with anything needing you reads as attention.
func groupState(members []panel.Panel) panel.State {
	best := panel.Exited
	for _, p := range members {
		if stateRank[p.State] > stateRank[best] {
			best = p.State
		}
	}
	return best
}

// selecting reports whether a multi-select is in progress (any panel marked). The
// marker column only appears while selecting, so the default dashboard is
// unchanged until the user presses the mark key.
func (m model) selecting() bool { return len(m.marked) > 0 }

// itemMarked reports whether every id an item covers is currently marked — a
// group shows as marked only when all its members are.
func (m model) itemMarked(it dashItem) bool {
	ids := it.ids()
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if !m.marked[id] {
			return false
		}
	}
	return true
}

// toggleMark flips the marks on every panel an item covers. Marking a group marks
// all its members at once, so a whole work item can be folded into a new group.
func (m *model) toggleMark(it dashItem) {
	if m.marked == nil {
		m.marked = make(map[string]bool)
	}
	on := !m.itemMarked(it) // if not all marked, mark all; else clear all
	for _, id := range it.ids() {
		if on {
			m.marked[id] = true
		} else {
			delete(m.marked, id)
		}
	}
}

// markedIDs is the marked panel ids in fleet order — the input to panel.group.
func (m model) markedIDs() []string {
	ids := make([]string, 0, len(m.marked))
	for _, p := range m.fleet {
		if m.marked[p.ID] {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

// markCell renders the fixed-width selection marker shown left of a card's title
// while selecting: a bright check when marked, blank space otherwise.
func markCell(marked bool) string {
	if marked {
		return lipgloss.NewStyle().Foreground(colCyan).Bold(true).Render("✓ ")
	}
	return "  "
}

// markStatus describes the current selection for the status line.
func (m model) markStatus() string {
	n := len(m.markedIDs())
	if n == 0 {
		return "selection cleared"
	}
	return fmt.Sprintf("%d panel(s) selected · %s to group", n, keyLabel(m.bindingKey(actGroup)))
}

// startGroup opens the name overlay for the marked panels, or nudges the user to
// select some first.
func (m model) startGroup() model {
	if len(m.markedIDs()) == 0 {
		m.status = fmt.Sprintf("press %s to select panels, then %s to group", keyLabel(m.bindingKey(actMark)), keyLabel(m.bindingKey(actGroup)))
		return m
	}
	m.input = inputGroupName
	m.inputBuf = ""
	m.status = fmt.Sprintf("name the work item · %d panel(s), enter to create", len(m.markedIDs()))
	return m
}

// nameConflict mirrors the server's uniqueness policy on the local fleet so the
// cockpit can reject a duplicate name before sending — and so the pending
// selection (or rename) survives the rejection instead of being cleared
// optimistically only to have the server bounce it. skipID/skipGroup exempt the
// item being renamed; an empty name or the allow-conflict setting never collide.
func (m model) nameConflict(name, skipID, skipGroup string) bool {
	if name == "" || m.allowNameConflict {
		return false
	}
	for _, p := range m.fleet {
		if p.ID != skipID && p.Title == name {
			return true
		}
		if p.Group != "" && p.Group != skipGroup && p.Group == name {
			return true
		}
	}
	return false
}

// commitGroup files the marked panels under the typed name and clears the
// selection. The server is the source of truth, so the broadcast re-syncs the
// fleet with the new group. A name that already belongs to another panel or
// group is rejected here, keeping the selection intact so the user can retype.
func (m model) commitGroup(name string) model {
	if name == "" {
		m.status = "a group needs a name"
		return m
	}
	if len(m.markedIDs()) == 0 {
		m.status = "no panels selected"
		return m
	}
	if !panel.GroupValid(name) {
		m.status = fmt.Sprintf("%q is not a valid group path", name)
		return m
	}
	if m.nameConflict(name, "", name) {
		m.status = fmt.Sprintf("the name %q is already taken — pick another", name)
		return m
	}
	groups, panels := m.nestMarkedInto(name)
	m.marked = nil
	m.status = groupStatus("grouped", groups, panels, name)
	return m
}

// addMarkedToGroup files the marked selection into the selected work item and
// clears the selection. The cursor must be on a group card.
func (m model) addMarkedToGroup() model {
	it, ok := m.selectedItem()
	if !ok || it.kind != itemGroup {
		m.status = "select a group to add to"
		return m
	}
	if len(m.markedIDs()) == 0 {
		m.status = "mark panels first, then add to a group"
		return m
	}
	groups, panels := m.nestMarkedInto(it.name)
	m.marked = nil
	m.status = groupStatus("added", groups, panels, it.name)
	return m
}

// nestMarkedInto files the marked selection under target: a fully-marked group is
// **nested** — re-parented as target/<its name>, keeping its own sub-structure —
// rather than flattened, and loose marked panels attach directly to target. So
// grouping a group into a group carries the group's name into the new parent. It
// returns how many groups were nested and how many loose panels moved.
//
// It reasons in top-level groups because that is the only unit the dashboard lets
// you mark: a card folds its whole subtree (dashItems folds by GroupTop) and
// toggleMark marks all of it, so a marked group is always a complete top-level
// subtree and GroupJoin(target, top) is a clean one-level re-parent. The fan-out is
// best-effort — each rename/group is its own command, so a rejected one (e.g. the
// nested path already exists) leaves the rest applied and the next snapshot shows
// the true state.
func (m model) nestMarkedInto(target string) (groups, panels int) {
	// One pass over the fleet, bucketing by top-level group ("" = a lone panel): each
	// top's marked ids plus its total membership, so "the whole group is marked" needs
	// no re-scan. order keeps the command sequence deterministic (map order is not).
	type bucket struct {
		ids           []string
		marked, total int
	}
	buckets := map[string]*bucket{}
	var order []string
	for _, p := range m.fleet {
		top := panel.GroupTop(p.Group)
		b := buckets[top]
		if b == nil {
			b = &bucket{}
			buckets[top] = b
			order = append(order, top)
		}
		b.total++
		if m.marked[p.ID] {
			b.marked++
			b.ids = append(b.ids, p.ID)
		}
	}

	var loose []string
	for _, top := range order {
		b := buckets[top]
		switch {
		case b.marked == 0 || top == target:
			// nothing marked here, or a group already at the target (a no-op).
		case top != "" && b.marked == b.total:
			// the whole group is marked: nest it, keeping its name as the sub-segment.
			m.sendf(proto.Command{Action: "panel.rename", Group: top, Name: panel.GroupJoin(target, top)})
			groups++
		default:
			loose = append(loose, b.ids...) // lone panels, and partial group selections
		}
	}
	if len(loose) > 0 {
		m.sendf(proto.Command{Action: "panel.group", IDs: loose, Group: target})
		panels = len(loose)
	}
	return groups, panels
}

// groupStatus phrases the result of a group/add that may have nested sub-groups,
// moved loose panels, or both.
func groupStatus(verb string, groups, panels int, target string) string {
	switch {
	case groups > 0 && panels > 0:
		return fmt.Sprintf("%s %d sub-group(s) + %d panel(s) into %q", verb, groups, panels, target)
	case groups > 0:
		return fmt.Sprintf("%s %d sub-group(s) into %q", verb, groups, target)
	default:
		return fmt.Sprintf("%s %d panel(s) into %q", verb, panels, target)
	}
}

// ungroupSelected dissolves the selected work item, returning its panels to the
// dashboard as lone cards. It is a no-op on a lone panel.
func (m model) ungroupSelected() model {
	it, ok := m.selectedItem()
	if !ok || it.kind != itemGroup {
		m.status = "select a group to ungroup"
		return m
	}
	m.sendf(proto.Command{Action: "panel.ungroup", Group: it.name})
	m.status = fmt.Sprintf("ungrouped %q", it.name)
	return m
}

// toggleFavourite stars or un-stars the selected dashboard item — a lone panel or
// a group. The server owns the favourite flag, so each toggle is sent on to it
// (and broadcast to every client); the local state is updated optimistically so
// the sort reflows at once, then the next snapshot reconciles it. Favourited
// cards sort to the front of the dashboard, so the cursor is moved to follow the
// toggled card to its new position — no one-frame flicker onto a neighbour.
func (m model) toggleFavourite() model {
	it, ok := m.selectedItem()
	if !ok {
		m.status = "nothing to favourite"
		return m
	}
	if it.kind == itemGroup {
		// A lens BUCKET is not a group the server has, so favouriting one would send
		// group.favourite naming something that does not exist — and the flag would
		// then sit in the server's state for a "group" called `attention` for good.
		// Favouriting a PANEL is fine under any lens: a panel is a panel.
		if why := m.lensRefusal(); why != "" {
			m.status = why
			return m
		}
		fav := !m.favGroups[it.name]
		if m.favGroups == nil {
			m.favGroups = map[string]bool{}
		}
		if fav {
			m.favGroups[it.name] = true
			m.sendf(proto.Command{Action: "group.favourite", Group: it.name})
			m.status = fmt.Sprintf("favourited %q", it.name)
		} else {
			delete(m.favGroups, it.name)
			m.sendf(proto.Command{Action: "group.unfavourite", Group: it.name})
			m.status = fmt.Sprintf("unfavourited %q", it.name)
		}
		m.cursorToItem(it)
		return m
	}
	fav := !it.panel.Favourite
	for i := range m.fleet {
		if m.fleet[i].ID == it.panel.ID {
			m.fleet[i].Favourite = fav // optimistic: reflow the sort before the snapshot lands
		}
	}
	if fav {
		m.sendf(proto.Command{Action: "panel.favourite", ID: it.panel.ID})
		m.status = "favourited " + it.panel.Title
	} else {
		m.sendf(proto.Command{Action: "panel.unfavourite", ID: it.panel.ID})
		m.status = "unfavourited " + it.panel.Title
	}
	m.cursorToItem(it)
	return m
}

// cursorToItem re-points the dashboard cursor at the given item after a reflow —
// matching a lone panel by id and a group by name against the freshly sorted
// dashItems, so the highlight stays on the same card. A no-match leaves the cursor
// put (clamped elsewhere), so a vanished item never wedges it.
func (m *model) cursorToItem(target dashItem) {
	for i, it := range m.dashItems() {
		if it.kind != target.kind {
			continue
		}
		if (it.kind == itemGroup && it.name == target.name) ||
			(it.kind == itemPanel && it.panel.ID == target.panel.ID) {
			m.cursor = i
			return
		}
	}
}

// startRename opens the rename overlay for the selected item, seeded with its
// current name and remembering whether a panel or a group is the target.
func (m model) startRename() model {
	it, ok := m.selectedItem()
	if !ok {
		m.status = "nothing to rename"
		return m
	}
	m.input = inputRename
	m.inputBuf = it.title()
	m.renameID, m.renameGroup = "", ""
	if it.kind == itemGroup {
		m.renameGroup = it.name
		m.status = "rename group · enter to save"
	} else {
		m.renameID = it.panel.ID
		m.status = "rename panel · enter to save"
	}
	return m
}

// commitRename sends the rename for whichever target startRename remembered. A
// name that collides with another panel or group is rejected before sending and
// the overlay stays open, seeded with the attempt, so the rename target is not
// lost to a round-trip the server would only bounce.
func (m model) commitRename(name string) model {
	if name == "" {
		m.status = "a name cannot be empty"
		return m
	}
	if m.nameConflict(name, m.renameID, m.renameGroup) {
		m.input = inputRename // keep the overlay open with the target remembered
		m.inputBuf = name
		m.status = fmt.Sprintf("the name %q is already taken — pick another", name)
		return m
	}
	switch {
	case m.renameGroup != "":
		// A group name is a path (renaming to "backend/db" nests it), so it must be a
		// valid path; a panel title has no such rule. Keep the overlay open on a bad
		// path so the attempt is not lost to a round-trip.
		if !panel.GroupValid(name) {
			m.input = inputRename
			m.inputBuf = name
			m.status = fmt.Sprintf("%q is not a valid group path", name)
			return m
		}
		m.sendf(proto.Command{Action: "panel.rename", Group: m.renameGroup, Name: name})
		m.status = fmt.Sprintf("renamed group to %q", name)
	case m.renameID != "":
		m.sendf(proto.Command{Action: "panel.rename", ID: m.renameID, Name: name})
		m.status = fmt.Sprintf("renamed panel to %q", name)
	default:
		m.status = "nothing to rename"
	}
	m.renameID, m.renameGroup = "", ""
	return m
}

// startDispatch opens the task-input overlay for an agent panel, seeded with its
// current brief so the action both assigns and re-assigns. A non-agent target is
// refused with a hint (the server is authoritative, but the cockpit steers).
func (m model) startDispatch(p panel.Panel) model {
	if !p.IsAgent() {
		m.status = "dispatch: select an agent panel"
		return m
	}
	m.input = inputDispatch
	m.inputBuf = p.Task // re-assign edits the existing brief; first dispatch starts empty
	m.dispatchID, m.dispatchGroup = p.ID, ""
	m.status = "dispatch task · enter to send"
	return m
}

// startDispatchGroup opens the task overlay for a whole work item: the brief is
// fanned to every member on commit (the cockpit path to racing N agents).
func (m model) startDispatchGroup(group string) model {
	m.input = inputDispatch
	m.inputBuf = ""
	m.dispatchID, m.dispatchGroup = "", group
	m.status = "dispatch to group · enter to send to every member"
	return m
}

// commitDispatch sends the typed brief to the remembered target — one agent panel
// or every member of a group. An empty brief is refused with the overlay left
// closed; dispatch assigns a task, it does not clear one.
func (m model) commitDispatch(prompt string) model {
	if prompt == "" {
		m.status = "a task cannot be empty"
		m.dispatchID, m.dispatchGroup = "", ""
		return m
	}
	switch {
	case m.dispatchGroup != "":
		m.sendf(proto.Command{Action: "panel.dispatch-group", Group: m.dispatchGroup, Prompt: prompt})
		m.status = fmt.Sprintf("dispatched to group %q · %s", m.dispatchGroup, truncate(prompt, 32))
	case m.dispatchID != "":
		m.sendf(proto.Command{Action: "panel.dispatch", ID: m.dispatchID, Prompt: prompt})
		m.status = "dispatched · " + truncate(prompt, 40)
	default:
		m.status = "nothing to dispatch"
	}
	m.dispatchID, m.dispatchGroup = "", ""
	return m
}

// startEnqueue opens the task overlay for the backlog: the brief is not sent to a
// panel but queued, and the server's scheduler drains it onto a free idle agent —
// in group, if named, otherwise any. It is the cockpit counterpart to the dispatch
// overlay, for handing work off without picking an agent.
func (m model) startEnqueue(group string) model {
	m.input = inputEnqueue
	m.inputBuf = ""
	m.enqueueGroup = group
	if group != "" {
		m.status = fmt.Sprintf("enqueue to %q · enter to queue for a free member", group)
	} else {
		m.status = "enqueue · enter to queue for any free agent"
	}
	return m
}

// commitEnqueue queues the typed brief onto the backlog, restricted to the
// remembered work item when one was selected. An empty brief is refused with the
// overlay closed — enqueue adds work, it does not queue a blank.
func (m model) commitEnqueue(prompt string) model {
	group := m.enqueueGroup
	m.enqueueGroup = ""
	if prompt == "" {
		m.status = "a task cannot be empty"
		return m
	}
	m.sendf(proto.Command{Action: "task.enqueue", Prompt: prompt, Group: group})
	if group != "" {
		m.status = fmt.Sprintf("enqueued to %q · %s", group, truncate(prompt, 32))
	} else {
		m.status = "enqueued · " + truncate(prompt, 40)
	}
	return m
}

// zoomGroup opens the group's split view: the member tiles you navigate as a
// unit before dropping into any one panel. Pins persist across views, so the
// split reopens with the panels you pinned already promoted to live tiles — and
// when exactly one member is pinned it is treated as the group's default and the
// split is skipped for that panel's own zoom.
func (m model) zoomGroup(it dashItem) model {
	m.mode = modeGroupZoom
	m.groupName = it.name
	m.groupFocus = 0
	m.groupArmed = false
	m.scrollOff = 0 // open at the live bottom
	m.scrolling = false
	m.summaryScope = false // always open on the group itself, never a stale sub-view
	// Pins are a per-scope concept, so build the set from this level's direct panels
	// (fleetGroup now that groupName is set), not the whole subtree — a pinned panel
	// nested in a sub-group belongs to that sub-group's split, not this one.
	direct := m.fleetGroup()
	m.groupPinned = pinsForMembers(direct)
	if only, ok := singlePinned(direct, m.groupPinned); ok {
		m = m.zoomInto(only)
		m.zoomGroupOrigin = it.name // back (C-t b) pops back to the split
		m.status = fmt.Sprintf("group · %s · %s (pinned)", it.name, only.Title)
		return m
	}
	m.attachGroupMembers()
	m.status = fmt.Sprintf("group · %s (%d panels)", groupBreadcrumb(it.name), len(direct))
	return m
}

// pinsForMembers builds a view's pin set from the members' server-owned Pinned
// flags. The set is keyed by id and confined to the given members, so a stale id
// from a closed panel never haunts a tile.
func pinsForMembers(members []panel.Panel) map[string]bool {
	pins := map[string]bool{}
	for _, p := range members {
		if p.Pinned {
			pins[p.ID] = true
		}
	}
	if len(pins) == 0 {
		return nil
	}
	return pins
}

// shownForGroups builds the per-group visible-tile count map from a snapshot's
// GroupView entries, keyed by group name. Only groups the server reports a count
// for appear; groups absent from the map fall back to the default N in
// groupShownN, so a fresh group or one the server has not annotated still works.
func shownForGroups(groups []proto.GroupView) map[string]int {
	if len(groups) == 0 {
		return nil
	}
	out := make(map[string]int, len(groups))
	for _, g := range groups {
		if g.Shown > 0 {
			out[g.Group] = g.Shown
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// layoutForGroups builds the per-group layout-name map from a snapshot's GroupView
// entries, keyed by group name. Only groups the server reports a layout for appear;
// a group absent from the map falls back to the default layout in groupLayoutName,
// so a fresh or un-annotated group still opens as a plain tiled split.
func layoutForGroups(groups []proto.GroupView) map[string]string {
	if len(groups) == 0 {
		return nil
	}
	out := make(map[string]string, len(groups))
	for _, g := range groups {
		if g.Layout != "" {
			out[g.Group] = g.Layout
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// favForGroups builds the set of favourited groups from a snapshot's GroupView
// entries, keyed by group name. Only groups the server reports as a favourite
// appear; a group absent from the map is not a favourite. The dashboard sorts
// these cards to the front.
func favForGroups(groups []proto.GroupView) map[string]bool {
	if len(groups) == 0 {
		return nil
	}
	out := make(map[string]bool, len(groups))
	for _, g := range groups {
		if g.Favourite {
			out[g.Group] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// singlePinned returns the lone pinned member when exactly one of the group's
// members is pinned, so entering can drop straight into it.
func singlePinned(members []panel.Panel, pins map[string]bool) (panel.Panel, bool) {
	var only panel.Panel
	n := 0
	for _, p := range members {
		if pins[p.ID] {
			only, n = p, n+1
		}
	}
	return only, n == 1
}

// foldGlyph is a fold row's disclosure marker: ▸ closed, ▾ open. The same two
// glyphs a group row uses, so "there is more under here" reads the same wherever
// it appears without a legend.
func (m model) foldGlyph(parent string) string {
	if m.foldOpen[parent] {
		return "▾"
	}
	return "▸"
}

// foldVerb is what enter does next on the fold row.
func (m model) foldVerb(parent string) string {
	if m.foldOpen[parent] {
		return "fold"
	}
	return "expand"
}

// renderFoldPreview is the tree pane's right side for the quiet row. It says what
// was folded and why, and then stops.
//
// It deliberately does NOT list the panels. A roster here would be a second, worse
// copy of the thing enter already gives you — the real rows, with the real verbs on
// them — and the whole point of the fold is that these panels are not asking for
// anything, so a list of their names is the least useful thing the pane could
// spend its height on.
func (m model) renderFoldPreview(it dashItem, width int) string {
	title := lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).
		Render(truncate(m.foldGlyph(it.parent)+" "+it.title(), width))
	rule := mutedStyle.Render(strings.Repeat("─", width))
	body := []string{
		mutedStyle.Render(fmt.Sprintf("%d panel(s) folded away: idle, or exited cleanly.", it.quiet)),
		"",
		mutedStyle.Render("Nothing here is asking for anything. Favourites, pins,"),
		mutedStyle.Render("marked panels and the card under the cursor are never"),
		mutedStyle.Render("folded, so the fold can never hide what you are on."),
		"",
		legend("enter", m.foldVerb(it.parent)),
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		title, rule, "", lipgloss.JoinVertical(lipgloss.Left, body...))
}

// renderGroupPreview is the tree pane's right side for a selected group: the
// work-item name, a member tally, and a roster of its panels with each one's
// state, so the group reads as a unit before you zoom in.
func (m model) renderGroupPreview(it dashItem, width int) string {
	title := lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).Render(truncate(it.title(), width))
	statusLine := groupBadge() + "  " +
		mutedStyle.Render(fmt.Sprintf("%d panel(s)", len(it.members))) + "  " + kindBreakdown(it.members)
	if chip := needChip(it.need); chip != "" {
		statusLine += "  " + chip + mutedStyle.Render(" need you")
	}
	if n := subGroupCount(it.members, it.name); n > 0 {
		statusLine += lipgloss.NewStyle().Foreground(colBrand).Render(fmt.Sprintf("  ▣ %d sub-group(s)", n))
	}
	rule := mutedStyle.Render(strings.Repeat("─", width))

	roster := make([]string, 0, len(it.members)+1)
	roster = append(roster, mutedStyle.Render(spaced("PANELS")))
	for _, p := range it.members {
		info := stateInfoFor(p)
		led := lipgloss.NewStyle().Foreground(info.color).Render(info.led)
		name := lipgloss.NewStyle().Foreground(colInk).Render(truncate(p.Title, width-4))
		roster = append(roster, led+" "+name)
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		title, statusLine, rule, "", lipgloss.JoinVertical(lipgloss.Left, roster...),
	)
}

// needChip renders a group's need count — "◆2" in attention's red — or the empty
// string when nothing in the group is waiting on a human. One helper for the card
// head, the tree row, and the preview, so the three cannot drift into three
// different glyphs for one number.
//
// It borrows attention's glyph rather than inventing one, because the count is the
// sum of four different reasons (asking, wedged, failed, finished) and a group card
// has no room to break them out. ◆ is what the fleet strip already uses for "this
// wants you", which is the only claim the number is making.
func needChip(n int) string {
	if n <= 0 {
		return ""
	}
	return lipgloss.NewStyle().Foreground(states[panel.Attention].color).Bold(true).Render(fmt.Sprintf("◆%d", n))
}

// groupBadge tags a card as a work item, mirroring kindBadge's look.
func groupBadge() string {
	return lipgloss.NewStyle().Foreground(colDark).Background(colBrand).Bold(true).Padding(0, 1).Render("GROUP")
}

// kindBreakdown summarises panels by kind — "2 agent · 1 shell" — in the kind
// colours, showing only the kinds present. A single em dash when there are none.
func kindBreakdown(panels []panel.Panel) string {
	agents, shells := kindCounts(panels)
	parts := make([]string, 0, 2)
	if agents > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colAgent).Render(fmt.Sprintf("%d agent", agents)))
	}
	if shells > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colShell).Render(fmt.Sprintf("%d shell", shells)))
	}
	if len(parts) == 0 {
		return mutedStyle.Render("—")
	}
	return strings.Join(parts, mutedStyle.Render(" · "))
}

// fleetBreakdown summarises the whole dashboard: the panels by kind plus how many
// items are work-item groups, so "5 agent · 3 shell · 2 group" reads the makeup at
// a glance. Empty for an empty fleet; the group count is dropped when there are
// none.
func fleetBreakdown(fleet []panel.Panel, items []dashItem) string {
	if len(fleet) == 0 {
		return ""
	}
	parts := []string{kindBreakdown(fleet)}
	groups := 0
	for _, it := range items {
		if it.kind == itemGroup {
			groups++
		}
	}
	if groups > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(colBrand).Render(fmt.Sprintf("%d group", groups)))
	}
	return strings.Join(parts, mutedStyle.Render(" · "))
}

// groupCountChips renders a compact per-state tally for a group's members, e.g.
// "◆1 ●2 ○1", each chip in its state colour. Only non-zero states show.
func groupCountChips(members []panel.Panel) string {
	counts := stateCounts(members)
	chips := make([]string, 0, len(stateOrder))
	for _, st := range stateOrder {
		n := counts[st]
		if n == 0 {
			continue
		}
		info := states[st]
		chips = append(chips, lipgloss.NewStyle().Foreground(info.color).Render(fmt.Sprintf("%s%d", info.led, n)))
	}
	if len(chips) == 0 {
		return mutedStyle.Render("—")
	}
	return strings.Join(chips, " ")
}
