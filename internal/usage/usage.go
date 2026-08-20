// Package usage reports the current account's token usage and API-equivalent
// cost for the day, so the cockpit can show it as a footer segment.
//
// Two data sources implement Provider. The local source reads Claude Code's own
// session transcripts under $HOME/.claude/projects — the only path that works for
// a personal Pro/Max subscription, since that usage never reaches the Admin API.
// The api source queries the Anthropic Admin usage/cost API, which reports the
// whole organization's Console/API-key billing and needs an admin key.
//
// The cost figure is always API-equivalent (tokens priced at the published
// per-model rates), not a subscription charge — a Pro/Max plan is flat-rate. It
// is a "what this would cost on the API" gauge, not a bill.
package usage

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// AdminKeyEnv is the environment variable the api source reads its Admin API key
// from. The key is deliberately never stored in the on-disk config, which is a
// plain YAML file the user hand-edits.
const AdminKeyEnv = "BATON_ANTHROPIC_ADMIN_KEY"

// DefaultWindow is how long a window lasts for the local source when the config
// leaves usage.window unset. It is configurable rather than baked in
// because plans differ and the vendor can change the figure — tracking that
// should not need a baton release.
const DefaultWindow = 5 * time.Hour

// SessionUsage is one session's share of a snapshot: the tokens it spent inside
// the window and their API-equivalent cost. Only the local source can fill this
// in — the Admin API reports the organization in aggregate and cannot attribute
// spend to a single session.
type SessionUsage struct {
	Tokens  int64
	CostUSD float64
}

// Snapshot is one account-usage sample over a billing window: token totals across
// every model, the API-equivalent cost in USD, the window the totals cover, and
// which source produced it.
type Snapshot struct {
	Input      int64     // uncached input tokens
	Output     int64     // output tokens
	CacheRead  int64     // input tokens read from a prompt cache
	CacheWrite int64     // input tokens written to a prompt cache (5m + 1h)
	CostUSD    float64   // API-equivalent cost in US dollars
	Since      time.Time // start of the window the totals cover
	Until      time.Time // end of that window; zero when the source cannot know it
	Source     string    // "local" | "api"

	// Resets marks Until as a real reset to count down to, rather than merely the
	// far edge of whatever period the source happened to query. Only a source that
	// can infer where the window opened sets it, and the footer counts down solely when
	// it is true: a wrong countdown is worse than none, because the whole point of
	// the number is that a decision gets made on it.
	Resets bool

	// Sessions is the per-session breakdown of the same totals, keyed by the Claude
	// Code session id that spent them. Nil when the source cannot attribute spend.
	// The values sum to the snapshot's own totals.
	Sessions map[string]SessionUsage
}

// TotalTokens is every token the snapshot counted, across the four buckets.
func (s Snapshot) TotalTokens() int64 { return s.Input + s.Output + s.CacheRead + s.CacheWrite }

// Empty reports whether the snapshot carries nothing worth showing.
func (s Snapshot) Empty() bool { return s.TotalTokens() == 0 && s.CostUSD == 0 }

// Countdown is how long is left before the window resets, and whether there is a
// reset to count down to at all. It reports false — never a zero and never a
// guess — when the source cannot see a reset, and equally once the window it
// named has run out: that window is over, the one after it has not opened yet,
// and a zero would assert a reset happening right now that nothing backs.
//
// Reporting it rather than clamping is what keeps the footer honest between
// polls. The cockpit ticks a held snapshot once a second while the daemon
// refreshes once every thirty, so a snapshot outlives its own window routinely —
// and a clamp turned that into a segment resting at 0:00:00 for as long as the
// snapshot was held, which for a daemon that had stopped answering was forever.
func (s Snapshot) Countdown(now time.Time) (time.Duration, bool) {
	if !s.Resets || s.Until.IsZero() {
		return 0, false
	}
	if d := s.Until.Sub(now); d > 0 {
		return d, true
	}
	return 0, false
}

// Spent is the fraction of the window already elapsed, 0 to 1, and whether there
// is a window to measure against. A caller colouring a segment by pressure leaves
// it alone when this reports false, rather than painting an invented reading.
//
// A window that has run out reports false, the same as Countdown: it is the same
// statement, and the two have to agree or the segment says two things at once —
// no time left to show, yet still coloured as if the account were up against a
// limit it is no longer measured against.
func (s Snapshot) Spent(now time.Time) (float64, bool) {
	if !s.Resets || s.Since.IsZero() || s.Until.IsZero() || !now.Before(s.Until) {
		return 0, false
	}
	total := s.Until.Sub(s.Since)
	if total <= 0 {
		return 0, false
	}
	return min(max(now.Sub(s.Since).Seconds()/total.Seconds(), 0), 1), true
}

// Share is what fraction of the window's tokens one session spent, 0 to 1. A
// session the snapshot never saw, or a window with no tokens in it yet, is 0.
func (s Snapshot) Share(session string) float64 {
	total := s.TotalTokens()
	if total <= 0 {
		return 0
	}
	return float64(s.Sessions[session].Tokens) / float64(total)
}

// Provider fetches the current account-usage snapshot from one data source.
type Provider interface {
	// Fetch returns today's usage so far. It may return a partial snapshot with a
	// non-nil error (e.g. the api source got tokens but not cost); callers should
	// still show a partial snapshot that carries data.
	Fetch(ctx context.Context) (Snapshot, error)
	// Source names the data source, "local" or "api".
	Source() string
}

// NewProvider selects the data source. "api" uses the Anthropic Admin API when an
// admin key is in the environment; "local" reads the Claude Code transcripts;
// "auto" (the default) prefers the api source when a key is present and otherwise
// falls back to local. An "api" request with no key also falls back to local, so
// the footer always has a working source rather than silently going dark.
//
// window is how long a window lasts for the local source; zero disables the
// countdown and falls back to a calendar-day total. The api source ignores it —
// it cannot see a reset (see APIProvider).
func NewProvider(source string, window time.Duration) Provider {
	// Only an explicit "local", or "auto"/"api" with no key, uses the local source;
	// every other case with a key present goes to the api source.
	key := os.Getenv(AdminKeyEnv)
	if key == "" || strings.EqualFold(strings.TrimSpace(source), "local") {
		return NewLocalProvider(window)
	}
	return NewAPIProvider(key)
}

// DefaultInterval is the refresh cadence to use when the config leaves it unset:
// the api source is coarse and rate-limited (once a minute), the local source is
// a cheap file scan (twice a minute).
func DefaultInterval(p Provider) time.Duration {
	if p != nil && p.Source() == "api" {
		return 60 * time.Second
	}
	return 30 * time.Second
}

// Format renders a snapshot as a compact footer segment, e.g.
// "1.2M tok · ≈$12.34 API". The cost is labelled "≈" and "API" on purpose: it is
// the API-equivalent price of the tokens, not a bill — on a flat-rate Pro/Max
// subscription nothing is actually charged. The cost is dropped when there is
// none, and an empty snapshot renders "" so the footer stays clean until real
// usage lands.
func Format(s Snapshot) string {
	if s.Empty() {
		return ""
	}
	tok := humanTokens(s.TotalTokens())
	if s.CostUSD <= 0 {
		return tok + " tok"
	}
	return fmt.Sprintf("%s tok · ≈$%.2f API", tok, s.CostUSD)
}

// FormatCountdown renders how long is left, in one of exactly two forms.
//
// Under a day it is a clock — "2:12:23" — because that is a wait somebody is
// sitting through, and the seconds ticking are the point: the number is being
// watched, and a figure that only moved once a minute would read as frozen.
//
// A day or more it is "2d8h". Once the answer is measured in days, minutes are
// noise — nobody waits out a weekly reset at the terminal — and the short form
// buys back the width for the bar beside it.
//
// There is no third form and no setting to choose one. Two forms cover the two
// ways the number is actually read, and a countdown whose shape varies by config
// is a countdown that has to be parsed before it can be understood.
//
// A negative duration clamps to zero rather than running backwards.
func FormatCountdown(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd%dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
	}
	return fmt.Sprintf("%d:%02d:%02d", int(d/time.Hour), int(d/time.Minute)%60, int(d/time.Second)%60)
}

// humanTokens abbreviates a token count: 1234567 → "1.2M", 9340 → "9.3K", 512 → "512".
func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	default:
		return strconv.FormatInt(n, 10)
	}
}

// tokenUsage is one message's token counts, split the way pricing needs: cache
// writes are separated into their 5-minute and 1-hour tiers, which bill at
// different multiples of the input rate.
type tokenUsage struct {
	Uncached     int64
	Output       int64
	CacheRead    int64
	CacheWrite5m int64
	CacheWrite1h int64
}

// price is a model's per-million-token USD rate for input and output. Cache reads
// and writes are priced as fixed multiples of the input rate (see costUSD).
type price struct{ in, out float64 }

// priceFor returns the per-MTok rate for a model id, matched by family so a new
// point release still prices correctly. An unrecognised model falls back to the
// Sonnet tier — a middle-of-the-road estimate rather than zero.
func priceFor(model string) price {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return price{in: 5, out: 25}
	case strings.Contains(m, "haiku"):
		return price{in: 1, out: 5}
	case strings.Contains(m, "fable"), strings.Contains(m, "mythos"):
		return price{in: 10, out: 50}
	case strings.Contains(m, "sonnet"):
		return price{in: 3, out: 15}
	default:
		return price{in: 3, out: 15}
	}
}

// costUSD is the API-equivalent cost of one message's tokens. Cache reads bill at
// 0.1×, 5-minute cache writes at 1.25×, and 1-hour writes at 2× the input rate —
// Anthropic's published prompt-caching multipliers.
func costUSD(model string, u tokenUsage) float64 {
	p := priceFor(model)
	const perM = 1_000_000.0
	return float64(u.Uncached)/perM*p.in +
		float64(u.Output)/perM*p.out +
		float64(u.CacheRead)/perM*p.in*0.1 +
		float64(u.CacheWrite5m)/perM*p.in*1.25 +
		float64(u.CacheWrite1h)/perM*p.in*2.0
}

// startOfDay is local midnight of t — the window every snapshot opens at.
func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
