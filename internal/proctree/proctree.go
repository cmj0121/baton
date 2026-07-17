// Package proctree builds the daemon's process tree — the baton daemon at the
// root, the fleet's nested work-item groups as scaffolding, each panel filed under
// its group with its process-group-leader pid, and every panel's live OS
// descendant processes hanging off it. It joins what baton knows (the fleet
// snapshot) to what the OS knows (the process table), so both the `baton ctl tree`
// CLI and the cockpit's process-tree overlay render from one implementation.
package proctree

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/proto"
)

// Kind tags a node's role: it drives how the line renders and is the JSON
// discriminator `baton ctl tree --json` consumers switch on. A small closed set,
// kept as a string to match the wire-enum convention the rest of baton uses (e.g.
// proto.Panel.State) — named consts here keep the producers and the render switch
// from drifting on a typo.
type Kind string

// The node kinds, in root-to-leaf order.
const (
	KindDaemon Kind = "daemon" // the daemon at the root: carries a pid + comm
	KindGroup  Kind = "group"  // a work-item scaffold node: a bare label, no pid
	KindPanel  Kind = "panel"  // a panel's group-leader line: a pid + comm
	KindProc   Kind = "proc"   // a raw OS descendant process: just a pid + comm
)

// Node is one line in the rendered process tree with its children.
type Node struct {
	Kind     Kind       `json:"kind"`
	Label    string     `json:"label,omitempty"` // empty for proc nodes, which render from Comm
	Pid      int        `json:"pid,omitempty"`
	Comm     string     `json:"comm,omitempty"`
	CPU      float64    `json:"cpu,omitempty"` // cumulative CPU% since the process started
	RSS      uint64     `json:"rss,omitempty"` // resident set size in bytes
	Panel    *PanelInfo `json:"panel,omitempty"`
	Children []*Node    `json:"children,omitempty"`
}

// Stat is a per-pid resource sample joined onto the tree: CPU is the process's
// cumulative CPU% since it started (a direct read, not an interval sample), and RSS
// is its resident set size in bytes. A pid absent from the stats map — an exited
// panel, or a process gone between the ppid and stat reads — carries a zero Stat and
// renders without the resource columns.
type Stat struct {
	CPU float64
	RSS uint64
}

// PanelInfo is the baton-side identity attached to a panel node, surfaced in JSON.
type PanelInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group,omitempty"`
	State string `json:"state,omitempty"`
}

// Build assembles the process tree from the fleet snapshot and an OS process table
// (children adjacency by ppid, and comm by pid). It is pure — the impure sampling
// lives in OSProcessTable/DaemonPid — so the layout is unit-testable with a
// synthetic table. Traversal roots at each panel's pid, never at dpid, so a
// zero/unknown daemon pid only blanks the root label.
func Build(dpid int, panels []proto.Panel, children map[int][]int, comm map[int]string, stats map[int]Stat) *Node {
	root := &Node{Kind: KindDaemon, Label: "baton (daemon)", Pid: dpid, Comm: comm[dpid], CPU: stats[dpid].CPU, RSS: stats[dpid].RSS}

	// Panels bucketed by their exact group path (""=ungrouped), and the set of
	// every group path plus its ancestors, so the scaffold has intermediate nodes
	// even for a group that only holds nested subgroups.
	byGroup := map[string][]proto.Panel{}
	groups := map[string]bool{}
	for _, p := range panels {
		byGroup[p.Group] = append(byGroup[p.Group], p)
		for g := p.Group; g != ""; g = panel.GroupParent(g) {
			groups[g] = true
		}
	}

	// Recurse the group subtree top-down: each group gets its nested subgroups
	// first, then its own panels, mirroring the dashboard's fold.
	var addGroups func(parent string, into *Node)
	addGroups = func(parent string, into *Node) {
		for _, seg := range childSegments(parent, groups) {
			gpath := panel.GroupJoin(parent, seg)
			g := &Node{Kind: KindGroup, Label: "[group: " + panel.GroupLeaf(gpath) + "]"}
			into.Children = append(into.Children, g)
			addGroups(gpath, g)
			for _, p := range sortedPanels(byGroup[gpath]) {
				g.Children = append(g.Children, panelNode(p, children, comm, stats))
			}
		}
	}
	addGroups("", root)

	// Ungrouped panels land under a synthetic bucket, after the real groups, so a
	// loose shell never disappears from the tree.
	if ung := byGroup[""]; len(ung) > 0 {
		u := &Node{Kind: KindGroup, Label: "[ungrouped]"}
		root.Children = append(root.Children, u)
		for _, p := range sortedPanels(ung) {
			u.Children = append(u.Children, panelNode(p, children, comm, stats))
		}
	}
	return root
}

// panelNode builds a panel's node — the group-leader line — and hangs its live OS
// descendant processes beneath it. A panel with pid 0 (exited) has no descendants.
func panelNode(p proto.Panel, children map[int][]int, comm map[int]string, stats map[int]Stat) *Node {
	label := "[" + p.Title
	if p.State != "" {
		label += "/" + p.State
	}
	label += "]"
	n := &Node{
		Kind:  KindPanel,
		Label: label,
		Pid:   p.Pid,
		Comm:  comm[p.Pid],
		CPU:   stats[p.Pid].CPU,
		RSS:   stats[p.Pid].RSS,
		Panel: &PanelInfo{ID: p.ID, Name: p.Title, Group: p.Group, State: p.State},
	}
	if p.Pid > 0 {
		attachDescendants(n, p.Pid, children, comm, stats, map[int]bool{p.Pid: true})
	}
	return n
}

// attachDescendants walks the OS process subtree rooted at pid, appending a proc
// node per descendant. seen guards against a pid-reuse cycle so the walk always
// terminates.
func attachDescendants(node *Node, pid int, children map[int][]int, comm map[int]string, stats map[int]Stat, seen map[int]bool) {
	for _, kid := range children[pid] {
		if seen[kid] {
			continue
		}
		seen[kid] = true
		k := &Node{Kind: KindProc, Pid: kid, Comm: comm[kid], CPU: stats[kid].CPU, RSS: stats[kid].RSS}
		node.Children = append(node.Children, k)
		attachDescendants(k, kid, children, comm, stats, seen)
	}
}

// childSegments returns the immediate child group segments of parent among the
// known group paths, sorted for a deterministic frame.
func childSegments(parent string, groups map[string]bool) []string {
	seen := map[string]bool{}
	for g := range groups {
		if seg, ok := panel.GroupChildSegment(g, parent); ok {
			seen[seg] = true
		}
	}
	segs := make([]string, 0, len(seen))
	for s := range seen {
		segs = append(segs, s)
	}
	sort.Strings(segs)
	return segs
}

// sortedPanels orders a group's panels by id, so the tree is stable across calls
// regardless of the snapshot's arrival order.
func sortedPanels(ps []proto.Panel) []proto.Panel {
	out := append([]proto.Panel(nil), ps...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Row is one rendered line: its box-drawing prefix (empty for the root) and the
// node it belongs to. Rows lets a caller reuse the tree walk while formatting each
// node's text itself — the cockpit overlay does this to colour a CPU bar into the
// line, where the plaintext Render cannot.
type Row struct {
	Prefix string
	Node   *Node
}

// Rows flattens the tree to a depth-first slice of rendered rows, computing the
// ├─/└─ connectors as it descends. The root carries an empty prefix.
func Rows(root *Node) []Row {
	rows := []Row{{Prefix: "", Node: root}}
	return appendRows(rows, root.Children, "")
}

func appendRows(rows []Row, nodes []*Node, prefix string) []Row {
	for i, n := range nodes {
		last := i == len(nodes)-1
		branch, childPrefix := "├─ ", prefix+"│  "
		if last {
			branch, childPrefix = "└─ ", prefix+"   "
		}
		rows = append(rows, Row{Prefix: prefix + branch, Node: n})
		rows = appendRows(rows, n.Children, childPrefix)
	}
	return rows
}

// Render draws the node tree with box-drawing connectors: the root on its own line
// and every descendant under an ├─/└─ branch. The trailing newline makes it
// drop-in for fmt.Print.
func Render(root *Node) string {
	rows := Rows(root)
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = r.Prefix + lineLabel(r.Node)
	}
	return strings.Join(lines, "\n") + "\n"
}

// lineLabel is a node's full plaintext line: its label plus its resource columns.
func lineLabel(n *Node) string {
	return LabelText(n) + ResourceText(n)
}

// LabelText formats a node's identity without its resource columns: groups print
// their bare label, a raw process leads with its pid, and daemon/panel nodes append
// their pid and comm. Callers that render the resource columns their own way (the
// cockpit's coloured CPU bar) pair this with their own suffix.
func LabelText(n *Node) string {
	switch n.Kind {
	case KindGroup:
		return n.Label
	case KindProc:
		s := fmt.Sprintf("pid=%d", n.Pid)
		if n.Comm != "" {
			s += "  " + n.Comm
		}
		return s
	}
	s := n.Label
	if n.Pid > 0 {
		s += fmt.Sprintf(" pid=%d", n.Pid)
	}
	if n.Comm != "" {
		s += "  " + n.Comm
	}
	return s
}

// ResourceText renders the CPU%/memory columns, gated on a live sample: a node with
// no stats entry (an exited panel, a group, or a process gone between samples) has
// RSS 0 and trails nothing, so the tree never shows a bogus "0.0%  0B".
func ResourceText(n *Node) string {
	if n.RSS == 0 {
		return ""
	}
	return fmt.Sprintf("  %.1f%%  %s", n.CPU, humanBytes(n.RSS))
}

// humanBytes renders a byte count in the largest unit that keeps it >= 1, to one
// decimal place (e.g. 1536 -> "1.5K", 47448064 -> "45.2M").
func humanBytes(n uint64) string {
	units := []string{"B", "K", "M", "G", "T", "P"}
	div, exp := uint64(1), 0
	for n/div >= 1024 && exp < len(units)-1 {
		div *= 1024
		exp++
	}
	return fmt.Sprintf("%.1f%s", float64(n)/float64(div), units[exp])
}

// OSProcessTable samples the whole OS process table into a children-by-ppid
// adjacency map, a comm-by-pid map, and a stats-by-pid map. A process whose ppid
// cannot be read is skipped (it cannot be placed); a missing comm just leaves the
// name blank, and a CPU%/RSS read that fails just leaves that pid without a stat —
// the process still appears, only without its resource columns.
func OSProcessTable() (children map[int][]int, comm map[int]string, stats map[int]Stat, err error) {
	procs, err := process.Processes()
	if err != nil {
		return nil, nil, nil, err
	}
	children = map[int][]int{}
	comm = map[int]string{}
	stats = map[int]Stat{}
	for _, p := range procs {
		pid := int(p.Pid)
		ppid, e := p.Ppid()
		if e != nil {
			continue
		}
		children[int(ppid)] = append(children[int(ppid)], pid)
		if name, e := p.Name(); e == nil {
			comm[pid] = name
		}
		var st Stat
		if cpu, e := p.CPUPercent(); e == nil {
			st.CPU = cpu
		}
		if mi, e := p.MemoryInfo(); e == nil && mi != nil {
			st.RSS = mi.RSS
		}
		if st.RSS > 0 || st.CPU > 0 {
			stats[pid] = st
		}
	}
	for _, kids := range children {
		sort.Ints(kids)
	}
	return children, comm, stats, nil
}

// DaemonPid reads this session's daemon pid from its pid file, returning 0 when the
// file is missing or malformed — the tree still renders, only the root label loses
// its pid.
func DaemonPid() int {
	data, err := os.ReadFile(paths.PidFile(paths.Socket()))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
