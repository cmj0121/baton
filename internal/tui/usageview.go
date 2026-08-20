package tui

import (
	"fmt"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

// usageBurners is how many panels the roster lists. A fleet can be large and the
// tail is not a decision — the question this answers is which one to stop, and
// that is asked of the top of the list.
const usageBurners = 8

// openUsage enters the overlay, remembering the view to come back to.
func (m model) openUsage(from mode) model {
	m.usageFrom = from
	m.mode = modeUsage
	m.status = "account usage"
	return m
}

// closeUsage leaves the overlay, restoring the view it was opened from.
func (m model) closeUsage() (tea.Model, tea.Cmd) {
	m.mode = m.usageFrom
	if m.mode == modeDashboard {
		m.status = "dashboard"
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
		return m.popupBox(lipgloss.JoinVertical(lipgloss.Left,
			sectionStyle.Render(spaced("ACCOUNT USAGE")), "",
			mutedStyle.Render(i18n.T(m.effLang(), "usage.view.no-reading",
				"no quota reading yet — a Claude Code panel reports one after its first turn")),
			"", m.usageLegend()))
	}

	rows := m.usageBars(lim)
	if roster := m.usageRoster(); len(roster) > 0 {
		rows = append(rows, "", m.usageRosterHeader())
		rows = append(rows, roster...)
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		m.usageHeader(lim), "",
		lipgloss.JoinVertical(lipgloss.Left, rows...), "",
		m.usageLegend())
	return m.popupBox(content)
}

// usageHeader names the overlay and says where the reading came from and how old
// it is. The age is not decoration: the statusline source is a push, so a reading
// can be perfectly true and half an hour old, and only the age lets someone tell
// that from a number that is being kept up to date.
func (m model) usageHeader(lim *proto.LimitsInfo) string {
	header := sectionStyle.Render(spaced("ACCOUNT USAGE"))
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
	row := fmt.Sprintf("%-16s %s", label, bar)
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
	return mutedStyle.Render(fmt.Sprintf("%-26s %7s %11s %8s",
		tr("usage.view.burning", "Burning this window"),
		tr("usage.view.share", "share"),
		tr("usage.view.tokens", "tokens"),
		tr("usage.view.of-5h", "of 5h")))
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
		ofQuota := "—" // no five-hour reading to multiply against; the share still stands
		if fiveHour > 0 {
			ofQuota = fmt.Sprintf("%.0f%%", share*fiveHour)
		}
		rows = append(rows, fmt.Sprintf("%s%-24s %6.0f%% %11s %8s",
			lipgloss.NewStyle().Foreground(colBrand).Render("▸ "),
			truncate(e.title, 24), share*100, humanTokens(e.tokens), ofQuota))
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
	return legend("u", "cycle footer", "esc", "close")
}
