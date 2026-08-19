package tui

import (
	"slices"
	"strings"

	"github.com/cmj0121/baton/internal/panel"
)

// This file projects the flat fleet into the dashboard's tree.
//
// The fleet is a list; a work item is a path ("backend/api/tests"); the dashboard
// is the one place a person sees the whole shape at once. Until this existed the
// dashboard threw the shape away — every panel under "backend" collapsed into one
// row whatever its depth, so nesting was a thing you could create and then never
// see. The hierarchy was only walked by descending into a group's split, which is
// a different view and a mandatory hop.
//
// The projection is two passes. buildTree turns the fleet into nodes, keeping
// children in FLEET ORDER so a group occupies the slot of its first member —
// the property the flat projection had, and the reason the cursor does not jump
// when a snapshot arrives with the same shape. flatten then walks that tree into
// the cursor's list, applying the favourite float and the quiet fold once per
// level rather than once for the whole dashboard.

// groupNode is one work item while the tree is being assembled: its own children
// in fleet order, and every panel beneath it.
type groupNode struct {
	path string // the full group path — the identity, and what every server op takes
	name string // the last segment — what a row displays at depth

	// children are this node's direct contents in fleet order: sub-groups and the
	// panels filed directly here, interleaved exactly as the fleet has them, so a
	// group appears where its first member does.
	children []treeChild

	// subtree is EVERY panel beneath this node, direct or nested. It is what a
	// group row reports as its members, open or closed, which is the invariant the
	// whole feature rests on: expansion is a view state and never a selection one,
	// so a bulk verb on a group means its whole subtree either way.
	subtree []panel.Panel

	index map[string]*groupNode // segment -> child node, for the build walk
}

// treeChild is one slot in a node's ordered contents: a panel filed directly
// here, or a sub-group. Exactly one of the two is set.
type treeChild struct {
	panel *panel.Panel
	group *groupNode
}

// newGroupNode returns an empty node at a path.
func newGroupNode(path string) *groupNode {
	return &groupNode{path: path, name: panel.GroupLeaf(path), index: map[string]*groupNode{}}
}

// child returns the sub-group for one path segment, creating and appending it in
// first-appearance order when it is new.
func (n *groupNode) child(seg string) *groupNode {
	if kid, ok := n.index[seg]; ok {
		return kid
	}
	kid := newGroupNode(panel.GroupJoin(n.path, seg))
	n.index[seg] = kid
	n.children = append(n.children, treeChild{group: kid})
	return kid
}

// buildTree assembles the fleet into a root node whose children are the top-level
// loose panels and work items, in fleet order.
//
// The two singletons are skipped: the conductor and the global shell are marks in
// the FLEET heading, not rows, exactly as they were before the tree existed.
func buildTree(fleet []panel.Panel) *groupNode {
	root := newGroupNode("")
	for i := range fleet {
		p := fleet[i]
		if p.Conductor || p.GlobalShell {
			continue
		}
		if p.Group == "" {
			root.children = append(root.children, treeChild{panel: &p})
			continue
		}
		at := root
		for _, seg := range strings.Split(p.Group, panel.GroupSep) {
			if seg == "" {
				continue // a malformed path ("a//b") must not mint a nameless node
			}
			at = at.child(seg)
			at.subtree = append(at.subtree, p) // every ancestor carries the panel
		}
		at.children = append(at.children, treeChild{panel: &p})
	}
	return root
}

// dashTree projects the fleet into the dashboard's row list: the flattened tree,
// with the filter applied, favourites floated and the quiet panels folded at each
// level. It is the single projection the cursor, every verb and both renderers
// read, so none of them can disagree about what row N is.
func (m model) dashTree() []dashItem {
	root := buildTree(m.lensFleet())
	need := m.needByGroup()
	return m.flatten(root, 0, need, nil)
}

// The filter narrows which ROWS are drawn. It deliberately does not narrow what a
// group row CONTAINS: the tree is built from the whole fleet, so a group's members
// — and therefore `ids()`, and therefore every bulk verb — mean the same thing
// filtered or not.
//
// Building the tree from a pre-filtered fleet was the obvious implementation and
// it is wrong in a way that would not have been noticed for a while: `w` on a work
// item while a filter was applied would have closed the panels you could SEE
// rather than the panels it holds, so the same keystroke on the same row would
// destroy a different amount depending on what you had typed into the search box.

// rowMatches reports whether a panel row survives the filter.
func (m model) rowMatches(p panel.Panel) bool {
	if m.filter == "" {
		return true
	}
	lf := strings.ToLower(m.filter)
	return strings.Contains(strings.ToLower(p.Title), lf) ||
		(p.Group != "" && strings.Contains(strings.ToLower(p.Group), lf))
}

// groupMatches reports whether a group row survives the filter: its own path
// matched, or something in its subtree did.
func (m model) groupMatches(n *groupNode) bool {
	if m.filter == "" {
		return true
	}
	if strings.Contains(strings.ToLower(n.path), strings.ToLower(m.filter)) {
		return true
	}
	for _, p := range n.subtree {
		if m.rowMatches(p) {
			return true
		}
	}
	return false
}

// flatten walks a node's children into rows at the given depth, appending to out.
//
// The order of operations is the same at every level and matters: build the level's
// items in fleet order, float the favourites, fold the quiet ones, mark the last
// row so the branch glyphs close, and only then recurse into whatever is expanded.
// Folding before recursing is what keeps a fold row's count honest — it counts the
// rows at ITS level, not the panels somewhere below them.
func (m model) flatten(n *groupNode, depth int, need map[string]int, out []dashItem) []dashItem {
	level := make([]dashItem, 0, len(n.children))
	for _, c := range n.children {
		if c.panel != nil {
			if !m.rowMatches(*c.panel) {
				continue
			}
			level = append(level, dashItem{kind: itemPanel, panel: *c.panel, depth: depth, parent: n.path})
			continue
		}
		if !m.groupMatches(c.group) {
			continue
		}
		level = append(level, dashItem{
			kind:     itemGroup,
			name:     c.group.path, // the FULL path: identity, and what every server op takes
			members:  c.group.subtree,
			need:     need[c.group.path],
			depth:    depth,
			parent:   n.path,
			expanded: m.groupExpanded(c.group.path),
			node:     c.group,
		})
	}
	level = m.floatFavourites(level)
	level = m.foldQuietLevel(level, n.path)

	for i := range level {
		level[i].last = i == len(level)-1
		out = append(out, level[i])
		if level[i].kind == itemGroup && level[i].expanded && level[i].node != nil {
			out = m.flatten(level[i].node, depth+1, need, out)
		}
	}
	return out
}

// floatFavourites lifts the favourited rows of one level to the front of that
// level, keeping fleet order within each partition. It is a stable sort for the
// reason the flat projection's was: the dashboard may promote what you marked as
// mattering, but it must never otherwise rearrange itself under your cursor.
func (m model) floatFavourites(level []dashItem) []dashItem {
	slices.SortStableFunc(level, func(a, b dashItem) int {
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
	return level
}

// groupExpanded reports whether a group's row is open. Groups are expanded by
// DEFAULT, and the map records the ones a person has explicitly closed.
//
// That default is the one that preserves what the dashboard was: a card grid
// showed every loose panel, so a tree that opened closed would show a person
// fewer panels than they had a moment ago. Crowding is the quiet fold's job, not
// the expansion default's — collapsing is for saying "I am done with this work
// item for now", which is a decision, not a starting position.
func (m model) groupExpanded(path string) bool { return !m.collapsed[path] }

// --- walking the tree ---------------------------------------------------------

// expandSelected is what → does: open what the cursor is on, or step into it.
//
// The three cases are the three kinds of row. A quiet fold opens — it was the only
// row with something inside it before the tree existed, and → has opened it since.
// A shut work item opens. An OPEN work item has nothing left to open, so → descends
// to its first child instead, which is how a tree is walked everywhere else.
//
// On a panel it does nothing rather than moving the cursor. A panel is a leaf, and
// a key that silently walks off it would make → mean "down" on most of the rows in
// a fleet.
func (m model) expandSelected() model {
	it, ok := m.selectedItem()
	if !ok {
		return m
	}
	switch {
	case it.kind == itemFold:
		return m.toggleFold()
	case it.kind == itemGroup && !it.expanded:
		return m.setCollapsed(it, false)
	case it.kind == itemGroup:
		m.cursorToFirstChild(it)
		return m
	}
	return m
}

// toggleExpand is what space does: show what is nested under the row, or hide it
// again.
//
// It is the same state ← and → walk, reached by the key every other tree in every
// other program uses for it — and unlike them it does not move the cursor, which
// is what makes it the key for LOOKING. Each level keeps its own state, so a
// sub-group inside an open work item toggles on its own and the nesting goes as
// deep as the fleet does.
//
// The fold row toggles too. It is the other row on the dashboard with something
// inside it, and a key that means "show me what is under here" that stopped at
// the quiet row would be a key with an exception to remember.
//
// A panel is a leaf and says so, rather than silently doing nothing: the whole
// point of a disclosure key is that you can press it to find out.
func (m model) toggleExpand() model {
	it, ok := m.selectedItem()
	if !ok {
		return m
	}
	if it.kind == itemFold {
		return m.toggleFold() // the quiet row opens in either layout: it is one row, and it holds rows
	}
	// Every other row on the cards gets the same answer, panel or work item. The
	// grid draws no nesting at all, so "there is nothing nested under THIS row" is
	// the wrong thing to say on it — the truth is that this layout is not showing
	// nesting, and which key does.
	if m.gridDash() {
		m.status = m.cardsHold()
		return m
	}
	if it.kind == itemGroup {
		return m.setCollapsed(it, it.expanded)
	}
	m.status = "nothing nested here — " + keyLabel(m.bindingKey(actExpand)) + " opens a work item"
	return m
}

// cardsHold is what space answers on the card grid.
//
// The cards draw a work item WHOLE — that is what makes a small fleet readable at
// a glance — so there is nothing on screen to open or shut, and the layout that
// can draw the inside of one is a keystroke away. The key is read from the binding
// rather than spelled here, so a rebind cannot leave the hint pointing at a key
// that does nothing.
//
// Only space needs it. The arrows never reach here on the cards: with no tree to
// walk they move the cursor instead, which is the one thing a horizontal key can
// usefully do in a grid.
func (m model) cardsHold() string {
	return "the cards draw a work item whole — " + keyLabel(m.bindingKey(actDashLayout)) + " shows the tree"
}

// toggleLayout is what V does: ask for the tree on a fleet the cards would draw,
// or take the cards back.
//
// It is the ONE key that changes the layout. The arrows walk the tree and nothing
// else — a person stepping out of the outermost row expects to stay put, not to be
// handed a different dashboard — and space opens and shuts a row and nothing else.
// A view this cheap to flip is better as a key you press on purpose than as the
// far edge of a key you press all day.
func (m model) toggleLayout() model {
	if m.mode != modeDashboard {
		return m
	}
	it, had := m.selectedItem()
	m.showTree = !m.showTree
	if had {
		m.cursorToItem(it) // the row list changes size; land back on the same row
	}
	m.clampCursor()
	switch {
	case !m.gridDash() && !m.showTree:
		// Nothing to give back: this fleet is past the cards on its own.
		m.status = "the fleet is past the cards — the tree draws it either way"
	case m.showTree:
		m.status = "tree · " + keyLabel(m.bindingKey(actDashLayout)) + " for the cards"
	default:
		m.status = "cards"
	}
	return m
}

// collapseSelected is what ← does: shut what the cursor is on, or step out of it.
//
// An open work item shuts. Anything else — a panel, a shut group, a fold row —
// jumps to the row that CONTAINS it, so ← walks back up the tree the way → walked
// down it. That pairing is the whole reason ← is not simply "shut this": from deep
// inside a work item, the useful meaning of "back" is the parent, not nothing.
func (m model) collapseSelected() model {
	it, ok := m.selectedItem()
	if !ok {
		return m
	}
	if it.kind == itemGroup && it.expanded {
		return m.setCollapsed(it, true)
	}
	m.cursorToParent(it)
	return m
}

// setCollapsed opens or shuts one work item and keeps the cursor on it.
//
// The re-anchor matters in the closing direction: shutting a group removes every
// row beneath it, so an index that pointed into those rows now points at something
// else or past the end. cursorToItem lands it back on the group by identity, which
// is where the rows it just swallowed now live.
func (m model) setCollapsed(it dashItem, shut bool) model {
	if m.collapsed == nil {
		m.collapsed = map[string]bool{}
	}
	if shut {
		m.collapsed[it.name] = true
		m.status = "collapsed " + it.name
	} else {
		delete(m.collapsed, it.name)
		m.status = "expanded " + it.name
	}
	m.cursorToItem(it)
	m.clampCursor()
	return m
}

// cursorToFirstChild moves onto the first row nested directly inside a group. A
// group with nothing under it leaves the cursor put, which is the honest answer.
func (m *model) cursorToFirstChild(it dashItem) {
	for i, row := range m.dashItems() {
		if row.parent == it.name && row.depth == it.depth+1 {
			m.cursor = i
			return
		}
	}
}

// cursorToParent moves onto the group row that contains the cursor's row. A
// top-level row has no parent and leaves the cursor put.
func (m *model) cursorToParent(it dashItem) {
	if it.parent == "" {
		return
	}
	for i, row := range m.dashItems() {
		if row.kind == itemGroup && row.name == it.parent {
			m.cursor = i
			return
		}
	}
}
