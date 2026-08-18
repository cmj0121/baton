package usage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LocalProvider reads Claude Code's session transcripts and aggregates the token
// usage inside the current window. Every Claude Code run — baton's own agent
// panels included — appends a JSONL transcript under
// $HOME/.claude/projects/<project>/<session>.jsonl, one line per message, with
// the assistant messages carrying a `usage` block.
//
// Because the transcripts are timestamped, this is the one source that can infer
// where the window opened: the message that opened it. That makes the reset a
// real countdown rather than a guess, and it is why a personal Pro/Max
// subscription — whose usage never reaches the Admin API — is exactly the case
// this source serves.
type LocalProvider struct {
	dir    string           // the .../projects root scanned for transcripts
	window time.Duration    // window length; 0 falls back to a calendar day
	now    func() time.Time // injectable clock (tests pin "now")
}

// NewLocalProvider builds a local source rooted at the user's Claude Code project
// transcripts. CLAUDE_CONFIG_DIR overrides the ~/.claude location, matching Claude
// Code's own env override.
//
// window is how long a window lasts once a message opens one. Zero (or negative)
// opts out of the countdown entirely and reports a calendar day instead — a plan
// that bills on something baton cannot model is better served by no countdown
// than a wrong one.
func NewLocalProvider(window time.Duration) *LocalProvider {
	return &LocalProvider{dir: claudeProjectsDir(), window: window, now: time.Now}
}

// Source implements Provider.
func (p *LocalProvider) Source() string { return "local" }

// usageKey is the cheap substring gate: only lines that mention a usage block are
// worth JSON-parsing, and most transcript lines (user turns, tool results) do not.
var usageKey = []byte(`"usage"`)

// Fetch scans the transcripts for the assistant messages inside the current
// window and sums their token usage, pricing each message by its own model.
// Files not touched since the scan floor are skipped whole — an append-only
// transcript last written before it cannot hold a message after it — which keeps
// a fleet of hundreds of sessions down to reading only the active few.
//
// The window opens at a message and lasts a fixed length from there, so the
// countdown runs that length down and the next message opens the next window.
// Once the last window has closed with nothing after it, there is no window in
// progress, and the snapshot says so rather than opening one at "now".
func (p *LocalProvider) Fetch(ctx context.Context) (Snapshot, error) {
	now := p.now()
	cutoff := startOfDay(now)
	if p.window > 0 {
		// Reach one whole window back past the day's start: a window still running now
		// can have opened that early, and the scan has to see the message that opened
		// it to anchor on it. The floor is a calendar instant rather than "now minus a
		// window" on purpose — an anchor that slides with the clock drags the reset
		// along with it, which is exactly how a countdown ends up pinned at zero.
		cutoff = cutoff.Add(-p.window)
	}
	sc := newScan(cutoff)

	err := filepath.WalkDir(p.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable dir/file is skipped, not fatal to the whole scan
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if info, ierr := d.Info(); ierr != nil || info.ModTime().Before(cutoff) {
			return nil // no message inside the window can live in a file last written before it
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sc.transcript(path, sessionOf(p.dir, path))
		return nil
	})
	// A missing projects dir (Claude Code never run here) is not an error — it just
	// means zero usage. WalkDir surfaces it via the root callback, which we ignore.
	if err != nil && !os.IsNotExist(err) {
		return sc.snapshot(cutoff), err
	}
	if p.window <= 0 {
		return sc.snapshot(cutoff), nil // the calendar-day fallback: totals, no reset
	}
	start, open := sc.window(now, p.window)
	if !open {
		// Every window the scan saw has already closed, and the next one opens on the
		// next message. Report nothing rather than a countdown to a window the account
		// is no longer in — and rather than the spend of a window that is over, which
		// would read as this window's.
		return Snapshot{Since: cutoff, Source: "local"}, nil
	}
	snap := sc.snapshot(start)
	snap.Until, snap.Resets = start.Add(p.window), true
	return snap, nil
}

// sessionOf is the session id a transcript belongs to, taken from its path under
// the projects root: <root>/<project>/<session>.jsonl for a session's own
// transcript, and <root>/<project>/<session>/subagents/agent-*.jsonl for the
// subagents it spawned. Both fold into the same id on purpose — a panel's
// subagents are that panel's spend, not somebody else's. A path that does not fit
// the layout yields "", which buckets as unattributed rather than guessing.
func sessionOf(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSuffix(parts[1], ".jsonl")
}

// counted is one deduplicated in-scope message: when it happened, which session
// spent it, and what it came to. The scan keeps these instead of summing as it
// goes because which window a message belongs to is not known until the walk is
// over — the window opens at a message, and which message that is only settles
// once every file has been read.
type counted struct {
	ts         time.Time
	session    string
	input      int64
	output     int64
	cacheRead  int64
	cacheWrite int64
	cost       float64
}

// scan is the running state of one Fetch: the floor every message is tested
// against, the dedup set shared across every file, and the messages kept so far.
// The dedup set has to span the whole walk, not one file, because the same
// message can appear in two transcripts (see fold).
type scan struct {
	cutoff  time.Time
	seen    map[string]struct{}
	entries []counted
}

func newScan(cutoff time.Time) *scan {
	return &scan{cutoff: cutoff, seen: make(map[string]struct{})}
}

// window is the window in progress at now: the messages are walked in time order,
// and one that lands at or after the running window's end opens the next one, so
// the start is always a message that really did open a window. It reports false
// once the last window has closed with nothing after it — the next window opens
// on the next message, and until that lands there is nothing to count down to.
//
// Anchoring on a message rather than on "now minus the window" is the whole
// point. A sliding anchor drags the window's end along with the clock, so under
// continuous use the reset is always a moment away: the countdown reads zero,
// stays zero, and never runs a window down and starts the next one.
func (sc *scan) window(now time.Time, length time.Duration) (start time.Time, open bool) {
	if len(sc.entries) == 0 {
		return time.Time{}, false
	}
	sort.Slice(sc.entries, func(i, j int) bool { return sc.entries[i].ts.Before(sc.entries[j].ts) })
	start = sc.entries[0].ts
	for _, e := range sc.entries[1:] {
		if !e.ts.Before(start.Add(length)) {
			start = e.ts
		}
	}
	return start, now.Before(start.Add(length))
}

// snapshot sums every message from since onward, totals and per-session alike. A
// message before it belongs to a window that has already closed, and folding one
// in would carry a finished window's spend into the current one — which is the
// number the whole footer is read off.
func (sc *scan) snapshot(since time.Time) Snapshot {
	snap := Snapshot{Since: since, Source: "local"}
	for _, e := range sc.entries {
		if e.ts.Before(since) {
			continue
		}
		snap.Input += e.input
		snap.Output += e.output
		snap.CacheRead += e.cacheRead
		snap.CacheWrite += e.cacheWrite
		snap.CostUSD += e.cost
		if e.session == "" {
			continue // a path we cannot attribute; it still counts toward the totals
		}
		if snap.Sessions == nil {
			snap.Sessions = make(map[string]SessionUsage)
		}
		b := snap.Sessions[e.session]
		b.Tokens += e.input + e.output + e.cacheRead + e.cacheWrite
		b.CostUSD += e.cost
		snap.Sessions[e.session] = b
	}
	return snap
}

// transcript folds one transcript file's in-window usage in, crediting it to
// session. It reads line by line with an unbounded reader (a single line can carry
// a pasted image and blow past bufio.Scanner's token cap), and only parses lines
// that mention usage.
func (sc *scan) transcript(path, session string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && bytes.Contains(line, usageKey) {
			sc.fold(line, session)
		}
		if err != nil {
			return // io.EOF or a read error: either way, done with this file
		}
	}
}

// transcriptEntry is the slice of a transcript line we read: the timestamp, the
// dedup keys, and the assistant message's model + usage.
type transcriptEntry struct {
	Timestamp string `json:"timestamp"`
	RequestID string `json:"requestId"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheCreation            *struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

// fold parses one line and, if it is an in-scope assistant message not already
// counted, keeps its tokens and cost for the window it lands in.
//
// Duplicate lines are keyed out by message id + request id, which matters more
// than it looks: forking a session (--fork-session) replays the parent's turns
// verbatim into the new transcript, keeping their original ids and timestamps. So
// the same spend genuinely appears in two files, and without the dedup it would
// be counted — and attributed — twice.
func (sc *scan) fold(line []byte, session string) {
	var e transcriptEntry
	if json.Unmarshal(line, &e) != nil || e.Message.Usage == nil {
		return
	}
	ts, err := time.Parse(time.RFC3339, e.Timestamp)
	if err != nil || ts.Before(sc.cutoff) {
		return
	}
	if e.Message.ID != "" || e.RequestID != "" {
		key := e.Message.ID + "|" + e.RequestID
		if _, dup := sc.seen[key]; dup {
			return
		}
		sc.seen[key] = struct{}{}
	}
	u := e.Message.Usage
	tu := tokenUsage{Uncached: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens}
	if u.CacheCreation != nil {
		tu.CacheWrite5m = u.CacheCreation.Ephemeral5m
		tu.CacheWrite1h = u.CacheCreation.Ephemeral1h
	} else {
		// No tier breakdown: price the whole cache write at the 5-minute rate, the
		// common default, rather than dropping it.
		tu.CacheWrite5m = u.CacheCreationInputTokens
	}
	sc.entries = append(sc.entries, counted{
		ts:         ts,
		session:    session,
		input:      u.InputTokens,
		output:     u.OutputTokens,
		cacheRead:  u.CacheReadInputTokens,
		cacheWrite: u.CacheCreationInputTokens,
		cost:       costUSD(e.Message.Model, tu),
	})
}

// claudeProjectsDir locates Claude Code's transcript root: $CLAUDE_CONFIG_DIR/projects
// when set (Claude Code's own override), else ~/.claude/projects.
func claudeProjectsDir() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return filepath.Join(v, "projects")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".claude", "projects")
	}
	return filepath.Join(".claude", "projects")
}
