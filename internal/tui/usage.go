package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/i18n"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/usage"
)

// usageMode is which view of the account usage the footer segment shows. U
// cycles through them.
//
// The two live views answer different questions, and a fleet needs both. The
// window view answers "is the account going to make it" — the spend so far and
// how long until it resets. The panel view answers "who is burning it", which is
// the question you act on: with a dozen agents running and two hours left, the
// decision is which one to stop, and that takes the focused work's share of the
// window rather than its raw token count.
type usageMode int

const (
	usageOff    usageMode = iota // no segment at all
	usageWindow                  // account-wide spend and the countdown to the reset
	usagePanel                   // the focused panel's or group's spend, and its share of the window
	usageLimits                  // the account's quota bars: how much of each window is gone
)

// usageModes is the cycle order U walks, ending back at off.
var usageModes = []usageMode{usageOff, usageWindow, usagePanel, usageLimits}

// String is the mode's stable config name, as persisted in settings.usage-mode.
func (u usageMode) String() string {
	switch u {
	case usageWindow:
		return "window"
	case usagePanel:
		return "panel"
	case usageLimits:
		return "limits"
	default:
		return "off"
	}
}

// parseUsageMode maps a persisted name back to a mode, defaulting to the window
// view — the one the segment has always shown.
func parseUsageMode(s string) usageMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return usageOff
	case "panel":
		return usagePanel
	case "limits":
		return usageLimits
	default:
		return usageWindow
	}
}

// next is the mode U advances to, wrapping at the end of the cycle.
func (u usageMode) next() usageMode {
	for i, m := range usageModes {
		if m == u {
			return usageModes[(i+1)%len(usageModes)]
		}
	}
	return usageWindow
}

// label names the mode on the status line, localised.
func (u usageMode) label(lang i18n.Lang) string {
	switch u {
	case usageWindow:
		return i18n.T(lang, "usage.mode.window", "window")
	case usagePanel:
		return i18n.T(lang, "usage.mode.panel", "focused panel")
	case usageLimits:
		return i18n.T(lang, "usage.mode.limits", "quota")
	default:
		return i18n.T(lang, "usage.mode.off", "off")
	}
}

// usageCap renders the account usage/cost segment (internal/usage). It is empty
// when the segment is off (U) or the daemon has nothing to report yet, so the
// strip stays clean until real usage lands.
//
// The colour tracks how far into the window the account is, not how much it has
// spent: the point is to act before the window runs out, not to watch it hit zero.
func (m model) usageCap() string {
	if m.usageMode == usageOff {
		return ""
	}
	text := m.usageSegment()
	if text == "" {
		return ""
	}
	return seg("⊙ "+truncate(text, m.usageSegmentWidth()), colInk, m.usagePressureColor())
}

// usageSegmentWidth is how much of the strip the segment may take. The quota view
// is given more than the others because it carries two bars and two countdowns,
// and a bar truncated mid-run is not a bar — it is a shorter bar, reading as less
// spent than there is.
func (m model) usageSegmentWidth() int {
	if m.usageMode == usageLimits {
		return 56
	}
	return 44
}

// usageSegment is the segment's text for the active mode, without the glyph or
// the styling. The window view falls back to the daemon's pre-rendered string
// whenever the structured payload is missing, so the spend still shows even if
// only the old field arrives.
func (m model) usageSegment() string {
	switch m.usageMode {
	case usagePanel:
		return m.usagePanelText()
	case usageLimits:
		return m.usageLimitsText()
	}
	return joinDot(m.usageText, m.usageCountdown())
}

// limitsBarWidth is the bar's cell count in the footer. It is short because the
// segment shares one row with the mode, the fleet counts and the clock; the
// percentage beside it carries the precision, and the bar carries the glance.
const limitsBarWidth = 6

// usageLimitsText is the quota view: how much of each rate-limit window is gone,
// and how long until it resets.
//
// It reads "5h ▓▓▓▓░░ 62% 2:14:31 · 7d ▓▓░░░░ 34% 3d 04:12", and it is empty
// whenever no source has reported — never a pair of bars at zero, which would
// assert a full tank on an account that may be minutes from a refusal.
//
// A reading nobody has restated in a while is marked with a leading "~". The
// statusline source is a push: it arrives when a panel renders and stops
// arriving when the fleet goes quiet, so an old reading is usually still true and
// the mark says "nobody has looked recently" rather than "this is wrong".
func (m model) usageLimitsText() string {
	lim := m.usageLimits()
	if lim == nil {
		return ""
	}
	var parts []string
	for _, w := range []struct {
		label string
		win   *proto.LimitWindow
	}{{"5h", lim.FiveHour}, {"7d", lim.SevenDay}} {
		if w.win == nil {
			continue
		}
		part := fmt.Sprintf("%s %s %.0f%%", w.label, usage.Bar(limitFraction(w.win), limitsBarWidth), w.win.UsedPercent)
		if left, ok := limitCountdown(w.win, m.now); ok {
			part += " " + usage.FormatCountdown(left, m.usageCountdownFormat())
		}
		parts = append(parts, part)
	}
	text := strings.Join(parts, " · ")
	if text != "" && m.usageLimitsStale() {
		text = "~ " + text
	}
	return text
}

// usageLimits is the rate-limit payload the daemon last sent, or nil when there
// is none — no source configured, or none that has produced a reading yet.
func (m model) usageLimits() *proto.LimitsInfo {
	if m.usageInfo == nil {
		return nil
	}
	return m.usageInfo.Limits
}

// usageLimitsStale reports whether the held reading is old enough to mark. The
// age is measured on the cockpit's own clock against the instant the reading was
// taken, which is why the daemon sends that instant rather than an age: an age
// would be as old as the last poll by the time it arrived.
func (m model) usageLimitsStale() bool {
	lim := m.usageLimits()
	if lim == nil {
		return false
	}
	at, err := time.Parse(time.RFC3339, lim.At)
	if err != nil {
		return true // stamped with something unreadable; never show it as current
	}
	return usage.Limits{At: at}.Stale(m.now)
}

// usageCountdownFormat is the countdown form the daemon's config asked for,
// defaulting to auto when there is no payload to read it from.
func (m model) usageCountdownFormat() string {
	if m.usageInfo == nil {
		return usage.CountdownAuto
	}
	return m.usageInfo.CountdownFormat
}

// limitFraction is one window's fill as 0–1.
func limitFraction(w *proto.LimitWindow) float64 {
	if w == nil {
		return 0
	}
	return (&usage.Window{UsedPercent: w.UsedPercent}).Fraction()
}

// limitCountdown is how long is left on one window, on the cockpit's clock. It
// reports false for a window with no reset, and equally for one whose reset has
// already passed — the reading no longer describes anything, and a 0:00:00 parked
// there would outlast every poll that failed to replace it.
func limitCountdown(w *proto.LimitWindow, now time.Time) (time.Duration, bool) {
	if w == nil || w.ResetsAt == "" {
		return 0, false
	}
	at, err := time.Parse(time.RFC3339, w.ResetsAt)
	if err != nil {
		return 0, false
	}
	return (&usage.Window{ResetsAt: at}).Countdown(now)
}

// usageCountdown is the "⏳ 2:14:31" half of the segment, empty when the source
// cannot see a reset — a countdown baton had to invent would be worse than none.
// The remaining time is computed here, against the cockpit's own clock, because
// it has to tick every second while the daemon polls once every thirty.
//
// That gap is why the reading goes through Snapshot.Countdown rather than a plain
// subtraction: a held payload outlives its own window between polls, and once it
// has, the cockpit no longer knows when anything resets. The segment drops the
// countdown then, instead of resting on the 0:00:00 a subtraction would floor at
// — a number that would sit there for as long as nothing newer arrived.
func (m model) usageCountdown() string {
	info := m.usageInfo
	if info == nil || !info.Resets || info.Until == "" {
		return ""
	}
	until, err := time.Parse(time.RFC3339, info.Until)
	if err != nil {
		return ""
	}
	left, ok := usage.Snapshot{Until: until, Resets: true}.Countdown(m.now)
	if !ok {
		return ""
	}
	return "⏳ " + usage.FormatCountdown(left, info.CountdownFormat)
}

// usagePanelText is the focused work's line: what it has spent inside this window
// and what share of the window that is. Work that the daemon cannot attribute —
// a shell, an agent that is not Claude Code, a panel restored from before a daemon
// restart — says so plainly rather than showing a zero that reads as "free".
func (m model) usagePanelText() string {
	info := m.usageInfo
	title, ids, ok := m.usageFocus()
	if info == nil || !ok {
		return ""
	}
	var tokens int64
	var attributed bool
	for _, id := range ids {
		pu, known := info.Panels[id]
		if !known {
			continue
		}
		tokens += pu.Tokens
		attributed = true
	}
	if !attributed {
		return title + " · " + i18n.T(m.effLang(), "usage.panel.unattributed", "not attributed")
	}
	text := title + " · " + humanTokens(tokens)
	if info.Tokens > 0 {
		text += fmt.Sprintf(" · %.0f%% %s", float64(tokens)/float64(info.Tokens)*100,
			i18n.T(m.effLang(), "usage.panel.of-window", "of window"))
	}
	return joinDot(text, m.usageCountdown())
}

// usageFocus is the work the panel view reports on: whatever the cockpit is
// pointed at right now. In a zoom that is the zoomed panel; in a group split the
// focused member; on the dashboard the selected panel, or every member of the
// selected group rolled up — a group is one work item, and "which one do I stop"
// is asked of work items as often as of single panels.
func (m model) usageFocus() (title string, ids []string, ok bool) {
	if m.mode == modeZoom && m.zoomID != "" {
		if p, found := m.fleetPanel(m.zoomID); found {
			return p.Title, []string{p.ID}, true
		}
	}
	// Only inside a split does the tile focus mean anything; on the dashboard it
	// still resolves to a slot, which is not what the cursor is pointing at.
	if m.mode == modeGroupZoom {
		if id := m.focusedMemberID(); id != "" {
			if p, found := m.fleetPanel(id); found {
				return p.Title, []string{p.ID}, true
			}
		}
	}
	it, has := m.selectedItem()
	if !has {
		return "", nil, false
	}
	if it.kind == itemGroup {
		ids = make([]string, 0, len(it.members))
		for _, mem := range it.members {
			ids = append(ids, mem.ID)
		}
		return it.name, ids, true
	}
	return it.panel.Title, []string{it.panel.ID}, true
}

// usagePressureColor is the segment's fill: blue while the window has room, amber
// past the warning threshold, red past the alarm. The thresholds come from the
// daemon, where the usage config is read. With no window to measure against the
// colour does not move at all, rather than painting an invented reading.
func (m model) usagePressureColor() lipgloss.Color {
	if m.usageMode == usageLimits {
		return m.limitsPressureColor()
	}
	info := m.usageInfo
	if info == nil || !info.Resets || info.Since == "" || info.Until == "" {
		return colBlue
	}
	since, err1 := time.Parse(time.RFC3339, info.Since)
	until, err2 := time.Parse(time.RFC3339, info.Until)
	if err1 != nil || err2 != nil {
		return colBlue
	}
	spent, ok := usage.Snapshot{Since: since, Until: until, Resets: true}.Spent(m.now)
	if !ok {
		return colBlue
	}
	switch {
	case info.AlarmAt > 0 && spent >= info.AlarmAt:
		return colRed
	case info.WarnAt > 0 && spent >= info.WarnAt:
		return colAmber
	default:
		return colBlue
	}
}

// limitsPressureColor is the quota segment's fill, taken from the fullest window
// rather than from the clock.
//
// That is the whole difference between this view and the window one. There, the
// pressure is how far into the window the account is, because the spend is a
// number with no ceiling to compare it against. Here there is a ceiling, and the
// question is how close to it the account has come — an account at 90% with four
// hours left is in far more trouble than one at 20% with ten minutes left, and
// only the fullest window can say so.
func (m model) limitsPressureColor() lipgloss.Color {
	lim := m.usageLimits()
	if lim == nil {
		return colBlue
	}
	var worst float64
	for _, w := range []*proto.LimitWindow{lim.FiveHour, lim.SevenDay, lim.SevenDayOpus, lim.SevenDaySonnet} {
		worst = max(worst, limitFraction(w))
	}
	warn, alarm := usage.DefaultLimitWarnAt, usage.DefaultLimitAlarmAt
	if m.usageInfo != nil && m.usageInfo.WarnAt > 0 && m.usageInfo.AlarmAt > m.usageInfo.WarnAt {
		warn, alarm = m.usageInfo.WarnAt, m.usageInfo.AlarmAt
	}
	switch {
	case worst >= alarm:
		return colRed
	case worst >= warn:
		return colAmber
	default:
		return colBlue
	}
}

// joinDot glues two footer fragments with the separator the segment uses,
// dropping either side when it is empty so no stray dot is left behind.
func joinDot(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + " · " + b
}

// humanTokens abbreviates a token count for the panel view, matching the form the
// daemon renders the account total in.
func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM tok", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK tok", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d tok", n)
	}
}
