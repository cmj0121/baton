package usage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cmj0121/baton/internal/paths"
)

// Codex's own rate-limit windows. Like groklimits.go this is a different
// measurement from the token reader beside it, not a better one: codex.go counts
// the tokens baton can attribute to a project, and this file repeats what codex
// was last told about the account's ceilings. Unlike grok there is no endpoint to
// ask. Every token_count event codex writes into a rollout carries a rate_limits
// block — the server's answer as of that model response — and the newest such
// block on disk is the reading.
//
// Two things follow from where the figure comes from, and both are limits of the
// reading rather than bugs in it:
//
//   - It reflects only this machine's last codex turn. Codex spent on another
//     machine, or in the web app, moves the account's real standing and moves
//     nothing here until a local turn writes a fresh block.
//   - It ages. A block written yesterday is yesterday's percentage. A window
//     whose own resets_at has already passed has certainly rolled over since, and
//     is dropped rather than shown at a fill it no longer has; a window that has
//     not reset yet is shown as last stated, which may understate it.
//
// The labels come from window_minutes, not from which slot a window sits in.
// 10080 is a week, and that is what this account's rollouts show in primary. 300
// as the five-hour window is UNVERIFIED: no local rollout has carried a
// secondary yet, so the mapping is the obvious reading of a five-hour throttle,
// not one checked against codex. Any other length is skipped rather than forced
// into a column that would misname it.

// codexLimitsKey is the substring gate, quoted to the marks for the same reason
// codexUsageKey is: the big response_item lines may mention the word, and only a
// line whose own type it is carries it unescaped.
var codexLimitsKey = []byte(`"type":"token_count"`)

// codexLimitsFiles is how many of the newest rollouts are opened looking for a
// rate_limits block. The newest one almost always has it; the rest cover a
// rollout that was just created and has not finished its first turn.
const codexLimitsFiles = 3

// codexLimitsTail is how much of each rollout's end is read. The newest block is
// near the end of the file by construction — codex appends — and a rollout can
// run to hundreds of megabytes, so reading from the top would spend a full scan
// on a figure that sits in the last few lines.
const codexLimitsTail = 4 << 20

// codexLimitsHorizon is how far back a rollout's mtime may lie and still be
// opened. It is the longest window codex states: every block in a file last
// written before it describes windows that have all reset since, and would be
// dropped by the reset check anyway.
const codexLimitsHorizon = 7 * 24 * time.Hour

// Window lengths codex states, in minutes, and the labels they carry.
const (
	codexMinutesFiveHour = 300
	codexMinutesWeek     = 10080
)

// codexRateWindow is one of the rate_limits slots.
type codexRateWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

// codexLimitsLine is the slice of a token_count event this reader wants. Either
// slot may be null, and is then a nil pointer rather than a zero window.
type codexLimitsLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type       string `json:"type"`
		RateLimits *struct {
			Primary   *codexRateWindow `json:"primary"`
			Secondary *codexRateWindow `json:"secondary"`
		} `json:"rate_limits"`
	} `json:"payload"`
}

// codexReading is one parsed rate_limits block and when codex wrote it.
type codexReading struct {
	ts    time.Time
	slots []*codexRateWindow
}

// CodexWindows is codex's own rate-limit windows as the newest local rollout
// states them, and whether there are any. ctx bounds the walk: a cancelled poll
// stops listing the tree and reports no reading rather than a partial one.
func CodexWindows(ctx context.Context) ([]VendorWindow, bool) {
	return codexWindowsAt(ctx, codexSessionsDir(), time.Now())
}

// codexWindowsAt reads the windows under root as of now.
//
// The newest reading is chosen by file mtime first and line timestamp second.
// The date in a rollout's path is when its session BEGAN: `codex resume` appends
// to the old date-named file, so a resumed session's rollout under last week's
// directory can hold a newer block than today's. The mtime is when codex last
// wrote, which is the question being asked. The files are tried newest first and
// the first one that holds any block wins.
func codexWindowsAt(ctx context.Context, root string, now time.Time) ([]VendorWindow, bool) {
	for _, path := range newestRollouts(ctx, root, now.Add(-codexLimitsHorizon), codexLimitsFiles) {
		r, ok := newestCodexReading(path)
		if !ok {
			continue
		}
		ws := r.windows(now)
		return ws, len(ws) > 0
	}
	return nil, false
}

type rolloutFile struct {
	path  string
	mtime time.Time
}

// newestRollouts lists the n most recently written rollout-*.jsonl files under
// root that were written at or after cutoff. Unreadable entries are skipped, the
// same as the token scan skips them; a cancelled ctx ends the walk with nothing.
//
// The cutoff keeps the sort to the files that could matter, but the walk itself
// still lists every directory. A date directory cannot be pruned by its own
// mtime: appending to a rollout — which is all `codex resume` does — moves the
// file's mtime and not its directory's, so last month's directory with a stale
// mtime can hold the freshest rollout on disk. Listing is cheap next to opening;
// the files outside the cutoff are stat'd and never opened.
func newestRollouts(ctx context.Context, root string, cutoff time.Time, n int) []string {
	var files []rolloutFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		if fi, err := d.Info(); err == nil && !fi.ModTime().Before(cutoff) {
			files = append(files, rolloutFile{path: path, mtime: fi.ModTime()})
		}
		return nil
	})
	if err != nil {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.After(files[j].mtime) })
	out := make([]string, 0, min(n, len(files)))
	for i := 0; i < len(files) && i < n; i++ {
		out = append(out, files[i].path)
	}
	return out
}

// newestCodexReading is the latest-timestamped rate_limits block in the tail of
// one rollout.
//
// It opens through paths.OpenRegular for the reason every reader here does — a
// FIFO named like a rollout must not park the caller — and reads only the last
// codexLimitsTail bytes. A seek lands mid-line, so the fragment before the first
// newline is discarded rather than handed to the decoder.
func newestCodexReading(path string) (codexReading, bool) {
	f, err := paths.OpenRegular(path)
	if err != nil {
		return codexReading{}, false
	}
	defer func() { _ = f.Close() }()

	r := bufio.NewReader(f)
	if fi, err := f.Stat(); err == nil && fi.Size() > codexLimitsTail {
		if _, err := f.Seek(fi.Size()-codexLimitsTail, io.SeekStart); err != nil {
			return codexReading{}, false
		}
		r.Reset(f)
		if _, _, err := cappedLine(r); err != nil {
			return codexReading{}, false
		}
	}

	var best codexReading
	found := false
	for {
		line, over, err := cappedLine(r)
		if !over && len(line) > 0 && bytes.Contains(line, codexLimitsKey) {
			if c, ok := decodeCodexLimits(line); ok && (!found || !c.ts.Before(best.ts)) {
				best, found = c, true
			}
		}
		if err != nil {
			return best, found
		}
	}
}

// decodeCodexLimits reads one token_count event's rate_limits block. A block
// with both slots null is a reading of nothing, and is not one.
func decodeCodexLimits(line []byte) (codexReading, bool) {
	var c codexLimitsLine
	if json.Unmarshal(line, &c) != nil || c.Type != "event_msg" || c.Payload.Type != "token_count" ||
		c.Payload.RateLimits == nil {
		return codexReading{}, false
	}
	ts, err := time.Parse(time.RFC3339, c.Timestamp)
	if err != nil {
		return codexReading{}, false
	}
	rl := c.Payload.RateLimits
	if rl.Primary == nil && rl.Secondary == nil {
		return codexReading{}, false
	}
	return codexReading{ts: ts, slots: []*codexRateWindow{rl.Primary, rl.Secondary}}, true
}

// windows labels the block's slots by their length, five-hour first so the list
// reads in the order the cockpit's columns do, and drops what cannot be said.
func (r codexReading) windows(now time.Time) []VendorWindow {
	var five, week []VendorWindow
	for _, s := range r.slots {
		if s == nil {
			continue
		}
		var reset time.Time // zero when codex states none
		if s.ResetsAt > 0 {
			reset = time.Unix(s.ResetsAt, 0)
			if !reset.After(now) {
				continue // rolled over since codex said this; the fill is no longer true
			}
		}
		w := VendorWindow{Fraction: (&Window{UsedPercent: s.UsedPercent}).Fraction(), ResetsAt: reset}
		switch s.WindowMinutes {
		case codexMinutesFiveHour:
			w.Label = WindowFiveHour
			five = append(five, w)
		case codexMinutesWeek:
			w.Label = WindowWeek
			week = append(week, w)
		}
	}
	return append(five, week...)
}
