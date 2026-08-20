package usage

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"time"
)

// The sink is the handoff between two processes that never talk to each other.
//
// Claude Code hands its session state — the account's rate limits included — to
// whatever command is configured as its status line. Baton launches Claude Code,
// so baton is in a position to be that command; but the status line runs in the
// panel's process tree, once per render, while the reading is wanted in the
// daemon. A file is the whole mechanism: the sink writes the four numbers it
// parsed, the daemon reads them on its usage tick.
//
// Everything below exists to keep that file honest under a fleet. Several panels
// write it concurrently at a few hertz each, so the write is atomic on a
// per-writer temporary; and a reading that says nothing new is not written at
// all, so a busy fleet does not churn the disk to restate a number that has not
// moved.

// wireLimits is Limits as it sits on disk. It is a separate type from Limits on
// purpose: the in-memory form uses time.Time and a pointer-per-window to say
// "absent", and JSON needs an encoding for both that survives a round trip
// through a file two different processes disagree about the age of.
type wireLimits struct {
	FiveHour       *wireWindow `json:"five_hour,omitempty"`
	SevenDay       *wireWindow `json:"seven_day,omitempty"`
	SevenDayOpus   *wireWindow `json:"seven_day_opus,omitempty"`
	SevenDaySonnet *wireWindow `json:"seven_day_sonnet,omitempty"`
	Credit         *wireCredit `json:"credit,omitempty"`
	Source         string      `json:"source,omitempty"`
	At             string      `json:"at,omitempty"` // RFC 3339; the reading's own timestamp
}

// wireWindow is one window on disk. ResetsAt is RFC 3339 and omitted when the
// source gave none, so an absent reset stays absent rather than decoding as the
// Unix epoch.
type wireWindow struct {
	UsedPercent float64 `json:"used_percentage"`
	ResetsAt    string  `json:"resets_at,omitempty"`
}

// wireCredit is the extra-usage balance on disk. Every amount is a pointer for
// the same reason it is in Credit: a null monthly limit means uncapped, which is
// the opposite reading from a limit of zero.
type wireCredit struct {
	Enabled     bool     `json:"enabled"`
	MonthlyUSD  *float64 `json:"monthly_usd,omitempty"`
	UsedUSD     *float64 `json:"used_usd,omitempty"`
	UsedPercent *float64 `json:"used_percentage,omitempty"`
}

func toWireWindow(w *Window) *wireWindow {
	if w == nil {
		return nil
	}
	out := &wireWindow{UsedPercent: w.UsedPercent}
	if !w.ResetsAt.IsZero() {
		out.ResetsAt = w.ResetsAt.UTC().Format(time.RFC3339)
	}
	return out
}

func (w *wireWindow) window() *Window {
	if w == nil {
		return nil
	}
	out := &Window{UsedPercent: w.UsedPercent}
	if w.ResetsAt != "" {
		if t, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil {
			out.ResetsAt = t
		}
	}
	return out
}

// MarshalLimits encodes a reading for the sink file.
func MarshalLimits(l Limits) ([]byte, error) {
	w := wireLimits{
		FiveHour:       toWireWindow(l.FiveHour),
		SevenDay:       toWireWindow(l.SevenDay),
		SevenDayOpus:   toWireWindow(l.SevenDayOpus),
		SevenDaySonnet: toWireWindow(l.SevenDaySonnet),
		Source:         l.Source,
	}
	if !l.At.IsZero() {
		w.At = l.At.UTC().Format(time.RFC3339)
	}
	if l.Credit != nil {
		w.Credit = &wireCredit{
			Enabled:     l.Credit.Enabled,
			MonthlyUSD:  l.Credit.MonthlyUSD,
			UsedUSD:     l.Credit.UsedUSD,
			UsedPercent: l.Credit.UsedPercent,
		}
	}
	return json.Marshal(w)
}

// UnmarshalLimits decodes a sink file. It reports false for anything that is not
// a reading worth showing — unparseable bytes, or a file whose windows are all
// absent — so a caller never has to distinguish "no file" from "an empty one".
//
// A reading with no timestamp decodes with a zero At, which Stale treats as stale
// by definition. That is the right answer rather than a defect: something wrote a
// number without saying when, and an unstamped number must never show as current.
func UnmarshalLimits(b []byte) (Limits, bool) {
	var w wireLimits
	if err := json.Unmarshal(b, &w); err != nil {
		return Limits{}, false
	}
	l := Limits{
		FiveHour:       w.FiveHour.window(),
		SevenDay:       w.SevenDay.window(),
		SevenDayOpus:   w.SevenDayOpus.window(),
		SevenDaySonnet: w.SevenDaySonnet.window(),
		Source:         w.Source,
	}
	if w.At != "" {
		if t, err := time.Parse(time.RFC3339, w.At); err == nil {
			l.At = t
		}
	}
	if w.Credit != nil {
		l.Credit = &Credit{
			Enabled:     w.Credit.Enabled,
			MonthlyUSD:  w.Credit.MonthlyUSD,
			UsedUSD:     w.Credit.UsedUSD,
			UsedPercent: w.Credit.UsedPercent,
		}
	}
	if l.Empty() {
		return Limits{}, false
	}
	return l, true
}

// ReadLimits loads the reading a sink last wrote. A missing or unreadable file is
// not an error — it means no panel has reported yet, which is the ordinary state
// of a fleet that has not run a Claude Code turn since baton was installed.
func ReadLimits(path string) (Limits, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Limits{}, false
	}
	return UnmarshalLimits(b)
}

// RefreshAfter is how long an unchanged reading may sit before the sink rewrites
// it purely to restamp it.
//
// It is what keeps "unchanged" from being mistaken for "stale". A busy fleet can
// hold the same two percentages for minutes at a time — the windows move in
// whole points, not continuously — and without a restamp the cockpit would start
// marking a live reading as old simply because it was steady. Fifteen seconds
// caps a single panel at four writes a minute, which is nothing, while keeping
// the age well inside StaleAfter.
const RefreshAfter = 15 * time.Second

// WriteLimitsIfChanged writes l to the sink file, unless what is already there
// says the same thing and was written recently enough to still count as current.
// It reports whether it wrote.
//
// Skipping the redundant write is the point. This runs from a status line, which
// Claude Code re-executes on every render — several times a second, from every
// panel in the fleet at once. Writing each time would mean thousands of disk
// round trips an hour to keep restating a number that changes a few times a
// minute.
func WriteLimitsIfChanged(path string, l Limits) (bool, error) {
	if prev, ok := ReadLimits(path); ok && sameReading(prev, l) && l.At.Sub(prev.At) < RefreshAfter {
		return false, nil
	}
	b, err := MarshalLimits(l)
	if err != nil {
		return false, err
	}
	return true, writeAtomic(path, b)
}

// sameReading reports whether two readings carry the same numbers, ignoring when
// they were taken. Only the windows are compared: the credit balance rides the
// same file but comes from a different source, and a status-line writer that has
// no credit data must not be read as having cleared it.
func sameReading(a, b Limits) bool {
	return sameWindow(a.FiveHour, b.FiveHour) &&
		sameWindow(a.SevenDay, b.SevenDay) &&
		sameWindow(a.SevenDayOpus, b.SevenDayOpus) &&
		sameWindow(a.SevenDaySonnet, b.SevenDaySonnet)
}

// sameWindow compares two windows, treating absence as a value: a window that has
// gone away is a change, not a match. Percentages are compared with a tolerance
// because they arrive as floats and a redundant write is not worth a rounding
// argument.
func sameWindow(a, b *Window) bool {
	if a == nil || b == nil {
		return a == b
	}
	return math.Abs(a.UsedPercent-b.UsedPercent) < 0.01 && a.ResetsAt.Equal(b.ResetsAt)
}

// writeAtomic writes data to path through a temporary of its own, so two sinks
// racing from two panels cannot interleave into one torn file.
//
// paths.WriteFileAtomic is the house helper for this and is not used here for
// exactly that reason: it names its temporary after the target, which is safe for
// its callers — one daemon writing its own state — and is not safe for a writer
// that runs once per panel per render. os.CreateTemp gives each writer a name
// nobody else can be holding.
func writeAtomic(path string, data []byte) (err error) {
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".usage-limits-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LimitsProvider fetches the account's rate-limit standing from one source.
type LimitsProvider interface {
	// Limits returns the current reading. It reports false when there is nothing
	// to show — no sample yet, the source switched off, a fetch that failed — which
	// a caller renders as no segment at all rather than as a zeroed one.
	Limits(ctx context.Context) (Limits, bool)
	// Source names the data source, "statusline" or "oauth".
	Source() string
}

// StatuslineLimits reads whatever the status-line sinks have dropped in the sink
// file. It holds no state and does no I/O beyond one small read per tick: all the
// work happens in the panels, which were going to render a status line anyway.
type StatuslineLimits struct{ path string }

// NewStatuslineLimits builds the status-line source over the given sink file.
func NewStatuslineLimits(path string) *StatuslineLimits { return &StatuslineLimits{path: path} }

// Source implements LimitsProvider.
func (p *StatuslineLimits) Source() string { return LimitsStatusline }

// Limits implements LimitsProvider.
func (p *StatuslineLimits) Limits(context.Context) (Limits, bool) { return ReadLimits(p.path) }
