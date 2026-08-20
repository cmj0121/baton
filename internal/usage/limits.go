package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// Limits is the account's standing against its subscription quotas: how much of
// each rate-limit window is gone, and when each one resets.
//
// It is a different measurement from Snapshot, not a better one, and the two are
// kept apart on purpose. Snapshot counts tokens baton read out of the
// transcripts, which is the only thing that can be attributed to a panel.
// Limits is what the vendor says about the account as a whole, which is the only
// thing that says whether the next turn will be refused. A fleet needs both: the
// first answers who is burning it, the second answers whether there is anything
// left to burn.
//
// Every window is a pointer because "absent" is a state the sources genuinely
// report — a window Claude Code has not seen yet this session, a per-model
// ceiling that does not apply to the plan, an account that is on API billing and
// has no subscription window at all. A nil window renders as nothing, never as
// zero: a bar resting at 0% asserts a full tank, which is the one reading worse
// than no reading.
type Limits struct {
	// FiveHour and SevenDay are the two windows every subscription has: the
	// rolling session throttle and the weekly ceiling.
	FiveHour *Window
	SevenDay *Window

	// SevenDayOpus and SevenDaySonnet are the per-model weekly ceilings, which
	// only some plans carry. Only the oauth source reports them; the statusline
	// payload has no per-model breakdown, so they stay nil there.
	SevenDayOpus   *Window
	SevenDaySonnet *Window

	// Credit is the extra-usage balance — pay-as-you-go spend past the
	// subscription's included allowance. Only the oauth source reports it.
	Credit *Credit

	// Source names where the reading came from, "statusline" or "oauth".
	Source string

	// At is when the reading was taken. It is carried rather than inferred
	// because the statusline source is a push: a sample arrives when some panel's
	// status line happens to render, and nothing arrives at all while the fleet is
	// idle. So the age of the reading is a fact about the reading, and a cockpit
	// that cannot say how old its number is should not be showing it as current.
	At time.Time
}

// Window is one rate-limit window: how much of it is spent, and when it resets.
//
// UsedPercent is 0–100 rather than a 0–1 fraction because that is the unit both
// sources report in, and converting on the way in would mean converting back for
// every place that shows a percentage. Fraction does the division for the one
// caller that wants it — the progress bar.
type Window struct {
	UsedPercent float64
	ResetsAt    time.Time
}

// Fraction is the window's fill as 0–1, for a caller drawing a bar or picking a
// colour. It clamps: a vendor that reports 103% past an overage is describing a
// bar that is full, not one that overflows its cell.
func (w *Window) Fraction() float64 {
	if w == nil {
		return 0
	}
	return min(max(w.UsedPercent/100, 0), 1)
}

// Countdown is how long is left before the window resets, and whether there is a
// reset to count down to at all. It mirrors Snapshot.Countdown exactly, including
// the refusal to report a zero: a window whose reset instant has passed is one
// the reading no longer describes, and the honest answer is that there is
// nothing to show until a fresher sample lands.
//
// This matters more here than it does for Snapshot, because the statusline source
// only samples when a panel renders. A held reading routinely outlives its own
// window, and clamping would park the countdown at 0:00:00 for as long as the
// fleet stayed quiet.
func (w *Window) Countdown(now time.Time) (time.Duration, bool) {
	if w == nil || w.ResetsAt.IsZero() {
		return 0, false
	}
	if d := w.ResetsAt.Sub(now); d > 0 {
		return d, true
	}
	return 0, false
}

// Credit is the extra-usage balance: whether pay-as-you-go past the subscription
// is switched on, the monthly ceiling it is capped at, and how much of that has
// been spent.
//
// The amounts are pointers for the same reason the windows are. The vendor
// reports a credit block with every field null when the feature is off, and a
// null monthly limit is "uncapped", not "capped at zero" — the two render as
// opposite readings and must not collapse into one.
type Credit struct {
	Enabled     bool
	MonthlyUSD  *float64 // the monthly ceiling; nil means uncapped
	UsedUSD     *float64 // spent against it so far
	UsedPercent *float64 // the vendor's own utilisation figure, 0–100
}

// Fraction is the credit balance's fill as 0–1. It prefers the vendor's own
// utilisation figure and falls back to dividing the amounts, so a source that
// reports one but not the other still draws a bar. It reports false when neither
// is available, or when the limit is uncapped and there is nothing to be a
// fraction of.
func (c *Credit) Fraction() (float64, bool) {
	if c == nil || !c.Enabled {
		return 0, false
	}
	if c.UsedPercent != nil {
		return min(max(*c.UsedPercent/100, 0), 1), true
	}
	if c.UsedUSD == nil || c.MonthlyUSD == nil || *c.MonthlyUSD <= 0 {
		return 0, false
	}
	return min(max(*c.UsedUSD / *c.MonthlyUSD, 0), 1), true
}

// Empty reports whether the reading carries no window worth showing. A Limits
// with only a disabled credit block is empty: nothing in it describes a
// constraint the account is running against.
func (l Limits) Empty() bool {
	if l.FiveHour != nil || l.SevenDay != nil || l.SevenDayOpus != nil || l.SevenDaySonnet != nil {
		return false
	}
	return l.Credit == nil || !l.Credit.Enabled
}

// Age is how old the reading is. A negative age — a sample stamped in the future
// by a clock correction — reports as zero rather than as a reading from the
// future, which would render as a nonsensical "-3s ago".
func (l Limits) Age(now time.Time) time.Duration {
	if l.At.IsZero() {
		return 0
	}
	if d := now.Sub(l.At); d > 0 {
		return d
	}
	return 0
}

// The limits-source names, as written in usage.limits and reported on Source.
const (
	LimitsOff        = "off"
	LimitsStatusline = "statusline"
	LimitsOAuth      = "oauth"
)

// The quota thresholds, used when the config has not set a usable pair. They are
// the same figures as the window ones and deliberately so: a user who has tuned
// when the footer turns amber has said something about their own tolerance, and
// it means the same thing whether the pressure is a clock running out or a
// ceiling being approached.
const (
	DefaultLimitWarnAt  = 0.75
	DefaultLimitAlarmAt = 0.90
)

// StaleAfter is how old a reading may get before a cockpit should mark it as no
// longer current. It is deliberately generous: the statusline source samples
// whenever a panel renders, which is often while the fleet works and never while
// it sits idle, and an idle fleet is not spending quota — so a reading that has
// stopped moving is usually still true. The mark says "nobody has looked
// recently", not "this is wrong".
const StaleAfter = 5 * time.Minute

// Stale reports whether the reading is old enough to mark. A reading with no
// timestamp is stale by definition: something produced it without saying when,
// and an unstamped number should never be shown as current.
func (l Limits) Stale(now time.Time) bool {
	return l.At.IsZero() || l.Age(now) > StaleAfter
}

// statuslinePayload is the slice of Claude Code's status-line stdin JSON that
// carries the account's rate limits. Claude Code hands the whole session state to
// whatever command is configured as the status line, and since v2.1.80 that
// includes the two subscription windows — which is what makes this source free:
// no network call, no credential, no token spent, just a payload baton is already
// in a position to read.
//
// Both windows are pointers because the documented contract is that each may be
// independently absent: the block appears only for a Claude.ai subscription, and
// only once the session has had its first API response back.
type statuslinePayload struct {
	RateLimits *struct {
		FiveHour *statuslineWindow `json:"five_hour"`
		SevenDay *statuslineWindow `json:"seven_day"`
	} `json:"rate_limits"`
}

// statuslineWindow is one window as the status-line payload spells it: a
// percentage already scaled 0–100, and a reset as Unix epoch seconds.
type statuslineWindow struct {
	UsedPercent float64 `json:"used_percentage"`
	ResetsAt    int64   `json:"resets_at"`
}

// window converts one payload window, dropping a reset the source did not give.
// A zero epoch is not midnight in 1970 here — it is the field's absence, and a
// window with no reset still reports a usable percentage.
func (w *statuslineWindow) window() *Window {
	if w == nil {
		return nil
	}
	out := &Window{UsedPercent: w.UsedPercent}
	if w.ResetsAt > 0 {
		out.ResetsAt = time.Unix(w.ResetsAt, 0)
	}
	return out
}

// ParseStatusline reads a Claude Code status-line stdin payload and returns the
// rate limits in it, stamped at when the sample was taken. It reports false when
// the payload is not JSON, or carries no rate-limit block, or carries one with
// both windows absent — all three of which are ordinary rather than exceptional,
// since every status-line render before the session's first API response looks
// exactly like that.
//
// The whole payload is deliberately not retained. It carries the session's
// transcript path, working directory and cost, none of which this source is for,
// and a sink that forwards only the four numbers it needs is a sink that cannot
// leak the rest.
func ParseStatusline(b []byte, at time.Time) (Limits, bool) {
	var p statuslinePayload
	if err := json.Unmarshal(b, &p); err != nil || p.RateLimits == nil {
		return Limits{}, false
	}
	l := Limits{
		FiveHour: p.RateLimits.FiveHour.window(),
		SevenDay: p.RateLimits.SevenDay.window(),
		Source:   LimitsStatusline,
		At:       at,
	}
	if l.Empty() {
		return Limits{}, false
	}
	return l, true
}

// The progress-bar glyphs. They are the same pair the cockpit's context bar uses,
// so a quota bar and a context bar read as the same kind of object rather than as
// two unrelated widgets that happen to sit near each other.
const (
	barFilled = "▓"
	barEmpty  = "░"
)

// Bar renders a 0–1 fraction as a fixed-width progress bar.
//
// It rounds rather than truncates, with one exception at each end: a non-zero
// fraction always shows at least one filled cell, and a fraction below 1 always
// leaves at least one empty one. Both ends matter more than the rounding does —
// an empty bar for a window that has been touched reads as "nothing spent", and
// a full bar for a window with room left reads as "you are out", and those are
// the two readings someone acts on.
func Bar(fraction float64, width int) string {
	if width <= 0 {
		return ""
	}
	f := min(max(fraction, 0), 1)
	filled := int(math.Round(f * float64(width)))
	if f > 0 && filled == 0 {
		filled = 1
	}
	if f < 1 && filled == width {
		filled = width - 1
	}
	return strings.Repeat(barFilled, filled) + strings.Repeat(barEmpty, width-filled)
}

// FormatLimits renders a reading as one plain-text line, e.g.
//
//	5h ▓▓▓▓▓▓░░░░ 62% 2:14:31 · 7d ▓▓▓░░░░░░░ 34% 3d 04:12
//
// It is the form the status-line sink prints when it has no status line of the
// user's own to defer to, and the shape the cockpit's own segment mirrors in
// colour. A window with no countdown to offer prints its bar and percentage
// alone: the reading is still worth showing, only the reset is not known.
//
// width is the bar's cell count; countdown is a usage.Countdown* format name.
func FormatLimits(l Limits, now time.Time, width int, countdown string) string {
	var parts []string
	for _, w := range []struct {
		label string
		win   *Window
	}{{"5h", l.FiveHour}, {"7d", l.SevenDay}} {
		if w.win == nil {
			continue
		}
		s := fmt.Sprintf("%s %s %.0f%%", w.label, Bar(w.win.Fraction(), width), w.win.UsedPercent)
		if d, ok := w.win.Countdown(now); ok {
			s += " " + FormatCountdown(d, countdown)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

// ParseLimitsSource normalises the usage.limits setting.

// ParseLimitsSource normalises the usage.limits setting. An unrecognised value
// falls back to the statusline source rather than to off: a typo should leave the
// feature working on the safe source, not silently switch it off, and statusline
// is the source that needs neither a credential nor a network call.
func ParseLimitsSource(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case LimitsOff:
		return LimitsOff
	case LimitsOAuth:
		return LimitsOAuth
	default:
		return LimitsStatusline
	}
}
