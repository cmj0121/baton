package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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
// panels in place, and each group collapsed into one item at the position of its
// first member. Order is stable and follows the fleet, so the cursor never jumps
// when a snapshot arrives with the same shape.
func (m model) dashItems() []dashItem {
	items := make([]dashItem, 0, len(m.fleet))
	groupAt := make(map[string]int) // group name -> index into items
	for _, p := range m.fleet {
		if p.Group == "" {
			items = append(items, dashItem{kind: itemPanel, panel: p})
			continue
		}
		if idx, ok := groupAt[p.Group]; ok {
			items[idx].members = append(items[idx].members, p)
			continue
		}
		groupAt[p.Group] = len(items)
		items = append(items, dashItem{kind: itemGroup, name: p.Group, members: []panel.Panel{p}})
	}
	return items
}

// title is the label shown for an item on the dashboard.
func (it dashItem) title() string {
	if it.kind == itemGroup {
		return it.name
	}
	return it.panel.Title
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
	ids := m.markedIDs()
	if len(ids) == 0 {
		m.status = "no panels selected"
		return m
	}
	if m.nameConflict(name, "", name) {
		m.status = fmt.Sprintf("the name %q is already taken — pick another", name)
		return m
	}
	m.sendf(proto.Command{Action: "panel.group", IDs: ids, Group: name})
	m.marked = nil
	m.status = fmt.Sprintf("grouped %d panel(s) into %q", len(ids), name)
	return m
}

// addMarkedToGroup files the marked panels into the selected work item and
// clears the selection. The cursor must be on a group card.
func (m model) addMarkedToGroup() model {
	it, ok := m.selectedItem()
	if !ok || it.kind != itemGroup {
		m.status = "select a group to add to"
		return m
	}
	ids := m.markedIDs()
	if len(ids) == 0 {
		m.status = "mark panels first, then add to a group"
		return m
	}
	m.sendf(proto.Command{Action: "panel.group", IDs: ids, Group: it.name})
	m.marked = nil
	m.status = fmt.Sprintf("added %d panel(s) to %q", len(ids), it.name)
	return m
}

// enterGroupView opens the selected group's split (the C-t g escape from the
// dashboard).
func (m model) enterGroupView() (tea.Model, tea.Cmd) {
	if it, ok := m.selectedItem(); ok && it.kind == itemGroup {
		return m.zoomGroup(it), nil
	}
	m.status = "select a group to view"
	return m, nil
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

// zoomGroup opens the group's split view: the member tiles you navigate as a
// unit before dropping into any one panel.
func (m model) zoomGroup(it dashItem) model {
	m.mode = modeGroupZoom
	m.groupName = it.name
	m.groupFocus = 0
	m.groupArmed = false
	m.groupCols = 0     // auto-fit until the user dials columns in
	m.groupPinned = nil // pins are per-view; start from the default tile fill
	m.attachGroupMembers()
	m.status = fmt.Sprintf("group · %s (%d panels)", it.name, len(m.groupMembers()))
	return m
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
	glyph := lipgloss.NewStyle().Foreground(info.color).Bold(true).Render("▣")
	name := lipgloss.NewStyle().Foreground(titleColor).Bold(true).Render(truncate(it.title(), cardInner-4))
	head := mark + glyph + " " + name

	badge := groupBadge()
	// Split the member count by kind, so a card says what kind of work it holds —
	// "2 agent · 1 shell" — not just how many panels.
	kindLine := badge + "  " + kindBreakdown(it.members)

	// The footer is the per-state chips, led by a sparkline in the group's rolled-up
	// colour while it is active — so a working group animates like a panel card.
	footer := groupCountChips(it.members)
	if activeState(st) {
		spark := lipgloss.NewStyle().Foreground(info.color).Render(sparkFor(st))
		footer = spark + "  " + footer
	}

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
