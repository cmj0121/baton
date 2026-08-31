// Package score is baton's fleet memory: short operator- and agent-submitted
// notes that are rendered into every directly dispatched brief so the whole
// fleet keeps acting on them.
//
// The store owns three sibling files in its directory (per the #38
// I-invariants):
//
//   - score-events.jsonl — the append-only event log. The log is the truth:
//     every mutation is an event, and any other file can be rebuilt from it.
//   - score.json — a snapshot of the entries by id. It is only a cache of the
//     log's fold, so a corrupt or missing snapshot never fails Open; the next
//     successful mutation rewrites it atomically.
//   - score.md — the human-facing projection, one entry per line. Operators may
//     edit or delete it freely; Reconcile folds their edits back in (dummy
//     until R1).
//
// This is the S0 walking skeleton (#39): the API and file shapes freeze here,
// while the interesting behaviour is deliberately dummy — each method's comment
// names the R-issue that replaces its internals. The package is stdlib-only and
// never logs; errors are returned for the server to log.
package score

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Schema is the current on-disk schema version, shared by the snapshot and the
// event log. Bump it on a breaking change to either shape; a snapshot written
// with a newer schema is ignored as if corrupt.
const Schema = 1

// renderLimit is how many entries a dummy Render returns (S0: first N in file
// order; R3 replaces the selection with real ranking).
const renderLimit = 7

// maxEntryRunes caps one submission, mirroring internal/server's
// maxReasonRunes. #37 asks a score entry to be one to three sentences, and 300
// runes is that with room to spare. The cap matters because renderLimit caps
// the entry COUNT, not the byte weight: without a length limit a single 200 KB
// submission sits inside the first renderLimit entries and is prepended to
// every direct dispatch, forever.
//
// Over-long submissions are REFUSED rather than truncated. Truncation is the
// right answer for a reason, which is a decoration on a card — a clipped
// sentence still reads as one. It is the wrong answer for a memory entry: the
// entry is replayed verbatim into every brief the fleet receives, so silently
// halving it manufactures an instruction nobody wrote ("never deploy without"
// …). Refusing tells the submitter, who is an agent or the operator and can
// resubmit something shorter, that nothing was stored.
const maxEntryRunes = 300

// The store's three files, siblings inside the score directory.
const (
	scoreMD     = "score.md"           // human-facing projection, one entry per line
	scoreJSON   = "score.json"         // snapshot cache of the entries by id
	scoreEvents = "score-events.jsonl" // append-only event log — the truth
)

// Event names recorded in the log — the full #38 §3 vocabulary, frozen in S0
// even where nothing emits them yet, so later issues need no schema bump.
const (
	EventSubmitted  = "submitted"   // a new entry entered the store (emitted now, by Submit)
	EventFolded     = "folded"      // an agent-side reinforcement folded into an entry (emitted now by Reinforce; R2 folds near-duplicate submissions too)
	EventRaised     = "raised"      // an entry's tier was promoted (R3)
	EventUserSignal = "user-signal" // the operator reinforced an entry (emitted now, by Reinforce)
	EventEdited     = "edited"      // an entry's text changed via score.md (R1)
	EventRetired    = "retired"     // an entry left the store (R6)
)

// Provenance records where an entry came from, so ranking (R3) can weight
// entries by the panel, profile, and directory that produced them.
type Provenance struct {
	SourcePanel   string `json:"source_panel,omitempty"`   // panel id that submitted it
	SourceProfile string `json:"source_profile,omitempty"` // agent profile of that panel
	SourceCwd     string `json:"source_cwd,omitempty"`     // working directory of that panel
	Source        string `json:"source"`                   // "user" or "agent"
}

// Entry is one remembered note. Its id is a short hex handle (like "e7f3a2")
// stable across snapshot rewrites, so score.md lines, log events, and the
// snapshot all name the same entry.
type Entry struct {
	Id             string     `json:"id"`
	Text           string     `json:"text"`
	Tier           int        `json:"tier"` // 1..3; wording in RenderBlock
	Provenance     Provenance `json:"provenance"`
	Reinforcements int        `json:"reinforcements"` // times Reinforce hit this entry
}

// Context is what the renderer knows about the dispatch asking for entries.
// S0 ignores it entirely; R3's ranking scores entries against it.
type Context struct {
	Panel   string
	Profile string
	Cwd     string
	Group   string
}

// snapshot is the persisted shape of score.json: a cache of the log's fold.
type snapshot struct {
	Schema  int              `json:"schema"`
	Entries map[string]Entry `json:"entries"`
}

// event is one line of score-events.jsonl.
type event struct {
	Schema int       `json:"schema"`
	Event  string    `json:"event"`
	Id     string    `json:"id"`
	At     time.Time `json:"at"`
	Source string    `json:"source,omitempty"` // who reinforced ("user"/"agent"); empty otherwise
}

// Store is the fleet memory backed by one directory. A nil *Store is the
// disabled store: renders are empty and mutations are refused plainly, so the
// server can hold nil when score is switched off. All methods are safe for
// concurrent use.
type Store struct {
	mu      sync.Mutex
	dir     string
	entries []Entry // score.md file order — the render order in S0
}

// errDisabled is returned by mutations on the disabled (nil) store.
var errDisabled = errors.New("score is disabled")

// Open opens (or creates) the store in dir. The directory is created 0700 and
// every file 0600. On the very first run — score.md absent — it seeds a comment
// header that shows operators the line format; a pre-existing score.md, however
// empty, is never reseeded. A corrupt score.json
// or a torn last line in the event log never fails Open: the log-and-continue
// semantics live in the returned data, and the caller logs. Open calls the
// *Locked helpers without holding the mutex — the store is not published to any
// other goroutine until Open returns, so their "caller holds the lock" contract
// is trivially satisfied here.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	s := &Store{dir: dir}
	if _, err := os.Stat(filepath.Join(dir, scoreMD)); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		if err := s.seedLocked(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Reconcile folds operator edits of score.md back into the store.
//
// S0 dummy (R1 replaces): it only re-reads score.md into memory — no edit
// detection, no edited/retired events, and no file rewritten, so it can never
// corrupt anything.
func (s *Store) Reconcile() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// Submit records a new note with its provenance and returns the stored entry.
// Text is sanitised and length-checked here, at the boundary the untrusted
// string enters the store on; see sanitizeSubmission and maxEntryRunes.
//
// S0 dummy (R2 replaces): every submission is a brand-new entry at tier 1 —
// no folding of near-duplicates into reinforcements yet.
func (s *Store) Submit(text string, prov Provenance) (Entry, error) {
	if s == nil {
		return Entry{}, errDisabled
	}

	text = sanitizeSubmission(text)
	if text == "" {
		return Entry{}, errors.New("score: empty submission")
	}
	if n := len([]rune(text)); n > maxEntryRunes {
		return Entry{}, fmt.Errorf("score: submission is %d runes, limit is %d", n, maxEntryRunes)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submitLocked(text, prov)
}

// sanitizeSubmission scrubs submitted text at the boundary it enters the store
// on, for the same reason internal/server's sanitizeReason scrubs an agent's
// reason where it enters the daemon — but against a sharper edge.
//
// A reason is drawn into a card; a score entry is written into a TTY. Every
// stored entry is prepended to every direct dispatch, and the dispatch bytes go
// to the panel's pty verbatim, because a terminal stream is passed through
// byte-exact by design. score.submit is open to every agent panel and exported
// as an MCP tool, so an unscrubbed entry lets one agent plant an OSC 52
// clipboard write, a cursor-control sequence, or a title rewrite that is
// replayed into every OTHER agent's terminal on every dispatch, indefinitely —
// the store is durable, so the payload outlives the panel that wrote it.
//
// strings.Fields alone does not stop this: it splits on unicode.IsSpace, and
// ESC (0x1b) is not whitespace, so an escape sequence survives flattening
// intact. The filter therefore drops the same three classes sanitizeReason
// does, and for the same reasons:
//
//   - Control characters (Cc — C0 and C1). A sequence loses its ESC and its
//     parameters stay behind as plain text ("[1;31m", not a colour). That is
//     deliberate: an entry that tried to carry an escape should look wrong to
//     whoever reads score.md, not quietly become clean prose.
//   - Format characters (Cf), which IsControl does not see: U+202E and the bidi
//     isolates render a line backwards, U+200B is invisible.
//   - The replacement character, what invalid UTF-8 decodes to.
//
// On top of that it keeps the store's own line discipline: the files are
// line-oriented, one entry per line, so every run of whitespace folds into a
// single space and the result is one line by construction.
func sanitizeSubmission(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	pendingSpace := false
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
			pendingSpace = true
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar:
			continue
		default:
			if pendingSpace && b.Len() > 0 {
				b.WriteRune(' ')
			}
			pendingSpace = false
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Reinforce bumps an entry's counter and logs who did it: a fold when an agent
// reinforces (in #38's model a fold IS the agent-side reinforcement), a
// user-signal when the operator does.
//
// S0 dummy (R3 replaces): the counter increments and the event is logged, but
// no tier promotion (raised) happens yet.
func (s *Store) Reinforce(id, source string) error {
	if s == nil {
		return errDisabled
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	i := s.indexLocked(id)
	if i < 0 {
		return fmt.Errorf("score: no entry %q", id)
	}
	name := EventFolded
	if source == "user" {
		name = EventUserSignal
	}
	// Durable append first, memory second: the log is the truth, so the counter
	// must never run ahead of a failed write.
	if err := s.appendEvent(event{Schema: Schema, Event: name, Id: id, At: time.Now().UTC(), Source: source}); err != nil {
		return err
	}
	s.entries[i].Reinforcements++
	return s.writeSnapshotLocked()
}

// Refine is the entry-management verb (promote, retire, edit, …).
//
// S0 stub: not implemented until R6.
func (s *Store) Refine(op, id, arg string) error {
	_, _, _ = op, id, arg
	return errors.New("score refine: not implemented until R6")
}

// Render returns the entries to inject for the given dispatch context, or nil
// when the store is empty or disabled. Everything it returns is injected into a
// real agent's brief, so nothing the store seeds may reach it — which is why the
// first-run seed is comment lines rather than entries (see seedLocked).
//
// S0 dummy (R3 replaces): the first renderLimit entries in file order — the
// context is ignored and nothing is ranked.
func (s *Store) Render(ctx Context) []Entry {
	_ = ctx
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.entries) == 0 {
		return nil
	}
	n := min(len(s.entries), renderLimit)
	out := make([]Entry, n)
	copy(out, s.entries[:n])
	return out
}

// RenderBlock renders the injectable text block: a bordered "── Score ──"
// section listing Render's entries with their tier wording. It returns the
// empty string when there is nothing to inject.
func (s *Store) RenderBlock(ctx Context) string {
	entries := s.Render(ctx)
	if len(entries) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("── Score ──\n")
	for _, e := range entries {
		b.WriteString("- " + e.Text + " [" + tierWording(e.Tier) + "]\n")
	}
	b.WriteString("───────────\n")
	return b.String()
}

// tierWording is how strongly a tier speaks in the injected block.
func tierWording(tier int) string {
	switch tier {
	case 2:
		return "note and take care"
	case 3:
		return "important"
	default:
		return "noted"
	}
}

// seedLocked writes the first-run header of score.md: comment lines that teach
// the entry format by showing one.
//
// They are deliberately NOT entries. An earlier shape seeded two real entries
// flagged as demo data and filtered them out at render time, but that flag lived
// only in score.json — which this package's own doctrine calls a disposable
// cache — so deleting the snapshot rebuilt them as ordinary entries and put
// "demo: …" back into every agent's brief. Lines that parseLine already skips
// need no flag, no cache, and no rebuild rule that a later issue has to
// remember: they cannot become entries, because they never were.
func (s *Store) seedLocked() error {
	header := []string{
		"# This file is baton's fleet memory — one entry per line, like:",
		"#   - [e7f3a2] the agent was asked to gain permission",
		"# Edit or delete lines freely; anything that is not an entry is ignored.",
	}
	for _, line := range header {
		if err := s.appendLine(scoreMD, line); err != nil {
			return err
		}
	}
	return nil
}

// submitLocked appends a new entry to all three files: a score.md line, a
// submitted event, and an atomic snapshot rewrite. The caller holds the lock.
func (s *Store) submitLocked(text string, prov Provenance) (Entry, error) {
	id, err := s.newIDLocked()
	if err != nil {
		return Entry{}, err
	}
	e := Entry{Id: id, Text: text, Tier: 1, Provenance: prov}

	if err := s.appendLine(scoreMD, "- ["+id+"] "+text); err != nil {
		return Entry{}, err
	}
	if err := s.appendEvent(event{Schema: Schema, Event: EventSubmitted, Id: id, At: time.Now().UTC()}); err != nil {
		return Entry{}, err
	}
	s.entries = append(s.entries, e)
	if err := s.writeSnapshotLocked(); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// loadLocked rebuilds the in-memory entries from disk: score.md gives the
// entries and their order, the snapshot fills in each id's metadata. A missing
// score.md means an emptied store (the operator may delete it), and a corrupt
// or newer-schema snapshot is ignored — its ids simply load with fresh tier-1
// metadata until the next mutation rewrites it.
func (s *Store) loadLocked() error {
	data, err := os.ReadFile(filepath.Join(s.dir, scoreMD))
	if err != nil {
		if os.IsNotExist(err) {
			s.entries = nil
			return nil
		}
		return err
	}

	known := s.loadSnapshot()
	var entries []Entry
	for _, line := range strings.Split(string(data), "\n") {
		id, text, ok := parseLine(line)
		if !ok {
			continue
		}
		e, found := known[id]
		if !found {
			e = Entry{Id: id, Tier: 1}
		}
		// score.md owns the text — and score.md is a DESIGNED input channel, so
		// this is the store's second writer of Entry.Text and must scrub like the
		// first. Without it "every entry in this store is inert" holds only at
		// Submit, and an operator-edited (or planted) OSC 52 sequence in score.md
		// would ride Render straight into a panel's pty. R1 gives Reconcile a real
		// caller that re-reads this file at runtime, so the invariant has to belong
		// to the store's edge rather than to one entry point.
		e.Text = sanitizeSubmission(text)
		entries = append(entries, e)
	}
	s.entries = entries
	return nil
}

// loadSnapshot reads score.json into an id→Entry map. Corruption is tolerated
// by design — the snapshot is only a cache of the log's fold — so any read or
// decode failure, and any newer schema, yields an empty map.
func (s *Store) loadSnapshot() map[string]Entry {
	data, err := os.ReadFile(filepath.Join(s.dir, scoreJSON))
	if err != nil {
		return nil
	}
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil || snap.Schema > Schema {
		return nil
	}
	return snap.Entries
}

// writeSnapshotLocked rewrites score.json atomically and durably from memory.
// The caller holds the lock.
func (s *Store) writeSnapshotLocked() error {
	snap := snapshot{Schema: Schema, Entries: make(map[string]Entry, len(s.entries))}
	for _, e := range s.entries {
		snap.Entries[e.Id] = e
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.dir, scoreJSON), data, 0o600)
}

// appendEvent appends one event line to the log.
func (s *Store) appendEvent(ev event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return s.appendLine(scoreEvents, string(data))
}

// appendLine durably appends line (plus a newline) to the named store file. If
// the file's last write was torn — no trailing newline, e.g. a crash mid-append
// — a newline is inserted first so the new record starts on its own line and
// the damage stays confined to the torn one.
func (s *Store) appendLine(name, line string) (err error) {
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()

	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() > 0 {
		last := make([]byte, 1)
		if _, err = f.ReadAt(last, st.Size()-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			line = "\n" + line
		}
	}
	if _, err = f.WriteString(line + "\n"); err != nil {
		return err
	}
	return f.Sync()
}

// newIDLocked draws a fresh short hex id, retrying on the (vanishingly rare)
// collision with an existing entry. The caller holds the lock.
func (s *Store) newIDLocked() (string, error) {
	for {
		var b [3]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		id := hex.EncodeToString(b[:])
		if s.indexLocked(id) < 0 {
			return id, nil
		}
	}
}

// indexLocked finds an entry by id, or -1. The caller holds the lock.
func (s *Store) indexLocked(id string) int {
	for i := range s.entries {
		if s.entries[i].Id == id {
			return i
		}
	}
	return -1
}

// parseLine decodes one score.md line of the shape "- [id] text". Anything
// else — headings, blank lines, an operator's stray prose — is skipped rather
// than rejected, because score.md is theirs to edit.
func parseLine(line string) (id, text string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(line), "- [")
	if !found {
		return "", "", false
	}
	id, text, found = strings.Cut(rest, "]")
	if !found || id == "" || strings.ContainsAny(id, " \t") {
		return "", "", false
	}
	return id, strings.TrimSpace(text), true
}

// writeFileAtomic writes data to path atomically and durably: a sibling temp
// file, fsync, rename into place, then a parent-directory fsync so the rename
// survives a crash. Same idiom as internal/state's snapshot — copied, not
// imported, to keep this package stdlib-only.
func writeFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	// Any failure from here on leaves a stale temp file behind; drop it on the
	// way out, so a half-written ".tmp" never lingers.
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()

	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}

	// Fsync the parent directory so the rename is durable. Not every platform
	// can open a directory for sync; that is not fatal to the write.
	if dir, derr := os.Open(filepath.Dir(path)); derr == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// Dir is the directory the store's files live in — what score.status reports.
// Empty on the disabled (nil) store.
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Len is how many entries the store holds — the honest count, unlike Render,
// which caps at renderLimit. Zero on the disabled (nil) store.
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
