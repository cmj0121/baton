package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/panel"
)

// This file draws the dashboard for a SMALL fleet: a grid of cards.
//
// The cards were deleted when the dashboard became one full-width tree, and the
// arithmetic behind that is still right at fifty panels — a tree row spends the
// whole terminal on columns, and one row beats one card on information at a third
// of the height. It is wrong at four. A row's columns earn their keep by letting
// you COMPARE many panels; with four of them there is nothing to compare, and the
// same width reads as a mostly empty line with a name at the far left.
//
// So the dashboard has two layouts again, and the thing that made the first pair
// unpleasant is deliberately not back: the cursor keys keep one meaning. ← and →
// are open/shut in both layouts, ↑ and ↓ step a whole grid row (one row in the
// tree, where a row IS the grid row), and j/k step one item in reading order so
// every card is reachable without arrows that mean something else here.

const (
	cardWidth = 32            // outer width of one card, incl. border + padding
	cardGap   = 1             // horizontal margin between cards
	cardInner = cardWidth - 4 // usable text width inside the border + padding

	// gridThreshold is how many top-level rows the dashboard draws as cards. At or
	// above it the fleet has outgrown a grid you take in at a glance and the tree
	// takes over.
	gridThreshold = 6
)

// gridCols is how many cards fit on a row at the given width (1–3).
func gridCols(width int) int {
	return min(3, max(1, width/(cardWidth+cardGap)))
}

// gridMode reports whether the dashboard draws cards rather than the tree, given
// the tree's full row list.
//
// It counts the TOP LEVEL only — a work item is one row however deep it goes —
// and that is the whole subtlety of the feature. Groups are expanded until someone
// shuts them, so counting every visible row would put five panels filed under one
// work item into the tree, which is precisely the fleet the cards exist for. It
// also means expanding a row can never flip the layout out from under the
// keystroke that expanded it.
//
// A filter or a lens forces the tree whatever the count says. Both exist to show
// you WHICH rows matched or bucketed, and a card grid can only draw the top level
// — so honouring the count there would answer a search for a panel nested three
// deep with a card for the work item holding it.
func (m model) gridMode(rows []dashItem) bool {
	if m.showTree { // asked for the tree on a fleet the cards would fit
		return false
	}
	if m.width < cardWidth+2 { // a card cannot be laid out narrower than it is
		return false
	}
	if m.filter != "" || !m.lens.real() {
		return false
	}
	return m.countTopLevel(rows) < gridThreshold
}

// gridDash reports whether the dashboard is currently on the card grid. It builds
// the tree to ask, exactly as dashItems does, so the two can never disagree about
// which layout the cursor is walking.
func (m model) gridDash() bool { return m.mode == modeDashboard && m.gridMode(m.dashTree()) }

// countTopLevel counts what the top of the tree HOLDS: one for each panel and each
// work item, and — for the quiet fold — the panels it stands for rather than the
// one row it draws.
//
// Counting rows was the obvious implementation and it made the layout flicker. The
// fold fires off a threshold, and what crosses that threshold is not the fleet: a
// panel stops being foldable while it is busy, and the card under the cursor is
// never folded at all. So zooming a panel and coming back, or moving the cursor
// onto a quiet one, could swing nine rows into one and swap the whole dashboard
// under a keystroke that meant nothing of the sort.
//
// Counting the panels behind the fold makes the number identical whether the fold
// fires, and whether it is open — the fold is a display device, and a display
// device must not be an input to which display is chosen. A work item still counts
// once, however deep it goes: that IS one thing at the top level, and one card.
func (m model) countTopLevel(rows []dashItem) int {
	n := 0
	for _, it := range rows {
		if it.depth != 0 {
			continue
		}
		if it.kind == itemFold {
			// Closed, the row stands for its panels; open, they are drawn as rows of
			// their own and counting both would count them twice.
			if !m.foldOpen[it.parent] {
				n += it.quiet
			}
			continue
		}
		n++
	}
	return n
}

// topLevelRows keeps only the top of the tree — what the grid draws, and what the
// cursor addresses while it is drawing it. A nested row can never be orphaned by
// this: a group row survives the filter whenever anything in its subtree does, so
// every row it drops has a row here that stands for it.
func topLevelRows(rows []dashItem) []dashItem {
	out := make([]dashItem, 0, len(rows))
	for _, it := range rows {
		if it.depth == 0 {
			out = append(out, it)
		}
	}
	return out
}

// cardGrid lays the dashboard out as a responsive grid of cards: a card per lone
// panel, and one card per work item, whole.
func (m model) cardGrid(items []dashItem) string {
	if len(items) == 0 {
		return ""
	}
	cols := gridCols(m.width) // grid mode here, so always the multi-column count
	rows := make([]string, 0, (len(items)+cols-1)/cols)
	for i := 0; i < len(items); i += cols {
		end := min(i+cols, len(items))
		cards := make([]string, 0, cols)
		for j := i; j < end; j++ {
			cards = append(cards, m.renderItemCard(items[j], j == m.cursor))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cards...))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// renderItemCard draws a dashboard item: a group card for a work item, the quiet
// card for a fold row, otherwise a panel card.
func (m model) renderItemCard(it dashItem, selected bool) string {
	switch it.kind {
	case itemGroup:
		return m.renderGroupCard(it, selected)
	case itemFold:
		return m.renderFoldCard(it, selected)
	}
	return m.renderCard(it, selected)
}

// cardStyle is the box every card is drawn in — one size and one height for all
// three kinds, so the grid's rows line up.
func cardStyle(selected bool) lipgloss.Style {
	border := colFaint
	if selected {
		border = colBrand
	}
	return lipgloss.NewStyle().
		Width(cardWidth-2).
		Padding(0, 1).
		MarginRight(cardGap).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border)
}

// cardTitleColor is a card's name colour: the brand highlight under the cursor.
func cardTitleColor(selected bool) lipgloss.TerminalColor {
	if selected {
		return colBrandHi
	}
	return colInk
}

// cardMarks is the decoration a card head carries before its glyph, with the gap
// that separates it from the glyph. It is rowMarks — the same source as the tree
// row's — so a card cannot quietly stop showing one of them.
func (m model) cardMarks(it dashItem) string {
	marks := m.rowMarks(it)
	if marks == "" {
		return ""
	}
	return marks + " "
}

// renderCard draws one panel as three tidy lines that never wrap: a status LED +
// title, a kind badge + state + directory, and a sparkline + meta footer.
func (m model) renderCard(it dashItem, selected bool) string {
	p := it.panel
	info := stateInfoFor(p)

	// The marks cost the title columns and never a line, so a marked, favourited,
	// carried card is the same size as every other one.
	marks := m.cardMarks(it)
	rec := m.logBadge(p)
	led := lipgloss.NewStyle().Foreground(info.color).Bold(true).Render(info.led)
	title := lipgloss.NewStyle().Foreground(cardTitleColor(selected)).Bold(true).
		Render(truncate(p.Title, max(1, cardInner-4-lipgloss.Width(marks)-lipgloss.Width(rec))))
	head := clampWidth(marks+rec+led+" "+title, cardInner)

	badge := kindBadge(p.Kind)
	state := lipgloss.NewStyle().Foreground(info.color).Render(info.label)
	kindLine := badge + "  " + state
	// The directory rides the state line rather than a line of its own: the card is
	// three lines by design, and the path is what tells fifty panels called
	// "shell #1"…"#50" apart. It is shortened rather than truncated, so the tail —
	// the part that identifies the panel — is what survives a narrow card.
	if room := cardInner - lipgloss.Width(kindLine) - 2; p.Cwd != "" && room > 6 {
		kindLine += "  " + mutedStyle.Render(shortPath(p.Cwd, room))
	}

	spark := lipgloss.NewStyle().Foreground(info.color).Render(p.Spark)
	// When the panel carries a dispatched brief, the task headlines the footer (▸)
	// instead of the bare activity line — for an agent at work the objective says
	// more at a glance than "running · 3m". Height stays at three lines either way.
	footText, glyph := p.Activity, ""
	if p.Task != "" {
		footText, glyph = p.Task, "▸ "
	}
	footer := spark + "  " + mutedStyle.Render(truncate(glyph+footText, cardInner-lipgloss.Width(spark)-2))

	return cardStyle(selected).Render(lipgloss.JoinVertical(lipgloss.Left, head, kindLine, footer))
}

// renderGroupCard draws a work item as one card: its rolled-up state, what kinds
// of panel it holds, and a chip per state.
func (m model) renderGroupCard(it dashItem, selected bool) string {
	st := groupState(it.members)
	info := states[st]

	marks := m.cardMarks(it)
	glyph := lipgloss.NewStyle().Foreground(info.color).Bold(true).Render("▣")
	// A nested group notes its immediate sub-group count right-aligned in the head —
	// the same place the split's sub-group tile shows it — rather than trailing the
	// kind line, so that line can never spill onto a second row and grow the card one
	// taller than a panel card. The need count joins it there, ahead of it, in
	// attention's own red: a card that folds fifty panels into one glyph otherwise
	// says how big the group is and nothing about how much of it is waiting on you.
	tally := needChip(it.need)
	if n := subGroupCount(it.members, it.name); n > 0 {
		sub := lipgloss.NewStyle().Foreground(colBrand).Render(fmt.Sprintf("▣%d", n))
		if tally != "" {
			sub = " " + sub
		}
		tally += sub
	}
	avail := cardInner - lipgloss.Width(marks) - 2 - lipgloss.Width(tally) // glyph + its trailing space = 2
	if tally != "" {
		avail-- // a gap before the right-aligned counts
	}
	name := lipgloss.NewStyle().Foreground(cardTitleColor(selected)).Bold(true).Render(truncate(it.title(), max(1, avail)))
	head := marks + glyph + " " + name
	if tally != "" {
		head = padEnds(head, tally, cardInner)
	}
	head = clampWidth(head, cardInner)

	// Split the member count by kind, so a card says what kind of work it holds —
	// "2 agent · 1 shell" — not just how many panels. Clamp it to the inner width so a
	// long breakdown truncates rather than wrapping and growing the card.
	kindLine := clampWidth(groupBadge()+"  "+kindBreakdown(it.members), cardInner)

	// The footer is the per-state chips, led by a sparkline in the group's rolled-up
	// colour while it is active — so a working group animates like a panel card. It is
	// clamped to the inner width for the same no-wrap, fixed-height reason.
	footer := groupCountChips(it.members)
	if activeState(st) {
		spark := lipgloss.NewStyle().Foreground(info.color).Render(groupSpark(it.members, st))
		footer = spark + "  " + footer
	}
	footer = clampWidth(footer, cardInner)

	return cardStyle(selected).Render(lipgloss.JoinVertical(lipgloss.Left, head, kindLine, footer))
}

// renderFoldCard draws the quiet row as a card, in idle's amber — the colour of
// the panels it stands for, so the card reads as a summary of them rather than as
// a control.
//
// It keeps renderCard's three-line shape. A card one line shorter than its
// neighbours would break the grid's row heights, and a card that told you what it
// held without telling you how to open it would send you looking for a legend.
func (m model) renderFoldCard(it dashItem, selected bool) string {
	info := states[panel.Idle]

	glyph := lipgloss.NewStyle().Foreground(info.color).Bold(true).Render(m.foldGlyph(it.parent))
	name := lipgloss.NewStyle().Foreground(cardTitleColor(selected)).Bold(true).Render(truncate(it.title(), max(1, cardInner-2)))
	head := clampWidth(glyph+" "+name, cardInner)
	kindLine := clampWidth(mutedStyle.Render("idle · exited cleanly"), cardInner)
	footer := clampWidth(legend("enter", m.foldVerb(it.parent)), cardInner)

	return cardStyle(selected).Render(lipgloss.JoinVertical(lipgloss.Left, head, kindLine, footer))
}
