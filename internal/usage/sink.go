package usage

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cmj0121/baton/internal/paths"
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

// maxSinkFile caps how much of the sink file is read.
//
// "One small read per tick" is what StatuslineLimits promises, and the promise
// held only for as long as whatever wrote the file kept it small. This is the
// DEFAULT limits source, so the daemon does this read every tick, forever; the
// file sits in the fleet's own directory where any process running as the fleet
// owner can grow it, and its writer is a status line invoked by an agent's own
// runtime rather than by baton.
//
// 64 KiB, sized on what MarshalLimits actually produces: five windows and a
// credit block, a few hundred bytes, so this is a hundred times a full reading.
// Anything past it is not a reading with some slack in it, it is a different
// file — and the caller already has somewhere to put "there is nothing to show".
const maxSinkFile = 64 << 10

// ReadLimits loads the reading a sink last wrote. A missing or unreadable file is
// not an error — it means no panel has reported yet, which is the ordinary state
// of a fleet that has not run a Claude Code turn since baton was installed. A file
// past maxSinkFile is read the same way: nothing to show.
//
// paths.OpenRegular rather than os.Open, and for the half maxSinkFile does not
// cover. The size of this file was bounded; its KIND was not, and open(2) on a
// FIFO with no writer never returns. This read sits on the usage poller's one
// goroutine, so a pipe left at the name does not cost a reading — it costs the
// poller, permanently, and the footer and the quota bars stop with it. Anything
// that is not a plain file joins the missing and the oversized.
func ReadLimits(path string) (Limits, bool) {
	f, err := paths.OpenRegular(path)
	if err != nil {
		return Limits{}, false
	}
	defer func() { _ = f.Close() }()
	// One byte past the cap, so a file that ran over is known without its tail
	// ever being held.
	//
	// The length test is not redundant with the LimitReader, even though a
	// truncated JSON object would fail to parse anyway. Resting on that would make
	// the refusal a side effect of encoding/json's intolerance for a cut object
	// rather than a decision this function made, and a format less brittle than
	// JSON would quietly turn the cap into a truncation.
	b, err := io.ReadAll(io.LimitReader(f, maxSinkFile+1))
	if err != nil || len(b) > maxSinkFile {
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
	return true, replaceFile(path, b)
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

// replaceFile writes data to path through a temporary of its own and renames it
// into place, so two sinks racing from two panels cannot interleave into one
// torn file: a reader sees the old file whole or the new one whole.
//
// IT IS NOT paths.WriteFileAtomic, the house helper, and it diverges in TWO
// ways, which is why it no longer carries a name one letter away from that one.
//
// THE TEMPORARY'S NAME is the first, and is why this exists at all. The helper
// names its temp after the target, which is safe for its callers — one daemon
// writing its own state — and is not safe for a writer that runs once per panel
// per render. os.CreateTemp gives each writer a name nobody else can be holding.
//
// NO FSYNC is the second, and it is deliberate rather than forgotten. The helper
// fsyncs the file and then the parent directory, so its writes survive a power
// loss; rename(2) alone is all this one promises — one visible step, not a
// durable one. The two syncs are measured, not guessed at — 164-192 µs per write
// without them against 5.6-6.6 ms with, on an Apple M2 Pro SSD, by the benchmark
// pair beside the sink's tests — and they would be paid on the path Claude Code
// re-runs to render a panel's status line, ahead of the wrapped status line the
// user actually sees. What they would buy is a
// reading that is restamped every RefreshAfter, discarded as stale after
// StaleAfter and produced again by the next render — usagesink.go's own rules
// say a lost one "will be along again in a second". The state file the helper
// writes is a user's layout, which will not.
func replaceFile(path string, data []byte) (err error) {
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), tempPrefix+"*"+tempSuffix)
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
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	sweepStaleTemps(filepath.Dir(path), time.Now())
	return nil
}

// tempPrefix and tempSuffix bracket the name os.CreateTemp issues above, split
// out so the writer and the sweep below cannot drift apart on what a temporary
// of this package's looks like.
const (
	tempPrefix = ".usage-limits-"
	tempSuffix = ".tmp"
)

// staleTempAge is how long a temporary must have sat untouched before a writer
// will remove it. A sink write is three syscalls on a 200-byte file — the
// benchmark in replaceFile's doc puts the whole of it at 164-192 µs — so a
// minute is four orders of magnitude more than a live writer needs, and the
// gate exists only so a temporary belonging to one of the OTHER panels writing
// concurrently is never taken out from under its rename.
//
// Getting that wrong is cheap in the one direction it can go wrong. Sweeping a
// live writer's temporary makes its rename fail, and a failed sink write loses
// a reading that "will be along again in a second" — the loss this file already
// argues is acceptable, not a new one.
const staleTempAge = time.Minute

// sweepStaleTemps removes the temporaries a writer killed between os.CreateTemp
// and the rename left behind. It runs after a successful write, which is a few
// times a minute for a whole fleet rather than once per render: WriteLimitsIfChanged
// skips the redundant writes, so the one os.ReadDir this costs is not paid on
// the status line's hot path.
//
// IT IS NEEDED HERE AND NOT IN paths.WriteFileAtomic, which is the reason this
// is not simply a call to that helper. The helper's temporary is named after its
// target, so there is exactly one of them and the next write opens it O_TRUNC and
// reuses it; the debris is self-limiting. os.CreateTemp — which this file needs,
// because a name derived from the target cannot be shared by a writer running
// once per panel per render — issues a name that is never handed out twice, so
// every kill leaves a file no later write will ever touch. Killing the real
// `baton usage-sink` at randomised moments left one in 15 of 500 runs, and a
// status line is precisely the process a supervisor kills for being slow.
//
// Errors are ignored, for the reason the whole sink ignores them: a temporary
// that cannot be removed is not worth failing a status line over. REGULAR FILES
// ONLY — os.Remove takes an empty directory as readily as a file, and nothing
// this package creates under that name is a directory.
func sweepStaleTemps(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, tempPrefix) || !strings.HasSuffix(name, tempSuffix) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) < staleTempAge {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
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
