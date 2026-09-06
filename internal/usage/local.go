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
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/paths"
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
//
// A provider outlives its polls, and it has to: where a window opened cannot be
// worked out afresh every time. A scan only reaches so far back, so the oldest
// message it can see is not reliably the one that opened anything — read as one,
// it drags the window boundaries onto the edge of whatever the scan happened to
// cover, which moves as the calendar does. So the anchor is carried instead, and
// derived only when there is none to carry.
// The walk, the size caps, the dedup, the window chain and the anchor are the
// same work for any vendor that appends timestamped session logs; only the root,
// the file names and the line's shape differ. Those three are the format field,
// so a second vendor is a vendorFormat and a registry entry rather than a second
// copy of everything above.
type LocalProvider struct {
	dir    string           // the root scanned for session logs
	window time.Duration    // window length; 0 falls back to a calendar day
	now    func() time.Time // injectable clock (tests pin "now")
	format vendorFormat     // which vendor's logs these are, and how a line reads

	mu     sync.Mutex // guards anchor; a Provider is reachable from any goroutine
	anchor time.Time  // where the window last seen opened, carried across polls
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
	return newFormatProvider(claudeFormat(), window)
}

// newFormatProvider is the constructor every vendor reader goes through: the
// engine above, pointed at one vendor's logs. It is unexported because a reader
// is reached through the registry in vendor.go, never built at a call site.
func newFormatProvider(f vendorFormat, window time.Duration) *LocalProvider {
	return &LocalProvider{dir: f.root, window: window, now: time.Now, format: f}
}

// Source implements Provider.
func (p *LocalProvider) Source() string { return p.format.source }

// recall is the anchor to continue the window chain from, or the zero time when
// there is none to trust. An anchor is trusted only while it sits inside the range
// the scan covers: the chain steps from one window's start to the first message
// after that window ends, so an anchor from before the floor would step over
// messages the scan never read and land the window in the wrong place. Better then
// to start the chain over from the oldest message actually in hand.
func (p *LocalProvider) recall(floor, now time.Time) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.anchor.Before(floor) || p.anchor.After(now) {
		return time.Time{}
	}
	return p.anchor
}

// remember keeps where the chain reached, so the next poll continues it rather
// than deriving it again — which is the whole point, since deriving it again is
// what let the calendar move it. A window that has already closed is worth
// keeping too: it is the link the next message chains off. Only a chain that
// found nothing at all leaves the anchor alone, so a quiet stretch does not
// discard a perfectly good one.
func (p *LocalProvider) remember(start time.Time) {
	if start.IsZero() {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.anchor = start
}

// usageKey is the cheap substring gate: only lines that mention a usage block are
// worth JSON-parsing, and most transcript lines (user turns, tool results) do not.
var usageKey = []byte(`"usage"`)

// vendorFormat is everything about one vendor's session logs that the engine
// cannot share: where they live, which files inside carry usage, the substring
// that makes a line worth parsing, and how one line decodes.
//
// Keeping it to four fields is the point. Everything a usage reader gets wrong
// under load — unbounded lines, FIFOs on a path baton does not own, the same
// message counted from two files, a window anchored on the clock instead of on a
// message — is in the engine and is written once. A vendor supplies only what is
// genuinely its own.
type vendorFormat struct {
	source string // the name this reader reports on Snapshot.Source
	root   string // the directory walked for logs
	only   string // the base filename that carries usage; "" means every .jsonl
	gate   []byte // the substring a line must contain to be worth decoding

	// decode turns one line into a record, or reports that it carries no usage.
	// It does no filtering: the cutoff, the ceiling and the dedup are the engine's,
	// so every vendor gets them and none can forget one.
	decode func(line []byte) (record, bool)
}

// record is one decoded usage line, in the terms every vendor shares.
//
// cost is the vendor's own figure where the vendor states one, and baton's
// per-model arithmetic only where it does not. The distinction is not cosmetic: a
// price table baked into baton goes stale the day the vendor reprices, and a
// reader that is handed the number has no reason to keep one.
type record struct {
	ts  time.Time
	key string // dedup key across files; "" means the line can never be a duplicate

	input, output, cacheRead, cacheWrite int64
	cost                                 float64
}

// claudeFormat is the Claude Code transcript reader: one JSONL file per session
// under the projects root, one line per message, usage on the assistant turns.
func claudeFormat() vendorFormat {
	return vendorFormat{
		source: "local",
		root:   claudeProjectsDir(),
		gate:   usageKey,
		decode: decodeClaude,
	}
}

// Fetch scans the transcripts for the assistant messages inside the current
// window and sums their token usage, pricing each message by its own model.
// Files not touched since the scan floor are skipped whole — an append-only
// transcript last written before it cannot hold a message after it — which keeps
// a fleet of hundreds of sessions down to reading only the active few.
//
// The floor is a day plus a window back, not a window, so the read is up to six
// times the files just before midnight and shrinks through the day. That is the
// price of the floor being a calendar instant: it has to hold still while the
// clock moves, or the chain it seeds moves with the clock. It is paid on a poll
// every thirty seconds, against files an append-only writer leaves cheap to skip.
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
		// can have opened that early, and the chain has to see the message that opened
		// it whenever there is no anchor to continue from.
		cutoff = cutoff.Add(-p.window)
	}
	sc := newFormatScan(cutoff, now, p.format)

	err := filepath.WalkDir(p.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable dir/file is skipped, not fatal to the whole scan
		}
		if d.IsDir() || !sc.format.carries(path) {
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
		// A halted walk read some files and not others, so its total spans neither a
		// window nor a day — it is a fraction of one, with no way to say which. Report
		// nothing and let the caller hold whatever it had; a number that looks like a
		// reading but under-counts by an unknown amount is the one thing worse.
		return Snapshot{Source: "local"}, err
	}
	// A dropped line is spend this reading does not carry, so the reading is a
	// little low and nothing on screen says why. Once per poll, with a count.
	if sc.oversized > 0 {
		log.Warn().Int("lines", sc.oversized).Int("limit", maxTranscriptLine).
			Msg("usage under-counts: transcript lines past the size limit were skipped")
	}
	if p.window <= 0 {
		return sc.snapshot(cutoff), nil // the calendar-day fallback: totals, no reset
	}
	start, open := sc.window(now, p.window, p.recall(cutoff, now))
	p.remember(start)
	if !open {
		// Every window the chain reached has already closed, and the next one opens on
		// the next message. Report nothing rather than a countdown to a window the
		// account is no longer in — and rather than the spend of a window that is over,
		// which would read as this window's. Since stays zero: there is no window for it
		// to be the start of, and the scan floor is not one.
		return Snapshot{Source: "local"}, nil
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

// scan is the running state of one Fetch: the floor and the ceiling every message
// is tested against, the dedup set shared across every file, and the messages kept
// so far. The dedup set has to span the whole walk, not one file, because the same
// message can appear in two transcripts (see fold).
//
// entries buffers every message in the whole floor..now range, not just the ones
// that end up counted — which window a message falls in is not known until the
// walk is over. That is one struct per assistant turn across a day plus a window,
// paid once per poll and released with the scan.
type scan struct {
	cutoff  time.Time
	now     time.Time
	format  vendorFormat
	seen    map[string]struct{}
	entries []counted

	// oversized counts the lines dropped for running past maxTranscriptLine. It
	// is kept so the drop can be said once per poll instead of once per line: the
	// thing that produces an oversized line tends to produce a lot of them, and a
	// log line each would be the disk-filling this cap exists to stop.
	oversized int
}

func newScan(cutoff, now time.Time) *scan {
	return newFormatScan(cutoff, now, claudeFormat())
}

// newFormatScan is newScan for a named vendor's log format.
func newFormatScan(cutoff, now time.Time, f vendorFormat) *scan {
	return &scan{cutoff: cutoff, now: now, format: f, seen: make(map[string]struct{})}
}

// carries reports whether a walked path is a file this format keeps usage in.
//
// The `only` filter is not an optimisation. A vendor that writes several JSONL
// files per session — a chat history, an event log, a usage log — has the same
// turn described in more than one of them, and reading them all would count the
// spend once per file that happens to mention it. Naming the one file that is the
// accounting record is how a reader says which of them is the books.
func (f vendorFormat) carries(path string) bool {
	if f.only != "" {
		return filepath.Base(path) == f.only
	}
	return strings.HasSuffix(path, ".jsonl")
}

// window is the window in progress at now. The chain starts at anchor — where the
// caller last saw a window open — and steps forward: a message landing at or after
// the running window's end opens the next one. With no anchor to continue from it
// starts at the oldest message in hand, which is only a guess at where a window
// opened, and is the reason an anchor is carried at all.
//
// It reports false once the chain's last window has closed with nothing after it:
// the next window opens on the next message, and until that lands there is nothing
// to count down to. A start the clock has not reached yet is refused as well: a
// window that has not begun is not the one in progress, and counting down to its
// end would report more time left than a window even is long.
//
// Anchoring on a message rather than on "now minus the window" is the whole
// point. A sliding anchor drags the window's end along with the clock, so under
// continuous use the reset is always a moment away: the countdown reads zero,
// stays zero, and never runs a window down and starts the next one.
func (sc *scan) window(now time.Time, length time.Duration, anchor time.Time) (start time.Time, open bool) {
	sort.Slice(sc.entries, func(i, j int) bool { return sc.entries[i].ts.Before(sc.entries[j].ts) })
	start = anchor
	if start.IsZero() {
		if len(sc.entries) == 0 {
			return time.Time{}, false
		}
		start = sc.entries[0].ts
	}
	for _, e := range sc.entries {
		if !e.ts.Before(start.Add(length)) {
			start = e.ts
		}
	}
	return start, !now.Before(start) && now.Before(start.Add(length))
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

// maxTranscriptLine caps how many bytes ONE transcript line may cost the daemon.
//
// bufio.Scanner's 64 KiB default is far too small here — a single line genuinely
// can carry a pasted image — but "too small" is not an argument for no cap at all,
// which is what this reader had. These files are written by a process baton does
// not control, on a path an agent panel can also write to, and the daemon rereads
// them on a poll every thirty seconds. Driven with one 256 MiB line in a transcript
// under the scan floor: 519 MiB allocated inside Fetch, and again on the next poll.
//
// Sixteen mebibytes, and the argument is the pasted image the unbounded reader was
// there for. The API's own per-image ceiling is about 5 MB, which is ~6.7 MB once
// base64 has had it, so 16 MiB holds two maximal images and the message wrapped
// round them. Past that a line is not a message any more.
const maxTranscriptLine = 16 << 20

// transcript folds one transcript file's in-window usage in, crediting it to
// session. It reads line by line, bounded at maxTranscriptLine, and only parses
// lines that mention usage.
// paths.OpenRegular rather than os.Open, for the half maxTranscriptLine does not
// cover: the line length was bounded, the file's KIND was not. The walk lists
// whatever is in the projects tree and takes anything ending .jsonl, and open(2)
// on a FIFO with no writer never returns — so one pipe named like a transcript
// parks the usage poller's goroutine for the daemon's life, taking the footer
// and the quota bars with it. A file that is not a plain file is skipped, which
// is what the walk already does with every other unreadable entry.
func (sc *scan) transcript(path, session string) {
	f, err := paths.OpenRegular(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	r := bufio.NewReader(f)
	for {
		line, over, err := cappedLine(r)
		if over {
			sc.oversized++
		}
		if len(line) > 0 && bytes.Contains(line, sc.format.gate) {
			sc.fold(line, session)
		}
		if err != nil {
			return // io.EOF or a read error: either way, done with this file
		}
	}
}

// cappedLine reads the next newline-terminated line, or reports it as oversized
// and returns nothing for it.
//
// A line past the cap is DROPPED rather than truncated, and the reader is left at
// the head of the next one. Truncating would hand fold a JSON fragment that is
// unparseable anyway, and a torn fragment that happened to parse would be worse:
// it would be counted. Dropping costs one message's tokens out of a figure that is
// a footer reading, and the caller says so out loud rather than under-counting in
// silence.
func cappedLine(r *bufio.Reader) (line []byte, over bool, err error) {
	for {
		// ReadSlice rather than ReadBytes: it hands back a view of the reader's own
		// buffer, so a line being discarded is never copied anywhere.
		frag, ferr := r.ReadSlice('\n')
		if !over && len(line)+len(frag) > maxTranscriptLine {
			over, line = true, nil // release what was held before giving up on it
		}
		if !over {
			line = append(line, frag...)
		}
		if ferr != bufio.ErrBufferFull {
			return line, over, ferr
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
	r, ok := sc.format.decode(line)
	if !ok {
		return
	}
	sc.keep(r, session)
}

// keep places one decoded record in the scan, or drops it. Every test a record
// has to pass lives here rather than in a decoder, so a new vendor cannot ship
// without the cutoff, the ceiling or the dedup.
func (sc *scan) keep(r record, session string) {
	if r.ts.Before(sc.cutoff) {
		return
	}
	if r.ts.After(sc.now) {
		// A message stamped after now — a clock corrected backwards, a vendor directory
		// synced from a machine running ahead — cannot be placed in a window that has
		// begun. Counted, it would open a window in the future and leave the countdown
		// showing more time than a window is long.
		return
	}
	if r.key != "" {
		if _, dup := sc.seen[r.key]; dup {
			return
		}
		sc.seen[r.key] = struct{}{}
	}
	sc.entries = append(sc.entries, counted{
		ts:         r.ts,
		session:    session,
		input:      r.input,
		output:     r.output,
		cacheRead:  r.cacheRead,
		cacheWrite: r.cacheWrite,
		cost:       r.cost,
	})
}

// decodeClaude reads one Claude Code transcript line. Cost is baton's own
// arithmetic here because the transcript states tokens and a model but no price.
func decodeClaude(line []byte) (record, bool) {
	var e transcriptEntry
	if json.Unmarshal(line, &e) != nil || e.Message.Usage == nil {
		return record{}, false
	}
	ts, err := time.Parse(time.RFC3339, e.Timestamp)
	if err != nil {
		return record{}, false
	}
	key := ""
	if e.Message.ID != "" || e.RequestID != "" {
		key = e.Message.ID + "|" + e.RequestID
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
	return record{
		ts:         ts,
		key:        key,
		input:      u.InputTokens,
		output:     u.OutputTokens,
		cacheRead:  u.CacheReadInputTokens,
		cacheWrite: u.CacheCreationInputTokens,
		cost:       costUSD(e.Message.Model, tu),
	}, true
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
