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

// LocalProvider reads an agent CLI's own session logs off the disk and aggregates
// the token usage inside the current window. Which CLI's logs, and how a line in
// them reads, is the format field; the default is Claude Code's, whose every run —
// baton's own agent panels included — appends a JSONL transcript under
// $HOME/.claude/projects/<project>/<session>.jsonl, one line per message, with
// the assistant messages carrying a `usage` block.
//
// Because the logs are timestamped, this is the one kind of source that can infer
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
//
// The walk, the size caps, the dedup, the window chain and the anchor are the
// same work for any vendor that appends timestamped session logs; only the root,
// the file names and the line's shape differ. Those three are the format, so a
// second vendor is a vendorFormat and a registry entry rather than a second copy
// of everything above.
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
// that makes a line worth parsing, how one line decodes, and what a project
// directory's name says about the project.
//
// Keeping it this small is the point. Everything a usage reader gets wrong
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

	// project reads a project directory's name — the first path segment under the
	// root — as the project's path, or "" when the name cannot be read back into
	// one. Nil means the name never can: Claude Code's encoding turns every "/" and
	// "." into "-", so only a cwd the logs state is trusted for it.
	project func(dir string) string

	// dirName is the project directory name the vendor gives a cwd, for checking a
	// stated cwd against the directory it was found in. Nil when the format states
	// no cwd to check.
	dirName func(cwd string) string
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
	cwd string // the working directory the line states; "" when the format has none

	input, output, cacheRead, cacheWrite int64
	cost                                 float64
}

// claudeFormat is the Claude Code transcript reader: one JSONL file per session
// under the projects root, one line per message, usage on the assistant turns.
func claudeFormat() vendorFormat {
	return vendorFormat{
		source:  "local",
		root:    claudeProjectsDir(),
		gate:    usageKey,
		decode:  decodeClaude,
		dirName: claudeDirName,
	}
}

// claudeDirName is the project directory Claude Code keeps a cwd's transcripts
// under: every character that is not an ASCII letter or digit becomes "-", so
// /Users/me/my.repo is -Users-me-my-repo. The CLI does that with a JavaScript
// regex over a JavaScript string, which works per UTF-16 code unit: a CJK
// character is one "-", not the three its UTF-8 bytes would make, and a
// character past U+FFFF — an emoji — is a surrogate pair and so two.
//
// It only runs forward — the directory name cannot be read back, since "-"
// stood for many things. Newer Claude Code also truncates a long name and appends
// a hash; no cwd encodes to that, so such a directory falls back to the first cwd
// seen, which is what every directory did before this check existed.
func claudeDirName(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
			b.WriteRune(r)
		case r > 0xFFFF:
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
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
	sc, err := p.walk(ctx, cutoff, now)
	if err != nil {
		return Snapshot{Source: p.format.source}, err
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
		return Snapshot{Source: p.format.source}, nil
	}
	snap := sc.snapshot(start)
	snap.Until, snap.Resets = start.Add(p.window), true
	return snap, nil
}

// Since sums every message from since up to now, with the same walk, dedup,
// ceiling and line cap as Fetch and none of its window chain: the caller names
// the period, so there is no window to infer and no anchor to carry. It is how a
// week's spend is read — a period the caller takes from the vendor's own quota
// reset, which the logs cannot know.
//
// The snapshot has no Until and no reset. A week figure is a total over a stated
// range, and a countdown on it would claim a window baton did not measure.
func (p *LocalProvider) Since(ctx context.Context, since time.Time) (Snapshot, error) {
	sc, err := p.walk(ctx, since, p.now())
	if err != nil {
		return Snapshot{Source: p.format.source}, err
	}
	return sc.snapshot(since), nil
}

// walk reads every log this format carries that could hold a message in
// [cutoff, now], and returns the scan holding them. It is the one walk both
// Fetch and Since go through, so the FIFO guard, the line cap, the mtime skip and
// the dedup cannot drift apart between the window figure and the week figure.
//
// Files not touched since cutoff are skipped whole: an append-only log last
// written before it cannot hold a message after it.
//
// A non-nil error means the walk halted partway. It read some files and not
// others, so its total spans no period anyone can name — it is a fraction of one,
// with no way to say which. The caller reports nothing and holds whatever it had;
// a number that looks like a reading but under-counts by an unknown amount is the
// one thing worse.
func (p *LocalProvider) walk(ctx context.Context, cutoff, now time.Time) (*scan, error) {
	sc := newFormatScan(cutoff, now, p.format)
	err := filepath.WalkDir(p.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable dir/file is skipped, not fatal to the whole scan
		}
		if d.IsDir() || !sc.format.carries(path) {
			return nil
		}
		if info, ierr := d.Info(); ierr != nil || info.ModTime().Before(cutoff) {
			return nil // no message inside the range can live in a file last written before it
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		project, session := originOf(p.dir, path)
		sc.transcript(path, project, session)
		return nil
	})
	// A missing projects dir (the CLI never run here) is not an error — it just
	// means zero usage. WalkDir surfaces it via the root callback, which we ignore.
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	// A dropped line is spend this reading does not carry, so the reading is a
	// little low and nothing on screen says why. Once per scan, with a count.
	if sc.oversized > 0 {
		log.Warn().Int("lines", sc.oversized).Int("limit", maxTranscriptLine).
			Msg("usage under-counts: transcript lines past the size limit were skipped")
	}
	return sc, nil
}

// originOf is the project directory and the session id a log belongs to, taken
// from its path under the root: <root>/<project>/<session>.jsonl for a session's
// own transcript, and <root>/<project>/<session>/subagents/agent-*.jsonl for the
// subagents it spawned. Both fold into the same session on purpose — a panel's
// subagents are that panel's spend, not somebody else's — and so into the same
// project. A path that does not fit the layout yields "" for both, which buckets
// as unattributed rather than guessing.
func originOf(root, path string) (project, session string) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return "", ""
	}
	return parts[0], strings.TrimSuffix(parts[1], ".jsonl")
}

// counted is one deduplicated in-scope message: when it happened, which session
// spent it, and what it came to. The scan keeps these instead of summing as it
// goes because which window a message belongs to is not known until the walk is
// over — the window opens at a message, and which message that is only settles
// once every file has been read.
type counted struct {
	ts         time.Time
	project    string // the project directory, the key a label is looked up by
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

	// cwds is the working directory a project directory's hint names, keyed by
	// that directory: the first cwd whose encoding is the directory's name (named
	// then records that it matched), else the first cwd seen. It is learned from
	// every line the gate lets through, in range or not, because the label is a
	// fact about the directory and not about the window: a project whose only
	// in-range line happens to lack a cwd is still the project its older lines named.
	cwds  map[string]string
	named map[string]bool

	// oversized counts the lines dropped for running past maxTranscriptLine. It
	// is kept so the drop can be said once per scan instead of once per line: the
	// thing that produces an oversized line tends to produce a lot of them, and a
	// log line each would be the disk-filling this cap exists to stop.
	oversized int
}

func newScan(cutoff, now time.Time) *scan {
	return newFormatScan(cutoff, now, claudeFormat())
}

// newFormatScan is newScan for a named vendor's log format.
func newFormatScan(cutoff, now time.Time, f vendorFormat) *scan {
	return &scan{cutoff: cutoff, now: now, format: f, seen: make(map[string]struct{}),
		cwds: make(map[string]string), named: make(map[string]bool)}
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

// snapshot sums every message from since onward: totals, per session and per
// project directory. A message before it belongs to a window that has already
// closed, and folding one in would carry a finished window's spend into the
// current one — which is the number the whole footer is read off.
//
// Every message lands in exactly one project directory, UnattributedProject
// included, so the project values sum to the totals the same way the whole
// snapshot does. The directories stay raw here, each with what the scan learned
// of its path: naming them is LabelProjects' job, done once over every scan's
// directories, so one directory cannot be named two ways by two scans.
func (sc *scan) snapshot(since time.Time) Snapshot {
	snap := Snapshot{Since: since, Source: sc.format.source}
	for _, e := range sc.entries {
		if e.ts.Before(since) {
			continue
		}
		snap.Input += e.input
		snap.Output += e.output
		snap.CacheRead += e.cacheRead
		snap.CacheWrite += e.cacheWrite
		snap.CostUSD += e.cost
		tokens := e.input + e.output + e.cacheRead + e.cacheWrite

		if snap.Projects == nil {
			snap.Projects = make(map[string]SessionUsage)
		}
		dir := e.project
		if dir == "" {
			dir = UnattributedProject
		} else if _, ok := snap.ProjectHints[dir]; !ok {
			if snap.ProjectHints == nil {
				snap.ProjectHints = make(map[string]ProjectHint)
			}
			snap.ProjectHints[dir] = sc.hint(dir)
		}
		pb := snap.Projects[dir]
		pb.Tokens += tokens
		pb.CostUSD += e.cost
		snap.Projects[dir] = pb

		if e.session == "" {
			continue // a path we cannot attribute; it still counts toward the totals
		}
		if snap.Sessions == nil {
			snap.Sessions = make(map[string]SessionUsage)
		}
		b := snap.Sessions[e.session]
		b.Tokens += tokens
		b.CostUSD += e.cost
		snap.Sessions[e.session] = b
	}
	return snap
}

// hint is what the scan knows of a project directory's path: the format's own
// reading of the name when it has a lossless one, else the cwd fold settled on.
// A hint with no path is still returned — LabelProjects names that directory
// unresolved rather than leaving it out.
func (sc *scan) hint(dir string) ProjectHint {
	if sc.format.project != nil {
		if p := sc.format.project(dir); p != "" {
			return ProjectHint{Path: p, Exact: true}
		}
	}
	return ProjectHint{Path: sc.cwds[dir], Exact: sc.named[dir]}
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
// project and session. It reads line by line, bounded at maxTranscriptLine, and
// only parses lines that mention usage.
// paths.OpenRegular rather than os.Open, for the half maxTranscriptLine does not
// cover: the line length was bounded, the file's KIND was not. The walk lists
// whatever is in the projects tree and takes anything ending .jsonl, and open(2)
// on a FIFO with no writer never returns — so one pipe named like a transcript
// parks the usage poller's goroutine for the daemon's life, taking the footer
// and the quota bars with it. A file that is not a plain file is skipped, which
// is what the walk already does with every other unreadable entry.
func (sc *scan) transcript(path, project, session string) {
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
			sc.fold(line, project, session)
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
	Cwd       string `json:"cwd"`
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
//
// A line's cwd teaches its project directory a label before any filtering. A
// session's cwd wanders into worktrees and subdirectories as it works, so not
// every cwd is the project: the one the vendor named the directory after is, and
// it wins wherever in the walk it turns up. A subagent's transcript is walked
// before its parent's (<session>/ sorts before <session>.jsonl), and its cwd is
// wherever the parent had wandered to when it spawned — measured, that named 5
// of 231 directories wrongly. Only when no cwd matches does the first one seen
// stand, which beats a label nobody can read.
func (sc *scan) fold(line []byte, project, session string) {
	r, ok := sc.format.decode(line)
	if !ok {
		return
	}
	if r.cwd != "" && project != "" && !sc.named[project] {
		if sc.format.dirName != nil && sc.format.dirName(r.cwd) == project {
			sc.cwds[project], sc.named[project] = r.cwd, true
		} else if sc.cwds[project] == "" {
			sc.cwds[project] = r.cwd
		}
	}
	sc.keep(r, project, session)
}

// keep places one decoded record in the scan, or drops it. Every test a record
// has to pass lives here rather than in a decoder, so a new vendor cannot ship
// without the cutoff, the ceiling or the dedup.
func (sc *scan) keep(r record, project, session string) {
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
		project:    project,
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
		cwd:        e.Cwd,
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
