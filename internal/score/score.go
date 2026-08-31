// Package score is baton's fleet memory: short operator- and agent-submitted
// notes that are rendered into every directly dispatched brief so the whole
// fleet keeps acting on them.
//
// The store owns three sibling files in its directory (per the #38
// I-invariants):
//
//   - score-events.jsonl — the append-only event log. The log is the truth:
//     every mutation is an event, and the entries are rebuilt from it at every
//     Open.
//   - score.json — a snapshot of the entries by id. It is only a cache of the
//     log's fold, so a corrupt or missing snapshot never fails Open and is never
//     read back; the next mutation rewrites it atomically.
//   - score.md — the human-facing projection, one entry per line. Operators may
//     edit or delete it freely; it is the truth for an entry's TEXT and its
//     EXISTENCE, and Reconcile folds their edits back in.
//
// Two rules decide every conflict between the three (#38 §3, invariant I3): the
// user's text wins, and the log replays whatever the file lost. One pass —
// reconcileLocked — implements both the boot recovery table and the live-editor
// table, because they are the same table read at different moments.
//
// The package is stdlib-only and never logs; errors and counters are returned
// for the server to log.
package score

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Schema is the current on-disk schema version, shared by the snapshot and the
// event log. Bump it on a breaking change to either shape; a snapshot written
// with a newer schema is ignored as if corrupt.
//
// R1 added `text` and `provenance` to the event and `aliases` to the entry, and
// R2 added `tier` to the event. All four are appended optional fields — an old
// line decodes with them zero, and an old reader ignores them — so the version
// stays 1, for the same reason proto.ProtocolVersion does not move on an
// appended field. What an appended
// field cannot fix is a record that never carried the text at all, and the
// pre-R1 log is exactly that; replayLocked and reconcileLocked handle it as
// "text unknown" rather than as an empty entry the file then edits, because
// treating it as an edit would manufacture the user signal invariant I6 rests
// on. See reconcileLocked's live branch.
const Schema = 1

// renderLimit is how many entries Render returns (S0: first N in file order;
// R3 replaces the selection with real ranking).
const renderLimit = 7

// maxEntryRunes caps the weight of one entry, mirroring internal/server's
// maxReasonRunes. #37 asks a score entry to be one to three sentences, and 300
// runes is that with room to spare. The cap matters because renderLimit caps
// the entry COUNT, not the byte weight: without a length limit a single 200 KB
// entry sits inside the first renderLimit entries and is prepended to every
// direct dispatch, forever.
//
// The cap is enforced ASYMMETRICALLY on the store's two input channels, and the
// asymmetry is deliberate:
//
//   - A SUBMISSION is refused outright, never truncated. The submitter is an
//     agent or the operator's cockpit and can resubmit something shorter, and
//     truncation would manufacture an instruction nobody wrote ("never deploy
//     without" …) that is then replayed verbatim into every brief.
//   - An OPERATOR'S LINE in score.md is kept exactly as written, in the file
//     and in the store, and only its INJECTION is withheld: Render skips it.
//     Refusing it would mean rewriting the operator's own file, which
//     contradicts invariant I3 — their text wins — and truncating it would
//     silently rewrite what they meant. Withholding the injection is the only
//     move that leaves the file theirs while keeping the weight off every
//     brief. Recovery.Oversized counts what is being withheld so the server can
//     say so, because a line that silently does nothing is worse than the bug.
const maxEntryRunes = 300

// maxAliases is how many prior wordings one entry keeps for folding.
//
// Eight, because an alias is worth keeping only while a repeat of it is more
// likely to mean this entry than to mean something else. An entry reworded eight
// times has drifted, and the ninth-oldest phrasing folding into what it says
// today is the shape of "remembering wrong" that #38 §1 spends its whole budget
// avoiding. The cap also bounds what an editing session can cost: three hundred
// rewords is three hundred wordings persisted in score.json and replayed at
// every boot, for an entry that is one line of text.
//
// Nothing is destroyed by the cap (I7): a dropped wording stays in the log with
// the edit that retired it. It simply stops folding.
const maxAliases = 8

// staleWindow is how long after score.md's recorded mtime the fingerprint is
// distrusted outright.
//
// Size, mtime, ctime, and inode together catch every edit a filesystem reports
// faithfully, but they cannot catch a same-size write that lands inside one
// timestamp tick on a filesystem whose granularity is coarse (HFS+ and FAT
// round to a second or two) or whose attributes are cached (NFS). "Fix a
// one-character typo" is exactly that shape. So any file whose mtime is within
// this window of now is treated as moved and re-parsed: a miss then lasts at
// most this long instead of persisting until the next write, which is the
// difference between a bounded error and a permanent one. The cost is that
// dispatches during active editing re-parse a small file, which is the moment
// the operator most wants to be current.
const staleWindow = 2 * time.Second

// The store's files, siblings inside the score directory.
const (
	scoreMD     = "score.md"           // human-facing projection, one entry per line
	scoreJSON   = "score.json"         // snapshot cache of the entries by id
	scoreEvents = "score-events.jsonl" // append-only event log — the truth
	scoreLock   = "score.lock"         // the single-writer claim; never read, never removed
)

// newline is the record separator of both line-oriented files, as bytes: the
// log is split on it at every boot and appended with it at every mutation.
var newline = []byte{'\n'}

// Event names recorded in the log — the full #38 §3 vocabulary, frozen in S0
// even where nothing emits them yet, so later issues need no schema bump.
const (
	EventSubmitted  = "submitted"   // a new entry entered the store (Submit, and reconcile admitting a user's line)
	EventFolded     = "folded"      // a repeat was counted into an existing entry rather than added as a line
	EventRaised     = "raised"      // an entry's tier was promoted by recurrence
	EventUserSignal = "user-signal" // the operator reinforced an entry (emitted now, by Reinforce)
	EventEdited     = "edited"      // an entry's text changed — via score.md now, via the conductor in R6
	EventRetired    = "retired"     // an entry left the store
)

// sourceRecovery stamps an event the recovery pass emitted on nobody's behalf,
// so replay can tell it from an operator's edit. Only "user" counts as the
// signal invariant I6 guards; every other source is bookkeeping.
const sourceRecovery = "recovery"

// seedHeader is what an ABSENT score.md is written back as: comment lines that
// teach the entry format by showing one.
//
// They are deliberately NOT entries. An earlier shape seeded two real entries
// flagged as demo data and filtered them out at render time, but that flag lived
// only in score.json — which this package's own doctrine calls a disposable
// cache — so deleting the snapshot rebuilt them as ordinary entries and put
// "demo: …" back into every agent's brief. Lines that parseLine already skips
// need no flag, no cache, and no rebuild rule that a later issue has to
// remember: they cannot become entries, because they never were.
var seedHeader = []string{
	"# This file is baton's fleet memory — one entry per line, like:",
	"#   - [e7f3a2] the agent was asked to gain permission",
	"# Edit or delete lines freely; anything that is not an entry is ignored.",
}

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
	Tier           int        `json:"tier"` // 1..3, earned by recurrence; wording in RenderBlock
	Provenance     Provenance `json:"provenance"`
	Reinforcements int        `json:"reinforcements"` // repeats counted into this entry since it was first said
	// Aliases are the entry's prior wordings, newest last, kept so a repeat of a
	// superseded phrasing still folds into this entry (invariant I4). The list is
	// deduplicated by folding key and capped at maxAliases.
	Aliases []string `json:"aliases,omitempty"`

	// norm is Text's folding key, computed where the text is set rather than once
	// per pass. Unexported, so it never reaches score.json — it is derived, and a
	// cache inside a cache is one more thing that can stop being true. Every
	// reconcile pass over an edited file used to normalise every entry again; at
	// a few thousand entries that was the pass's whole allocation budget, and the
	// pass runs on the dispatch path while the operator is still typing.
	norm string
}

// Injectable reports whether the entry is light enough to prepend to every
// direct dispatch. It is the ONE statement of that policy: Render selects among
// injectable entries, the withheld gauge counts the rest, and Submit refuses a
// submission that would not be one. See maxEntryRunes for why the store's two
// input channels then act on the same answer differently.
//
// len([]rune) rather than utf8.RuneCountInString on purpose — the compiler
// rewrites this exact form into that call, allocating nothing, and the explicit
// one benchmarked no faster.
func (e Entry) Injectable() bool {
	return len([]rune(e.Text)) <= maxEntryRunes
}

// newEntry admits raw text into the store as a fresh tier-1 entry, scrubbing it
// on the way in.
//
// It and setText are the only ways text reaches Entry.Text, so an admission
// path added later — R2's folding, R6's Refine — cannot store a wording that
// never passed sanitize. That structural guarantee is worth more here than the
// usual "scrub at the boundary" rule, because the store has several channels
// text arrives on and what sanitize stops is an escape sequence replayed into
// every agent's pty on every dispatch, durably.
func newEntry(id, raw string, prov Provenance) Entry {
	e := Entry{Id: id, Tier: 1, Provenance: prov}
	e.setText(raw)
	return e
}

// setText replaces an entry's wording, scrubbing it on the way in — see
// newEntry. sanitize is idempotent, so a caller that had to scrub the text
// already, to compare it or to log it, pays only a scan here.
func (e *Entry) setText(raw string) {
	e.Text = sanitize(raw)
	e.norm = normalize(e.Text)
}

// Context is what the renderer knows about the dispatch asking for entries.
// S0 ignores it entirely; R3's ranking scores entries against it.
type Context struct {
	Panel   string
	Profile string
	Cwd     string
	Group   string
}

// Delta is what ONE reconcile pass changed in the operator's files. The pass
// RETURNS it, so the server logs what that pass did rather than subtracting a
// before-reading from an after-reading: the subtraction cross-attributed a
// concurrent Submit's work to whichever pass happened to be reading, and every
// counter #41-#46 adds would have had to be added to it by hand.
//
// Its fields all obey one rule — a count of actions this pass took — which is
// why the gauge and the health counters live in Health instead.
type Delta struct {
	Admitted     int // score.md lines with no live entry, taken in as user-sourced
	Reattributed int // of those, ones whose id the log already knew: the original provenance is lost
	Adopted      int // entries whose wording the log never carried, taken from the file
	Superseded   int // entries whose text the operator changed
	Folded       int // entries a duplicate line was counted into — at most one per entry; see View.Folds for the lines removed
	Retired      int // entries score.md no longer carries
	Reprojected  int // entries written back into a score.md that had gone missing
}

// Fold is one repeat counted into an entry that already said it: what survived,
// what repeated it, where the repeat came from, and where the entry stands now.
//
// It covers BOTH ways a repeat arrives, because they are one lifecycle event and
// #38 asks for one log line per fold. A submission that folded leaves score.md
// untouched, so it is the mutation an operator cannot see by looking; a folded
// LINE is the pass's only destructive act, and a count alone cannot say which
// line went. The store never logs, so the records ride out to the server, which
// is the single place they become log lines.
type Fold struct {
	Id   string // the entry the repeats were counted into
	Text string // that entry's surviving wording
	// Repeat is one of those repeats exactly as it was written: the one that
	// moved the entry's counter where any of them did, and otherwise the first
	// the pass saw. Where a record covers several lines the counter moves at most
	// once, so naming the line that earned it is what keeps Counted readable.
	Repeat string
	Prov   Provenance // where that repeat came from: a panel, or the operator's own file
	// Reinforcements is where the entry stands AFTER this fold, so the log line
	// that announces a fold also answers the only question it raises.
	Reinforcements int
	// Duplicates is how many repeats this fold covers — more than one only on the
	// file path, where a paste can carry the same wording many times. Counted says
	// whether they moved the entry's counter, which they do not when the store
	// already owes their removal (see Store.owed).
	Duplicates int
	Counted    bool
	// FromFile says the repeats were LINES in score.md, which the pass then takes
	// out; Removed whether they actually left it, which they do not when the
	// rewrite that should have taken them out failed. Removed is meaningless on a
	// submission fold, where no line ever existed to remove.
	//
	// The flags are separate because the pass can do each without the others, and
	// one flag for several would let the log claim a deletion that never happened.
	// None is inferable from another.
	FromFile bool
	Removed  bool
}

// maxFoldNotes caps the fold records held for the next read to report. Every
// View drains them — that is every dispatch, list and status — so the buffer is
// normally one pass deep; the cap exists only so a daemon nobody ever reads from
// cannot accumulate them without limit. Past it the RECORDS are dropped, never
// the folds: Delta and the event log still carry what happened.
const maxFoldNotes = 128

// maxOwedRemovals caps how many wordings ONE entry may be remembered as owing a
// removal for. See Store.owed for the debt itself; this is only its bound.
//
// Both sources are finite but neither is small by construction. A pass's set is
// one wording per duplicate LINE the file still shows, which is bounded by a
// file the pass has already read whole — but the boot derivation's is one per
// fold event in a log that grows forever, and the file it would be checked
// against has not been read yet.
//
// Sixty-four is chosen for what it costs to be wrong. Byte-identical re-pastes
// collapse to one, so reaching it takes sixty-four DIFFERENTLY typed duplicates
// of a single observation, folded across sixty-four passes without one
// successful rewrite between them. Past it the OLDEST goes, because a wording
// folded longer ago is the least likely to still be on a line — and the cost of
// forgetting one is at worst a repeat counted that should have been swallowed,
// never a line removed, because owed reaches nothing but the decision to count.
const maxOwedRemovals = 64

// Health is the store's standing rather than any one pass's work: what it is
// currently withholding, and what has gone wrong since Open.
type Health struct {
	// Oversized is how many entries are currently too long to inject. Their text
	// is intact in score.md and in the store; only Render withholds them. See
	// maxEntryRunes for why an operator's line is kept rather than refused. It
	// reaches score.status as well as the daemon log, because invariant I8 says
	// an operator must not have to read the log to learn why their edit is not
	// taking effect.
	Oversized int
	// TornEvents is how many unparsable log lines were skipped at Open, and
	// CacheWriteFailures how many score.json rewrites failed. Neither costs any
	// data — the first is a torn append, the second a cache the store rebuilds
	// from the log — but a rising CacheWriteFailures is the early symptom of the
	// full or read-only disk that will break the next append, which does.
	TornEvents         int
	CacheWriteFailures int
	// The three things the store does quietly and correctly, counted because
	// each is otherwise discoverable only by subtracting one number from
	// another — and because each of them is the store choosing to remember
	// less, which an operator is entitled to see it doing.
	//
	// SwallowedRepeats is duplicate lines removed without being counted, because
	// the fold was already durable and only the removal was owed (see the fold
	// branch of reconcileLocked). UnreportedFolds is fold records dropped past
	// maxFoldNotes, so a pass that removed two hundred lines and named a hundred
	// and twenty-eight of them says so. AliasEvictions is prior wordings pushed
	// out of an entry by maxAliases, each one a phrasing that will no longer
	// fold — the entry it would have joined simply gains a twin instead.
	SwallowedRepeats int
	UnreportedFolds  int
	AliasEvictions   int
}

// View is one consistent look at the store, taken without letting go of its
// lock: what a dispatch would inject, how much the store holds, what it is
// withholding, and what the reconcile pass that produced the answer changed.
//
// It exists because a read needs all of those AT ONCE. Assembling a status
// reply from Unlocked, Len, Render and the gauge took the store lock four
// separate times, so a concurrent Submit landed between two of them and the
// reply reported an entry total and a rendered total read from two different
// views — the very gap "one reconcile for both counts" claimed to close.
type View struct {
	Entries  []Entry // what a dispatch would inject, in render order
	Block    string  // Entries as the injectable text block; empty when there are none
	Total    int     // every entry the store holds, injectable or not
	Health   Health
	Delta    Delta  // what this look's pass changed; zero when score.md had not moved
	Folds    []Fold // repeats folded since the last look, for the server to log
	Unlocked bool   // the store is running without its single-writer claim
}

// snapshot is the persisted shape of score.json: a cache of the log's fold.
type snapshot struct {
	Schema  int              `json:"schema"`
	Entries map[string]Entry `json:"entries"`
}

// event is one line of score-events.jsonl. Text and Prov are what make the log
// replayable on its own: without them a rebuilt entry would have no wording and
// no source, and score.json could not be regenerated from the log alone.
type event struct {
	Schema int         `json:"schema"`
	Event  string      `json:"event"`
	Id     string      `json:"id"`
	At     time.Time   `json:"at"`
	Source string      `json:"source,omitempty"`     // who acted ("user"/"agent"); empty otherwise
	Text   string      `json:"text,omitempty"`       // the entry's text at submitted/edited, and the repeat's own wording at folded
	Prov   *Provenance `json:"provenance,omitempty"` // where a submitted entry, or a folded repeat, came from
	// RemovedLine marks a fold that took a LINE out of score.md. It is named for
	// its EFFECT rather than for where the repeat came from, because the effect
	// is what the boot derivation is built on: only a fold that removed a line
	// can owe a removal. A later path that folds a file line without removing it
	// — R6's merge, the id-carrying duplicate the recovery table already admits —
	// must leave this false, and a name about provenance would have read as true
	// for it.
	//
	// Nothing else can supply the distinction: Source says WHO repeated the
	// wording, and an operator submitting through `ctl score submit` is "user"
	// exactly as their own file is.
	//
	// Appended and optional, so the schema stays 1. A fold logged before this
	// field existed decodes with it false and simply seeds nothing — which is
	// the pre-derivation behaviour, not a wrong one.
	RemovedLine bool `json:"removed_line,omitempty"`
}

// foldEvent records one repeat counted into id. It carries the REPEAT's own text
// and provenance, not the entry's: a fold is the one mutation whose input
// reaches no file otherwise — score.md keeps the wording that was already there
// and score.json keeps a count — so without them the store could say how often
// something has been said but never who said it, which is what #38 leans on
// where it declines to police the content of a submission.
//
// The event is EventFolded whoever repeated the wording, and "user" or "agent"
// lives in Source alone. #38's glossary calls a fold the agent-side
// reinforcement because that is the common case, not because the name is the
// discriminator — R4's user signal must key on Source (invariant I6), never on
// the name.
func foldEvent(id, text string, prov Provenance, at time.Time, removedLine bool) event {
	return event{
		Schema: Schema, Event: EventFolded, Id: id, At: at,
		Source: prov.Source, Text: text, Prov: &prov, RemovedLine: removedLine,
	}
}

// Store is the fleet memory backed by one directory. A nil *Store is the
// disabled store: renders are empty and mutations are refused plainly, so the
// server can hold nil when score is switched off. All methods are safe for
// concurrent use.
type Store struct {
	mu      sync.Mutex
	dir     string
	entries []Entry             // score.md file order — the render order in S0
	burned  map[string]struct{} // every id the log has ever named; never reissued
	boot    Delta               // what Open's recovery pass did to the operator's files
	health  Health

	// The three files' paths, joined once here because dir is immutable after
	// Open and all three are read or written on the dispatch path.
	mdPath     string
	jsonPath   string
	eventsPath string

	// The last-seen fingerprint of score.md, which gates the read paths: a
	// dispatch re-parses the file only when it actually moved. mdKnown false
	// forces the next pass to re-read regardless, and every write the STORE
	// itself makes to score.md clears it — see forgetMDLocked.
	mdKnown bool
	mdMod   time.Time
	mdCtime time.Time
	mdSize  int64
	mdIno   uint64

	// folds are the fold records the next View reports.
	//
	// owed is the removals the store still owes: entry id → the exact bytes of
	// EVERY duplicate line that is already folded in the log but may still be in
	// score.md. It is ONE fact with one lifetime and one clearing rule — a debt
	// is settled by a pass that successfully rewrites score.md without those
	// bytes, and a pass that rewrites nothing settles every debt it did not
	// re-incur, because it has just read the whole file and found none of them.
	//
	// EVERY wording, not the newest, because one entry can owe several at once: a
	// pass whose rewrite fails leaves each duplicate it folded sitting in the
	// file, and the next pass counts any of them the store has forgotten. Holding
	// one wording per entry made that a ladder — a static file climbing a tier
	// per pass for as long as the disk stayed broken — and #37 demotes nothing,
	// so the tier outlived the episode that manufactured it. The wordings are a
	// SLICE rather than a set: nothing here iterates them to produce a result,
	// countable only asks whether one is present, and a slice cannot let map
	// order reach an answer (invariant I1). addOwed keeps them deduplicated and
	// bounded; see maxOwedRemovals.
	//
	// It is never written down. Persisting it would put recovery-relevant state
	// in score.json, the one file #38 calls a disposable cache and Open never
	// reads — the mistake the S0 demo flag made, provable by deleting the
	// snapshot and watching the behaviour change. It survives a restart by being
	// DERIVED instead, at Open, from the two things that are true: the fold event
	// records the wording it removed (event.RemovedLine), and score.md either
	// still shows those exact bytes or does not.
	//
	// Say plainly what the derivation catches, because it is wider than the
	// failed rewrite it was built for: ANY fold whose exact bytes are back in
	// score.md at the next boot. A clean fold, the operator retyping that line
	// verbatim, and a restart before the next read is enough — no disk failure
	// anywhere — and their genuine repeat is removed and counted zero.
	//
	// Two things bound that, and both are structural rather than hopeful. Only a
	// fold that took a LINE out of the file is recorded as owing anything, so a
	// submission an agent folded can never make a pass swallow an operator's
	// typing. And the comparison is BYTE-EXACT: after a clean fold of "Run the
	// linter first.", retyping "RUN THE LINTER FIRST!" folds and counts normally.
	// What misfires is a byte-identical retype — the re-paste-from-the-same-
	// clipboard case, which is the one that most deserves swallowing.
	//
	// One invariant keeps it safe, and reconcileLocked's countable closure is
	// where that is enforced rather than asserted: owed reaches nothing but the
	// decision to COUNT. It can never remove a line folding had not already
	// decided to remove.
	folds []Fold
	owed  map[string][]string

	// release drops the directory lock, and unlocked records that the filesystem
	// could not provide one. See Open.
	release  func()
	unlocked bool
}

// errDisabled is returned by mutations on the disabled (nil) store.
var errDisabled = errors.New("score is disabled")

// Open opens (or creates) the store in dir. The directory is created 0700 and
// every file 0600.
//
// SINGLE WRITER. Open takes an exclusive advisory lock on score.lock and holds
// it until Close. Two daemons on two sockets both default to $HOME/.baton — and
// BATON_SOCK is the documented way to run a second fleet — so they would share
// one score.json.tmp and one in-memory view of the same files: their snapshots
// clobber each other and their entry sets silently diverge. The lock is
// preferred over merely documenting "run one daemon" because the store is a
// cache-plus-log pair whose consistency it cannot check after the fact — an
// unenforced rule here fails silently, and the loser of the race is the
// operator's own text. The second daemon's Open fails with a plain message the
// server reports through score.status and score.submit, rather than leaving the
// operator to guess why their memory is empty. It is the same idiom the daemon
// already uses for the fleet itself, over paths.LockFile (see lock_unix.go).
//
// Where the filesystem cannot lock at all — a network $HOME is exactly where
// the default lands in corporate setups — the store runs UNLOCKED rather than
// refusing to boot, and says so through Unlocked. No fleet memory is a worse
// outcome than an unguarded one.
//
// Boot then applies #38 §3's recovery table: the log is replayed for every
// entry's bookkeeping and for the set of ids that may never be reissued, and
// one reconcile pass over score.md decides existence and text. A corrupt
// score.json is never even read, and a torn last line in the event log is
// skipped and counted (see Recovery) rather than failing Open.
//
// Open calls the *Locked helpers without holding the mutex — the store is not
// published to any other goroutine until Open returns, so their "caller holds
// the lock" contract is trivially satisfied here.
func Open(dir string) (s *Store, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	release, locked, err := lockDir(filepath.Join(dir, scoreLock))
	if err != nil {
		return nil, err
	}
	s = newStore(dir)
	s.release, s.unlocked = release, !locked
	// A store that fails to boot must not keep the directory claimed.
	defer func() {
		if err != nil {
			s.Close()
			s = nil
		}
	}()

	if err = s.replayLocked(); err != nil {
		return s, err
	}
	fi, exists, serr := statMD(s.mdPath)
	if serr != nil {
		return s, serr
	}
	if s.boot, err = s.reconcileLocked(fi, exists); err != nil {
		return s, err
	}
	// Settle what the boot pass did not. #38's first verification check — delete
	// score.json, restart, and the rebuilt cache is byte-identical — has to hold
	// on a boot that changed nothing too, and a pass that DID change something
	// has already written the snapshot itself. Gating on the delta is what keeps
	// that from being two full rewrites and four fsyncs on every such boot.
	if s.boot == (Delta{}) {
		s.commitLocked()
	}
	return s, nil
}

// newStore assembles a store over dir with its three file paths joined once —
// dir is immutable afterwards and all three are touched on the dispatch path.
// Open adds the directory claim and the recovery pass.
func newStore(dir string) *Store {
	return &Store{
		dir: dir, burned: map[string]struct{}{},
		mdPath:     filepath.Join(dir, scoreMD),
		jsonPath:   filepath.Join(dir, scoreJSON),
		eventsPath: filepath.Join(dir, scoreEvents),
	}
}

// Close releases the directory lock. It is safe on a nil store and on a store
// already closed; the daemon holds one store for its whole life, so this is for
// shutdown and for a process that reopens the same directory.
func (s *Store) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release != nil {
		s.release()
		s.release = nil
	}
}

// Unlocked reports that the store is running without its single-writer claim
// because the filesystem could not provide one. The caller should say so: a
// second daemon on the same directory will not be refused.
func (s *Store) Unlocked() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unlocked
}

// Boot is what Open's recovery pass did to the operator's files. Zero on the
// disabled (nil) store.
func (s *Store) Boot() Delta {
	if s == nil {
		return Delta{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.boot
}

// Health is what the store is currently withholding and what has gone wrong
// since Open. Zero on the disabled (nil) store.
func (s *Store) Health() Health {
	if s == nil {
		return Health{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.health
}

// Reconcile folds operator edits of score.md back into the store: a reworded
// line supersedes (the old wording becomes an alias and the edit counts as a
// user signal), a line with no id becomes a new user-sourced entry whose id is
// written back, and a deleted line retires its entry — recorded in the log,
// never destroyed (invariant I7). It returns what THIS pass changed.
//
// The stat that decides whether anything moved runs OFF the store lock, and the
// lock is taken only to compare the result. When score.md has not moved — the
// overwhelmingly common case on a dispatch — the whole call is one stat and an
// uncontended lock, and no file is parsed. See mdMovedLocked for what "moved"
// means and how a coarse-granularity filesystem is handled. The mutation paths
// do not use the gate; see Submit.
//
// A read wants View instead: it runs this pass and answers from the result
// without letting go of the lock in between.
func (s *Store) Reconcile() (Delta, error) {
	if s == nil {
		return Delta{}, nil
	}

	fi, exists, err := statMD(s.mdPath) // s.mdPath is immutable after Open
	if err != nil {
		return Delta{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reconcileGatedLocked(fi, exists)
}

// View reconciles score.md and answers from the result WITHOUT letting go of
// the lock in between. It is the whole read side of the store: the entries a
// dispatch would inject and the block they render as, the totals a status reply
// reports, the gauge that explains the gap between them, and what the pass
// changed for the caller to log.
//
// Both halves matter. Folding the reconcile in makes #38's invariant I2 — no
// render, list or status acts on a stale view — structural rather than three
// remembered call sites in the server. Answering under the same lock is what
// makes the consistency the reply claims actually true: assembled from four
// separate calls, a concurrent Submit landed between two of them and the reply
// reported an entry total and a rendered total from two different views.
//
// A failed pass still yields a view: the store keeps the last one it did manage
// to read, and a brief built on slightly old memory beats no brief at all. The
// error rides alongside, for the server to log — this package never logs.
func (s *Store) View(ctx Context) (View, error) {
	if s == nil {
		return View{}, nil
	}

	fi, exists, err := statMD(s.mdPath)

	s.mu.Lock()
	defer s.mu.Unlock()
	var delta Delta
	if err == nil {
		delta, err = s.reconcileGatedLocked(fi, exists)
	}
	entries := s.renderLocked(ctx)
	return View{
		Entries: entries, Block: renderBlock(entries), Total: len(s.entries),
		Health: s.health, Delta: delta, Folds: s.drainFoldsLocked(), Unlocked: s.unlocked,
	}, err
}

// statMD stats score.md, reporting whether it is there. A missing file is not
// an error — it is a row of the recovery table all its own, see projectLocked —
// but anything else is.
func statMD(path string) (os.FileInfo, bool, error) {
	fi, err := os.Stat(path)
	switch {
	case err == nil:
		return fi, true, nil
	case os.IsNotExist(err):
		return nil, false, nil
	default:
		return nil, false, err
	}
}

// reconcileGatedLocked is the READ paths' pass: it runs only when score.md has
// moved since the last one. The caller holds the lock and supplies the stat the
// gate compares, taken off the lock and BEFORE the pass, so a save racing the
// read makes the pass look stale and re-run rather than seen.
func (s *Store) reconcileGatedLocked(fi os.FileInfo, exists bool) (Delta, error) {
	if !s.mdMovedLocked(exists, fi) {
		return Delta{}, nil
	}
	return s.reconcileLocked(fi, exists)
}

// reconcileNowLocked is the MUTATION paths' pass: unconditional, because no
// gate over filesystem timestamps is exact and a write must never overwrite a
// save it has not read (see Submit). The caller holds the lock.
func (s *Store) reconcileNowLocked() (Delta, error) {
	fi, exists, err := statMD(s.mdPath)
	if err != nil {
		return Delta{}, err
	}
	return s.reconcileLocked(fi, exists)
}

// mdMovedLocked reports whether score.md differs from the last pass's
// fingerprint. The caller holds the lock.
//
// The fingerprint is size, mtime, ctime, and inode rather than a content hash,
// because the gate has to be cheaper than the work it skips. Inode catches the
// write-temp-and-rename save that vim and most editors actually perform; ctime
// catches an in-place write whose mtime a tool restored. staleWindow then
// bounds what none of them can see — see its comment.
func (s *Store) mdMovedLocked(exists bool, fi os.FileInfo) bool {
	switch {
	case !s.mdKnown, !exists:
		// The store only ever fingerprints a file it has just read, and an absent
		// score.md returns before that (see reconcileLocked), so a file that is
		// not there now has moved by definition.
		return true
	case time.Since(fi.ModTime()) < staleWindow:
		return true
	}
	ino, ctime := fileIdentity(fi)
	return fi.Size() != s.mdSize || !fi.ModTime().Equal(s.mdMod) ||
		ino != s.mdIno || !ctime.Equal(s.mdCtime)
}

// noteMDFromLocked records an already-taken stat as the last one seen. The stat
// must have been taken BEFORE the file was read, so an operator's save that
// raced the read is not mistaken for one already folded in. A file the STORE
// wrote is never fingerprinted at all — see forgetMDLocked. The caller holds
// the lock.
func (s *Store) noteMDFromLocked(fi os.FileInfo) {
	s.mdKnown = true
	s.mdMod, s.mdSize = fi.ModTime(), fi.Size()
	s.mdIno, s.mdCtime = fileIdentity(fi)
}

// forgetMDLocked drops the fingerprint, so the next read re-parses score.md
// whatever its stat says. The caller holds the lock.
//
// Every write the store makes to score.md ends here, and deliberately does NOT
// re-fingerprint the path it just wrote. That would be a race with a permanent
// consequence: an editor's rename-save landing between the write and the stat
// clobbers what the store appended, and the store would then record the
// clobbering file as its own — the gate reporting "in sync" forever while the
// fleet is told about an entry the file no longer has. One extra parse after
// each write costs nothing next to that.
//
// Fingerprinting the file the store ACTUALLY wrote — fstat the temp fd before
// the rename, fstat the appended fd after the sync — was considered and
// rejected. It buys nothing on the rewrite path, because rename bumps the
// inode's ctime after that stat and the gate then reports "moved" anyway; and
// on the append path it reopens exactly the permanent-miss window above, for an
// in-place save landing between the write and the fstat. Pessimistic here is
// bounded, and exact is not.
//
// A pass that failed BEFORE folding the file in ends here too, for the plainer
// reason that the edits it did not read have to be read by the next one.
func (s *Store) forgetMDLocked() {
	s.mdKnown = false
}

// writeMDLocked replaces score.md with lines, atomically and durably, and
// settles the fingerprint. The caller holds the lock.
//
// It and appendMDLocked are the only writers of score.md, so the fingerprint is
// settled by the write itself rather than by each caller remembering to settle
// it. A forgotten settle is silent, permanent, and reproducible only with an
// editor in the loop — and R2's folding and R6's Refine each add a writer.
func (s *Store) writeMDLocked(lines []string) error {
	err := writeFileAtomic(s.mdPath, []byte(strings.Join(lines, "\n")), 0o600)
	s.forgetMDLocked()
	return err
}

// appendMDLocked durably appends one entry line to score.md and settles the
// fingerprint. See writeMDLocked. The caller holds the lock.
func (s *Store) appendMDLocked(line string) error {
	err := appendDurable(s.mdPath, []byte(line+"\n"))
	s.forgetMDLocked()
	return err
}

// applyLocked is the store's mutation order, in one place: the events are made
// durable, and ONLY then does apply move memory to match them. The log is the
// truth, so nothing the store believes — a counter, a tier, an entry — may
// outlive a failed append, and a path that reversed the two would report a
// promotion the next boot has never heard of.
//
// It was three hand-written sequences and three doc comments saying the same
// thing before, one per mutation path, and #43's user signal would have been the
// fourth — on the path where a tier running ahead of a failed append matters
// most. apply reports its own failure (submitLocked has a score.md line to
// append between the two), and memory that never landed is not committed.
//
// The commit is not part of the outcome; see refreshCacheLocked. The caller
// holds the lock.
func (s *Store) applyLocked(evs []event, apply func() error) error {
	if err := s.appendEvents(evs); err != nil {
		return err
	}
	if err := apply(); err != nil {
		return err
	}
	s.commitLocked()
	return nil
}

// commitLocked settles what a mutation leaves behind: the gauge of entries too
// heavy to inject, and the score.json cache. Every path that changes the entry
// set ends here, so neither can be forgotten by one added later. The caller
// holds the lock.
func (s *Store) commitLocked() {
	s.recountOversizedLocked()
	s.refreshCacheLocked()
}

// Submit records a note with its provenance and returns the entry it landed in,
// and whether it FOLDED into one the store already held rather than starting a
// new one — #38's "get back new or folded into id". Text is sanitised and
// weighed here, at the boundary the untrusted string enters the store on; see
// sanitize and maxEntryRunes.
//
// A repeat is counted into the existing entry instead of adding a line, so the
// same observation submitted by twelve panels is one entry with twelve
// reinforcements rather than twelve entries nobody can rank. What counts as a
// repeat is normalize, and nothing else.
func (s *Store) Submit(text string, prov Provenance) (Entry, bool, error) {
	if s == nil {
		return Entry{}, false, errDisabled
	}

	// Scrubbed and weighed as the entry it would become, so the admission policy
	// is asked once and in one place rather than restated here. A repeat is
	// weighed too: an over-long submission is refused whether or not it would
	// have folded, because the caller's mistake is the same either way.
	e := newEntry("", text, prov)
	switch {
	case e.Text == "":
		return Entry{}, false, errors.New("score: empty submission")
	case !e.Injectable():
		return Entry{}, false, fmt.Errorf("score: submission is %d runes, limit is %d", len([]rune(e.Text)), maxEntryRunes)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Read-reconcile-write (invariant I3): a save the operator made since the
	// last read is folded in BEFORE the append, so their text is never
	// overwritten and a line they deleted is not resurrected by this write. The
	// reconcile is unconditional rather than fingerprint-gated: writes are rare,
	// and no gate over filesystem timestamps is exact.
	//
	// It also has to run before the fold target is chosen: a wording the operator
	// has just deleted must not swallow this submission, and one they have just
	// typed should.
	if _, err := s.reconcileNowLocked(); err != nil {
		return Entry{}, false, err
	}
	if i := s.foldTargetLocked(e.Text); i >= 0 {
		folded, err := s.foldLocked(i, e.Text, prov)
		return folded, true, err
	}
	stored, err := s.submitLocked(e.Text, prov)
	return stored, false, err
}

// sanitize scrubs text at the boundary it enters the store on, for the same
// reason internal/server's sanitizeReason scrubs an agent's reason where it
// enters the daemon — but against a sharper edge.
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
//
// It is applied to submissions, to replayed log records, to score.md's lines
// and bullets, and to the ids on them — every channel text reaches an Entry on.
// newEntry and setText are what make that exhaustive rather than remembered.
func sanitize(text string) string {
	if !needsScrub(text) {
		return text
	}
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

// needsScrub reports whether text needs the builder above at all. It says no
// only for plain, single-spaced, printable ASCII, which is what nearly every
// entry is — and every line of score.md is scrubbed twice on every dispatch
// while the file is being edited, so the common case must not rebuild the
// string. Anything else falls through and sanitize itself decides.
//
// The three rejected classes mirror sanitize's own: a byte outside printable
// ASCII (which is every control byte, and every rune sanitize might drop as Cf,
// C1, or the replacement character), and a space that is leading, trailing, or
// repeated (which is the whitespace folding).
func needsScrub(text string) bool {
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c < 0x20, c >= 0x7f:
			return true
		case c == ' ' && (i == 0 || i == len(text)-1 || text[i-1] == ' '):
			return true
		}
	}
	return false
}

// normalize is the folding key. Two wordings fold into one entry when, and only
// when, their normalised forms are byte-identical (#38 §1). This comment is the
// contract: it is what an operator wondering why two lines did not merge, and
// what R6's semantic `merge`, both read.
//
// What it considers:
//
//   - CASE. Unicode simple lower-casing, so "Run The Tests" and "run the tests"
//     are one entry. Simple rather than special-cased, and no locale is ever
//     consulted: invariant I1 asks the same log to yield the same tiers on every
//     machine, and locale-aware casing (Turkish dotless i) would make that false.
//   - WHITESPACE. Every run of whitespace collapses to one space and the ends
//     are trimmed, so a line an editor re-wrapped or re-indented still folds.
//   - TRAILING PUNCTUATION. Punctuation at the very END is dropped, so "run the
//     tests." folds with "run the tests". Only at the end: interior punctuation
//     is content — "don't" is not "dont", and "a, b" is not "a b".
//
// What it deliberately does NOT consider:
//
//   - MEANING. No stemming, no stop words, no synonyms, no similarity score, no
//     edit distance. Two wordings of the same observation each sit at tier 1 and
//     neither ever climbs. #38 §1 accepted that outright — Score remembers less
//     rather than remembering wrong — and merging by meaning is the conductor's
//     job in R6, where an agent proposes it and a human can see it happen.
//   - WORD ORDER. "tests before push" and "before push tests" are two entries.
//   - ACCENTS or UNICODE NORMAL FORM. The NFC and NFD spellings of one accented
//     word do not fold; normalising that needs golang.org/x/text and this
//     package is stdlib-only.
//
// It is a pure function of its argument — no clock, no map, no locale, no
// package state — which is what makes a replayed log fold identically (I1).
// It is sanitize plus two steps, rather than its own builder, because every
// input it is given has already been through sanitize — setText scrubs, aliases
// are former Entry.Texts, and both reconcile call sites scrub their line first —
// so the whitespace half of the contract was being implemented twice. Composing
// them keeps the "an editor re-wrapped it and it still folds" promise true for a
// caller that has NOT scrubbed, and costs nothing in the common case: sanitize
// returns its argument untouched for plain ASCII (needsScrub), strings.ToLower
// does the same when there is nothing to lower, and the trim is a reslice.
func normalize(text string) string {
	return strings.TrimRightFunc(strings.ToLower(sanitize(text)), trimmable)
}

// trimmable is what normalize drops from the END of a wording: punctuation, so
// "run the tests." folds with "run the tests", and the space a trimmed
// punctuation mark can leave behind ("run the tests ." → "run the tests ").
// Nothing else can end a sanitized string in a space.
func trimmable(r rune) bool {
	return unicode.IsPunct(r) || unicode.IsSpace(r)
}

// normEq reports whether text normalises to key, without normalising it: it
// walks text's runes through the same transform normalize applies and compares
// each against key as it goes. key MUST already be a normalize output — every
// caller has one in hand — since the comparison relies on it carrying no
// trimmable tail of its own.
//
// It exists because the folding key of an ALIAS is the one key the store cannot
// cache. Entry.norm covers the current wording, but a submission is matched
// against every prior wording too (invariant I4), and normalising those on each
// Submit built and threw away a string per alias per entry: at five thousand
// entries holding the full maxAliases, forty thousand allocations under the
// store mutex, stalling every concurrent View, to answer one lookup.
func normEq(text, key string) bool {
	var (
		n       int  // bytes of key matched so far
		emitted bool // anything at all has come out of the transform
		over    bool // past key's end, on runes only a trailing trim can excuse
		pending bool // a run of whitespace is waiting to become one space
	)
	// match consumes one transformed rune, reporting whether equality is still
	// possible. Runes past key's end are not a mismatch if the trim would have
	// taken them — that is how "run the tests." matches the key "run the tests".
	match := func(r rune) bool {
		switch {
		case over, n == len(key):
			if !trimmable(r) {
				return false
			}
			over = true
			return true
		}
		kr, size := utf8.DecodeRuneInString(key[n:])
		if kr != r {
			return false
		}
		n += size
		return true
	}
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
			pending = true
			continue
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == unicode.ReplacementChar:
			continue
		}
		if pending && emitted && !match(' ') {
			return false
		}
		pending = false
		emitted = true
		if !match(unicode.ToLower(r)) {
			return false
		}
	}
	return n == len(key)
}

// foldIndex answers "which entry already says this", mapping a normalised
// wording to the POSITION of the entry that owns it in the slice it was built
// over. Positions rather than ids because every consumer wants one: the pass
// that reads it goes on to reinforce the entry and name it in a fold record, and
// an id would have to be scanned back into a position to do either.
//
// It indexes what the entries VISIBLY say — their current wordings, in their
// order, and nothing else. Folding on the score.md path deletes the operator's
// line, and the justification for deleting it is that the file still shows the
// wording on another line: true of a current-text match, false of an alias. So a
// remembered wording may still COUNT a repeat on the submission path, where
// nothing is deleted (see foldTargetLocked), but on the file path it admits a
// new entry instead.
//
// It is only ever built and read, never iterated, so no answer of the store's
// depends on Go's map order (I1). Where two entries claim one wording — the
// operator pasted a duplicate and both lines carry ids — the FIRST in the entry
// order wins, so the winner is the file's order rather than the hash's.
type foldIndex map[string]int

// newFoldIndex indexes the given entries by their current wordings.
func newFoldIndex(entries []Entry) foldIndex {
	f := make(foldIndex, len(entries))
	for i := range entries {
		f.addKey(entries[i].norm, i)
	}
	return f
}

// addKey registers an already-normalised wording at position at, keeping the
// first claim on it.
//
// A wording that normalises to nothing is never registered. A line of pure
// punctuation is exactly that, and indexing it would fold every other such line
// into the first one — entries that share no word at all, merged because both
// were noise.
func (f foldIndex) addKey(key string, at int) {
	if key == "" {
		return
	}
	if _, taken := f[key]; !taken {
		f[key] = at
	}
}

// lookup reports which entry text is a repeat of, if any.
func (f foldIndex) lookup(text string) (int, bool) {
	at, ok := f[normalize(text)]
	return at, ok
}

// foldTargetLocked is the index of the entry text repeats, or -1 for a wording
// the store has never seen. An entry is repeated by its current text AND by
// every prior wording it has kept, which is what makes a repeat of a superseded
// phrasing still fold into the entry that was reworded (invariant I4). The
// caller holds the lock.
//
// Two linear passes rather than an index, because the index would be built for
// ONE lookup and thrown away — the build is already O(entries), so the map is
// pure overhead, and the alias half of it could not even reuse Entry.norm.
// Current wordings are scanned first and aliases second, which is the same
// precedence a single index gave: a wording an entry still says beats one it
// used to.
//
// Keeping an index on the Store instead would be state to invalidate at every
// submit, fold, reword, admission and retirement — the kind that goes stale
// silently — in a call that is about to fsync twice. Submit is a mutation path,
// not the render path: a dispatch reads through View, which builds the file
// pass's index only when score.md has actually moved.
func (s *Store) foldTargetLocked(text string) int {
	key := normalize(text)
	if key == "" {
		// Nothing owns the empty key; see addKey.
		return -1
	}
	for i := range s.entries {
		if s.entries[i].norm == key {
			return i
		}
	}
	for i := range s.entries {
		for _, a := range s.entries[i].Aliases {
			if normEq(a, key) {
				return i
			}
		}
	}
	return -1
}

// foldLocked counts a repeat into the entry at i instead of adding a line: one
// log event, one counter, and score.md is not touched at all — there is no new
// line to append, and the surviving wording is the one already in the file. It
// removed no line, so it owes none: the event is written with RemovedLine false.
// The caller holds the lock.
//
// The fold is RECORDED as well as counted, on the same buffer the file path
// uses, so the one log line per fold #38's lifecycle asks for has one producer
// and one shape. A submission that folded is the mutation an operator cannot see
// by looking — score.md does not move — which is precisely why it must be said.
func (s *Store) foldLocked(i int, text string, prov Provenance) (Entry, error) {
	now := time.Now().UTC()
	e := s.entries[i]
	evs := []event{foldEvent(e.Id, text, prov, now, false)}
	e.Reinforcements++
	if err := s.applyLocked(evs, func() error {
		s.entries[i] = e
		s.noteFoldsLocked([]Fold{{
			Id: e.Id, Text: e.Text, Repeat: text, Prov: prov,
			Reinforcements: e.Reinforcements, Duplicates: 1, Counted: true,
		}})
		return nil
	}); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Reinforce bumps an entry's counter and logs who did it: a fold when an agent
// reinforces (in #38's model a fold IS the agent-side reinforcement), a
// user-signal when the operator does. Either way the count can earn the entry a
func (s *Store) Reinforce(id, source string) error {
	if s == nil {
		return errDisabled
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Read-reconcile-write, as in Submit: reinforcing an entry the operator has
	// since deleted must fail, not resurrect it.
	if _, err := s.reconcileNowLocked(); err != nil {
		return err
	}
	i := s.indexLocked(id)
	if i < 0 {
		return fmt.Errorf("score: no entry %q", id)
	}
	name := EventFolded
	if source == "user" {
		name = EventUserSignal
	}
	now := time.Now().UTC()
	e := s.entries[i]
	evs := []event{{Schema: Schema, Event: name, Id: id, At: now, Source: source}}
	e.Reinforcements++
	return s.applyLocked(evs, func() error {
		s.entries[i] = e
		return nil
	})
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
// real agent's brief, so nothing the store seeds may reach it — which is why an
// absent score.md is written back as comment lines rather than entries — and
// nothing over the weight cap may reach it either, which is why an over-long
// operator line is skipped here rather than refused at the file (maxEntryRunes).
//
// Render does NOT reconcile: the caller does, once per read path, so that a
// status reply's Len and Render see one consistent view and the store never
// does file I/O from a function with no way to report its errors.
//
// S0 dummy (R3 replaces): the first renderLimit renderable entries in file
// order — the context is ignored and nothing is ranked.
func (s *Store) Render(ctx Context) []Entry {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renderLocked(ctx)
}

// renderLocked is Render's body, so View — which must not let go of the lock
// between reconciling and answering — can ask without dropping it. The caller
// holds the lock.
func (s *Store) renderLocked(ctx Context) []Entry {
	_ = ctx
	var out []Entry
	for _, e := range s.entries {
		if !e.Injectable() {
			continue
		}
		if out == nil {
			// Sized on first use rather than up front, because Render's contract is
			// nil — not an empty slice — when nothing renders, and scoreList tells
			// the two apart to keep score.list an array rather than JSON null.
			out = make([]Entry, 0, min(renderLimit, len(s.entries)))
		}
		out = append(out, e)
		if len(out) == renderLimit {
			break
		}
	}
	return out
}

// RenderBlock renders the injectable text block: a bordered "── Score ──"
// section listing Render's entries with their tier wording. It returns the
// empty string when there is nothing to inject. A dispatch takes the same block
// off View, which reconciles first.
func (s *Store) RenderBlock(ctx Context) string {
	return renderBlock(s.Render(ctx))
}

// renderBlock formats already-selected entries as that block.
func renderBlock(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}

	var b strings.Builder
	b.Grow(64 * (len(entries) + 1))
	b.WriteString("── Score ──\n")
	for _, e := range entries {
		b.WriteString("- ")
		b.WriteString(e.Text)
		b.WriteString(" [")
		b.WriteString(tierWording(e.Tier))
		b.WriteString("]\n")
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

// submitLocked appends a new entry to all three files. The caller holds the
// lock.
//
// The write order is what makes the reported outcome honest, and applyLocked is
// where it is stated: the log goes first, then score.md, and memory only after
// both landed, so a failure at either step returns an error with the entry
// absent from memory, from Render, and — after the next boot's recovery pass,
// which retires a logged entry score.md lacks — from the store. The snapshot
// comes last and its failure is NOT an error, because score.json is a cache this
// package rebuilds from the log at every Open: reporting a failed cache refresh
// as a failed submission would tell the caller nothing was stored when the entry
// is durable in two files.
func (s *Store) submitLocked(text string, prov Provenance) (Entry, error) {
	id, err := s.newIDLocked()
	if err != nil {
		return Entry{}, err
	}
	e := newEntry(id, text, prov)
	if err := s.applyLocked([]event{{
		Schema: Schema, Event: EventSubmitted, Id: id, At: time.Now().UTC(),
		Source: prov.Source, Text: text, Prov: &prov,
	}}, func() error {
		if err := s.appendMDLocked(formatLine(e.Id, e.Text)); err != nil {
			return err
		}
		s.entries = append(s.entries, e)
		return nil
	}); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// replayLocked rebuilds the entries and the burned-id set from the event log —
// the truth. A missing log is an empty store; an unparsable line is a torn
// append, so it is skipped and counted rather than failing the boot. The caller
// holds the lock.
//
// Every id the log has ever named is burned, live or retired. An id must never
// be reissued: the log is keyed by id, so a reissued id would silently graft a
// retired entry's history onto a new one.
func (s *Store) replayLocked() error {
	data, err := os.ReadFile(s.eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	live := map[string]*Entry{}
	placed := map[string]bool{} // ids already in order; a retire-then-restore must not re-add
	var order []string
	// Split and decode over the file's own bytes, and reuse one event across the
	// loop: a string split copies the whole log a second time and every line a
	// third, which on a 200k-event boot is most of the garbage the daemon makes
	// before it serves anything.
	var ev event
	for _, line := range bytes.Split(data, newline) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		ev = event{} // reused, so a field this record omits must not carry over
		if json.Unmarshal(line, &ev) != nil || ev.Id == "" || ev.Event == "" {
			s.health.TornEvents++
			continue
		}
		s.burned[ev.Id] = struct{}{}
		switch ev.Event {
		case EventSubmitted:
			if !placed[ev.Id] {
				order = append(order, ev.Id)
				placed[ev.Id] = true
			}
			var prov Provenance
			switch {
			case ev.Prov != nil:
				prov = *ev.Prov
			default:
				// A record from before events carried a provenance object still
				// carried the source as a plain field. Take it: R3 ranks on
				// provenance and I6 rests on the user/agent distinction, so
				// "unknown" is a worse default than the one fact the line has.
				prov.Source = ev.Source
			}
			e := newEntry(ev.Id, ev.Text, prov)
			live[ev.Id] = &e
		case EventFolded, EventUserSignal:
			if e := live[ev.Id]; e != nil {
				e.Reinforcements++
				// The wording a fold took out of score.md, so the boot pass can
				// check it against the file — see Store.owed.
				//
				// RemovedLine is what narrows this to the folds that can owe a
				// removal at all. A SUBMIT-path fold never touches score.md: an
				// agent repeated something, the entry's counter moved, and no line
				// was ever there to remove. Seeding from those too made the boot
				// pass swallow an operator who later typed the same bytes into
				// their file — a genuine repeat, against a removal that was never
				// owed by anyone. A user-signal is out for the same reason: only a
				// fold removes a line.
				if ev.Event == EventFolded && ev.RemovedLine && ev.Text != "" {
					if s.owed == nil {
						s.owed = map[string][]string{}
					}
					s.owed[ev.Id] = addOwed(s.owed[ev.Id], sanitize(ev.Text))
				}
			}
		case EventEdited:
			if e := live[ev.Id]; e != nil {
				reword(e, ev.Text, &s.health.AliasEvictions)
				// An edit through score.md is itself the user signal #38 §3 asks
				// for; a conductor reword (R6) carries another source and changes
				// only the wording.
				if ev.Source == "user" {
					e.Reinforcements++
				}
			}
		case EventRetired:
			delete(live, ev.Id)
		}
	}

	s.entries = s.entries[:0]
	for _, id := range order {
		if e := live[id]; e != nil {
			s.entries = append(s.entries, *e)
		}
	}
	// A retired entry can owe nothing: its lines are gone with it.
	for id := range s.owed {
		if live[id] == nil {
			delete(s.owed, id)
		}
	}
	return nil
}

// reconcileLocked is #38 §3's table — the boot recovery one and the live-editor
// one, which are the same table read at different moments. score.md decides
// which entries exist and what they say; the log supplies everything else and
// records what the pass changed. The caller holds the lock.
//
//   - a line whose id is live and whose text is unchanged → nothing happened
//   - a line whose id is live and whose text changed → superseded: the old
//     wording becomes an alias and the edit counts as a user signal
//   - a line whose id is live but whose text the log never carried → the text is
//     adopted silently: unknown is not "was empty", so it is not an edit and
//     must not manufacture the user signal I6 rests on
//   - a line whose id is unknown or retired → admitted as a user-sourced entry
//     under that id (the crash-after-md-before-log window, and the operator
//     restoring a line they had deleted), and admitted even where its wording
//     repeats a live entry's: only an ID-LESS duplicate folds. A line that
//     carries an id carries the operator's own decision about which entry it is,
//     and I3 says their file wins. The cost is that restoring a deleted line
//     whose wording duplicates a live entry leaves two entries saying the same
//     thing, which no later fold can join — so #38 §3's "folding cleans those
//     up" holds for the id-less duplicate an editing operator actually makes,
//     and R6's conductor `merge` is what resolves the other one.
//   - a bullet with no id, or a second line repeating an id already resolved,
//     that REPEATS a wording the file still shows on another line → folded into
//     that entry and dropped from the file; see the resolution below
//   - any other bullet with no id, or second line repeating an id → a new
//     user-sourced entry whose id is generated and written back into the file
//   - a live entry no line carries → retired, in the log, never destroyed (I7)
//
// An ABSENT score.md is not in that table at all; see projectLocked.
//
// The whole pass is computed without touching the disk, then committed as ONE
// append and ONE fsync, and the file is rewritten only after that append lands.
// Per-line durability would be per-line fsync, and this runs on the dispatch
// path: emptying a thousand-entry file would hold the store mutex — and with it
// every other score-touching connection — for seconds. The log still records one
// event per change; only the syscall pattern is batched.
//
// score.md itself is rewritten only to write back ids the pass assigned and to
// remove the duplicate lines it folded: every other byte of the operator's file
// is preserved verbatim.
func (s *Store) reconcileLocked(fi os.FileInfo, exists bool) (delta Delta, err error) {
	// The gauge and the cache follow the entry set, and this is the only place a
	// pass can have moved it — however the pass returns, including the early exit
	// for an absent score.md and the file rewrite that failed after its events
	// were already durable.
	defer func() {
		if delta != (Delta{}) {
			s.commitLocked()
		}
	}()

	// What the pass has to say about itself. The counters are accumulated HERE
	// and folded into s.health only where the pass commits its entries, so "a
	// pass that does not commit changes nothing" needs no unwind step to get
	// wrong: it is simply not applied. The earlier shape mutated s.health during
	// the compute phase and restored it on a deferred tail guarded by a zero
	// delta — which a pass whose only work was a SWALLOWED fold satisfies, so a
	// failing rewrite reverted a counter belonging to a pass that kept every
	// other piece of its state. (Counters replayLocked moves are a different
	// thing entirely: they describe the whole log's history, as TornEvents
	// always has.)
	var pass Health

	// countable reports whether a duplicate line may move its entry's counter. It
	// is the ONLY thing the owed derivation reaches, and that is a fact about this
	// closure's signature rather than a claim about statement order in a loop
	// three hundred lines long: the sole thing it can return is a bool, and the
	// line's removal is decided by the caller before it is asked. So the worst the
	// derivation can do — however wrong its premise, on any log, from any build —
	// is count a repeat it should have counted, or fail to. It can never remove a
	// line that folding had not already decided to remove.
	countable := func(id, text string) bool {
		return !slices.Contains(s.owed[id], text)
	}

	var lines []string
	if exists {
		data, rerr := os.ReadFile(s.mdPath)
		switch {
		case rerr == nil:
			lines = strings.Split(string(data), "\n")
		case os.IsNotExist(rerr):
			exists = false
		default:
			return Delta{}, rerr
		}
	}
	if !exists {
		return s.projectLocked()
	}

	known := make(map[string]int, len(s.entries))
	for i := range s.entries {
		known[s.entries[i].Id] = i
	}

	var (
		// Sized for the steady state — the entries the store already holds, plus
		// room for a handful of lines the operator has just added.
		next     = make([]Entry, 0, len(s.entries)+8)
		out      = make([]string, 0, len(lines))
		resolved = make(map[string]bool, len(s.entries))
		pending  []event
		rewrite  bool
		now      = time.Now().UTC()
		// The provenance of everything score.md contributes: whoever wrote the
		// line, the store only knows it as the operator's own file.
		userProv = Provenance{Source: "user"}
		// bullets are the lines that named no live entry — a plain bullet, or one
		// repeating an id the pass already placed. Whether each is a repeat of
		// something else in the file cannot be known until every line has been
		// read, so they are collected here and resolved below, in file order.
		bullets []bullet
		// folds is what the pass folded, one record per entry: for the server to
		// log, and — if the rewrite fails — for the removals the store still owes.
		folds []Fold
	)
	// admit takes a line into the store as the operator's, under the id the line
	// already carries.
	admit := func(id, text string) Entry {
		pending = append(pending, event{
			Schema: Schema, Event: EventSubmitted, Id: id, At: now,
			Source: userProv.Source, Text: text, Prov: &userProv,
		})
		s.burned[id] = struct{}{}
		resolved[id] = true
		delta.Admitted++
		return newEntry(id, text, userProv)
	}

	for _, line := range lines {
		id, text, ok := parseLine(line)
		if ok {
			text = sanitize(text)
		}
		// A line with an id but no text is not an entry: text is what an entry is.
		// It stays in the file as the operator's own prose, and the entry it used
		// to carry retires below — restoring the text re-admits it under its id.
		// What is left is a line the operator wrote as a bullet with no id, which
		// one is drawn for below.
		if !ok || text == "" {
			bullet, isEntry := parseBullet(line)
			if !isEntry {
				out = append(out, line)
				continue
			}
			id, text = "", sanitize(bullet)
		}

		idx, live := known[id]
		switch {
		case id == "", resolved[id]:
			// A bullet carrying no id, or a file repeating an id the pass already
			// placed. It becomes either a fold or a new entry, and which one
			// depends on lines this pass has not read yet, so the line is kept as
			// the operator wrote it and the decision is taken below.
			out = append(out, line)
			bullets = append(bullets, bullet{at: len(out) - 1, text: text})
		case live:
			e := s.entries[idx]
			resolved[id] = true
			switch {
			case e.Text == "":
				// The log recorded this entry before events carried their text, so
				// the store never knew a wording to be edited AWAY from. Adopt the
				// file's — and RECORD the adoption, or every boot re-adopts and a
				// wording the log never learned dies with the line under I7. The
				// event is sourced to the recovery pass rather than to the
				// operator, because nobody edited anything: a user signal here
				// would be manufactured, and I6 rests on that signal being real.
				pending = append(pending, event{
					Schema: Schema, Event: EventEdited, Id: id, At: now,
					Source: sourceRecovery, Text: text,
				})
				e.setText(text)
				delta.Adopted++
			case e.Text != text:
				pending = append(pending, event{
					Schema: Schema, Event: EventEdited, Id: id, At: now,
					Source: "user", Text: text,
				})
				reword(&e, text, &pass.AliasEvictions)
				e.Reinforcements++
				delta.Superseded++
			}
			next = append(next, e)
			out = append(out, line)
		default:
			if _, seen := s.burned[id]; seen {
				// The log knows this id but has no live entry for it: its
				// submission was torn away or retired, so whatever provenance it
				// carried is gone and the entry re-enters as the operator's.
				// Counted separately because R3 ranks on provenance.
				delta.Reattributed++
			}
			next = append(next, admit(id, text))
			out = append(out, line)
		}
	}

	// Now that every line has been read, the bullets can be told apart: a repeat
	// of an entry the file still carries, or a new entry of the operator's.
	//
	// The index is built over NEXT — the entries that survive this pass — which
	// is what makes a fold safe and order-independent at once. A repeat can never
	// be counted into an entry the retire loop below is about to remove, because
	// such an entry is not in next; and a duplicate pasted above its twin behaves
	// exactly like one pasted below it, because neither is resolved until both
	// lines have been read.
	//
	// It answers with a POSITION in next, so the fold record and the count both
	// land here, beside the decision that earned them, rather than in a second
	// loop over every surviving entry whose only work was turning ids back into
	// positions. Determinism is untouched: bullets are walked in file order,
	// which is what that loop was reconstructing.
	//
	// Both maps below are left nil until something needs them: a file whose every
	// line already carries an id — the overwhelmingly common one, re-read on every
	// dispatch while the operator has it open — allocates nothing here at all.
	var (
		folded  map[int]int // fold record index by position in next
		dropped []int       // positions in out to remove
		// owing is what the pass will owe if its rewrite fails: entry id → every
		// wording it folded out of the file, since a failed rewrite leaves all of
		// them on their lines. Collected where the line is dropped rather than
		// from the fold records, which hold one wording per entry however many
		// lines went. Left nil until a line is actually folded, so the common pass
		// allocates nothing.
		owing map[string][]string
	)
	if len(bullets) > 0 {
		index := newFoldIndex(next)
		// countRepeat records ONE repeat against the entry at position target in
		// next. The event's SOURCE is what says the operator folded this, not the
		// event's NAME; see foldEvent. RemovedLine is true because this pass is
		// about to take the line out — the effect, not the provenance, is what
		// decides whether a removal can be owed.
		//
		// The one-per-entry-per-pass cap is the callers' to keep, and both of them
		// keep it the same way: they call this only where the entry's fold record
		// is not already marked Counted, and they mark it immediately after.
		countRepeat := func(target int, text string) {
			pending = append(pending, foldEvent(next[target].Id, text, userProv, now, true))
			next[target].Reinforcements++
			delta.Folded++
		}
		for _, b := range bullets {
			target, repeat := index.lookup(b.text)
			if !repeat {
				id, ierr := s.newIDLocked()
				if ierr != nil {
					return Delta{}, ierr
				}
				next = append(next, admit(id, b.text))
				out[b.at] = formatLine(id, b.text)
				// The entry just appended already holds this wording's key: the
				// bullet's text was normalised for the lookup above and again by
				// setText, and normalising it a third time here bought nothing.
				index.addKey(next[len(next)-1].norm, len(next)-1)
				rewrite = true
				continue
			}
			// The duplicate LINE is dropped from score.md, and that is the one
			// place this pass edits the operator's file beyond writing an id back.
			// Leaving it would be worse in both directions: with no id it would
			// fold again on every pass, counting one paste forever; with the
			// target's id written into it, the file would carry one entry on two
			// lines and the next pass would split them again. What is lost is a
			// line that normalises to one still in the file, and nothing is
			// destroyed (I7) — the exact bytes, and the fact that the operator
			// typed them, are on the fold event.
			//
			// Decided BEFORE anything is counted, and without consulting the owed
			// derivation — the invariant countable exists to make structural.
			dropped = append(dropped, b.at)
			rewrite = true
			// Every dropped line owes its removal until the rewrite lands, whether
			// or not it moved a counter. The two are separate questions, and a
			// wording this pass forgets is one the next pass counts again.
			if owing == nil {
				owing = make(map[string][]string, 1)
			}
			owing[next[target].Id] = addOwed(owing[next[target].Id], b.text)
			if at, seen := folded[target]; seen {
				folds[at].Duplicates++
				// The entry takes one reinforcement per pass, but WHICH of its
				// duplicate lines earns it must not be settled by whichever the
				// file happens to list first. An earlier line may have been
				// declined because the store already owed ITS removal, while this
				// one is a genuine repeat with a claim to the count. So the
				// question is asked again for this line, and the record promoted if
				// it passes.
				//
				// The pass's cap survives it: the only route in is a record that
				// is not yet Counted, and the promotion marks it Counted before the
				// next duplicate is read, so however many lines a paste carries the
				// entry still moves once. The record is re-pointed at the line that
				// actually earned the count, since a record naming a line it
				// declined while claiming Counted would be the log describing
				// something that did not happen.
				if !folds[at].Counted && countable(next[target].Id, b.text) {
					countRepeat(target, b.text)
					folds[at].Counted, folds[at].Repeat = true, b.text
					folds[at].Reinforcements = next[target].Reinforcements
				}
				continue
			}
			// ONE reinforcement per entry per pass, however many lines carry the
			// wording. A pass is one observation of the file's state, and #37's
			// model is that recurrence means the observation CAME BACK after being
			// recorded — a 500-line clipboard paste is one action, not five
			// hundred returns. (A stale editor buffer re-saved by hand still
			// counts once per save; the store cannot tell that from deliberate
			// emphasis, and R4 weighs what a user's repeat is worth.)
			//
			// The count is also skipped when the store already OWES this exact
			// line's removal: it folded once, the log holds it, and only the
			// deletion is outstanding — counting again would let one paste climb
			// the ladder on its own. What is declined is THIS line's claim to the
			// pass's one count, not the entry's: another duplicate may still earn
			// it, which is what the promotion above is for. The loss is one
			// occurrence and never text, since those exact bytes are already in the
			// log on the earlier fold event; it lands on the SwallowedRepeats gauge
			// and the fold record names the wording, so the store says out loud
			// what it declined to count. The alternative at this boundary was one paste counted TWICE
			// and promoted, and "remembers less" is the side of that trade this
			// store takes everywhere else.
			counted := countable(next[target].Id, b.text)
			if counted {
				countRepeat(target, b.text)
			} else {
				pass.SwallowedRepeats++
			}
			if folded == nil {
				folded = make(map[int]int, 2)
			}
			folded[target] = len(folds)
			folds = append(folds, Fold{
				Id: next[target].Id, Text: next[target].Text, Repeat: b.text, Prov: userProv,
				Reinforcements: next[target].Reinforcements, Duplicates: 1, Counted: counted, FromFile: true,
			})
		}
	}

	for i := range s.entries {
		id := s.entries[i].Id
		if resolved[id] {
			continue
		}
		pending = append(pending, event{Schema: Schema, Event: EventRetired, Id: id, At: now})
		delta.Retired++
	}

	// One append, one fsync, for everything the pass decided — the folds
	// included, and BEFORE the rewrite that removes the lines they came from.
	// That order is the whole guarantee: score.md is the only place a duplicate
	// line exists, so destroying it before the record of it is durable is the one
	// way this store can lose an operator's text outright, and no probe of the
	// log's writability is the same promise as a landed write. What the order
	// costs is a fold that is durable while its line is still in the file, which
	// the owed bookkeeping below settles.
	//
	// Nothing above touched the store, so a failure here leaves it exactly as it
	// was and says so — the pass's own counters included, since they are still
	// sitting in `pass`. The dropped fingerprint makes the next read try again.
	// Ids drawn for a failed pass stay burned, which costs nothing — an id nobody
	// used is simply never issued.
	if len(pending) > 0 {
		if err := s.appendEvents(pending); err != nil {
			s.forgetMDLocked()
			return Delta{}, err
		}
	}
	// The commit point: memory and the counters that describe it move together,
	// once, and only here. Everything after this either finishes the file or
	// fails trying, and both keep what this line just made true.
	s.entries = next
	s.health.SwallowedRepeats += pass.SwallowedRepeats
	s.health.AliasEvictions += pass.AliasEvictions

	if len(dropped) > 0 {
		// Drop the folded lines from the file the pass is about to write. They
		// were kept in place until now so that a pass failing anywhere above
		// leaves the operator's file exactly as they wrote it. Filtered in place:
		// out was made by this pass, nothing else holds it, and every element only
		// ever moves leftward.
		kept, d := out[:0], 0
		for i, line := range out {
			if d < len(dropped) && dropped[d] == i {
				d++
				continue
			}
			kept = append(kept, line)
		}
		out = kept
	}

	if !rewrite {
		// No line was dropped — every fold asks for a rewrite — so this pass
		// re-incurred nothing, and it has just read the whole file without finding
		// the bytes of any debt it was carrying. Every one of them is settled.
		s.owed = nil
		// Remember the stat taken BEFORE the read rather than a fresh one: a save
		// landing between the two makes the next pass look stale and re-run, which
		// is idempotent — the other order would mark an unread edit as seen.
		s.noteMDFromLocked(fi)
		return delta, nil
	}

	// A whole-file replace, because the ids have to be written back into the
	// lines that lack them and a partial rewrite of a file a person is editing is
	// worse. It is the one place the store can lose an operator's work: a save
	// landing between the ReadFile above and this rename is overwritten, and
	// unlike a lost server write it is not replayable from the log. The window is
	// a few hundred microseconds and only opens when the operator has just added
	// an id-less bullet, which is why it is accepted rather than locked against —
	// the alternative is holding a lock across an operator's editing session.
	//
	// A failure here is returned, not unwound: the events above are already
	// durable, so memory keeps them, and the file simply does not have the ids
	// yet, so the next pass re-admits those lines under new ids and retires
	// these. Noisy in the log, correct in the end. The fingerprint is dropped
	// either way, by the write itself.
	if err = s.writeMDLocked(out); err != nil {
		// The duplicate lines are still in the file and their folds are already
		// durable, so their removal is now what the store OWES. "Noisy in the log,
		// correct in the end" holds for an admit, an edit and a retire, because
		// each is idempotent across passes — the next pass reads the same file and
		// decides the same thing. A fold is not. Its trigger is the LINE this
		// write should have removed, so without this the next pass counts the same
		// paste again, and the one after that again, until the disk lets go: one
		// paste climbing the ladder on its own.
		//
		// Debts this pass did NOT re-incur are settled by the same assignment,
		// which is the whole clearing rule: their bytes were not in the file this
		// pass just read.
		s.owed = owing
		// Reported all the same, with Removed false. The fold is durable and it
		// counted, so saying nothing would hide a change the store really made —
		// but the lines are still in the operator's file, and a record that let
		// the daemon announce a deletion here would be describing something that
		// did not happen.
		s.noteFoldsLocked(folds)
		return delta, err
	}
	// The lines are gone from the file now, and only now, so this is where a
	// record may say so — and where every debt is settled, this pass's folds
	// included: the file was rewritten without their bytes.
	for i := range folds {
		folds[i].Removed = true
	}
	s.noteFoldsLocked(folds)
	s.owed = nil
	return delta, nil
}

// addOwed records that one more wording is owed a removal, keeping the list
// deduplicated — the same bytes on two lines are one debt — and bounded at
// maxOwedRemovals by dropping the OLDEST. Both places a debt is taken on go
// through it, so the shape and the bound are one rule rather than two.
//
// Order is the order the debts were taken on, which is file order in a pass and
// log order at boot. Nothing reads it as a sequence; it is a slice so that
// nothing CAN read a map's order as one (invariant I1).
func addOwed(owed []string, text string) []string {
	if slices.Contains(owed, text) {
		return owed
	}
	if len(owed) == maxOwedRemovals {
		owed = append(owed[:0], owed[1:]...)
	}
	return append(owed, text)
}

// bullet is a score.md line that named no live entry, held with the position it
// occupies in the pass's output so the pass can decide later whether the line
// becomes an entry or is folded away. See reconcileLocked.
type bullet struct {
	at   int
	text string
}

// noteFoldsLocked keeps this pass's fold records for the next View to report.
// The caller holds the lock.
//
// They are buffered rather than returned because a fold can be discovered by any
// pass, including the one a Submit runs before it writes, and the caller of a
// mutation has nowhere to put them. Every read drains the buffer, so the entry a
// fold deleted a line from is named in the daemon log whichever path noticed it.
func (s *Store) noteFoldsLocked(folds []Fold) {
	for i := range folds {
		if len(s.folds) >= maxFoldNotes {
			// Counted, not merely dropped: without this the only trace of a
			// removal nobody named is the arithmetic between a pass reporting two
			// hundred folds and a log carrying a hundred and twenty-eight lines.
			s.health.UnreportedFolds += len(folds) - i
			return
		}
		s.folds = append(s.folds, folds[i])
	}
}

// drainFoldsLocked takes the buffered fold records, leaving none behind: each
// one describes a deletion that happened once and is reported once. The caller
// holds the lock.
func (s *Store) drainFoldsLocked() []Fold {
	folds := s.folds
	s.folds = nil
	return folds
}

// projectLocked handles an ABSENT score.md, which #38's recovery table lists
// separately from an empty one. An EMPTY score.md is a statement: the operator
// deleted the lines, so the entries retire and the file wins. A MISSING file is
// not — an rsync that skipped it, a restore without it, a stray rm — and
// retiring on that reading would take the whole fleet memory out behind one log
// line. The table's ruling is to re-project what the log holds into a fresh
// file. Nothing is destroyed either way. The caller holds the lock.
//
// With no entries this is simply the first run: the header alone, which teaches
// the format and can never become memory.
func (s *Store) projectLocked() (Delta, error) {
	out := append([]string(nil), seedHeader...)
	for _, e := range s.entries {
		out = append(out, formatLine(e.Id, e.Text))
	}
	out = append(out, "") // trailing newline
	if err := s.writeMDLocked(out); err != nil {
		return Delta{}, err
	}
	// The file now carries one line per entry and no duplicates, so nothing the
	// store owed a removal is in it any more; see Store.owed.
	s.owed = nil
	return Delta{Reprojected: len(s.entries)}, nil
}

// recountOversizedLocked refreshes the gauge of entries too heavy to inject.
// The caller holds the lock.
func (s *Store) recountOversizedLocked() {
	n := 0
	for _, e := range s.entries {
		if !e.Injectable() {
			n++
		}
	}
	s.health.Oversized = n
}

// appendAlias keeps a prior wording for folding, newest last, without repeats
// and no more than maxAliases of them. It never writes through the source
// slice's spare capacity, because the entry it came from may still be held by a
// caller of Render.
//
// "Without repeats" means without repeats AS THE INDEX SEES THEM: two wordings
// with one folding key are one alias, because that is all either can ever match.
// Storing "x" and "X." both would grow the list — and score.json, and every
// boot's replay — with a distinction nothing downstream can act on.
//
// Past the cap the OLDEST wording goes, which is the one least likely to be
// repeated next; see maxAliases for why there is a cap at all.
func appendAlias(aliases []string, prior string) (out []string, evicted bool) {
	if prior == "" {
		return aliases, false
	}
	key := normalize(prior)
	for _, a := range aliases {
		// normEq rather than normalize(a) == key: this runs on every reword, and
		// building a key per alias just to throw it away was nine of its ten
		// allocations at the cap.
		if normEq(a, key) {
			return aliases, false
		}
	}
	out = append(aliases[:len(aliases):len(aliases)], prior)
	if len(out) > maxAliases {
		// Reported to the caller rather than done quietly: an evicted wording
		// stops folding, so the next repeat of it starts a second entry saying
		// what this one already says. That is the safe direction — a missed fold
		// costs a duplicate at tier 1, where a wrong one merges two things a
		// person did not ask to merge — but it is not nothing, and the health
		// gauge is where the store says what it has chosen to forget.
		out, evicted = out[len(out)-maxAliases:], true
	}
	return out, evicted
}

// reword retires an entry's current wording into its aliases and gives it the
// new one — the three steps that must happen together, with the eviction counter
// the one most easily forgotten. It is package-level and takes the counter by
// pointer because the two callers count into different places: replayLocked
// straight into the store's standing health, and a reconcile pass into the
// counters it applies only if it commits. R6's merge is slated to be the third.
func reword(e *Entry, text string, evictions *int) {
	var evicted bool
	e.Aliases, evicted = appendAlias(e.Aliases, e.Text)
	if evicted {
		*evictions++
	}
	e.setText(text)
}

// refreshCacheLocked rewrites score.json from memory. Its error is deliberately
// not returned: the snapshot is a cache the store rebuilds from the log at
// every Open and never reads back, so a failed refresh loses nothing and the
// next mutation retries it. Surfacing it as a mutation's error would report a
// durable write as a failure, which is the dishonesty this file exists to
// remove — but a silent failure is an ops blind spot, so it is counted for the
// server to report.
func (s *Store) refreshCacheLocked() {
	if err := s.writeSnapshotLocked(); err != nil {
		s.health.CacheWriteFailures++
	}
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
	return writeFileAtomic(s.jsonPath, data, 0o600)
}

// appendEvents appends every event as its own log line, in one write and one
// fsync. Batching is what keeps a large reconcile off the dispatch path's neck:
// the cost of a durable append is the fsync, so a pass over a thousand changed
// lines must not pay a thousand of them.
func (s *Store) appendEvents(evs []event) error {
	// Marshalled straight into the buffer that is written, because a batch can be
	// the whole store: joining the records and then re-joining that with the
	// trailing newline copied a thousand-event retire batch three times.
	var buf bytes.Buffer
	for _, ev := range evs {
		data, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		buf.Write(data)
		buf.Write(newline)
	}
	return appendDurable(s.eventsPath, buf.Bytes())
}

// appendDurable appends rec to the file at path, ALL OF IT OR NONE OF IT. rec
// carries its own trailing newline. If the file's last write was torn — no
// trailing newline, e.g. a crash mid-append — a newline is inserted first so the
// new record starts on its own line and the damage stays confined to the torn
// one.
//
// The all-or-nothing part is what a batched reconcile needs. A write that runs
// out of disk lands a PREFIX of what it was given: a 400-event retire batch
// stopped 356 events in, the pass reported failure and reverted memory, and the
// log was left insisting on 356 retirements the store believed had never
// happened. The next boot then settled that disagreement the only way it can —
// re-admitting those entries as the operator's — which quietly downgrades the
// provenance invariant I6 rests on, over a full disk. So a failed write is cut
// back to the length the file had before it. A torn TAIL is still possible from
// a crash, which no userspace code can prevent and replay already tolerates;
// this closes the case the store can see coming.
func appendDurable(path string, rec []byte) (err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
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
	size := st.Size()
	if size > 0 {
		last := make([]byte, 1)
		if _, err = f.ReadAt(last, size-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			// Its own one-byte write rather than a copy of rec with a newline in
			// front of it; the rollback below covers both writes either way.
			if _, err = f.Write(newline); err != nil {
				rollbackAppend(f, size)
				return err
			}
		}
	}
	if _, err = f.Write(rec); err != nil {
		rollbackAppend(f, size)
		return err
	}
	if err = f.Sync(); err != nil {
		// The bytes may or may not have reached the disk, so treat the append as
		// failed and unwind it for the same reason as a short write.
		rollbackAppend(f, size)
		return err
	}
	return nil
}

// rollbackAppend cuts a failed append back to the length the file had before it
// started, so a partial batch never becomes half a truth. Best effort by
// necessity — the caller is already returning the error that got us here, and a
// filesystem that cannot truncate leaves exactly the torn tail replay tolerates.
func rollbackAppend(f *os.File, size int64) {
	if err := f.Truncate(size); err != nil {
		return
	}
	_ = f.Sync()
}

// newIDLocked draws a fresh short hex id, retrying until it hits one the store
// has never used. The candidate is checked against the BURNED set, not against
// the live entries: an id whose entry the operator deleted is retired, not
// free, and reissuing it would point that entry's log history at the newcomer.
// The caller holds the lock.
func (s *Store) newIDLocked() (string, error) {
	for {
		var b [3]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		id := hex.EncodeToString(b[:])
		if _, taken := s.burned[id]; !taken {
			s.burned[id] = struct{}{}
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

// formatLine renders one entry as the score.md line parseLine reads back. The
// two live side by side because a writer that drifted from the reader would
// emit lines the store's own parser rejects, and the next pass would re-admit
// each of them under a fresh id and retire the original — silent entry churn
// rather than a compile error.
func formatLine(id, text string) string {
	return "- [" + id + "] " + text
}

// parseLine decodes one score.md line of the shape "- [id] text". Anything
// else — headings, blank lines, an operator's stray prose — is skipped rather
// than rejected, because score.md is theirs to edit.
//
// The id is scrubbed like the text, and for the same reason: score.md is an
// input channel, and Entry.Id rides score.list to a cockpit that draws it into
// a terminal. A control byte is no safer in an id than in a wording.
func parseLine(line string) (id, text string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(line), "- [")
	if !found {
		return "", "", false
	}
	id, text, found = strings.Cut(rest, "]")
	if !found || strings.ContainsAny(id, " \t") {
		return "", "", false
	}
	if id = sanitize(id); id == "" {
		return "", "", false
	}
	return id, strings.TrimSpace(text), true
}

// parseBullet decodes a line the operator wrote as an entry but gave no id: a
// markdown bullet, which is the shape score.md's own header teaches. Only
// bullets qualify, so a comment, a heading, or a paragraph left in the file
// stays prose and never becomes memory that reaches an agent's brief.
func parseBullet(line string) (text string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(line), "- ")
	if !found {
		return "", false
	}
	if rest = strings.TrimSpace(rest); rest == "" {
		return "", false
	}
	return rest, true
}

// writeFileAtomic writes data to path atomically and durably: a sibling temp
// file, fsync, rename into place, then a parent-directory fsync so the rename
// survives a crash. Same idiom as internal/state's snapshot — copied, not
// imported, to keep this package stdlib-only. The fixed ".tmp" name is safe
// because Open holds the directory's single-writer lock.
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
// which caps at renderLimit and withholds anything over the weight cap. Zero on
// the disabled (nil) store.
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
