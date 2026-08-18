package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/panel"
)

// This file draws one dashboard row.
//
// It replaced a card grid, and the replacement had to be an upgrade rather than a
// trade. A card was 32 columns wide and three lines tall and carried about eighty
// characters — the LED, the title, the kind, the state, the working directory, the
// output sparkline and either the activity line or the dispatched task. A 30-column
// sidebar row carried an LED and a truncated name, so swapping one for the other at
// that width would have been a plain loss for the small fleets that saw the grid.
//
// So the tree takes the WHOLE width and spends it on columns, which a card grid
// could not: at 200 columns the old split used 95 and left 105 empty. One row now
// beats one card on information and costs a third of the height, which is the
// arithmetic that makes fifty panels legible at all.
//
// Columns appear as the terminal earns them, in order of how much they tell you
// about a panel you cannot see: what state it is in, then where it is working,
// then whether it is producing anything, then what it was asked to do. Below the
// first breakpoint the row is what the sidebar was, so a narrow terminal is no
// worse off than before.
//
// Two rules keep the rendering honest. The row is assembled against a HARD budget
// and never exceeds it — lipgloss wraps rather than truncates, so a row one column
// too wide becomes two rows and the tree's shape is gone. And text is fitted while
// it is still plain: truncate counts cell widths without skipping ANSI escapes, so
// cutting an already-styled cell splits an escape sequence and corrupts the rest
// of the line.

// The pane width at which each column joins the row. They are measured against
// the TREE PANE, not the terminal: the preview takes its width first when it is
// showing, and a row only ever knows what it was given.
const (
	colStateAt = 54  // the lifecycle state, spelled out
	colDirAt   = 84  // the working directory, tail-shortened
	colSparkAt = 114 // the output-rate sparkline
	colTaskAt  = 134 // the dispatched task, or the activity line

	// Column widths, each including the single space that separates it from the
	// next. The name column takes whatever is left, so it is the one that grows
	// with the terminal — a fleet of worktrees under one repo differs at the END of
	// long names, and that is what a wide terminal should buy.
	wKind  = 8 // " AGENT " plus the gap
	wState = 11
	wDir   = 24
	wSpark = 9
	wTask  = 28

	// indentStep is one level of nesting. Two columns reads as a step without a
	// deep tree walking off the right-hand side.
	indentStep = 2

	// minNameWidth floors the name column. A column only joins the row if the name
	// can keep this much, so the thing that identifies a panel is never squeezed
	// out by the things that describe it.
	minNameWidth = 14
)

// column is one right-hand field: how wide it is, and the pane width at which it
// joins the row.
type column struct {
	width int
	at    int
}

var rowColumnSpec = []column{
	{wKind, colStateAt}, // the kind badge rides in with the state; both or neither
	{wState, colStateAt},
	{wDir, colDirAt},
	{wSpark, colSparkAt},
	{wTask, colTaskAt},
}

// columnBudget is how many of the right-hand columns this pane width affords, and
// how wide their block is.
//
// The block is a FIXED width for every row in one render, which is what makes the
// columns line up. Sizing it per row is the obvious implementation and it does not
// work: a row carrying a task ends up with a wider block than one without, so its
// state lands in a different place from the state on the row above it, and a column
// that does not line up is not a column.
func columnBudget(width int) (n, block int) {
	if width < colStateAt {
		return 0, 0
	}
	for _, c := range rowColumnSpec {
		if width < c.at || width-block-c.width < minNameWidth {
			break
		}
		block += c.width
		n++
	}
	return n, block
}

// treeRow renders one dashboard row at exactly the given width.
func (m model) treeRow(it dashItem, selected bool, width int) string {
	if width < 1 {
		return ""
	}
	lead := m.rowLead(it)
	trail := m.rowTrail(it)

	avail := width - lipgloss.Width(lead) - lipgloss.Width(trail)
	if avail < 1 {
		return rowStyle(selected, width).Render(clip(lead, width))
	}

	n, block := columnBudget(width)
	if block > avail-minNameWidth {
		n, block = 0, 0 // a deeply nested row keeps its name over its description
	}
	cols := m.rowColumns(it, n)
	name := clip(it.label(), max(1, avail-block))
	gap := max(0, avail-lipgloss.Width(name)-block)

	return rowStyle(selected, width).Render(lead + name + trail + strings.Repeat(" ", gap) + cols)
}

// rowStyle is a row's full-width style. A selected row is drawn in inverse video
// across the whole width rather than behind a caret, which is what makes the
// highlight findable at fifty rows without spending two columns on every one.
func rowStyle(selected bool, width int) lipgloss.Style {
	s := lipgloss.NewStyle().Width(width).MaxHeight(1)
	if selected {
		return s.Foreground(colDark).Background(colBrand).Bold(true)
	}
	return s.Foreground(colInk)
}

// rowLead is everything before the name: the indentation, the branch glyph, the
// selection mark, the favourite and log badges, and the status glyph.
func (m model) rowLead(it dashItem) string {
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", it.depth*indentStep))
	if it.depth > 0 {
		if it.last {
			b.WriteString("└─ ")
		} else {
			b.WriteString("├─ ")
		}
	}
	if m.selecting() {
		b.WriteString(markCell(m.itemMarked(it)))
	}
	if m.itemFavourite(it) {
		b.WriteString(lipgloss.NewStyle().Foreground(colBrandHi).Render("⊙"))
	}
	// A carried row is marked where it still SITS. Nothing is sent until the drop,
	// so drawing it under the cursor would be drawing a move that has not happened
	// — and the row you are about to displace is exactly the one you need to see.
	if m.grabbedRow(it) {
		b.WriteString(lipgloss.NewStyle().Foreground(colBrandHi).Bold(true).Render("⇅"))
	}

	switch it.kind {
	case itemFold:
		b.WriteString(lipgloss.NewStyle().Foreground(states[panel.Idle].color).Render(m.foldGlyph(it.parent)))
	case itemGroup:
		// The disclosure marker IS the group's glyph rather than an extra column: a
		// row that is a container already says so by having one, and the old ▣ said
		// nothing the indentation does not.
		open := "▸"
		if it.expanded {
			open = "▾"
		}
		b.WriteString(lipgloss.NewStyle().Foreground(states[groupState(it.members)].color).Bold(true).Render(open))
	default:
		b.WriteString(m.logBadge(it.panel))
		info := stateInfoFor(it.panel)
		b.WriteString(lipgloss.NewStyle().Foreground(info.color).Render(info.led))
	}
	b.WriteString(" ")
	return b.String()
}

// rowTrail is what follows a group's name immediately: how many panels it holds,
// how many sub-groups it contains, and how many of them want a human.
//
// It rides the name rather than sitting in a column because it describes THIS row
// rather than comparing it with the others — and a column that was empty on every
// panel row would be a stripe of nothing down most of the dashboard.
func (m model) rowTrail(it dashItem) string {
	if it.kind != itemGroup {
		return ""
	}
	trail := mutedStyle.Render(fmt.Sprintf(" (%d)", len(it.members)))
	if n := subGroupCount(it.members, it.name); n > 0 {
		trail += " " + lipgloss.NewStyle().Foreground(colBrand).Render(fmt.Sprintf("▣%d", n))
	}
	if it.need > 0 {
		trail += " " + needChip(it.need)
	}
	return trail
}

// rowColumns renders the first n right-hand columns for a row.
//
// Every row supplies a value for every column, empty where the field does not
// apply — a work item has no working directory and the quiet fold has no kind —
// because a skipped column would shift everything after it and break the alignment
// the fixed block exists to give.
//
// Text is fitted while it is still plain and styled afterwards, so no cut ever
// lands inside an escape sequence.
func (m model) rowColumns(it dashItem, n int) string {
	if n == 0 {
		return ""
	}
	var kind, state, dir, spark, tail string
	var tint lipgloss.TerminalColor = colInk

	switch it.kind {
	case itemFold:
		tail = "idle · exited cleanly"

	case itemGroup:
		st := groupState(it.members)
		tint, state = states[st].color, states[st].label
		// A group's sparkline is the roll-up of its members', so a working work item
		// animates like a panel does. It is the one process-shaped field a group
		// earns: "is anything happening in there" is exactly the question a
		// collapsed row cannot otherwise answer.
		if activeState(st) {
			spark = groupSpark(it.members, st)
		}
		tail = kindBreakdown(it.members)

	default:
		p := it.panel
		info := stateInfoFor(p)
		tint, state, spark = info.color, info.label, p.Spark
		if p.Cwd != "" {
			dir = shortPath(p.Cwd, wDir-1)
		}
		// The task headlines when there is one: for an agent at work the objective
		// says more at a glance than "running · 3m", which is the same call the card
		// made when it had one line for both.
		tail = p.Activity
		if p.Task != "" {
			tail = "▸ " + p.Task
		}
		kind = "badge" // a styled chip, placed whole below
	}

	cells := []string{"", "", "", "", ""}
	if kind != "" {
		badge := kindBadge(it.panel.Kind)
		cells[0] = badge + strings.Repeat(" ", max(0, wKind-lipgloss.Width(badge)))
	} else {
		cells[0] = strings.Repeat(" ", wKind)
	}
	cells[1] = cell(state, tint, wState)
	cells[2] = cell(dir, colFaint, wDir)
	cells[3] = cell(spark, tint, wSpark)
	cells[4] = cell(tail, colFaint, wTask)

	return strings.Join(cells[:n], "")
}

// cell fits plain text into exactly n display columns and colours it, so the
// columns line up down the list whatever is in them.
func cell(text string, colour lipgloss.TerminalColor, n int) string {
	text = clip(text, n-1)
	return lipgloss.NewStyle().Foreground(colour).Render(text) + strings.Repeat(" ", max(0, n-lipgloss.Width(text)))
}

// clip truncates text to n display columns, measuring the way the LAYOUT does.
//
// It exists because the package's own truncate does not. truncate measures with
// runewidth's default condition, which has East Asian ambiguous width switched on
// and therefore counts a sparkline's block glyphs as two cells each; lipgloss —
// which lays every row out — counts them as one. Mixing the two makes a column
// truncate against one ruler and pad against another, and a seven-bar sparkline
// comes out as "▂▃▅…" in a column with room for all of it.
//
// Only this file is changed. The discrepancy is older and wider than the tree, and
// a package-wide fix to how text is measured does not belong in a change to how
// the dashboard is drawn.
func clip(text string, n int) string {
	if n < 1 {
		return ""
	}
	if lipgloss.Width(text) <= n {
		return text
	}
	if n == 1 {
		return "…"
	}
	limit := n - 1 // leave a cell for the ellipsis
	var b strings.Builder
	w := 0
	for _, r := range text {
		rw := lipgloss.Width(string(r))
		if w+rw > limit {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}
