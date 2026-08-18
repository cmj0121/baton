package tui

import (
	"strings"

	"github.com/cmj0121/baton/internal/panel"
)

// The group-by lens: look at the same fleet through a different set of parents.
//
// A work item is something you BUILD — you mark panels and name the group, and
// the server remembers it. That is the right shape for work you have decided
// belongs together, and the wrong shape for the moment you have fifty panels and
// have decided nothing yet. The structure is usually already in the data: twelve
// worktrees, three agent profiles, seven panels asking for a human. The lens draws
// that structure without asking anyone to file anything.
//
// It is a VIEW and never a mutation. Switching to `group by: state` does not move
// a single panel into a group called "attention"; it draws the tree with state as
// the parent instead of the work item. Nothing is sent, and switching back leaves
// the fleet exactly as it was — which is also why the reorganising verbs are
// refused under a lens: there is no such thing as moving a panel "into" a
// directory or "out of" a state.

// lens is which parents the dashboard tree is built from.
type lens int

const (
	lensWork    lens = iota // the work items a person built — the real tree
	lensDir                 // the directory each panel is working in
	lensProfile             // the agent profile each panel was spawned from
	lensState               // the lifecycle state each panel is in
)

// lensOrder is the cycle the binding walks, work item first because it is the
// only one that is not a lens at all — it is the fleet's own structure, and the
// place a person must always be able to get back to in one predictable direction.
var lensOrder = []lens{lensWork, lensDir, lensProfile, lensState}

// String is the lens's name on the heading and in the status line.
func (l lens) String() string {
	switch l {
	case lensDir:
		return "directory"
	case lensProfile:
		return "profile"
	case lensState:
		return "state"
	default:
		return "work item"
	}
}

// real reports whether this lens shows the fleet's own structure — the work items
// the server holds — rather than a projection over it. Only the real tree can be
// reorganised: the rest have no parents anybody can move a panel between.
func (l lens) real() bool { return l == lensWork }

// bucket is the parent path a panel gets under this lens.
//
// The bucket doubles as the group path the tree is built from, so the whole
// projection needs nothing but this: buildTree already turns paths into nodes, and
// a lens is exactly "compute a different path".
func (l lens) bucket(p panel.Panel) string {
	switch l {
	case lensDir:
		return p.Cwd // re-based against the fleet's common prefix by lensFleet
	case lensProfile:
		if p.Profile == "" {
			if p.IsAgent() {
				return "(no profile)"
			}
			return "shells"
		}
		return p.Profile
	case lensState:
		return p.State.String()
	default:
		return p.Group
	}
}

// lensFleet re-labels the fleet's groups for the current lens, so everything
// downstream — buildTree, the fold, the cursor, every renderer — works on it
// unchanged.
//
// The re-label is on a COPY. The model's own fleet keeps the real work items,
// because a lens must not be able to leak into what a verb reads: `w` on a row
// resolves through the panel ids, and the moment `m.fleet` said a panel was in
// "attention" something would eventually write that back.
func (m model) lensFleet() []panel.Panel {
	if m.lens.real() {
		return m.fleet
	}
	out := make([]panel.Panel, len(m.fleet))
	copy(out, m.fleet)

	if m.lens == lensDir {
		// The directory lens is the one that NESTS. A path is already a group path,
		// so it needs no re-labelling — but it does need re-basing, because the
		// absolute prefix every panel shares would otherwise be five rows of one
		// child each before the tree said anything.
		//
		// The flagship case is what forces this: twelve git worktrees are twelve
		// sibling directories, and bucketing on the whole path gives twelve buckets
		// holding one panel apiece, which is worse than no structure at all. Re-based
		// and nested, they gather under the directory that actually holds them.
		base := commonDirPrefix(m.fleet)
		for i := range out {
			out[i].Group = dirBucket(out[i].Cwd, base)
		}
		return promoteLoneDirs(out)
	}
	for i := range out {
		out[i].Group = sanitiseBucket(m.lens.bucket(out[i]))
	}
	return out
}

// commonDirPrefix is the deepest directory every panel's working directory sits
// under, or "" when they share nothing. Panels with no known directory are
// ignored rather than collapsing the prefix to nothing.
func commonDirPrefix(fleet []panel.Panel) string {
	var common []string
	seen := false
	for _, p := range fleet {
		if p.Cwd == "" || p.Conductor || p.GlobalShell {
			continue
		}
		segs := strings.Split(strings.Trim(p.Cwd, panel.GroupSep), panel.GroupSep)
		if !seen {
			common, seen = segs, true
			continue
		}
		n := 0
		for n < len(common) && n < len(segs) && common[n] == segs[n] {
			n++
		}
		common = common[:n]
	}
	if len(common) == 0 {
		return ""
	}
	return panel.GroupSep + strings.Join(common, panel.GroupSep)
}

// dirBucket is a panel's directory expressed relative to the fleet's common
// prefix — a group path, so the tree nests it for free. A panel sitting exactly at
// the common prefix belongs to no bucket and stays at the top level.
func dirBucket(cwd, base string) string {
	if cwd == "" {
		return "(unknown directory)"
	}
	rel := strings.TrimPrefix(cwd, base)
	rel = strings.Trim(rel, panel.GroupSep)
	if rel == "" {
		return "" // this IS the common directory: a bucket of everything is no bucket
	}
	return rel
}

// promoteLoneDirs lifts a panel out of a directory bucket that holds nothing but
// it, so the bucket does not cost a row to say what the panel's own directory
// column already says.
//
// Without it a fleet of git worktrees reads as twelve two-row pairs — a `feat-3`
// header and the one panel inside it — which is more rows than having no buckets
// at all, and the lens exists to spend fewer. A bucket with sub-directories under
// it is kept whatever it holds directly: that one is carrying structure, not just
// a name.
//
// One pass, not a fixpoint. A parent that becomes lone because its only child was
// promoted keeps its row, and that is the right answer as often as not — it is now
// the directory those panels are actually in.
func promoteLoneDirs(fleet []panel.Panel) []panel.Panel {
	count := map[string]int{}
	for _, p := range fleet {
		if p.Group != "" {
			count[p.Group]++
		}
	}
	hasChild := map[string]bool{}
	for path := range count {
		for parent := panel.GroupParent(path); parent != ""; parent = panel.GroupParent(parent) {
			hasChild[parent] = true
		}
	}
	for i := range fleet {
		g := fleet[i].Group
		if g != "" && count[g] == 1 && !hasChild[g] {
			fleet[i].Group = panel.GroupParent(g)
		}
	}
	return fleet
}

// sanitiseBucket makes a bucket usable as a single group path segment. A label
// carrying the separator would split into parents that do not exist, so it is
// flattened rather than nested — a lens is one level deep by construction.
func sanitiseBucket(s string) string {
	if s == "" {
		return ""
	}
	out := []rune(s)
	for i, r := range out {
		if string(r) == panel.GroupSep {
			out[i] = '⁄' // a fraction slash: reads as a path, parses as one segment
		}
	}
	return string(out)
}

// cycleLens steps to the next lens and keeps the cursor on the same panel.
//
// Keeping the cursor is the difference between a feature people use and one they
// try once: the buckets are different, so a row number means nothing across the
// switch, and landing somewhere arbitrary in a fifty-row tree is indistinguishable
// from the dashboard having lost your place.
func (m model) cycleLens(delta int) model {
	if m.grabbing() {
		m = m.cancelGrab() // a row in the air has nowhere to land in a different tree
	}
	kind, id, group, had := m.selectedKey()

	at := 0
	for i, l := range lensOrder {
		if l == m.lens {
			at = i
		}
	}
	m.lens = lensOrder[wrapIndex(at, delta, len(lensOrder))]

	// A panel keeps its identity across the switch; a work item does not exist in
	// any other lens, so the cursor falls back to the panel the row held.
	if kind == itemGroup && had {
		if first, ok := m.firstPanelOfGroup(group); ok {
			kind, id = itemPanel, first
		}
	}
	m.restoreCursor(kind, id, "", had && kind == itemPanel)
	m.status = "group by: " + m.lens.String()
	return m
}

// firstPanelOfGroup is the id of the first panel under a work item, so a cursor
// resting on a group row has something to follow into a lens that has no groups.
func (m model) firstPanelOfGroup(group string) (string, bool) {
	for _, p := range m.fleet {
		if panel.GroupIsUnder(group, p.Group) {
			return p.ID, true
		}
	}
	return "", false
}

// lensRefusal is why a reorganising verb does nothing under a lens, or "" when
// the verb is allowed.
//
// The refusal is explicit rather than silent, and it names the lens. A key that
// simply did nothing would read as a broken binding; a key that acted would have
// to invent a meaning for "move this panel into the directory /w/api", which is
// not a thing a person can be asked to have meant.
func (m model) lensRefusal() string {
	if m.lens.real() {
		return ""
	}
	return "group by: " + m.lens.String() + " is a view, not a work item — press " +
		m.bindingKey(actLens) + " to go back to work items first"
}
