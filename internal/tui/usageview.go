package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/i18n"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/usage"
)

// The usage overlay (modeUsage, v U): the account's quota bars in full, and the
// panels spending them.
//
// The footer segment has one row and has to choose; this has the space to show
// the whole picture at once, which is the picture a fleet is actually run on. Two
// halves, and they come from two different places on purpose:
//
//   - The bars are the vendor's own reading of the account. They say whether
//     there is anything left, and nothing but the vendor can say it.
//   - The roster underneath is baton's, from the transcripts of the sessions it
//     handed out. It says who spent it, and nothing but baton can say that —
//     the account-wide reading has no idea a fleet exists.
//
// The last column is the two multiplied: a panel's share of the window's tokens
// against how much of the five-hour quota is gone. That is the number a decision
// is made on. With a dozen agents running and two hours left, "stop the one that
// has eaten a quarter of your limit" is actionable in a way that neither half is
// on its own.

// usageBarWidth is the bar's cell count in the overlay. It is wide because there
// is room, and a wide bar is the point of opening this at all: the footer already
// gives the number, and what the overlay adds is being able to see four windows
// against each other at a glance.
const usageBarWidth = 16

// The roster's and the bar rows' column widths. They were spelled as fmt verbs
// inline until the columns had to be measured in display cells rather than runes;
// naming them keeps a header and the rows under it from drifting apart one edit
// at a time.
const (
	barLabelWidth     = 16
	rosterTitleWidth  = 24
	rosterShareWidth  = 7
	rosterTokensWidth = 11
	rosterQuotaWidth  = 8
)

// usageBurners is how many panels the roster lists. A fleet can be large and the
// tail is not a decision — the question this answers is which one to stop, and
// that is asked of the top of the list.
const usageBurners = 8

// openUsage enters the overlay, remembering the view to come back to.
func (m model) openUsage(from mode) model {
	m.usageFrom = from
	m.mode = modeUsage
	m.status = m.tr("status.account-usage", "account usage")
	return m
}

// closeUsage leaves the overlay, restoring the view it was opened from.
func (m model) closeUsage() (tea.Model, tea.Cmd) {
	m.mode = m.usageFrom
	if m.mode == modeDashboard {
		m.status = m.tr("mode.dashboard.status", "dashboard")
	}
	return m, nil
}

// handleUsageKey owns the keyboard while the overlay is up. There is nothing to
// scroll — the reading is four rows and a bounded roster — so the only verbs are
// leaving and cycling the footer segment, which is the setting a user is most
// likely to want to change while looking straight at what it shows.
func (m model) handleUsageKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q":
		return m.closeUsage()
	case "u":
		return m.cycleUsageMode()
	}
	return m, nil
}

// usageView renders the overlay: the quota bars, the roster of what is spending
// them, and a key legend.
func (m model) usageView() string {
	lim := m.usageLimits()
	if lim == nil {
		// No source configured, or none that has reported yet. Saying which would be
		// guessing at the daemon's config from the cockpit; saying nothing at all
		// would leave someone staring at an empty box wondering if it was broken.
		//
		// The vendor roll still goes in. It is the half of this overlay that does not
		// depend on a quota source at all — which agents the fleet's machine has, and
		// which of them baton can account for — and it is exactly what somebody who
		// opened this and found no bars needs to see.
		body := []string{sectionStyle.Render(spaced(m.tr("usage.title", "ACCOUNT USAGE"))), "",
			mutedStyle.Render(i18n.T(m.effLang(), "usage.view.no-reading",
				"no quota reading yet — a Claude Code panel reports one after its first turn"))}
		if vendors := m.usageVendorSection(); len(vendors) > 0 {
			body = append(body, "")
			body = append(body, vendors...)
		}
		body = append(body, "", m.usageLegend())
		return m.popupBox(lipgloss.JoinVertical(lipgloss.Left, body...))
	}

	rows := m.usageBars(lim)
	if roster := m.usageRoster(); len(roster) > 0 {
		rows = append(rows, "", m.usageRosterHeader())
		rows = append(rows, roster...)
	}
	if vendors := m.usageVendorSection(); len(vendors) > 0 {
		rows = append(rows, "")
		rows = append(rows, vendors...)
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		m.usageHeader(lim), "",
		lipgloss.JoinVertical(lipgloss.Left, rows...), "",
		m.usageLegend())
	return m.popupBox(content)
}

// usageVendorSection is the vendor roll: every agent backend the fleet's machine
// knows, and what baton can honestly say about each one's usage.
//
// The bars above are Anthropic's account. This is the fleet's, and the two are
// different questions — an operator running a mixed fleet can be nowhere near a
// Claude limit and still have no idea what the other agents have spent. The
// section answers "which of these can baton even account for", once, in place of
// the operator discovering it by noticing a number never moves.
//
// A vendor with no reading gets a mark and "---", never a bar and never a zero.
// That is still the whole point of the section: "baton cannot see grok's usage"
// and "grok has used nothing" are opposite claims, and a row that drew an empty
// bar would make the second one for free. The mark's shape and colour are what
// separate the two states that have no figure; see noReadingCell for why the
// sentence that used to spell them out is gone.
//
// A vendor baton CAN read gets four columns, and they come from three places.
//
// What is left of the five-hour window and of the week belong to the account
// whose books that column describes. Claude's pair comes from the Anthropic
// limits reading. Grok publishes a weekly credit pool of its own and no
// five-hour throttle, so its 7d cell fills from that pool and its 5h cell stays
// a dash. Lending Anthropic's numbers across the column would still print a
// ceiling grok never named.
//
// `spent` is the vendor's OWN reader: everything it can see on this machine,
// including the sessions nobody spawned from here. Every readable vendor has it,
// and for a vendor baton cannot attribute it is the only figure on the row that
// will ever be filled in — which is why it is back after a version without it
// left grok showing three dashes on an agent that had been running all day.
//
// `panels` is baton's own attribution, and it is claude-only in practice rather
// than by intent: attribution needs a session id, withSessionID hands one to
// Claude Code alone, so a grok panel cannot appear there however hard it works.
// The two are kept apart rather than merged because they measure different
// things, and on a fleet whose agents also run outside baton the gap between them
// is itself the reading.
func (m model) usageVendorSection() []string {
	if m.usageInfo == nil || len(m.usageInfo.Vendors) == 0 {
		// Nil is an older daemon, which never said. Drawing a header over nothing
		// would suggest the fleet has no agent backends.
		return nil
	}
	tr := func(k, def string) string { return i18n.T(m.effLang(), k, def) }
	def := m.effDefaultAgent()
	spend := m.panelSpendByAgent()

	var fiveHour, sevenDay *proto.LimitWindow
	if lim := m.usageLimits(); lim != nil {
		fiveHour, sevenDay = lim.FiveHour, lim.SevenDay
	}

	// The two leading spaces stand in for the mark every row below carries, so the
	// header's columns line up with theirs.
	rows := []string{mutedStyle.Render("  " +
		pad(tr("usage.view.agent", "Agent"), vendorNameWidth) + " " +
		pad(tr("usage.view.session-left", "5h left"), vendorQuotaWidth) + " " +
		pad(tr("usage.view.week-left", "7d left"), vendorWeekWidth) + " " +
		pad(tr("usage.view.resets-in", "resets"), vendorResetWidth) + " " +
		pad(tr("usage.view.spent", "spent"), vendorSpentWidth) + " " +
		tr("usage.view.panels", "panels"))}
	for _, v := range m.usageInfo.Vendors {
		name := v.Vendor
		if v.Vendor == def {
			// The one the footer is reporting, marked so the two screens agree about
			// which agent the headline number belongs to.
			name += " *"
		}
		mark := lipgloss.NewStyle().Foreground(m.vendorMarkColor(v)).Render(vendorMark(v))
		if v.State != vendorReading {
			rows = append(rows, mark+pad(name, vendorNameWidth)+" "+mutedStyle.Render(noReadingCell))
			continue
		}
		rows = append(rows, mark+pad(name, vendorNameWidth)+" "+
			m.vendorQuotaCell(v, fiveHour, usage.WindowFiveHour, vendorQuotaWidth)+" "+
			m.vendorQuotaCell(v, sevenDay, usage.WindowWeek, vendorWeekWidth)+" "+
			m.vendorResetCell(v, fiveHour)+" "+
			vendorSpentCell(v)+" "+
			m.vendorPanelCell(spend[v.Vendor]))
	}
	return rows
}

// The vendor roll's column widths. The name is the widest agent name plus the
// default's mark; a quota cell holds "100%"; the reset holds "2:14:31" and
// nothing longer, because FormatCountdown collapses anything past a day to
// "3d4h".
const (
	vendorNameWidth  = 12
	vendorQuotaWidth = 8
	vendorWeekWidth  = 8
	vendorResetWidth = 9
	vendorSpentWidth = 10
)

// pad lays out one cell in exactly w DISPLAY columns; padLeft does it for a
// column read from the right, which is where a figure belongs.
//
// The ruler is lipgloss's, and picking it is the whole job. There are three in
// play and they disagree:
//
//   - fmt pads by counting runes. 代理 is two runes and four columns, so every
//     translated header in this overlay sat two cells right of the rows below it.
//   - runewidth, which the package's own truncate measures with, counts an East
//     Asian ambiguous rune as two cells.
//   - lipgloss counts it as one — and lipgloss is what lays every row of this
//     popup out, so it is the ruler the finished screen is measured by.
//
// A cell padded against one and laid out against another is a column that drifts,
// which is why clip (treerow.go) exists and why both of these go through it.
//
// Styling happens after padding, never before: a styled cell carries escape
// sequences, and every one of them would count against the width.
func pad(s string, w int) string {
	s = clip(s, w)
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

func padLeft(s string, w int) string {
	s = clip(s, w)
	return strings.Repeat(" ", max(0, w-lipgloss.Width(s))) + s
}

// noReadingCell stands for a whole row baton has no reading for, where the
// vendor's stated reason used to be spelled out.
//
// Three cells rather than one, so it reads as "none of this row is known" beside
// a single hyphen, which marks one column that is not.
//
// What it replaced was a forty-character English sentence — and English is what
// it was in every language, because the reason is the DAEMON's own words
// (internal/usage/vendor.go:122) and the cockpit prints them verbatim. It made
// the roll's longest line out of its least useful one, and made it untranslatable
// besides.
//
// The reason itself is not lost, and that is what makes this affordable. Which of
// the three states a row is in is what the mark and its colour say — a filled
// mark read, a hollow amber one installed and unreadable, a dash not installed at
// all — with the table in docs/USAGE.md spelling them out. And for the agent the
// question is usually asked about, the fleet's default, the footer segment still
// says why in words (usage.go:157).
const noReadingCell = "---"

// unknownCell is what a column shows where baton has no reading: an ASCII hyphen,
// deliberately, where the typographically better em dash used to be. An em dash is
// East Asian ambiguous, so a terminal set for CJK draws it in two cells while
// lipgloss lays the row out for one, and the column below it steps sideways on
// exactly the machines this cockpit is most often read on. A hyphen is one cell
// everywhere and there is nothing left to disagree about.
const unknownCell = "-"

// vendorQuotaCell is one window's REMAINING share for one vendor — "62%" — or a
// dash where baton holds no quota reading that belongs to this vendor.
//
// Remaining rather than spent, and that is not the same choice the bars above
// made. A bar is a shape you compare against the bar below it; a cell in a row of
// three is read once, for a decision about whether to start another agent here,
// and "what is left" is the form that answer comes in.
//
// A quota is the vendor's own statement about its own account. Claude's pair
// comes from the Anthropic limits payload. Any other vendor fills only from a
// window it labelled as this column — grok's weekly pool is "7d", and nothing
// here is "5h" — so Anthropic's numbers cannot leak onto a name that never
// published them.
func (m model) vendorQuotaCell(v proto.VendorUsage, account *proto.LimitWindow, label string, width int) string {
	w := vendorQuotaWindow(v, account, label)
	if w == nil {
		return mutedStyle.Render(pad(unknownCell, width))
	}
	return pad(fmt.Sprintf("%.0f%%", (1-limitFraction(w))*100), width)
}

// vendorQuotaWindow is the ceiling share that belongs in one column of one row.
// Claude reads the account payload; everyone else reads a window they labelled
// as this column, or nothing.
func vendorQuotaWindow(v proto.VendorUsage, account *proto.LimitWindow, label string) *proto.LimitWindow {
	if v.Vendor == usage.LimitsVendor && account != nil {
		return account
	}
	return labeledVendorWindow(v, label)
}

func labeledVendorWindow(v proto.VendorUsage, label string) *proto.LimitWindow {
	for i := range v.Windows {
		if v.Windows[i].Label != label {
			continue
		}
		w := v.Windows[i]
		return &proto.LimitWindow{UsedPercent: w.UsedPercent, ResetsAt: w.ResetsAt}
	}
	return nil
}

// vendorResetCell is when this agent's window rolls over, and every readable
// vendor has one — which is the whole reason it is a column of its own rather
// than a suffix on the quota cell, where only the one account that publishes a
// quota could ever have carried it.
//
// Two different instants can land here and the row says which by what is beside
// them. For the account the limits reading belongs to it is the QUOTA's reset,
// the same instant the 5h bar above counts down to, so the cell agrees with the
// "left" figure two columns along. For every other vendor it is that vendor's
// own ceiling reset when they published one (grok's week), otherwise the end of
// the window baton measured its spend over.
//
// Neither is invented and neither is borrowed: a vendor with no window of its own
// and no quota gets the mark, exactly as it does everywhere else on this row.
func (m model) vendorResetCell(v proto.VendorUsage, w *proto.LimitWindow) string {
	if v.Vendor == usage.LimitsVendor && w != nil {
		if left, ok := limitCountdown(w, m.now); ok {
			return pad(usage.FormatCountdown(left), vendorResetWidth)
		}
	}
	if left := m.vendorCountdown(v); left != "" {
		return pad(left, vendorResetWidth)
	}
	return mutedStyle.Render(pad(unknownCell, vendorResetWidth))
}

// vendorSpentCell is what the vendor's own reader saw this window: every session
// on this machine, whether or not baton spawned it.
//
// It is the row's load-bearing column for any agent baton cannot attribute, and
// it is the reason the roll can say anything at all about grok. A reading of
// nothing is a mark rather than "0 tok": the reader looked and the window is
// empty, which is a true zero — but it shares a column with agents whose figure
// is a real total, and a bare 0 there reads as a claim about the agent rather
// than about the window.
//
// The cost the old single-column standing carried ("· ≈$8.08 API") does not fit
// beside four columns at any terminal width worth laying out for. It is still on
// the footer segment for the default agent.
func vendorSpentCell(v proto.VendorUsage) string {
	if v.Tokens <= 0 {
		return mutedStyle.Render(pad(unknownCell, vendorSpentWidth))
	}
	return pad(humanTokens(v.Tokens), vendorSpentWidth)
}

// vendorPanelCell is the last column: what the panels the fleet is running on
// this agent have spent this window, and how many of them there are.
//
// Nothing attributed is a dash rather than "0 tok", and for a non-claude agent it
// is always a dash: attribution runs on the session id withSessionID hands to
// Claude Code and to nothing else, so a grok panel cannot reach this column
// however hard it works. The dash means "baton cannot attribute this", never
// "this agent is idle" — the spent column beside it is what says whether the
// agent has been working.
func (m model) vendorPanelCell(s agentSpend) string {
	if s.panels == 0 {
		return mutedStyle.Render(unknownCell)
	}
	unit := m.tr("usage.view.panels-many", "panels")
	if s.panels == 1 {
		unit = m.tr("usage.view.panels-one", "panel")
	}
	return joinDot(humanTokens(s.tokens), fmt.Sprintf("%d %s", s.panels, unit))
}

// agentSpend is what the panels on one agent have spent this window: baton's own
// attribution, which is a different measurement from the vendor's books and not
// interchangeable with them. The vendor reader scans everything on the machine,
// including sessions nobody spawned from here; this counts only what the fleet
// can name.
type agentSpend struct {
	tokens int64
	panels int
}

// panelSpendByAgent groups the per-panel usage by the agent profile each panel
// was spawned from.
//
// A panel with no profile is attributed to nobody rather than charged to the
// fleet default. The default is what a profile-less panel WOULD have run had it
// been spawned today, which is not evidence about what it actually ran — and the
// cost of guessing is somebody else's tokens sitting under a name, in the one
// column of this row that baton is the sole author of.
func (m model) panelSpendByAgent() map[string]agentSpend {
	info := m.usageInfo
	if info == nil || len(info.Panels) == 0 {
		return nil
	}
	out := make(map[string]agentSpend, len(info.Vendors))
	for id, pu := range info.Panels {
		p, ok := m.fleetPanel(id)
		if !ok || p.Profile == "" {
			continue
		}
		s := out[p.Profile]
		s.tokens += pu.Tokens
		s.panels++
		out[p.Profile] = s
	}
	return out
}

// vendorMark is the glyph in front of a vendor's row. The three states get three
// different marks so the column can be read down without reading the text: a
// reading is a filled mark, an installed agent baton cannot account for is a
// hollow one, and an agent that is not here at all is a dash.
func vendorMark(v proto.VendorUsage) string {
	switch v.State {
	case vendorReading:
		return "▸ "
	case vendorAbsent:
		return "· "
	default:
		return "◦ "
	}
}

// vendorMarkColor colours the mark by what it is saying. Absent is muted because
// it is not a problem — an agent nobody installed is not a gap in baton's
// reporting. An installed agent with no source is amber: that IS a gap, and it is
// the one an operator would otherwise mistake for a quiet account.
func (m model) vendorMarkColor(v proto.VendorUsage) lipgloss.Color {
	switch v.State {
	case vendorReading:
		return colBrand
	case vendorAbsent:
		return colMuted
	default:
		return colAmber
	}
}

// usageHeader names the overlay and says where the reading came from and how old
// it is. The age is not decoration: the statusline source is a push, so a reading
// can be perfectly true and half an hour old, and only the age lets someone tell
// that from a number that is being kept up to date.
func (m model) usageHeader(lim *proto.LimitsInfo) string {
	header := sectionStyle.Render(spaced(m.tr("usage.title", "ACCOUNT USAGE")))
	meta := joinDot(lim.Source, m.usageAgeNote())
	if meta == "" {
		return header
	}
	style := mutedStyle
	if m.usageLimitsStale() {
		style = lipgloss.NewStyle().Foreground(colAmber)
	}
	return header + "   " + style.Render(meta)
}

// usageAgeNote is how old the reading is, in words. A reading that has just
// landed says so rather than counting "0:00:00 ago", which is both noise and,
// read quickly, the opposite of what it means.
func (m model) usageAgeNote() string {
	age, ok := m.usageReadingAge()
	if !ok {
		return ""
	}
	if age < usageJustNow {
		return i18n.T(m.effLang(), "usage.view.just-now", "just now")
	}
	return usage.FormatCountdown(age) + " " + i18n.T(m.effLang(), "usage.view.ago", "ago")
}

// usageJustNow is how recent a reading has to be to read as current rather than
// as an age. It is a few seconds because that is the cadence a status line
// reports at while a panel is working.
const usageJustNow = 5 * time.Second

// usageBars is the four quota rows, one per window the source reported. A window
// it did not report gets no row at all — a bar at zero would assert a full tank
// on a ceiling that may not even apply to the plan.
func (m model) usageBars(lim *proto.LimitsInfo) []string {
	tr := func(k, def string) string { return i18n.T(m.effLang(), k, def) }
	rows := make([]string, 0, 4)
	for _, r := range []struct {
		label string
		win   *proto.LimitWindow
	}{
		{tr("usage.view.session", "Session (5h)"), lim.FiveHour},
		{tr("usage.view.week", "Week (all)"), lim.SevenDay},
		{tr("usage.view.week-opus", "Week (Opus)"), lim.SevenDayOpus},
		{tr("usage.view.week-sonnet", "Week (Sonnet)"), lim.SevenDaySonnet},
	} {
		if r.win == nil {
			continue
		}
		rows = append(rows, m.usageBarRow(r.label, limitFraction(r.win), m.usageResetNote(r.win)))
	}
	if row, ok := m.usageCreditRow(lim.Credit); ok {
		rows = append(rows, row)
	}
	return rows
}

// usageBarRow lays out one row: label, bar, and a trailing note.
//
// There is no percentage column. Four rows of bars stacked against each other is
// a shape you compare by looking, and a column of numbers beside them invites
// reading each one instead — which is slower and says nothing the lengths do not.
// The note keeps what a bar genuinely cannot draw: when the window resets, or
// what the credit balance stands at in money.
//
// The bar takes its colour from its own fill rather than from the segment's, so a
// single window against its ceiling shows red even while the others are quiet —
// which is the whole reason to look at them side by side.
func (m model) usageBarRow(label string, fraction float64, note string) string {
	bar := lipgloss.NewStyle().Foreground(m.usageFillColor(fraction)).Render(usage.Bar(fraction, usageBarWidth))
	row := pad(label, barLabelWidth) + " " + bar
	if note != "" {
		row += "   " + mutedStyle.Render(note)
	}
	return row
}

// usageResetNote is the "resets 2:14:31" trailer, empty for a window with no
// reset to count down to.
func (m model) usageResetNote(w *proto.LimitWindow) string {
	left, ok := limitCountdown(w, m.now)
	if !ok {
		return ""
	}
	return i18n.T(m.effLang(), "usage.view.resets", "resets") + " " + usage.FormatCountdown(left)
}

// usageCreditRow is the extra-usage balance, and whether there is one to show. A
// disabled balance is not a balance at zero: it is a feature that is switched
// off, and a row for it would read as money already spent.
func (m model) usageCreditRow(c *proto.LimitCredit) (string, bool) {
	if c == nil || !c.Enabled {
		return "", false
	}
	cr := &usage.Credit{Enabled: true, MonthlyUSD: c.MonthlyUSD, UsedUSD: c.UsedUSD, UsedPercent: c.UsedPercent}
	fraction, _ := cr.Fraction()
	note := ""
	switch {
	case c.UsedUSD != nil && c.MonthlyUSD != nil:
		note = fmt.Sprintf("$%.2f / $%.2f", *c.UsedUSD, *c.MonthlyUSD)
	case c.UsedUSD != nil:
		// No ceiling reported means uncapped, which is the opposite of capped at
		// zero — so the spend is shown with nothing to divide it by.
		note = fmt.Sprintf("$%.2f / %s", *c.UsedUSD, i18n.T(m.effLang(), "usage.view.uncapped", "uncapped"))
	}
	return m.usageBarRow(i18n.T(m.effLang(), "usage.view.credit", "Extra credit"), fraction, note), true
}

// usageFillColor is one bar's colour, by how full it is. It uses the same
// thresholds as the footer segment so the two never disagree about what amber
// means.
func (m model) usageFillColor(fraction float64) lipgloss.Color {
	warn, alarm := usage.DefaultLimitWarnAt, usage.DefaultLimitAlarmAt
	if m.usageInfo != nil && m.usageInfo.WarnAt > 0 && m.usageInfo.AlarmAt > m.usageInfo.WarnAt {
		warn, alarm = m.usageInfo.WarnAt, m.usageInfo.AlarmAt
	}
	switch {
	case fraction >= alarm:
		return colRed
	case fraction >= warn:
		return colAmber
	default:
		return colBrand
	}
}

// usageRosterHeader is the column strip over the roster.
func (m model) usageRosterHeader() string {
	tr := func(k, def string) string { return i18n.T(m.effLang(), k, def) }
	return mutedStyle.Render(pad(tr("usage.view.burning", "Burning this window"), rosterTitleWidth+2) + " " +
		padLeft(tr("usage.view.share", "share"), rosterShareWidth) + " " +
		padLeft(tr("usage.view.tokens", "tokens"), rosterTokensWidth) + " " +
		padLeft(tr("usage.view.of-5h", "of 5h"), rosterQuotaWidth))
}

// usageRoster is the panels spending the window, heaviest first.
//
// The last column is what the overlay exists for. A panel's share of the window's
// tokens is baton's own reading and says nothing about limits; the five-hour
// utilisation is the vendor's and says nothing about panels. Multiplied, they say
// how much of the account's actual ceiling this one panel has eaten — which is
// the question "which one do I stop" is really asking.
func (m model) usageRoster() []string {
	info := m.usageInfo
	if info == nil || info.Tokens <= 0 || len(info.Panels) == 0 {
		return nil
	}
	type entry struct {
		title  string
		tokens int64
	}
	entries := make([]entry, 0, len(info.Panels))
	for id, pu := range info.Panels {
		if pu.Tokens <= 0 {
			continue
		}
		title := id
		if p, found := m.fleetPanel(id); found {
			title = p.Title
			if p.Group != "" {
				title = p.Group + " / " + p.Title
			}
		}
		entries = append(entries, entry{title: title, tokens: pu.Tokens})
	}
	// Heaviest first, and by title when two have spent the same, so the order does
	// not flicker between polls on a map's iteration order.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].tokens != entries[j].tokens {
			return entries[i].tokens > entries[j].tokens
		}
		return entries[i].title < entries[j].title
	})
	if len(entries) > usageBurners {
		entries = entries[:usageBurners]
	}

	var fiveHour float64
	if lim := m.usageLimits(); lim != nil && lim.FiveHour != nil {
		fiveHour = lim.FiveHour.UsedPercent
	}

	rows := make([]string, 0, len(entries))
	for _, e := range entries {
		share := float64(e.tokens) / float64(info.Tokens)
		ofQuota := unknownCell // no five-hour reading to multiply against; the share still stands
		if fiveHour > 0 {
			ofQuota = fmt.Sprintf("%.0f%%", share*fiveHour)
		}
		rows = append(rows, lipgloss.NewStyle().Foreground(colBrand).Render("▸ ")+
			pad(e.title, rosterTitleWidth)+" "+
			padLeft(fmt.Sprintf("%.0f%%", share*100), rosterShareWidth)+" "+
			padLeft(humanTokens(e.tokens), rosterTokensWidth)+" "+
			padLeft(ofQuota, rosterQuotaWidth))
	}
	return rows
}

// usageReadingAge is how old the held reading is, on the cockpit's clock.
func (m model) usageReadingAge() (time.Duration, bool) {
	lim := m.usageLimits()
	if lim == nil || lim.At == "" {
		return 0, false
	}
	at, err := time.Parse(time.RFC3339, lim.At)
	if err != nil {
		return 0, false
	}
	return usage.Limits{At: at}.Age(m.now), true
}

// usageLegend is the overlay's key hint.
func (m model) usageLegend() string {
	return legend("u", m.tr("usage.legend.cycle", "cycle footer"), "esc", m.tr("legend.close", "close"))
}
