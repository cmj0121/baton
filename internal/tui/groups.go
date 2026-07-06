package tui

import (
	"fmt"
	"slices"
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
)

// dashItem is one cursor-addressable cell on the dashboard. A group collapses
// all of its member panels into a single card; a lone panel stands on its own.
// The cursor indexes dashItems, not the flat fleet, so everything that acts on
// the selection resolves through here.
type dashItem struct {
	kind    dashKind
	panel   panel.Panel   // itemPanel: the panel itself
	name    string        // itemGroup: the work-item name
	members []panel.Panel // itemGroup: panels filed under name, in fleet order
}

// dashItems projects the flat fleet into the dashboard's cursor model: lone
// panels in place, and each top-level group collapsed into one item at the position
// of its first member. With nested groups the dashboard shows only the top level — a
// group folds its **whole subtree** (every descendant panel) into one card, and the
// hierarchy is walked by descending in the split. Order is stable and follows the
// fleet, so the cursor never jumps when a snapshot arrives with the same shape.
func (m model) dashItems() []dashItem {
	items := make([]dashItem, 0, len(m.fleet))
	groupAt := make(map[string]int) // top-level group name -> index into items
	for _, p := range m.fleet {
		if p.Conductor {
			continue // the conductor is a mark in the FLEET heading, not a card/group
		}
		if p.Group == "" {
			items = append(items, dashItem{kind: itemPanel, panel: p})
			continue
		}
		top := panel.GroupTop(p.Group) // fold the subtree under its top-level group
		if idx, ok := groupAt[top]; ok {
			items[idx].members = append(items[idx].members, p)
			continue
		}
		groupAt[top] = len(items)
		items = append(items, dashItem{kind: itemGroup, name: top, members: []panel.Panel{p}})
	}
	items = filterItems(items, m.filter)
	// Float favourited cards to the front, preserving the fleet order within the
	// favourited and non-favourited partitions (a stable sort), so both the grid
	// and the tree view — which project through here — show favourites first.
	slices.SortStableFunc(items, func(a, b dashItem) int {
		af, bf := m.itemFavourite(a), m.itemFavourite(b)
		switch {
		case af && !bf:
			return -1
		case !af && bf:
			return 1
		default:
			return 0
		}
	})
	return items
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

// filterItems narrows the dashboard to items matching the filter — a
// case-insensitive substring on a panel's title, or a group's name or any
// member's title (so a group surfaces when one of its panels matches). An empty
// filter returns the list untouched.
func filterItems(items []dashItem, filter string) []dashItem {
	if filter == "" {
		return items
	}
	lf := strings.ToLower(filter)
	out := make([]dashItem, 0, len(items))
	for _, it := range items {
		if itemMatches(it, lf) {
			out = append(out, it)
		}
	}
	return out
}

// itemMatches reports whether a dashboard item matches the (already
// lower-cased) filter.
func itemMatches(it dashItem, lf string) bool {
	if it.kind == itemPanel {
		return strings.Contains(strings.ToLower(it.panel.Title), lf)
	}
	if strings.Contains(strings.ToLower(it.name), lf) {
		return true
	}
	for _, p := range it.members {
		if strings.Contains(strings.ToLower(p.Title), lf) {
			return true
		}
	}
	return false
}

// title is the label shown for an item on the dashboard.
func (it dashItem) title() string {
	if it.kind == itemGroup {
		return it.name
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

// ids is the panel ids an item covers: one for a panel, every member for a group.
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
var stateRank = map[panel.State]int{
	panel.Attention: 5,
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

// renderGroupCard draws a work item as one card: a group glyph and name, the
// member count, and a row of per-state count chips so the group's health reads at
// a glance. It mirrors renderCard's three-line shape and selection glow.
func (m model) renderGroupCard(it dashItem, selected bool) string {
	st := groupState(it.members)
	info := states[st]

	border := colFaint
	titleColor := colInk
	if selected {
		border = colBrand
		titleColor = colBrandHi
	}

	mark := ""
	if m.selecting() {
		mark = markCell(m.itemMarked(it))
	}
	// A favourite prefixes a ⊙ before the group glyph, exactly as renderCard marks a
	// favourited panel. The name's width shrinks by the prefix so the head keeps to
	// one row and the card stays the same size as a panel card.
	fav := ""
	if m.itemFavourite(it) {
		fav = lipgloss.NewStyle().Foreground(colBrandHi).Render("⊙") + " "
	}
	glyph := lipgloss.NewStyle().Foreground(info.color).Bold(true).Render("▣")
	// A nested group notes its immediate sub-group count right-aligned in the head —
	// the same place the split's sub-group tile shows it — rather than trailing the
	// kind line, so that line can never spill onto a second row and grow the card one
	// taller than a panel card.
	sub := ""
	if n := subGroupCount(it.members, it.name); n > 0 {
		sub = lipgloss.NewStyle().Foreground(colBrand).Render(fmt.Sprintf("▣%d", n))
	}
	avail := cardInner - lipgloss.Width(mark) - lipgloss.Width(fav) - 2 - lipgloss.Width(sub) // glyph + its trailing space = 2
	if sub != "" {
		avail-- // a gap before the right-aligned count
	}
	name := lipgloss.NewStyle().Foreground(titleColor).Bold(true).Render(truncate(it.title(), max(1, avail)))
	head := mark + fav + glyph + " " + name
	if sub != "" {
		head = padEnds(head, sub, cardInner)
	}
	head = clampWidth(head, cardInner)

	badge := groupBadge()
	// Split the member count by kind, so a card says what kind of work it holds —
	// "2 agent · 1 shell" — not just how many panels. Clamp it to the inner width so a
	// long breakdown truncates rather than wrapping and growing the card.
	kindLine := clampWidth(badge+"  "+kindBreakdown(it.members), cardInner)

	// The footer is the per-state chips, led by a sparkline in the group's rolled-up
	// colour while it is active — so a working group animates like a panel card. It is
	// clamped to the inner width for the same no-wrap, fixed-height reason.
	footer := groupCountChips(it.members)
	if activeState(st) {
		spark := lipgloss.NewStyle().Foreground(info.color).Render(groupSpark(it.members, st))
		footer = spark + "  " + footer
	}
	footer = clampWidth(footer, cardInner)

	style := lipgloss.NewStyle().
		Width(cardWidth-2).
		Padding(0, 1).
		MarginRight(cardGap).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border)

	return style.Render(lipgloss.JoinVertical(lipgloss.Left, head, kindLine, footer))
}

// renderGroupPreview is the tree pane's right side for a selected group: the
// work-item name, a member tally, and a roster of its panels with each one's
// state, so the group reads as a unit before you zoom in.
func (m model) renderGroupPreview(it dashItem, width int) string {
	title := lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).Render(truncate(it.title(), width))
	statusLine := groupBadge() + "  " +
		mutedStyle.Render(fmt.Sprintf("%d panel(s)", len(it.members))) + "  " + kindBreakdown(it.members)
	if n := subGroupCount(it.members, it.name); n > 0 {
		statusLine += lipgloss.NewStyle().Foreground(colBrand).Render(fmt.Sprintf("  ▣ %d sub-group(s)", n))
	}
	rule := mutedStyle.Render(strings.Repeat("─", width))

	roster := make([]string, 0, len(it.members)+1)
	roster = append(roster, mutedStyle.Render(spaced("PANELS")))
	for _, p := range it.members {
		info := states[p.State]
		led := lipgloss.NewStyle().Foreground(info.color).Render(info.led)
		name := lipgloss.NewStyle().Foreground(colInk).Render(truncate(p.Title, width-4))
		roster = append(roster, led+" "+name)
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		title, statusLine, rule, "", lipgloss.JoinVertical(lipgloss.Left, roster...),
	)
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
