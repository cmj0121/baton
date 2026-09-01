// Package score is baton's fleet memory: short operator- and agent-submitted
// notes that are rendered into every directly dispatched brief so the whole
// fleet keeps acting on them.
//
// The store owns two sibling files in its directory (per the #38
// I-invariants):
//
//   - score-events.jsonl — the append-only event log. The log is the truth:
//     every mutation is an event, and the entries are rebuilt from it at every
//     Open.
//   - score.md — the human-facing projection, one entry per line. Operators may
//     edit or delete it freely; it is the truth for an entry's TEXT and its
//     EXISTENCE, and Reconcile folds their edits back in.
//
// Two rules decide every conflict between them (#38 §3, invariant I3): the
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
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Schema is the current on-disk schema version, stamped on every record of the
// event log. Bump it on a breaking change to the log's shape.
//
// R1 added `text` and `provenance` to the event and `aliases` to the entry, R2
// added `tier` to the event, and R4 added `user_signals` to the entry. All of
// them are appended optional fields — an old line decodes with them zero, and
// an old reader ignores them — so the version stays 1, for the same reason
// proto.ProtocolVersion does not move on an appended field. R6 added two event
// NAMES rather than a field, which is additive in the same direction: replay
// never reads a record's schema and its EVENT switch has no default, so a build
// that does not know a name skips the record and under-claims. See the event
// constants.
//
// What an appended field cannot fix is a record that never carried the text at
// all, and the
// pre-R1 log is exactly that; replayLocked and reconcileLocked handle it as
// "text unknown" rather than as an empty entry the file then edits, because
// treating it as an edit would manufacture the user signal invariant I6 rests
// on. See reconcileLocked's live branch.
const Schema = 1

// defaultWorkingSet is how many entries one brief carries when score.working-set
// is unset. Seven, because #37 asks for a handful rather than a list: the block
// is prepended to every direct dispatch, and past a handful the agent is being
// handed a document to weigh rather than a few standing facts to act on. It is
// a starting point, not a discovery — which is why the knob exists.
//
// It caps the working set, NOT the store: everything outside it is still held,
// still reconciled, and still ranked, and score.list shows all of it with each
// entry's Standing — capped there too, the tier of everything past the seventh
// entry appeared in no surface at all (#42).
const defaultWorkingSet = 7

// defaultRankWeight is what an unset ranking weight becomes: x2 per dimension,
// so an entry matching all three context dimensions and freshest in the log
// outranks a stale unmatched one of the same tier by x16.
//
// Two rather than something finer because the weights are multiplied, not
// added: three dimensions at x2 already span a factor of eight, which is wider
// than the tier ladder itself, and a fourth factor for recency sits on top of
// that. A larger unit would let one matching dimension outweigh the earned tier
// entirely, which inverts what #37 says importance is.
const defaultRankWeight = 2.0

// minRankWeight is the floor every weight is raised to, and the value that
// switches a dimension OFF.
//
// Below one, a weight would PENALISE a match — an entry from this very panel's
// directory ranking below one from anywhere else — and no operator wants that
// semantics, so it is not offered. At exactly one the dimension multiplies by
// one whether it matches or not, which is the whole of "this does not matter to
// me" and the only rule an operator has to remember about the knob.
const minRankWeight = 1.0

// maxRankWeight bounds the other end, and is about arithmetic rather than taste.
// The rank is a product of five factors, so a weight far past this one carries
// the product out of float64 and every matching entry lands on +Inf together —
// the ranking silently stopping ranking, which is the failure mode this store
// spends its whole budget avoiding. A million already means "always first" by
// any measure the tier ladder can offer.
const maxRankWeight = 1e6

// Rank is the weight each ranking dimension carries. See Store.rankLocked for
// how they combine and Factors for what one entry's arithmetic looks like.
//
// A weight is a MULTIPLIER on a match, never a penalty on a miss: a dimension
// that does not match multiplies by one. So one is the floor and the off
// switch, and clamp is where both those rules live.
type Rank struct {
	// Recency is what the entry the fleet touched most recently is worth. It is
	// the one weight that is not a match/miss: every entry's recency factor is
	// interpolated linearly between one at the oldest last-movement position in
	// the log and this weight at the newest. See recencyFactor.
	//
	// Last MOVEMENT, not last reinforcement: an operator's reword moves an
	// entry's position while counting no reinforcement at all, so a fresh-looking
	// entry may have earned nothing lately. See Store.lastAt for the full list of
	// what moves it and noteEventLocked for why the edit is on that list.
	Recency float64 `json:"recency"`
	// Cwd, Profile and Group are each worth their weight when the entry's
	// recorded value for that dimension equals the dispatching panel's, and one
	// otherwise. They are independent, so an entry matching all three is worth
	// their product.
	Cwd     float64 `json:"cwd"`
	Profile float64 `json:"profile"`
	Group   float64 `json:"group"`
}

// Policy is everything about the store's behaviour an operator configures: the
// recurrence threshold a tier is earned at, how many entries one brief carries,
// and what each ranking dimension is worth.
//
// It is ONE value rather than a knob per setter because every one of them is a
// number the live store compares — none is state to swap — so they reload
// together on SIGHUP and are chosen together at Open. A zero field means "not
// set" and lands on this package's default; see clamp for each.
type Policy struct {
	PromoteAt     int
	UserSignalsAt int
	WorkingSet    int
	Rank          Rank
}

// ceiling is the highest tier e may currently be raised to: the top of the
// ladder once the user has said it UserSignalsAt times, and agentEarnedTier
// until then. It is the ONE place those two constants are chosen between, so
// invariant I6 is a property of this function rather than of every caller that
// promotes — and lifting the ceiling for an entry the user has spoken for
// cannot move any other entry's tier, because nothing here writes one.
func (p Policy) ceiling(e Entry) int {
	if e.UserSignals >= p.UserSignalsAt {
		return maxEarnedTier
	}
	return agentEarnedTier
}

// clamp is the policy a caller's numbers actually become. Every field is
// operator input arriving from a YAML file, so this is the boundary the store
// validates at, and it is total: there is no rejected policy, only a clamped
// one, because a daemon that refuses to boot over a weight is worse than one
// that says what it is running.
func (p Policy) clamp() Policy {
	return Policy{
		PromoteAt:     clampPromoteAt(p.PromoteAt),
		UserSignalsAt: clampUserSignalsAt(p.UserSignalsAt),
		WorkingSet:    clampWorkingSet(p.WorkingSet),
		Rank: Rank{
			Recency: clampWeight(p.Rank.Recency),
			Cwd:     clampWeight(p.Rank.Cwd),
			Profile: clampWeight(p.Rank.Profile),
			Group:   clampWeight(p.Rank.Group),
		},
	}
}

// clampWorkingSet is how many entries a brief actually carries. Zero — the
// value of a config key nobody wrote — and anything below it fall back to
// defaultWorkingSet, exactly as clampPromoteAt does, because "unset" and
// "fewer than one entry" are the same instruction to switch the feature off,
// and score.enabled is where that is said. There is no upper bound: an operator
// who asks for thirty gets thirty, and #37's "a handful" is a default rather
// than a rule the store enforces on them.
func clampWorkingSet(n int) int {
	if n < 1 {
		return defaultWorkingSet
	}
	return n
}

// clampWeight is what one ranking weight actually becomes.
//
// Two branches, and they say different things. Zero or less — the value of a
// key nobody wrote, and a negative nobody means — is UNSET, so it lands on
// defaultRankWeight, the same reading clampPromoteAt gives its zero. A weight
// the operator did write is kept, raised to minRankWeight if it is below the
// floor and lowered to maxRankWeight if it is past the ceiling; see both
// constants for why each end exists.
//
// The NaN branch is LOAD-BEARING, not tidiness, and it is tested by name
// because every ordinary comparison against a NaN is false so it would
// otherwise fall through the ordering into the "keep it" branch. A NaN weight
// makes a NaN rank, and a NaN rank makes rankBefore non-total: `a.Rank !=
// b.Rank` is TRUE for two NaNs, so the first key claims the pair is separable
// and `a.Rank > b.Rank` is false, so it separates them the wrong way round from
// b's point of view — and the position and id keys underneath are never
// reached. The sort's output then depends on the algorithm's comparison order,
// which is #38's verification check 4 failing outright. This branch is the only
// thing that keeps a NaN out of the arithmetic: every other input to the rank is
// an int tier or finite by construction.
//
// Note what this means for switching a dimension off: the value is 1.0, not 0.
// Zero is "I did not set this", and there is no spelling of "off" that is also
// a plausible typo for a weight.
func clampWeight(w float64) float64 {
	if math.IsNaN(w) || w <= 0 {
		return defaultRankWeight
	}
	return min(max(w, minRankWeight), maxRankWeight)
}

// maxBlockRunes caps the whole rendered block, counted over the runes its entry
// LINES contribute. It is the working-set budget's backstop, not a second
// budget.
//
// score.working-set has a floor and deliberately no ceiling: #37 calls a handful
// a default rather than a rule, and an operator who asks for thirty entries
// should get thirty. But the budget bounds the entry COUNT and nothing bounds
// what a count buys, so `working-set: 1000000` on a full store prepends a third
// of a megabyte to every direct dispatch — the same failure maxEntryRunes exists
// to stop, one level up. Capping the BYTES rather than the count is what closes
// it without taking the count knob away from the operator, and it holds however
// the size is reached: many entries, or few maximal ones.
//
// Eight thousand is TWENTY-FOUR entries at the maxEntryRunes limit — 8000/300 is
// twenty-six, but blockRunes counts the decoration too, and a maximal tier-2
// entry costs 324 runes with its bullet and its "note and take care". Twenty-five
// where they are all tier 1. Either way it is about three and a half times the
// default budget of seven, so it is out of the way of any working set #37 would
// call a handful.
//
// Past it the block simply stops — the lowest-ranked entries are dropped whole,
// never truncated mid-entry (#42's rendering rule) — and the drop is neither
// silent nor guessed at: score.list gives every entry a Standing, and an entry
// the backstop excluded reads block-full rather than below-budget. score.status
// says so too, beside the oversized gauge that explains the other cap.
//
// RUNES, not bytes, matching maxEntryRunes so the two caps count the same
// thing — but do not read "8000" as "8 KB" when sizing anything downstream.
// A full block of ASCII entries is about 7.8 KB; the same 8000 runes of zh-TW
// is about 22.8 KB, because UTF-8 spends three bytes on a CJK rune and only the
// bullet and the tier wording stay one. This project ships translated docs and
// has zh-TW users, so that is the ordinary case rather than the exotic one. The
// unit stays runes because the cap is about how much an agent is asked to read,
// which is closer to runes than to bytes, and because a byte cap would silently
// hold a CJK fleet to a third of the entries.
const maxBlockRunes = 8000

// maxEntryRunes caps the weight of one entry, mirroring internal/server's
// maxReasonRunes. #37 asks a score entry to be one to three sentences, and 300
// runes is that with room to spare. The cap matters because the working-set
// budget caps the entry COUNT, not the byte weight: without a length limit a
// single 200 KB entry outranks its way into the working set and is prepended to
// every direct dispatch, forever.
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

// maxEarnedTier is the highest tier ANY path in this package may write. It is
// the ladder's end: tierWording has no rung above it, so an entry standing
// above it would render as a rung this build has no words for.
//
// Three paths put a number in Entry.Tier — newEntry, reinforceLocked and
// replayLocked — and each is bounded where it stands. That there are only three
// is checked rather than remembered: TestEveryTierWriteIsRegistered parses every
// non-test file in this package and fails on a tier write it does not already
// know about, so a fourth cannot arrive unnoticed. What it does NOT check is
// what any of them then does with the number; the behavioural tests are what
// hold the bound itself.
const maxEarnedTier = 3

// agentEarnedTier is as high as RECURRENCE alone climbs, and is invariant I6
// stated as a number.
//
// #37 reserves the top rung for the user asking repeatedly, so no run of agent
// submissions may reach it: an agent that can climb there alone can make the
// fleet reinforce its own wrong conclusion, which is the failure Score exists
// to notice rather than to commit. An entry stops here until the user has said
// it Policy.UserSignalsAt times; see Policy.ceiling, which is the one place the
// two constants are chosen between.
//
// The bound is on the COMPUTED path — reinforceLocked, the only place a raised
// event is built. replayLocked is bounded by maxEarnedTier instead, and
// deliberately: a tier is replayed from the log rather than recomputed from the
// counts, so that the same log yields the same tiers whatever this machine's
// thresholds say (invariant I1). That is not a hole in I6, because the only
// thing that ever WRITES a raised event above this rung is a reinforcement the
// user's own signals unlocked. A log claiming otherwise came from a hand edit,
// and #38 declines to be a boundary against filesystem access.
const agentEarnedTier = 2

// maxAliases is how many prior wordings one entry keeps for folding.
//
// Eight, because an alias is worth keeping only while a repeat of it is more
// likely to mean this entry than to mean something else. An entry reworded eight
// times has drifted, and the ninth-oldest phrasing folding into what it says
// today is the shape of "remembering wrong" that #38 §1 spends its whole budget
// avoiding. The cap also bounds what an editing session can cost: three hundred
// rewords is three hundred wordings carried on the entry and rebuilt from the
// log at every boot, for an entry that is one line of text.
//
// Nothing is destroyed by the cap (I7): a dropped wording stays in the log with
// the edit that retired it. It simply stops folding.
const maxAliases = 8

// defaultPromoteAt is how many times an entry must be said before it is raised
// from tier 1 ("noted") to tier 2 ("note and take care") — counting its first
// submission and every reinforcement since.
//
// Three, because the number has to separate a recurrence from a coincidence and
// nothing smaller does. Two would promote on the first repeat, and one repeat is
// routinely an artefact rather than a pattern: an agent retrying a failed task
// submits the same observation twice in a minute, and two panels hitting one
// wall at the same moment do too. The third time is the first that cannot be
// explained by a single incident — it means the observation came back AFTER
// having already been recorded and repeated, which is exactly what #37 calls
// evidence rather than declaration. Higher than three delays every promotion by
// days of fleet time for a certainty nothing here needs: tier 2 says "note and
// take care", not "obey", and the entry was already being injected at tier 1.
//
// It is a starting point, not a discovery — which is why score.promote-at
// exists. See ScoreConfig.
const defaultPromoteAt = 3

// defaultUserSignalsAt is how many USER signals an entry needs before it may
// climb past agentEarnedTier — how many times the user has to say a thing for
// #37's "the user asking repeatedly" to be satisfied.
//
// Two, and a knob of its own rather than a reuse of score.promote-at, because
// the two answer different questions: promote-at asks how often an observation
// must recur before recurrence is evidence, and this asks how many times the
// user must weigh in before their weighing in is deliberate. One would make it
// "the user mentioned it", which is not repetition at all; three demands for
// one thing is rare enough in practice that the top tier would go unused.
//
// It counts the user SAYING THE THING AGAIN — a duplicate line in score.md that
// folds, a repeat submitted from their shell, a brief that matches — and never a
// correction to a line they already wrote; see the reword branch of
// reconcileLocked for why an edit is not a repetition.
//
// It is deliberately the SMALLEST number that means "more than once", because
// the count it is compared against can be SHORT — in one place, by at most one
// occurrence. A byte-identical retype of a just-folded wording is swallowed
// across a daemon restart, because the boot pass cannot tell an operator who
// retyped the same bytes from a removal it already owed; that is the only way a
// user signal genuinely goes missing, and Health.SwallowedRepeats counts it.
// Two tolerates it — the user says the thing a third time and the entry climbs
// — where a larger threshold makes them buy the rung twice.
//
// The store's other two conservative counts are NOT losses, and are not what
// this number is sized against. One reconcile pass counting a single
// reinforcement however many duplicate lines carry the wording is #37's own
// ruling, settled in R2: recurrence means the observation came back after being
// recorded, and one paste is one action rather than five hundred returns. And a
// repeated editor save of one duplicate counting once per save is R2 leaving
// the judgement here — this issue's answer is that it counts, because a user
// who saves the same duplicate twice has said it twice, which is the whole
// question the top tier turns on.
const defaultUserSignalsAt = 2

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
	scoreEvents = "score-events.jsonl" // append-only event log — the truth
	scoreLock   = "score.lock"         // the single-writer claim; never read, never removed
)

// newline is the record separator of both line-oriented files, as bytes: the
// log is split on it at every boot and appended with it at every mutation.
var newline = []byte{'\n'}

// Event names recorded in the log.
//
// The first six are #38 §3's vocabulary, frozen in S0 even where nothing emitted
// them yet. R6 adds two, because the conductor does two things §3's list has no
// word for: it JOINS two entries the normaliser failed to fold, and it pulls one
// DOWN that was raised in error. Spelling either with a borrowed name would have
// made the log lie to whoever reads it — a `raised` record carrying a lower tier
// is the plainest example — and the log is the operator's own history.
//
// Adding a name is additive in the direction a version guards: a build that does
// not know one skips the record (replayLocked's event switch has no default), so it
// under-claims — an entry without the alias, or on the rung it earned — exactly
// as panel.ParseState does for a state string it does not know. Schema therefore
// stays 1; see Schema, and note that replayLocked never reads a record's schema
// at all, so the field is stamped for whoever reads the log and for nothing else.
const (
	EventSubmitted  = "submitted"   // a new entry entered the store (Submit, and reconcile admitting a user's line)
	EventFolded     = "folded"      // a repeat was counted into an existing entry rather than added as a line
	EventRaised     = "raised"      // an entry's tier was promoted by recurrence
	EventUserSignal = "user-signal" // the operator reinforced an entry (emitted now, by Reinforce)
	EventEdited     = "edited"      // an entry's text changed — via score.md, or the conductor's reword
	EventRetired    = "retired"     // an entry left the store
	EventMerged     = "merged"      // the conductor gave an entry another's wording to fold on; Text is that wording
	EventLowered    = "lowered"     // the conductor pulled an entry down a rung; Tier is the rung it landed on
)

// SourceUser and SourceAgent are the two sources a reinforcement can carry, and
// the whole of #38 §4's discrimination.
//
// They are exported because this package does not make that distinction — the
// SERVER does, from the connection a command arrived on, and hands the answer
// here as provenance. Naming them once is what keeps the one string invariant
// I6 rests on from being spelled differently at the two ends of that hand-off.
//
// The distinction lives in the SOURCE and never in the event name. A user
// submission that folds into an entry emits EventFolded stamped SourceUser, not
// EventUserSignal, so a rule keyed on the name would miss every user signal
// that happened to repeat something already stored — which, repetition being
// the whole point of Score, is the case that matters most.
const (
	SourceUser  = "user"
	SourceAgent = "agent"
)

// sourceRecovery stamps an event the recovery pass emitted on nobody's behalf,
// so the history shows it as the machine's bookkeeping rather than as something
// the operator did. Nothing branches on it any more — an `edited` event moves no
// counter whatever its source — but the log is read by people, and an adoption
// attributed to the operator would be the history telling them they wrote
// something they did not.
const sourceRecovery = "recovery"

// sourceConductor stamps every event Refine emits. It names the DOOR the
// mutation came through, not a claim the store verified: Refine is the
// conductor's verb (#38 §1), and the SERVER's gate is what makes that true, by
// comparing the connection against the panel it marked Conductor.
//
// It is a third source beside SourceUser and SourceAgent for the reason
// sourceRecovery is one: it is neither. Only SourceUser moves Entry.UserSignals,
// and only an `edited` event stamped SourceUser moves an entry's recency (see
// noteEventLocked) — so giving the conductor's corrections a name of their own
// is what keeps R4's ruling from being re-opened by a new caller. A conductor
// reword counts nothing and moves nothing.
//
// Unexported, unlike the two sources #38 §4 discriminates on, because no caller
// supplies it: the store writes it, on the one path only the conductor reaches.
const sourceConductor = "conductor"

// Provenance records where an entry came from, so the ranking can weight an
// entry by the panel, profile, group, and directory that produced it. See
// Factors for which of these fields the ranking actually reads.
//
// Every field but Source is EMPTY on an entry the operator's own cockpit or
// score.md contributed: the store is told "user" and nothing else, because the
// server fills the rest from the panel the submitting connection declared and
// that panel's row in the fleet, and a cockpit connection declares no panel. So
// an operator's own entries never match a context dimension and rank on tier
// and recency alone.
//
// SourceGroup is appended and optional, so the schema stays 1 for the same
// reason the R1 and R2 additions did — see Schema. An entry stored before it
// existed decodes with it empty, which is exactly how the ranking treats an
// unrecorded dimension anyway.
type Provenance struct {
	SourcePanel   string `json:"source_panel,omitempty"`   // panel id that submitted it
	SourceProfile string `json:"source_profile,omitempty"` // agent profile of that panel
	SourceCwd     string `json:"source_cwd,omitempty"`     // working directory of that panel
	SourceGroup   string `json:"source_group,omitempty"`   // fleet group of that panel
	Source        string `json:"source"`                   // SourceUser or SourceAgent
}

// Entry is one remembered note. Its id is a short hex handle (like "e7f3a2")
// stable for the entry's whole life, so a score.md line and every log record
// about it name the same entry.
type Entry struct {
	Id   string `json:"id"`
	Text string `json:"text"`
	// Tier is the earned rung, 1 to maxEarnedTier; the wording each renders with
	// is in tierWording. Recurrence alone stops at agentEarnedTier — the top rung
	// is the user's to grant (invariant I6), and Policy.ceiling is where the two
	// bounds are chosen between.
	Tier           int        `json:"tier"`
	Provenance     Provenance `json:"provenance"`
	Reinforcements int        `json:"reinforcements"` // repeats counted into this entry since it was first said
	// UserSignals is how many of those reinforcements came from the USER rather
	// than from an agent — the count Policy.ceiling weighs, and so the only thing
	// that lets an entry past agentEarnedTier.
	//
	// It counts reinforcements and not occurrences, which is #38's glossary
	// exactly: a user signal IS a reinforcement that originates from the user, so
	// the submission that created the entry is not one of them however it was
	// sourced. An entry the user wrote into score.md and never touched again has
	// said itself once, which is not "repeatedly".
	//
	// What counts is Provenance.Source == SourceUser, never the event's NAME: a
	// user submission that folds emits EventFolded, and keying on EventUserSignal
	// would miss it. Appended and optional, so the schema stays 1 — see Schema.
	UserSignals int `json:"user_signals,omitempty"`
	// Aliases are the entry's prior wordings, newest last, kept so a repeat of a
	// superseded phrasing still folds into this entry (invariant I4). The list is
	// deduplicated by folding key and capped at maxAliases.
	Aliases []string `json:"aliases,omitempty"`

	// norm is Text's folding key, computed where the text is set rather than once
	// per pass. Unexported, so it reaches no file — it is derived from Text, and
	// a stored copy is one more thing that can stop being true. Every
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

// Context is what the renderer knows about the dispatch asking for entries: the
// panel a brief is being built for, and the three properties of that panel an
// entry's provenance can be matched against.
//
// Only Cwd, Profile and Group are ranked; see Factors, which is the list of
// what the arithmetic actually reads. Panel is carried for the caller's own
// use — a plugin hook is handed it, and it is what a fold record names — and is
// deliberately NOT a ranking dimension: an entry submitted by one panel is not
// thereby about that panel, panel ids are per-session while the log outlives
// them, and #38's fleet scope wants an entry to reach every panel rather than
// return to the one that said it.
//
// The zero Context is a legitimate value and not a missing one: it is what a
// read from the operator's cockpit carries, since a cockpit is not a panel and
// has no directory, profile or group of its own to match. Every context factor
// then reads one — see matchFactor, which never matches an empty value.
// The fields carry wire names because score.list ECHOES the context it ranked
// against, so an operator can see that a listing was contextless rather than
// inferring it from a column of ones. Every one is omitempty, which makes the
// contextless read a visibly empty object.
type Context struct {
	Panel   string `json:"panel,omitempty"`
	Profile string `json:"profile,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Group   string `json:"group,omitempty"`
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
	Raised       int // entries this pass's reinforcements earned a tier
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
	// Reinforcements, UserSignals and Tier are where the entry stands AFTER this
	// fold, so the log line that announces a fold also answers the questions it
	// raises — including the one R4 added, which is how close this entry now is
	// to the rung only the user can grant.
	Reinforcements int
	UserSignals    int
	Tier           int
	// At is when the fold happened, which is NOT when it is reported. Every
	// record but a signal's is buffered for the next read to drain, and that read
	// is the next dispatch, list or status — seconds or minutes later, or never
	// if the daemon stops first. A line stamped with its drain time is a line
	// that misdates the only event it exists to describe, so the moment is
	// carried rather than taken from the clock at the far end.
	At time.Time
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
	// FromSignal says the repeat arrived through Signal — text MATCHED against
	// the store rather than offered to it, which today is a brief the user
	// dispatched. It is the third door and needs a name of its own because
	// nothing else here separates it from the second: a user's Signal fold and a
	// user's Submit fold carry the same source and touch no line either way, and
	// the two are not the same thing to an operator who submitted nothing. #38 §4
	// accepts that a brief may coincidentally repeat an unrelated entry; that
	// cost is only bearable while it is VISIBLE, and this is what lets the log
	// name the door rather than leave it to be inferred.
	//
	// It rides the EVENT as well as this record (see event.Signal). A fold record
	// lives until the next read drains it; the log outlives the daemon, and
	// "which of my briefs promoted this" is a question asked days later.
	FromSignal bool
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
	// TornEvents is how many unparsable log lines were skipped at Open. It costs
	// no data: an append-only file can only be damaged in its last record, and
	// appendDurable unwinds every failure it can see coming, so what this counts
	// is the tail a crash tore off.
	TornEvents int
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
	// RejectedTiers is how many tier records the replay refused: a `raised`
	// naming a tier this build will not grant, and a `lowered` naming one that is
	// not strictly below the rung the entry is already on. Two records, one
	// counter, because they are one fact about the log and the answer to both is
	// the same: the entry keeps the tier it earned. See maxEarnedTier and
	// replayLocked.
	//
	// It was named for raises alone until R6 added the second record it counts,
	// and the name had to move with the meaning: an operator reading a key about
	// refused RAISES on a log whose only refused record was a `lowered` goes
	// hunting for a promotion that never happened, which is the failure this file
	// spends its comments on.
	//
	// Zero for a log this daemon wrote on its own, so anything else is a
	// hand-edited log, a truncated record that decoded oddly, or a log from a
	// build that knows a tier this one does not — none of them errors, but none
	// of them silent either.
	RejectedTiers int
}

// Factors is one entry's ranking arithmetic, dimension by dimension. The rank
// is their product and nothing else, which is what lets a surface report the
// reason beside the number: multiplied out, "3.4" answers "why is this entry in
// my brief" with a figure, and #38's invariant I8 asks for an answer an
// operator can act on without reading the event log.
//
// Every field is a multiplier of at least one. Tier is the earned ladder, 1/2/3
// and never configurable. Recency slides between one and the configured weight
// with the entry's position in the log. Cwd, Profile and Group are each the
// configured weight on a match and one on a miss — so a field reading exactly
// one is a dimension that either did not match or was switched off, and the
// policy in force is what tells those two apart (see Store.Policy).
type Factors struct {
	Tier    float64 `json:"tier"`
	Recency float64 `json:"recency"`
	Cwd     float64 `json:"cwd"`
	Profile float64 `json:"profile"`
	Group   float64 `json:"group"`
}

// product is the rank: the five factors multiplied, in this order and nowhere
// else. Every rank the store reports comes from here, so a surface that
// multiplies the breakdown out reproduces the reported number EXACTLY rather
// than to within a rounding — float64 multiplication is deterministic, but only
// for a fixed order of operands.
func (f Factors) product() float64 {
	return f.Tier * f.Recency * f.Cwd * f.Profile * f.Group
}

// Standing is where one entry ended up relative to the working set, and — when
// it is outside — WHY.
//
// It exists because "why is this entry in my brief" and "why is this one not"
// are the same question and the breakdown only answers the first. There are
// three ways to be out, they are not distinguishable from an entry's own fields,
// and an operator who cannot tell them apart cannot act: a budget too small is
// fixed with score.working-set, a full block by shortening entries or lowering
// the budget, and an over-long line by editing score.md. Reporting one number
// and leaving the rest to be inferred is the gap invariant I8 names.
//
// The values are hyphenated strings rather than an int, because they travel on
// the wire to a person and to `jq`, and a number would need this comment beside
// it to mean anything.
type Standing string

const (
	// StandingActive is in the working set: an entry a dispatch for this context
	// would actually inject.
	StandingActive Standing = "active"
	// StandingBelowBudget is ranked below score.working-set. The entry is fine;
	// there were simply higher-ranked ones ahead of it. It is reported for EVERY
	// entry past the count, including the ones the rune backstop had already
	// stopped: two caps exclude those, and this is the one the operator can turn.
	StandingBelowBudget Standing = "below-budget"
	// StandingBlockFull is ranked inside the budget and still left out, because
	// the block's rune backstop was spent before the entry was reached — see
	// maxBlockRunes. It means the budget is wider than the bytes allow, which is
	// invisible from the entry itself, and it is the one exclusion widening
	// score.working-set would not fix.
	StandingBlockFull Standing = "block-full"
	// StandingOversized is too long to inject at any budget (maxEntryRunes). It is
	// the only one of the three that is a property of the entry rather than of
	// what was ahead of it, and the only one an operator fixes by editing the
	// line.
	StandingOversized Standing = "oversized"
)

// Ranked is one entry with the arithmetic that placed it, and where that placing
// left it. It is the OPERATOR's view of the ranking — score.list is built from
// it — and never the dispatch's, which needs only the entries themselves.
type Ranked struct {
	Entry
	Rank    float64 `json:"rank"`
	Factors Factors `json:"factors"`
	// Standing is where this entry ended up, and why when it is out; see
	// Standing. Active is exactly Standing == StandingActive, carried as its own
	// field because "is it in the brief" is the common question and
	// `select(.active)` is what the CLI's help advertises. Both are assigned from
	// one call to budget.take, so they cannot come to disagree.
	Standing Standing `json:"standing"`
	Active   bool     `json:"active"`

	// at is the entry's last-movement position in the log (see Store.lastAt) — the
	// same number Recency is interpolated from — kept for the tie-break in
	// rankBefore. It is not on the wire: a raw log ordinal explains nothing an
	// operator can use, and Factors.Recency already carries the part of it that
	// changes the answer.
	at int
}

// rankBefore reports whether a sorts ahead of b in the ranking. It is a strict
// TOTAL order, which is what #38's verification check 4 needs — the same log and
// context yielding the same working set on two machines — because a merely
// partial order leaves the sort free to place ties by whatever the algorithm
// happens to do.
//
// Total ONLY because no rank can be NaN. A NaN would satisfy the first key's
// inequality while failing its comparison, so the pair would be treated as
// separable and ordered inconsistently, and the two keys below would never be
// consulted. clampWeight is where that is prevented, at the one boundary a NaN
// can enter through; see its NaN branch.
//
// The three keys, in order: rank descending, then last-movement position
// descending, then id ascending. Position before id so a tie between two equally
// ranked entries goes to the one the fleet touched most recently, which is the
// same preference the recency factor states; id last because it is unique, so
// the order is total there and cannot fall through.
//
// Both tie-break keys are derived from the LOG, never from score.md's line
// order. The file is the operator's — they can shuffle it in an editor — and
// invariant I5 wants the ranking to be a function of the log and the context.
func rankBefore(a, b Ranked) bool {
	switch {
	case a.Rank != b.Rank:
		return a.Rank > b.Rank
	case a.at != b.at:
		return a.at > b.at
	default:
		return a.Id < b.Id
	}
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
	Entries []Entry // what a dispatch would inject, in render order
	Block   string  // Entries as the injectable text block; empty when there are none
	Total   int     // every entry the store holds, injectable or not
	Health  Health
	Delta   Delta  // what this look's pass changed; zero when score.md had not moved
	Folds   []Fold // repeats folded since the last look, for the server to log
	// Ranked is EVERY entry the store holds, ranked against this look's context
	// and marked with whether it made the working set. It is populated by Explain
	// and left nil by View: the dispatch path needs the working set and nothing
	// else, and building a breakdown per entry per brief would put the whole
	// store's arithmetic on the path a brief is delivered through.
	Ranked []Ranked
	// Policy is the tuning in force — the recurrence threshold, the working-set
	// budget, and the ranking weights. It rides the view rather than a second
	// call because score.status reports it beside the counts it explains, and a
	// knob read through its own lock is a knob read from a different moment than
	// everything it is meant to explain.
	Policy Policy
	// BlockFull says the working set stopped because the block's rune backstop
	// was spent (maxBlockRunes), rather than because the budget or the store ran
	// out.
	//
	// It is here for the reason Health.Oversized is: three separate caps can make
	// Entries shorter than the budget, and an unexplained gap between them looks
	// the same whichever one bit. Two of the three already had names on the
	// status surface; this is the third. An operator whose brief carries five of
	// a budget of forty is told which knob to reach for rather than left to
	// subtract one number from another.
	BlockFull bool
	Unlocked  bool // the store is running without its single-writer claim
}

// event is one line of score-events.jsonl. Text and Prov are what make the log
// replayable on its own: without them a rebuilt entry would have no wording and
// no source, and the log is the only thing an entry is ever rebuilt from.
type event struct {
	Schema int         `json:"schema"`
	Event  string      `json:"event"`
	Id     string      `json:"id"`
	At     time.Time   `json:"at"`
	Source string      `json:"source,omitempty"`     // who acted: SourceUser, SourceAgent, or sourceRecovery on nobody's behalf; empty otherwise
	Text   string      `json:"text,omitempty"`       // the entry's text at submitted/edited, the repeat's own wording at folded, and the absorbed entry's wording at merged
	Prov   *Provenance `json:"provenance,omitempty"` // where a submitted entry, or a folded repeat, came from
	Tier   int         `json:"tier,omitempty"`       // the tier reached, at raised; the tier landed on, at lowered
	// RemovedLine marks a fold that took a LINE out of score.md. It is named for
	// its EFFECT rather than for where the repeat came from, because the effect
	// is what the boot derivation is built on: only a fold that removed a line
	// can owe a removal. A path that folds a file line without removing it — the
	// id-carrying duplicate the recovery table already admits — must leave this
	// false, and a name about provenance would have read as true for it.
	//
	// R6's merge takes a line out too, and emits no fold event at all: it counts
	// nothing, so there is no repeat to record, and the retired entry's line
	// cannot be owed to anyone because the entry itself is gone. See mergeLocked.
	//
	// Nothing else can supply the distinction: Source says WHO repeated the
	// wording, and an operator submitting through `ctl score submit` is "user"
	// exactly as their own file is.
	//
	// Appended and optional, so the schema stays 1. A fold logged before this
	// field existed decodes with it false and simply seeds nothing — which is
	// the pre-derivation behaviour, not a wrong one.
	RemovedLine bool `json:"removed_line,omitempty"`
	// Signal marks a fold that came through Store.Signal — a brief the user
	// dispatched — rather than through a submission or a duplicate line.
	//
	// Without it the two user-sourced doors are byte-identical in the log, and
	// the difference is exactly what #38 §4 asks to stay visible: a submission is
	// something the operator chose to record, and a signal is a coincidence
	// between their brief and an entry they may never have thought about. Nothing
	// in the store branches on it — it changes no count and no tier — so it is
	// evidence rather than state, and an old record decoding with it false says
	// only that the daemon that wrote it had no signals to distinguish.
	Signal bool `json:"signal,omitempty"`
}

// foldEvent records one repeat counted into id. It carries the REPEAT's own text
// and provenance, not the entry's: a fold is the one mutation whose input
// reaches no file otherwise — score.md keeps the wording that was already there
// and nothing else records the repeat — so without them the store could say how
// often something has been said but never who said it, which is what #38 leans on
// where it declines to police the content of a submission.
//
// The event is EventFolded whoever repeated the wording, and "user" or "agent"
// lives in Source alone. #38's glossary calls a fold the agent-side
// reinforcement because that is the common case, not because the name is the
// discriminator — R4's user signal must key on Source (invariant I6), never on
// the name.
func foldEvent(id, text string, prov Provenance, at time.Time, removedLine, signal bool) event {
	return event{
		Schema: Schema, Event: EventFolded, Id: id, At: at,
		Source: prov.Source, Text: text, Prov: &prov,
		RemovedLine: removedLine, Signal: signal,
	}
}

// Store is the fleet memory backed by one directory. A nil *Store is the
// disabled store: renders are empty and mutations are refused plainly, so the
// server can hold nil when score is switched off. All methods are safe for
// concurrent use.
type Store struct {
	mu      sync.Mutex
	dir     string
	entries []Entry             // score.md file order; the RENDER order is the ranking's
	burned  map[string]struct{} // every id the log has ever named; never reissued
	boot    Delta               // what Open's recovery pass did to the operator's files
	health  Health

	// The two files' paths, joined once here because dir is immutable after Open
	// and both are read or written on the dispatch path.
	mdPath     string
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

	// policy is the tuning the store compares against: the recurrence threshold,
	// the working-set budget, and the ranking weights. Always clamped — Open and
	// SetPolicy are the only writers and both clamp on the way in.
	policy Policy

	// seq is how many event records the log holds, and lastAt is each entry's
	// LAST-MOVEMENT position in it: the record that most recently did something
	// to that entry. Together they are the whole of what the ranking knows about
	// time (invariant I5): a position, never a clock, so a laptop that slept for
	// a week and an NTP correction cannot reorder a working set.
	//
	// MOVEMENT rather than reinforcement, and the difference is one case. The
	// records that move it are the submission that created the entry, every
	// reinforcement, AND an edit the operator made — and since R4 ruled that a
	// reword counts no reinforcement, that last one moves the position while
	// leaving both counters alone. So an entry's position is not its
	// reinforcement count's twin, and reading it as one is wrong in exactly the
	// case an operator is most likely to be looking at. noteEventLocked holds the
	// list and says why the edit is on it.
	//
	// They are maintained in exactly two places, and the two must agree or a
	// restart would reorder the fleet's memory: replayLocked counts the records
	// it parses out of the log, and appendEvents counts the records it lands in
	// it. Both go through noteEventLocked, which is why it exists.
	//
	// lastAt is keyed by id and may name ids the entry set does not carry — an
	// event that became durable on a path that then failed to reach memory, as
	// burned may — because it is read only through an entry that is present. A
	// retire deletes its key, which is what keeps a long-running daemon's map
	// bounded by the live set rather than by the log.
	seq    int
	lastAt map[string]int

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
	// It is never written down. There is no file to write it to that is not the
	// log itself, and a debt is not an event — writing it as one would put a
	// record in the operator's history for something that merely has not happened
	// yet. It survives a restart by being
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

// Open opens (or creates) the store in dir under the policy p, clamped as
// Policy.clamp describes, so a zero field from a config key nobody wrote lands
// on this package's default. The directory is created 0700 and every file 0600.
//
// The policy is taken HERE rather than set after the fact, because Open's own
// recovery pass already makes durable tier decisions: it folds duplicate lines
// and reads the rewordings made while the daemon was down — the expected
// workflow — and every one of those goes through reinforceLocked. A store
// constructed on the defaults and retuned a moment later would have promoted
// entries under a policy nobody chose, and the `raised` events recording it are
// durable, replayed at every boot, and uncorrectable: #37 demotes nothing.
// SetPolicy is the RE-tuning path (SIGHUP) and nothing else.
//
// The ranking half of the policy makes no durable decision — it changes order,
// never tier — so its reason for being here is the weaker one: nothing then
// depends on the daemon calling its reload path before its first dispatch,
// which is an ordering in another package that no test in this one would catch
// breaking.
//
// SINGLE WRITER. Open takes an exclusive advisory lock on score.lock and holds
// it until Close. Two daemons on two sockets both default to $HOME/.baton — and
// BATON_SOCK is the documented way to run a second fleet — so they would append
// to one log and rewrite one score.md from two in-memory views of it: their
// entry sets silently diverge and each one's rewrite drops the other's lines.
// The lock is preferred over merely documenting "run one daemon" because the
// store cannot check either file's consistency after the fact — an unenforced
// rule here fails silently, and the loser of the race is the operator's own
// text. The second daemon's Open fails with a plain message the
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
// one reconcile pass over score.md decides existence and text. A torn last line
// in the event log is skipped and counted (see Health.TornEvents) rather than
// failing Open.
//
// Open calls the *Locked helpers without holding the mutex — the store is not
// published to any other goroutine until Open returns, so their "caller holds
// the lock" contract is trivially satisfied here.
func Open(dir string, p Policy) (s *Store, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	release, locked, err := lockDir(filepath.Join(dir, scoreLock))
	if err != nil {
		return nil, err
	}
	s = newStore(dir, p)
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
	// Settle what the boot pass did not: a pass that changed something has already
	// recounted, and one that changed nothing still has to, because the gauge is
	// computed over an entry set the replay built.
	if s.boot == (Delta{}) {
		s.recountOversizedLocked()
	}
	return s, nil
}

// newStore assembles a store over dir with both its file paths joined once —
// dir is immutable afterwards and both are touched on the dispatch path — and
// under the policy it will spend its whole life comparing against unless a
// reload retunes it. Open adds the directory claim and the recovery pass.
func newStore(dir string, p Policy) *Store {
	return &Store{
		dir: dir, burned: map[string]struct{}{}, policy: p.clamp(),
		lastAt:     map[string]int{},
		mdPath:     filepath.Join(dir, scoreMD),
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

// clampPromoteAt is the recurrence threshold a caller's number actually becomes.
// Below two — including the zero of a config field nobody set — it falls back to
// defaultPromoteAt. Two is the floor because a tier is EARNED here: at one, an
// entry would arrive at tier 2 on the submission that created it, which is
// granting importance rather than counting it, and #37 has no way to say that.
func clampPromoteAt(n int) int {
	if n < 2 {
		return defaultPromoteAt
	}
	return n
}

// clampUserSignalsAt is the user-signal threshold a caller's number actually
// becomes. Below two — including the zero of a config field nobody set — it
// falls back to defaultUserSignalsAt.
//
// Two is the floor for the reason it is the default: #37 reserves the top tier
// for the user asking REPEATEDLY, and at one a single signal would grant it, so
// the knob would be able to say something #37 has no way to mean. There is no
// upper bound — an operator who wants the top rung to take five demands gets
// five — which is the asymmetry clampPromoteAt has for the same reason.
func clampUserSignalsAt(n int) int {
	if n < 2 {
		return defaultUserSignalsAt
	}
	return n
}

// SetPolicy RE-tunes a running store, and is only that: the policy a store is
// born with is Open's argument, so no pass ever runs under a policy nobody
// chose. It is safe on the disabled (nil) store and safe to call on a running
// one, which is what lets score.promote-at, score.rank and score.working-set
// ride the daemon's SIGHUP reload while score.dir and score.enabled still need
// a restart: every field here is a number this store COMPARES, not a store to
// swap under in-flight dispatches. p is clamped as Policy.clamp describes.
//
// Retuning never re-tiers and never demotes. Tiers are replayed from the log's
// raised events, so an entry that earned tier 2 keeps it and one that has not
// yet earned it is measured against the new threshold at its next
// reinforcement; the ranking half changes ORDER and touches no tier at all.
//
// It reports whether this call MOVED anything, so the daemon can log a reload
// that changed the policy and stay quiet about the far more common one that did
// not. The policy itself is Policy's job, which the caller already asks on the
// branch where the config would not load.
func (s *Store) SetPolicy(p Policy) (changed bool) {
	if s == nil {
		return false
	}
	p = p.clamp()
	s.mu.Lock()
	defer s.mu.Unlock()
	changed = s.policy != p
	s.policy = p
	return changed
}

// Policy is the tuning in force: the recurrence threshold, the working-set
// budget, and the ranking weights, all clamped. Zero on the disabled (nil)
// store.
//
// A knob whose effect an operator cannot observe is a knob they cannot trust
// (invariant I8), and the ranking weights are the sharpest case of that: a
// weight of one is indistinguishable in a rank breakdown from a dimension that
// simply did not match. So the value is readable here and on every View, which
// is where a reply reporting the tuning beside the counts it explains takes it
// from — one hold of the lock for both.
func (s *Store) Policy() Policy {
	if s == nil {
		return Policy{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policy
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
// the lock in between. It is the DISPATCH path's read: the entries a dispatch
// would inject and the block they render as, the totals a status reply reports,
// the gauge that explains the gap between them, and what the pass changed for
// the caller to log. An operator asking why an entry is in the brief wants
// Explain, which adds the ranking's own arithmetic.
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
	return s.look(ctx, false)
}

// Explain is View plus the ranking laid out: every entry the store holds, in
// rank order, each with the factors that produced its rank and whether it made
// the working set. It is the OPERATOR's read — score.list — and never a
// dispatch's, which needs the working set and nothing else.
//
// It is a second entry point rather than a field View always fills because the
// cost is not the same: a breakdown per entry is an allocation the size of the
// store, and View runs once per brief. Everything else about the two reads is
// identical, reconcile included, so a surface built on either sees the same
// consistency guarantees (see View).
func (s *Store) Explain(ctx Context) (View, error) {
	return s.look(ctx, true)
}

// look is View's and Explain's shared body: one stat off the lock, one gated
// reconcile pass, and everything that reads the entry set done in one hold of
// the lock. explain chooses which of the two rankings is run — see Explain.
//
// What is deliberately OUTSIDE the hold is the sort and the block formatting.
// Both work on slices this call already owns, so neither needs the lock, and
// both are O(n) or worse in the size of the store: holding the mutex across a
// twenty-thousand-entry sort would stall every concurrent dispatch for as long
// as one operator's score.list takes. Everything the view CLAIMS about the
// store — the totals, the health, the pass's delta, the policy — is still read
// in the single hold, which is the consistency View's doc promises.
func (s *Store) look(ctx Context, explain bool) (View, error) {
	if s == nil {
		return View{}, nil
	}

	fi, exists, err := statMD(s.mdPath)

	var (
		v      View
		ranked []Ranked
	)
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		var delta Delta
		if err == nil {
			delta, err = s.reconcileGatedLocked(fi, exists)
		}
		var (
			entries []Entry
			full    bool
		)
		if explain {
			ranked = s.rankAllLocked(ctx)
		} else {
			entries, full = s.renderLocked(ctx)
		}
		v = View{
			Entries: entries, Total: len(s.entries),
			Health: s.health, Delta: delta, Folds: s.drainFoldsLocked(),
			Policy: s.policy, BlockFull: full, Unlocked: s.unlocked,
		}
	}()
	if explain {
		// The working set is READ OFF the ranked list rather than selected a
		// second time, so an operator's view of what is injected cannot disagree
		// with what a dispatch for the same context actually injects.
		v.Ranked, v.BlockFull = orderRanked(ranked, v.Policy)
		for i := range v.Ranked {
			if v.Ranked[i].Active {
				v.Entries = append(v.Entries, v.Ranked[i].Entry)
			}
		}
	}
	v.Block = renderBlock(v.Entries)
	return v, err
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
// The recount is not part of the outcome: it reads the entry set apply has just
// settled and can neither fail nor undo it. The caller holds the lock.
func (s *Store) applyLocked(evs []event, apply func() error) error {
	if err := s.appendEvents(evs); err != nil {
		return err
	}
	if err := apply(); err != nil {
		return err
	}
	s.recountOversizedLocked()
	return nil
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
		folded, _, err := s.foldLocked(i, e.Text, prov, false)
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
//     rather than remembering wrong — and joining two such entries is the
//     conductor's `merge` (see Store.Merge), which is a direct correction
//     rather than a proposal: nothing here asks a human first, and what makes
//     that bearable is that the merge counts nothing, is in the log, and leaves
//     the operator's file theirs to edit.
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

// reinforceLocked counts ONE reinforcement into e, from source, and raises its
// tier when the count earns one — returning the raised event to append when it
// did. Every path that counts a repeat — a folded submission, a folded line, a
// brief the user dispatched, Reinforce — ends here, so the ladder is climbed in
// one place and a path added later cannot promote by its own rules. The caller
// holds the lock.
//
// A REWORD is deliberately not on that list. Correcting a line's wording is one
// statement re-spelled rather than a second statement, so it counts nothing; see
// the reword branch of reconcileLocked.
//
// It is also the only place a raised event is built, which is what makes
// invariant I6 provable rather than merely intended: an entry passes
// agentEarnedTier exactly when Policy.ceiling says the user has signalled for
// it, and source is the only thing that moves UserSignals. It raises at most
// one step per reinforcement, so an entry cannot skip a rung by being submitted
// twenty times at once — reaching the top therefore costs at least PromoteAt
// occurrences for the middle rung and one more after it.
//
// source is the REINFORCEMENT's, never the entry's: an agent repeating what the
// user first wrote is an agent signal, and the user repeating what an agent
// first said is a user one. Any source that is not SourceUser counts toward
// recurrence and nothing else — which today means an agent's, since the
// recovery pass never reaches here at all. Its adoption of a wording the log
// never carried is an `edited` event stamped sourceRecovery and no
// reinforcement, deliberately: nobody edited anything, and a manufactured user
// signal is what invariant I6 cannot survive.
func (s *Store) reinforceLocked(e *Entry, source string, at time.Time) (event, bool) {
	e.Reinforcements++
	if source == SourceUser {
		e.UserSignals++
	}
	// Occurrences, not reinforcements: the submission that created the entry is
	// the first time it was said, and the threshold is stated in the units an
	// operator counts in. See defaultPromoteAt.
	if e.Tier >= s.policy.ceiling(*e) || e.Reinforcements+1 < s.policy.PromoteAt {
		return event{}, false
	}
	e.Tier++
	return event{Schema: Schema, Event: EventRaised, Id: e.Id, At: at, Tier: e.Tier}, true
}

// foldLocked counts a repeat into the entry at i instead of adding a line: one
// log event, one counter, and score.md is not touched at all — there is no new
// line to append, and the surviving wording is the one already in the file. It
// removed no line, so it owes none: the event is written with RemovedLine false.
// The caller holds the lock.
//
// It serves both of the store's TEXT-matching doors — Submit, where the repeat
// was offered as an observation, and Signal, where it was only matched — which
// is what fromSignal names. They differ in nothing but the record they leave,
// and splitting them would have been two copies of an append, a counter and a
// fold note for that one field.
//
// The fold is RECORDED as well as counted, and the record is RETURNED as well as
// recorded, because the two doors want it at different moments. A submission's
// goes on the same buffer the file path uses, for the next read to drain: it is
// the one shape #38's "one log line per fold" has, with one producer. A signal's
// is handed straight back instead, and is deliberately not buffered — a brief
// promoting an entry is the case #38 §4 asks to stay visible, and a line that
// appears on the NEXT dispatch, stamped with that dispatch's clock and lost
// entirely if the daemon stops first, is not visibility. The caller logs it on
// the dispatch that caused it.
func (s *Store) foldLocked(i int, text string, prov Provenance, fromSignal bool) (Entry, Fold, error) {
	now := time.Now().UTC()
	e := s.entries[i]
	evs := []event{foldEvent(e.Id, text, prov, now, false, fromSignal)}
	if raised, ok := s.reinforceLocked(&e, prov.Source, now); ok {
		evs = append(evs, raised)
	}
	var rec Fold
	if err := s.applyLocked(evs, func() error {
		s.entries[i] = e
		rec = Fold{
			Id: e.Id, Text: e.Text, Repeat: text, Prov: prov, At: now,
			Reinforcements: e.Reinforcements, UserSignals: e.UserSignals, Tier: e.Tier,
			Duplicates: 1, Counted: true, FromSignal: fromSignal,
		}
		if !fromSignal {
			s.noteFoldsLocked([]Fold{rec})
		}
		return nil
	}); err != nil {
		return Entry{}, Fold{}, err
	}
	return e, rec, nil
}

// Reinforce bumps an entry's counter and logs who did it: a fold when an agent
// reinforces (in #38's model a fold IS the agent-side reinforcement), a
// user-signal when the operator does. Either way the count can earn the entry a
// tier — see reinforceLocked, which is where every path that counts a repeat
// ends.
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
	if source == SourceUser {
		name = EventUserSignal
	}
	now := time.Now().UTC()
	e := s.entries[i]
	evs := []event{{Schema: Schema, Event: name, Id: id, At: now, Source: source}}
	if raised, ok := s.reinforceLocked(&e, source, now); ok {
		evs = append(evs, raised)
	}
	return s.applyLocked(evs, func() error {
		s.entries[i] = e
		return nil
	})
}

// Signal counts text as a reinforcement of the entry it repeats, and changes
// nothing at all when it repeats none. It is #38 §4's second source of the user
// signal: a brief the user dispatched, matched against the store with the same
// normaliser folding uses.
//
// It is Submit's fold half WITHOUT Submit's other half, and that is the whole
// difference between them. A brief is evidence that something the fleet already
// remembers still matters; it is not an observation being offered, so text that
// matches nothing must leave no trace. Reaching for Submit here would fill
// score.md with one entry per dispatch.
//
// prov is the caller's to supply, exactly as Submit's is, and for the reason
// invariant I6 exists: this package does not know who called it, so a store that
// stamped SourceUser here on its own authority would be the store deciding the
// one thing #38 §4 says only the connection can. The server passes what the
// connection says (see Server.connProvenance) and calls this only for the user's
// own dispatches.
//
// The text is NOT weighed against maxEntryRunes. That cap exists to keep a
// heavy entry out of every future brief, and Signal admits nothing — a brief
// longer than any entry simply matches none.
//
// It IS scrubbed, though the match would work without it: normalize sanitises on
// its own. What the scrub is for is everything the text is then kept in — the
// fold event's own wording, replayed at every boot, and the fold record a server
// turns into a log line. Submit's fold path hands foldLocked an already-scrubbed
// Entry.Text, so doing it here is what keeps the two doors recording the same
// kind of string; see sanitize for what an unscrubbed one costs.
//
// What it matches is narrower than "the brief mentions the entry", and the
// difference matters to anyone judging the risk. normalize is sanitize plus
// lowercasing plus a trailing-punctuation trim and NOTHING else, so the whole
// brief must normalise to the whole entry: "table-driven" does not match "table
// driven", and a brief that quotes two entries verbatim matches neither of them.
// In practice that means the door fires on short, command-like briefs a person
// retypes — and essentially never on natural prose. The realistic coincidence is
// a SHORT entry ("run the tests") reached by two entirely ordinary dispatches,
// which is why the record below has to name what matched.
//
// It returns the fold RECORD rather than the entry, and does not buffer it. A
// caller logs it on the dispatch that caused it — see foldLocked for why a
// signal is the one fold that must not wait for the next read. hit reports
// whether the text folded at all; on a miss the record is zero. The coincidence
// #38 §4 accepts is reversible the way every reinforcement is: by editing
// score.md.
func (s *Store) Signal(text string, prov Provenance) (rec Fold, hit bool, err error) {
	if s == nil {
		return Fold{}, false, errDisabled
	}
	text = sanitize(text)
	if text == "" {
		return Fold{}, false, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Read-reconcile-write, as in Submit and Reinforce, and unconditional for the
	// reason theirs are: a wording the operator has just deleted must not swallow
	// this, one they have just typed should match it, and no gate over filesystem
	// timestamps is exact enough to decide that.
	//
	// It is the one mutation path that runs on the DISPATCH path, so it does pay
	// a re-parse of score.md the render's own gated pass had just skipped. That
	// is the cost BenchmarkReconcileReparse measures, once per dispatch a person
	// typed, and the alternative is a fold counted into a line that is no longer
	// in their file.
	if _, rerr := s.reconcileNowLocked(); rerr != nil {
		return Fold{}, false, rerr
	}
	i := s.foldTargetLocked(text)
	if i < 0 {
		return Fold{}, false, nil
	}
	_, folded, ferr := s.foldLocked(i, text, prov, true)
	return folded, ferr == nil, ferr
}

// DrainFolds hands back every fold record the store is still holding, and clears
// them. It is what a caller runs at SHUTDOWN.
//
// Fold records are buffered for the next read to drain, which on a running
// daemon is the next dispatch, list or status. On a stopping one there is no
// next read: a fold counted seconds before a SIGTERM was durable in the log and
// yet had no line anywhere, which is #38's "one log line per fold" quietly not
// happening in the one case an operator is most likely to be investigating.
// Each record carries its own At, so a line drained here is still stamped with
// the moment the fold happened rather than with the shutdown.
//
// Safe on the disabled (nil) store, like every other method here.
func (s *Store) DrainFolds() []Fold {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drainFoldsLocked()
}

// Merge, Reword and Lower are the conductor's three corrections: Merge joins two
// entries the normaliser failed to fold, Reword replaces an entry's wording, and
// Lower pulls one down a rung. Every one of them is #38 §1's CORRECTION right,
// and none of them is an execution right.
//
// What that distinction costs, stated once here because it is the line all three
// are built on: NOTHING a correction does moves a counter or raises a tier. Not
// a reinforcement, not a user signal, not an entry's recency. R4 ruled that a
// reword is one statement re-spelled rather than a second statement, and a merge
// is the same claim about two lines; letting either one buy what a repetition
// buys would hand an agent panel the currency invariant I6 reserves for the
// user. So a merged entry does not inherit the counts of the entry it absorbed,
// and the escape hatch #38 §1 asks for is about the FUTURE: after a merge, a
// repeat of either wording folds into the survivor (invariant I4) instead of
// starting the split over.
//
// The one tier they write moves DOWN and can only move down — see lowerLocked,
// and replayLocked's `lowered` case, which refuses a record that would raise.
//
// Nothing is destroyed (invariant I7): a merge retires an entry and a reword
// supersedes a wording, and both leave the text they replace in the event log.
// Both are reversible the way every other score change is — by editing the file.
//
// THREE METHODS rather than one verb taking an operation string, which is the
// call internal/control already made one layer up and for the same reason: the
// three take three different arguments — a second entry, a new wording, nothing
// at all — so a shared verb types them all as one `arg` the compiler cannot
// check, and the switch it needs is only the caller's switch moved here.

// Merge folds the entry named by from into the one named by id: id survives and
// gains from's wording as an alias, from is retired. See mergeLocked, which is
// where the ordering of its two durable steps is argued.
func (s *Store) Merge(id, from string) error {
	return s.refine(id, func(i int) error { return s.mergeLocked(i, from) })
}

// Reword replaces the wording of the entry named by id, keeping the old one as
// an alias so a repeat of it still folds. See rewordLocked.
func (s *Store) Reword(id, text string) error {
	return s.refine(id, func(i int) error { return s.rewordLocked(i, text) })
}

// Lower pulls the entry named by id down one rung, and takes no tier to move it
// to. See lowerLocked, which is where that is the proof of invariant I6.
func (s *Store) Lower(id string) error {
	return s.refine(id, s.lowerLocked)
}

// refine is the prologue all three corrections share, and the reason they are
// three methods rather than three copies of it: the disabled store, the lock,
// the reconcile, and resolving id to a live entry. apply is then handed that
// entry's index, under the lock, on a store that has just read the operator's
// file.
//
// Read-reconcile-write (invariant I3), as in Submit, Reinforce and Signal:
// correcting an entry the operator has since deleted must fail rather than
// resurrect it, and the wording being corrected must be the one currently in
// their file. It also settles score.md's line for every live entry, which is
// what replaceMDLineLocked relies on.
func (s *Store) refine(id string, apply func(i int) error) error {
	if s == nil {
		return errDisabled
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.reconcileNowLocked(); err != nil {
		return err
	}
	i := s.indexLocked(id)
	if i < 0 {
		return fmt.Errorf("score refine: no entry %q", id)
	}
	return apply(i)
}

// mergeLocked joins the entry at other into the one at keep: the survivor gains
// the absorbed wording as an alias, the absorbed entry retires, and its line
// leaves score.md. The caller holds the lock and has reconciled.
//
// It is the escape hatch #38 §1 names for the cost exact-match folding accepts —
// two observations that mean the same thing in different words, each sitting at
// tier 1 and never climbing. What it buys is the alias: from here on, a
// submission of either wording folds into the survivor. It does NOT recount the
// past, so the survivor's tier is exactly the tier it had, and its counters are
// exactly the counters it had. That is the invariant a merge is most likely to
// be "improved" into breaking — carrying the absorbed entry's reinforcements
// across looks like tidiness and is in fact the one arithmetic that would let an
// agent panel assemble the rung invariant I6 reserves for the user, since
// reinforceLocked promotes on Reinforcements and Policy.ceiling reads
// UserSignals. TestMergeInheritsNoCountsFromWhatItAbsorbs is the test that
// fails when someone tries it.
//
// Only the absorbed entry's CURRENT wording is carried over. Its own aliases are
// left in the log with it — a prior phrasing of a line that itself turned out to
// be a duplicate is two removes from anything the fleet is likely to say next,
// and Entry.Aliases holds maxAliases of them, so carrying a whole list would
// evict the survivor's own history to make room for it.
//
// # Why a merge is TWO durable steps rather than one
//
// The obvious shape appends `merged` and `retired` together and then rewrites
// score.md. Its crash window is not merely untidy, it LAUNDERS PROVENANCE: the
// log says the absorbed entry retired, the file still shows its line, and the
// recovery table then re-admits that line as a fresh USER-sourced entry with its
// reinforcements zeroed and its aliases gone (#38 §3, "the user's text wins").
// An agent-sourced entry silently becomes an operator-sourced one, and that
// distinction is the whole of what #38 §4 uses to fence the top tier — measured,
// and reported as `admitted=1 reattributed=1`, blaming an operator who did
// nothing. Re-running the merge repairs the file and none of the rest.
//
// So the retire is appended only AFTER the line is out of the file, and each
// window that leaves is one the store already knows how to resolve:
//
//   - crash after the alias, before the file: both entries live, the file
//     untouched, the alias durable and idempotent. The merge simply did not
//     happen; running it again completes it, and nothing was re-attributed.
//   - crash after the file, before the retire: the log still holds the entry and
//     no line carries it, which is the recovery table's own retire row — the
//     merge completes itself at the next reconcile, in the log, with the entry's
//     whole history intact (I7).
//
// The cost is a second fsync, and a merge was not cheap to begin with: two
// durable appends and a whole-file rewrite of score.md, measured at p90 156 ms
// and max 304 ms, with this store's mutex held for all of it. Store.View shares
// that mutex, so a merge can hold up a brief being assembled for another panel
// by up to a third of a second. That is the price of the ordering above rather
// than an oversight, and the server's rate cap is what keeps it to one such
// stall at a time; nothing here should be described as cheap.
func (s *Store) mergeLocked(keep int, other string) error {
	j := s.indexLocked(other)
	switch {
	case j < 0:
		return fmt.Errorf("score refine: no entry %q", other)
	case keep == j:
		return errors.New("score refine: an entry cannot be merged into itself")
	}
	now := time.Now().UTC()
	survivor, absorbed := s.entries[keep], s.entries[j]
	// The alias is computed before the append so its eviction can be reported,
	// and applied to a COPY so a failed append leaves the store untouched — the
	// order applyLocked exists to keep. NOTHING else of the absorbed entry is
	// read: not its counts, not its tier, not its signals. See this function's
	// doc for why that is the load-bearing line.
	var evictions int
	alias(&survivor, absorbed.Text, &evictions)
	// The append and the memory move by hand rather than through applyLocked, for
	// the one thing applyLocked would add: its recount. This is the only mutation
	// the store makes in two durable steps, and the state the first of them
	// describes — the survivor holding the alias, the absorbed entry still live —
	// never leaves this hold of the mutex, so a gauge computed over it could
	// never be read before the second step recomputed it. The retire below
	// carries the one recount for both halves. The ORDER argued above is
	// untouched: this moves memory, never the log.
	if err := s.appendEvents([]event{{
		Schema: Schema, Event: EventMerged, Id: survivor.Id, At: now,
		Source: sourceConductor, Text: absorbed.Text,
	}}); err != nil {
		return err
	}
	s.entries[keep] = survivor
	s.health.AliasEvictions += evictions
	// The absorbed entry's line goes, and every other byte of the operator's file
	// stays. Before the retire, never after — see the doc.
	if err := s.replaceMDLineLocked(absorbed.Id, ""); err != nil {
		return err
	}
	return s.applyLocked([]event{{
		Schema: Schema, Event: EventRetired, Id: absorbed.Id, At: now, Source: sourceConductor,
	}}, func() error {
		s.entries = slices.Delete(s.entries, j, j+1)
		// The debt this entry owed can never be settled by removing a line now,
		// because the line that carried its id has just gone with it. It is a
		// narrow case — only a fold whose rewrite failed leaves a debt at all —
		// but a debt under a burned id would outlive the entry and decline to
		// count a genuine repeat if the recovery table ever re-admitted it.
		// replayLocked drops the same keys for the same reason at boot.
		delete(s.owed, absorbed.Id)
		return nil
	})
}

// rewordLocked replaces the entry's wording, keeping the old one as an alias so
// a repeat of it still folds (invariant I4). The caller holds the lock and has
// reconciled.
//
// It goes through the same package-level reword the operator's own edits and the
// log replay go through, rather than re-spelling the three steps, because the
// alias-eviction counter is the one of them most easily forgotten — and a
// conductor that could reword without keeping the prior wording would be exactly
// the "conductor edit makes future repeats miss" #38 §1 rules out.
//
// The text is weighed and scrubbed on the way in like a submission's, at the
// same boundary and against the same caps: it reaches every agent's terminal on
// every dispatch, and where it came from does not change what an escape sequence
// in it would do. It is also asked the question Submit asks of a new note —
// whether the store already says this — for the reason below.
func (s *Store) rewordLocked(i int, raw string) error {
	e := s.entries[i]
	// Built as the entry it would become and asked the same two questions Submit
	// asks of a new note, rather than re-spelling either. Entry.Injectable is the
	// ONE statement of the weight rule, and a second copy of it here would sit on
	// the store's newest input channel — the one path that can then store an entry
	// Render silently withholds from every brief. setText also settles the folding
	// key the collision check below needs.
	cand := e
	cand.setText(raw)
	text := cand.Text
	switch {
	case text == "":
		return errors.New("score refine: reword needs the new wording")
	case !cand.Injectable():
		return fmt.Errorf("score refine: wording is %d runes, limit is %d", len([]rune(text)), maxEntryRunes)
	case text == e.Text:
		// Refused rather than accepted as a no-op, because it is not one: the
		// entry would take its own current wording as an alias and spend a slot of
		// maxAliases on a phrasing already covered by Entry.Text.
		return errors.New("score refine: the wording is unchanged")
	}
	// A reword must not MANUFACTURE the split that merge exists to cure. Every
	// other admission path asks foldTargetLocked whether the store already says
	// this — Submit before it adds a line, the file pass before it admits a
	// bullet — and a reword that skipped the question could land one entry on
	// another's exact folding key: two live entries, two identical lines in the
	// operator's file, both eligible for the working set, neither able to fold
	// into the other ever again, their counts split for good. It is not a remote
	// shape either. score_reword's whole job is fixing an ambiguous wording, so
	// converging two near-duplicates onto one good sentence is the natural move
	// for the thing holding the tool.
	//
	// REFUSED rather than folded, and the refusal names the other entry. Folding
	// here would decide which of the two survives on the store's own authority,
	// silently retire the one the conductor named, and count a reinforcement on a
	// path this whole verb promises counts nothing. Merge is the verb for joining
	// two entries, it says which one survives, and it counts nothing — so the
	// refusal is a signpost to it rather than a dead end.
	//
	// j == i is the entry matching ITSELF, which is not a collision and must stay
	// allowed: it is every reword that only changes case or trailing punctuation,
	// and every reword back to one of this entry's own prior wordings.
	if j := s.foldTargetLocked(text); j >= 0 && j != i {
		return fmt.Errorf("score refine: entry %s already says that; merge them rather than rewording one into the other", s.entries[j].Id)
	}
	now := time.Now().UTC()
	evs := []event{{Schema: Schema, Event: EventEdited, Id: e.Id, At: now, Source: sourceConductor, Text: text}}
	// Counted into a local and folded into the store's health only where the
	// mutation commits, the way a reconcile pass holds its own counters.
	var evictions int
	reword(&e, text, &evictions)
	return s.applyLocked(evs, func() error {
		s.entries[i] = e
		s.health.AliasEvictions += evictions
		return s.replaceMDLineLocked(e.Id, formatLine(e.Id, e.Text))
	})
}

// lowerLocked pulls the entry down one rung. The caller holds the lock and has
// reconciled.
//
// ONE rung, with no target to name, and that is the whole proof that invariant
// I6 survives the conductor: the verb takes no tier at all, so there is no
// number for a caller to get wrong or for a compromised one to choose. The
// decrement below is guarded by the bottom of the ladder and is the only tier
// this file's conductor path writes; replayLocked's `lowered` case applies the
// same rule to a record read back, so a hand-edited log cannot raise through it
// either. Nothing anywhere in Refine calls Policy.ceiling, because nothing in
// Refine goes up.
//
// It is #37's one and only demotion, and it destroys nothing (invariant I7): the
// tier the entry gave up is still in the `raised` record that granted it, and
// the `lowered` record says who took it back and when.
func (s *Store) lowerLocked(i int) error {
	e := s.entries[i]
	if e.Tier <= 1 {
		return fmt.Errorf("score refine: entry %s is already on the bottom rung", e.Id)
	}
	now := time.Now().UTC()
	e.Tier--
	evs := []event{{Schema: Schema, Event: EventLowered, Id: e.Id, At: now, Source: sourceConductor, Tier: e.Tier}}
	return s.applyLocked(evs, func() error {
		s.entries[i] = e
		return nil
	})
}

// replaceMDLineLocked rewrites score.md with the first line carrying id replaced
// by repl, or removed when repl is empty. The caller holds the lock.
//
// THE FIRST line, because that is the one reconcileLocked resolves the entry
// against: a second line repeating an id is a bullet to that pass, not this
// entry. Acting on both would make the two writers of this file disagree about
// which line an entry is.
//
// Every other byte is written back exactly as it was read — the operator's
// prose, their comments, their blank lines. That is the rule reconcileLocked
// already keeps ("score.md itself is rewritten only to…"), and it is the one a
// second writer of this file could most easily break by projecting the entry set
// over the top of it; see projectLocked, which does exactly that and is reserved
// for a file that is not there at all.
//
// A file with no line for the id is left alone rather than reported: Refine
// reconciled before it got here, so every live entry has a line, and inventing
// an error for a state the caller has just ruled out would be a refusal nobody
// could act on.
func (s *Store) replaceMDLineLocked(id, repl string) error {
	data, err := os.ReadFile(s.mdPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	i := slices.IndexFunc(lines, func(line string) bool {
		got, text, ok := parseLine(line)
		return ok && got == id && text != ""
	})
	if i < 0 {
		return nil
	}
	if repl == "" {
		return s.writeMDLocked(slices.Delete(lines, i, i+1))
	}
	lines[i] = repl
	return s.writeMDLocked(lines)
}

// Render returns the working set for the given dispatch context — the
// highest-ranked entries a brief carries — or nil when the store is empty or
// disabled. It can be SHORTER than the policy's working-set budget for either of
// two reasons: an entry too long to inject is skipped (maxEntryRunes), and the
// block stops on a whole entry once its rune backstop is spent (maxBlockRunes).
// Explain is where those are told apart, one Standing per entry.
//
// Everything it returns is injected into a real agent's brief, so it carries
// only what the fleet earned — the store seeds nothing, so there is nothing else
// for it to carry — and nothing over the weight cap reaches it either, which is
// why an over-long operator line is skipped here rather than refused at the file
// (maxEntryRunes).
//
// Render does NOT reconcile: the caller does, once per read path, so that a
// status reply's Len and Render see one consistent view and the store never
// does file I/O from a function with no way to report its errors.
func (s *Store) Render(ctx Context) []Entry {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _ := s.renderLocked(ctx)
	return entries
}

// recencySpanLocked is the oldest and newest last-movement positions across
// every entry the store holds — the two ends recencyFactor interpolates between.
// See Store.lastAt for what a movement is; it is not the same set of records as
// a reinforcement. The caller holds the lock.
//
// The span is taken over ALL entries rather than over the injectable ones the
// working set is drawn from, so that score.list's numbers and a dispatch's are
// the same numbers. It could not change an ORDER either way: the factor is
// monotone in the position, so narrowing the span rescales every entry's
// recency without reordering any two of them.
func (s *Store) recencySpanLocked() (oldest, newest int) {
	if len(s.entries) == 0 {
		return 0, 0
	}
	oldest = s.lastAt[s.entries[0].Id]
	newest = oldest
	for i := 1; i < len(s.entries); i++ {
		at := s.lastAt[s.entries[i].Id]
		oldest, newest = min(oldest, at), max(newest, at)
	}
	return oldest, newest
}

// recencyFactor maps one entry's last-movement position (see Store.lastAt)
// linearly onto [1, w]: the oldest entry in the store gets 1, the newest gets
// the configured weight, everything between gets its share.
//
// A POSITION, never a timestamp, which is the whole of invariant I5. The log
// records times for the person reading the history and nothing ranks on them,
// so a laptop that slept for a week, an NTP correction, and a timezone change
// all leave the working set exactly as it was.
//
// A span of zero — one entry, or a store whose entries all last moved on the
// same record — has no oldest-to-newest ramp to sit on, so every entry gets the
// floor. That is a uniform multiplier, so it cannot change an order; it is the
// floor rather than the weight only so the reported rank does not claim a
// recency bonus that no comparison was made for.
//
// DO NOT HOIST THE RATIO OUT OF THE EXPRESSION. Go permits — and on arm64 the
// compiler takes — the fusion of `x*y + z` into a single FMA instruction with
// one rounding instead of two, while amd64 emits two. That is a licensed
// difference, not a compiler bug, and it is worth one ULP: enough to flip an
// exact tie, which is enough to give two machines different working sets for
// the same log and break #38's verification check 4 across architectures.
//
// The expression below is safe by its SHAPE and by nothing else. The addition's
// right operand is a DIVISION, and there is no fused multiply-divide-add, so
// the multiply's result must be rounded before the divide and the divide's
// before the add — three roundings, identical everywhere. Written the obvious
// way instead:
//
//	ratio := float64(at-oldest) / float64(newest-oldest)
//	return 1 + (w-1)*ratio
//
// the add's operand becomes a bare multiply and arm64 emits FMADDD. Both forms
// were compiled and disassembled to confirm it. Factors.product is safe for a
// different reason — a pure multiply chain has no add to fuse into.
func recencyFactor(at, oldest, newest int, w float64) float64 {
	if newest <= oldest {
		return minRankWeight
	}
	return 1 + (w-1)*float64(at-oldest)/float64(newest-oldest)
}

// matchFactor is what one context dimension is worth: the configured weight
// when the entry's recorded value equals the dispatch's, and one otherwise.
//
// An EMPTY value never matches, on either side. An entry with no recorded cwd
// does not know where it came from, and a dispatch with no cwd is not asking
// about one — treating the two as equal would hand every entry the operator
// submitted from their own cockpit (which records no panel, profile, group or
// cwd at all; see Provenance) a full match on every dimension of a context the
// server could not fill, which is the widest possible boost for the least
// possible evidence. So "unknown" is not a value that can agree with anything,
// including itself.
func matchFactor(have, want string, w float64) float64 {
	if have != "" && have == want {
		return w
	}
	return minRankWeight
}

// rankLocked is the ranking, and the only place it is computed:
//
//	rank = tier x recency x cwd x profile x group
//
// Multiplicative rather than lexicographic (settled with the user, #42) so the
// dimensions compose: an entry two tiers down can still lead the brief if it is
// fresh AND from this panel's directory, profile and group, which is the
// judgement an operator makes by hand and the ordering #38 §5 describes.
//
// It is a pure function of the log and the context — no clock is reachable from
// here — which is invariant I5 and #38's verification check 4: the same log
// replayed on another machine ranks identically for the same context.
//
// The caller holds the lock and supplies the span, which is computed once per
// look rather than once per entry.
func (s *Store) rankLocked(e Entry, ctx Context, oldest, newest int) Ranked {
	at := s.lastAt[e.Id]
	f := Factors{
		Tier:    float64(e.Tier),
		Recency: recencyFactor(at, oldest, newest, s.policy.Rank.Recency),
		Cwd:     matchFactor(e.Provenance.SourceCwd, ctx.Cwd, s.policy.Rank.Cwd),
		Profile: matchFactor(e.Provenance.SourceProfile, ctx.Profile, s.policy.Rank.Profile),
		Group:   matchFactor(e.Provenance.SourceGroup, ctx.Group, s.policy.Rank.Group),
	}
	return Ranked{Entry: e, Rank: f.product(), Factors: f, at: at}
}

// blockRunes is what one entry adds to the rendered block: its text plus the
// bullet, the tier wording, and the brackets and newline renderBlock wraps them
// in. The border lines are fixed overhead outside the budget, since they are
// there whether the block carries one entry or twenty.
//
// It must track writeBlockLine, which is what actually renders the line it is
// predicting the length of. A drift makes the cap approximate rather than wrong
// — the budget is a backstop with several times the default working set of
// headroom — but the pairing is checked by a test rather than left to the next
// reader to notice. Arithmetic rather than len(the built string) because this
// runs per entry on the dispatch path and the string would be thrown away.
func blockRunes(e Entry) int {
	return len("- ") + len([]rune(e.Text)) + len(" [") + len(tierWording(e.Tier)) + len("]\n")
}

// minBlockRunes is the least one entry can possibly add: a single rune of text
// at the shortest tier wording. It is the shape of the CHEAPEST line rather than
// a guess, and TestBlockRunesArithmetic holds it to that.
const minBlockRunes = len("- ") + 1 + len(" [") + len("noted") + len("]\n")

// MaxReachableWorkingSet is the largest working-set budget the rune backstop can
// ever let a brief spend, even on the shortest entries the store can hold.
//
// It is exported because a budget above it is DEAD CONFIG, in the same sense a
// cwd weight is dead when panel.track-cwd is off: the operator wrote a number
// down, the daemon accepted it, and no dispatch will ever reach it. The daemon
// says so on load rather than leaving the gap between `working_set` and
// `rendered` to be puzzled over — see the caller in cmd/baton.
//
// There is deliberately no CLAMP to match. #37 leaves how many entries a brief
// carries to the operator, and a budget that is merely optimistic still behaves:
// it simply never binds, and the rune backstop does. Refusing it would be this
// package overruling a choice it was told not to make.
const MaxReachableWorkingSet = maxBlockRunes / minBlockRunes

// budget is the working set's two limits taken together: how many entries a
// brief carries (the policy's) and how many runes their lines may add up to
// (maxBlockRunes). Both selection paths ask it, so an entry is in the working
// set exactly when renderLocked takes it and when orderRanked marks it Active.
//
// closed is the standing the budget has settled on, empty until one of the two
// limits stops it. It is what lets a walk carry on assigning reasons after it
// has stopped taking: an operator asking why an entry is NOT in their brief
// needs an answer for every entry, not only for the ones the walk reached before
// it gave up. Once set it never changes, which is what keeps a later, lighter
// entry from being taken behind one that did not fit — see take.
//
// seen counts the candidates OFFERED rather than the slots left over, because a
// closed budget still owes every entry behind it a reason: once BOTH caps have
// bitten, how many entries were ahead of this one is the only thing that tells
// them apart. Oversized entries are not counted — they were never candidates, so
// they must not push a lighter entry past the count.
type budget struct {
	slots  int // entries the count budget allows
	seen   int // injectable entries offered so far, taken or not
	runes  int // block runes still allowed
	closed Standing
}

func newBudget(p Policy) budget { return budget{slots: p.WorkingSet, runes: maxBlockRunes} }

// take decides an entry's Standing, and is the ONE place that decision is made —
// so the reason score.list reports and the selection a brief actually makes
// cannot say different things.
//
// The three ways out of the working set fail differently, and the differences
// are what keep the two selection paths agreeing:
//
//   - StandingOversized is checked FIRST and charges nothing. An entry too heavy
//     to inject at all (maxEntryRunes) was never a candidate — renderLocked
//     filters it out before ranking — so orderRanked must not let it consume a
//     slot, must not let it close the budget, and must not describe it by a
//     budget already closed. Its own weight is the more specific truth either
//     way.
//   - StandingBelowBudget is the count budget spent: this entry had at least
//     score.working-set injectable entries ranked ahead of it. It is decided
//     before the block is consulted, and it OUTRANKS a block that closed earlier.
//     Past the count both caps really do exclude the entry, and only one of them
//     is a knob — maxBlockRunes is not configurable — so answering block-full
//     there sends the operator to shorten entries when widening
//     score.working-set is what would let this one in.
//   - StandingBlockFull is the rune backstop, and only for entries INSIDE the
//     count budget: the ones a wider budget would not have helped. It ends the
//     taking rather than skipping ahead to something lighter that would have
//     fitted. Terminal on purpose: renderLocked holds only the highest-ranked few
//     and has no lighter candidate to promote, so a skipping rule would let the
//     two paths disagree about the working set. Filling in rank order and
//     stopping is the rule both can keep, and it costs at most one entry's worth
//     of headroom out of twenty-four.
//
// Which cap closed the budget FIRST is still what closed records, so
// View.BlockFull keeps meaning "the runes ran out" however many below-budget
// entries are listed behind it.
func (b *budget) take(e Entry) Standing {
	if !e.Injectable() {
		return StandingOversized
	}
	b.seen++
	switch {
	case b.seen > b.slots:
		if b.closed == "" {
			b.closed = StandingBelowBudget
		}
		return StandingBelowBudget
	case b.closed != "":
		return b.closed
	}
	n := blockRunes(e)
	if n > b.runes {
		b.closed = StandingBlockFull
		return b.closed
	}
	b.runes -= n
	return StandingActive
}

// renderLocked is Render's body, so View — which must not let go of the lock
// between reconciling and answering — can ask without dropping it. The caller
// holds the lock.
//
// It selects into a slice the size of the working set rather than ranking the
// whole store into one list and sorting it. This runs on the DISPATCH path,
// once per brief, and the budget is a handful while the store is not: sorting
// five thousand entries to keep seven would put the store's whole arithmetic,
// and an allocation the size of it, on the path a brief is delivered through.
// rankAllLocked is the shape that does rank everything, and it is reached only
// by an operator asking.
//
// The two must agree, and they do because they share rankLocked and
// rankBefore: an insertion into a list kept in that order yields the same first
// N as sorting by it, since the order is total (see rankBefore).
func (s *Store) renderLocked(ctx Context) (out []Entry, full bool) {
	n := s.policy.WorkingSet
	oldest, newest := s.recencySpanLocked()
	// Kept in rank order as it fills; at most n long, and n is a handful.
	var top []Ranked
	for _, e := range s.entries {
		if !e.Injectable() {
			continue
		}
		r := s.rankLocked(e, ctx, oldest, newest)
		at := len(top)
		for at > 0 && rankBefore(r, top[at-1]) {
			at--
		}
		if at == n {
			continue // worse than every entry already held, and the set is full
		}
		if len(top) < n {
			top = append(top, Ranked{})
		}
		copy(top[at+1:], top[at:])
		top[at] = r
	}
	// The rune backstop is applied to the SELECTED set, in rank order, and not
	// during the insertion above: an entry that fits early can be evicted by a
	// better one later, so charging it on the way in would spend budget on
	// entries that never made the working set. top holds only injectable entries
	// and is no longer than the count budget, so the only standing that can end
	// this walk is StandingBlockFull and the result is a prefix. See
	// maxBlockRunes.
	b := newBudget(s.policy)
	kept := 0
	for _, r := range top {
		if b.take(r.Entry) != StandingActive {
			break
		}
		kept++
	}
	full = b.closed == StandingBlockFull
	if kept == 0 {
		// nil, not an empty slice: scoreList tells the two apart to keep
		// score.list's entries an array rather than JSON null.
		return nil, full
	}
	out = make([]Entry, kept)
	for i := range out {
		out[i] = top[i].Entry
	}
	return out, full
}

// rankAllLocked ranks EVERY entry the store holds against ctx, in the entry
// set's own order and without marking anything — orderRanked does both of those,
// off the lock. The caller holds the lock.
//
// It exists because a multiplicative rank without its breakdown answers "why is
// this entry in my brief" with a number rather than a reason, and invariant I8
// says an operator must not need the event log to understand what the fleet is
// being told. Uncapped, for the same reason: capped at the working set, the tier
// of everything past it appeared in no surface at all (#42).
func (s *Store) rankAllLocked(ctx Context) []Ranked {
	if len(s.entries) == 0 {
		return nil
	}
	oldest, newest := s.recencySpanLocked()
	out := make([]Ranked, len(s.entries))
	for i, e := range s.entries {
		out[i] = s.rankLocked(e, ctx, oldest, newest)
	}
	return out
}

// orderRanked sorts a ranked set and marks the working set in it. It runs OFF
// the store lock, which is why it is a package function over a slice rather
// than a method: the sort is O(n log n) with an allocation the size of the
// store behind it, and holding the mutex across it would stall every concurrent
// dispatch for as long as an operator's score.list takes. The ranking itself
// has to be under the lock, since it reads the entry set; the ordering does
// not, since the slice is the caller's own copy by then.
//
// The copies are safe to hold: an Entry's Text is a string, and its Aliases are
// only ever replaced with a fresh slice, never written through spare capacity —
// which appendAlias does deliberately, for exactly this reason.
//
// Standing, and the Active flag derived from it, are decided here rather than by
// the caller so that one rule marks the working set — and it is the SAME rule
// renderLocked applies, see budget.take. An entry can be out of the brief for
// three different reasons and none of them is visible from the entry itself, so
// the reason is carried rather than left to be inferred; see Standing.
func orderRanked(out []Ranked, p Policy) (ranked []Ranked, full bool) {
	slices.SortFunc(out, func(a, b Ranked) int {
		switch {
		case rankBefore(a, b):
			return -1
		case rankBefore(b, a):
			return 1
		default:
			return 0
		}
	})
	// Every entry gets a standing, including the ones past the point where the
	// budget stopped taking: "why is this NOT in my brief" is a question about
	// all of them, and a walk that broke out here would leave the tail with no
	// answer at all. budget.closed is what makes carrying on safe — see take.
	b := newBudget(p)
	for i := range out {
		out[i].Standing = b.take(out[i].Entry)
		out[i].Active = out[i].Standing == StandingActive
	}
	return out, b.closed == StandingBlockFull
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
		writeBlockLine(&b, e)
	}
	b.WriteString("───────────\n")
	return b.String()
}

// writeBlockLine writes the one line an entry contributes to the block. It is
// its own function so that blockRunes, which must predict this line's length
// without building it, can be checked against it by a test rather than by a
// comment asking the next reader to keep them in step.
func writeBlockLine(b *strings.Builder, e Entry) {
	b.WriteString("- ")
	b.WriteString(e.Text)
	b.WriteString(" [")
	b.WriteString(tierWording(e.Tier))
	b.WriteString("]\n")
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
// which retires a logged entry score.md lacks — from the store.
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
		s.noteEventLocked(ev)
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
				// The SOURCE, not the name — see SourceUser. Both event names reach
				// here and either can carry either source, so counting EventUserSignal
				// records instead would miss every user submission that folded and
				// would count an agent's Reinforce as the user's.
				if ev.Source == SourceUser {
					e.UserSignals++
				}
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
		case EventRaised:
			// The tier is REPLAYED from the log rather than recomputed from the
			// counts against the current threshold. Both invariants want it that
			// way: I1, because the same log then yields the same tiers whatever
			// score.promote-at says on this machine today, and #37's "nothing is
			// demoted", because raising the threshold must not quietly pull
			// entries back down.
			//
			// It is bounded by maxEarnedTier — the ladder's end — so an entry can
			// never be rendered at a rung tierWording has no words for. It is one
			// of the three tier writes TestEveryTierWriteIsRegistered knows about.
			// It is deliberately NOT bounded by Policy.ceiling,
			// which is the computed path's bound: ceiling reads a threshold this
			// machine configures, so replaying through it would give one log two
			// different sets of tiers on two machines, which is invariant I1
			// exactly. I6 is not weakened by that, because the only thing that
			// ever writes a raised event past agentEarnedTier is reinforceLocked
			// with the user's signals behind it.
			//
			// A raise above the bound is IGNORED rather than lowered to it: a
			// record that cannot be true is not evidence of a smaller true one, so
			// the entry keeps the tier it earned and this build under-claims
			// rather than lies, as panel.ParseState does for a state string it
			// does not know. The log is the operator's own file and #38 declines
			// to be a boundary against filesystem access, so this guards the
			// constant, not them.
			switch e := live[ev.Id]; {
			case e == nil:
			case ev.Tier >= 1 && ev.Tier <= maxEarnedTier:
				e.Tier = ev.Tier
			default:
				// Counted rather than merely ignored: a log that asks for a tier
				// this build will not grant is a fact about the log, and silence
				// about it is how an operator ends up asking why an entry reads as
				// "noted" when the history says otherwise.
				s.health.RejectedTiers++
			}
		case EventEdited:
			if e := live[ev.Id]; e != nil {
				// The wording changes and NO counter moves, whatever the source —
				// which is the computed path's rule exactly, so a restart cannot
				// disagree with the pass that wrote the record. See the reword
				// branch of reconcileLocked for why an edit counts nothing, and
				// note that this is also why the recovery pass's own adoption
				// (sourceRecovery) needs no branch of its own any more: nobody
				// edited anything there either.
				reword(e, ev.Text, &s.health.AliasEvictions)
			}
		case EventMerged:
			// The conductor gave this entry another's wording to fold on. Only the
			// alias is replayed: the merge changed no count and no tier when it
			// happened (see Refine), so there is nothing else of it to rebuild, and
			// the entry it absorbed is retired by its own record below.
			if e := live[ev.Id]; e != nil {
				alias(e, ev.Text, &s.health.AliasEvictions)
			}
		case EventLowered:
			// A `lowered` record may only move a tier DOWN, and the guard is what
			// makes invariant I6 hold across a RESTART as well as across a call.
			// lowerLocked can write nothing else — it decrements — so on a log this
			// daemon wrote the guard never fires; what it stops is a hand-edited
			// log turning the conductor's one demotion into a promotion the ladder
			// never granted. Rejected rather than clamped, and counted, exactly as
			// an out-of-range `raised` record is.
			switch e := live[ev.Id]; {
			case e == nil:
			case ev.Tier >= 1 && ev.Tier < e.Tier:
				e.Tier = ev.Tier
			default:
				s.health.RejectedTiers++
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
	// Same for its position: an id with no live entry is never ranked, so keeping
	// its last-movement position would grow the map with the whole log's
	// history of retirements. A retire deletes the key as it is replayed; this
	// catches the id whose events arrived in an order no retire closed — a
	// submission torn away from its own entry, and the ids the recovery table
	// re-admits under a fresh submission later in the same log.
	for id := range s.lastAt {
		if live[id] == nil {
			delete(s.lastAt, id)
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
//     wording becomes an alias, and NOTHING is counted — a correction is not a
//     repetition
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
	// The gauge follows the entry set, and this is the only place a pass can have
	// moved it — however the pass returns, including the early exit for an absent
	// score.md and the file rewrite that failed after its events were already
	// durable.
	defer func() {
		if delta != (Delta{}) {
			s.recountOversizedLocked()
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
		userProv = Provenance{Source: SourceUser}
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
				// Recorded, aliased, and COUNTED AS NOTHING. A reword is not a
				// repetition: #37's model is that an observation came back after
				// being recorded, and one statement corrected three times is one
				// statement said once. R1 and R2 counted it as a reinforcement,
				// which was harmless while recurrence stopped at agentEarnedTier —
				// R4 is what turns a typo fix into the currency that unlocks the
				// top rung, and three ordinary corrections to one line reaching
				// tier 3 is not what #37 reserves it for.
				//
				// What DOES count as the user saying a thing again is the user
				// saying it again: a duplicate line typed into score.md that folds
				// below, a `ctl score submit` that folds, and a brief that matches
				// (Signal). All three are a second statement; this is one statement
				// re-spelled. The entry keeps the tier it earned, the prior wording
				// stays an alias (invariant I4), and the edit is in the log either
				// way (I7).
				//
				// What that alias buys is narrower than I4 sounds, and the
				// narrowing bites on THIS door. A repeat of the old wording folds
				// when it arrives through Submit or Signal, which match against
				// aliases; a duplicate LINE carrying the old wording does not,
				// because the file pass indexes only the wordings the file
				// currently shows (see newFoldIndex). It is admitted as a second
				// entry saying the same thing instead. That is the deliberate,
				// pre-existing rule this table already states above — folding a
				// file line DELETES it, and deleting the operator's line is only
				// justified while the file still shows that wording — and R6's
				// conductor `merge` is what joins the pair. So the operator whose
				// reword this is may see their old phrasing come back as its own
				// entry, and nothing here counts that as a repetition either.
				pending = append(pending, event{
					Schema: Schema, Event: EventEdited, Id: id, At: now,
					Source: SourceUser, Text: text,
				})
				reword(&e, text, &pass.AliasEvictions)
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
	// It answers with a POSITION in next, so the fold record and the reinforcement
	// both land here, beside the decision that earned them, rather than in a
	// second loop over every surviving entry whose only work was turning ids back
	// into positions. Determinism is untouched: bullets are walked in file order,
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
		// next: the fold event, the reinforcement it earns, and the raise that may
		// follow. The event's SOURCE is what says the operator folded this, not the
		// event's NAME; see foldEvent. RemovedLine is true because this pass is
		// about to take the line out — the effect, not the provenance, is what
		// decides whether a removal can be owed.
		//
		// The one-per-entry-per-pass cap is the callers' to keep, and both of them
		// keep it the same way: they call this only where the entry's fold record
		// is not already marked Counted, and they mark it immediately after.
		countRepeat := func(target int, text string) {
			pending = append(pending, foldEvent(next[target].Id, text, userProv, now, true, false))
			if raised, ok := s.reinforceLocked(&next[target], userProv.Source, now); ok {
				pending = append(pending, raised)
				delta.Raised++
			}
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
					folds[at].Reinforcements, folds[at].UserSignals, folds[at].Tier =
						next[target].Reinforcements, next[target].UserSignals, next[target].Tier
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
				Id: next[target].Id, Text: next[target].Text, Repeat: b.text, Prov: userProv, At: now,
				Reinforcements: next[target].Reinforcements, UserSignals: next[target].UserSignals,
				Tier: next[target].Tier, Duplicates: 1, Counted: counted, FromFile: true,
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
// With no entries this is the first run, and what it writes is an EMPTY file.
// The store seeds nothing: what a fresh install shows is what the fleet has
// earned, which on a fresh install is nothing at all. The format the file's
// header used to teach is docs/SCORE.md's to teach now.
func (s *Store) projectLocked() (Delta, error) {
	var out []string
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
// Storing "x" and "X." both would grow the list — and every boot's replay of it
// — with a distinction nothing downstream can act on.
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

// alias keeps prior as one of the entry's wordings and counts what the cap threw
// out to make room — the pair that must happen together, with the counter the
// half most easily forgotten. It is what the conductor's MERGE does on its own
// (a wording is gained, none is retired) and the first half of what a reword
// does.
//
// It is package-level and takes the counter by pointer because its callers count
// into different places: replayLocked straight into the store's standing health,
// a reconcile pass into the counters it applies only if it commits, and the two
// conductor corrections into a local they fold in on the same commit.
func alias(e *Entry, prior string, evictions *int) {
	var evicted bool
	e.Aliases, evicted = appendAlias(e.Aliases, prior)
	if evicted {
		*evictions++
	}
}

// reword retires an entry's current wording into its aliases and gives it the
// new one — the three steps that must happen together. See alias for the counter.
func reword(e *Entry, text string, evictions *int) {
	alias(e, e.Text, evictions)
	e.setText(text)
}

// noteEventLocked gives one log record its position and records what that
// position means for the ranking. The caller holds the lock.
//
// It is the ONE place recency is derived, called by replayLocked for every
// record it parses out of the log and by appendEvents for every record it lands
// in it. The two must produce identical positions or a restart would reorder
// the fleet's memory, and one function called from both is what makes that
// structural rather than two lists someone keeps in step by hand.
//
// The names that move an entry's position are every reinforcement — a fold and
// a user signal — plus the submission that created the entry, which is where an
// entry nothing has reinforced yet sits, plus an edit the OPERATOR made.
//
// That last one is the only place recency and the reinforcement count disagree,
// and they are MEANT to: recency is what is current, the count is what was
// earned. A reword counts nothing (see reconcileLocked) because a correction is
// not a repetition, and it moves the position anyway.
//
// The case that decides it is an operator correcting a WRONG entry. If the edit
// did not move the position, the corrected text would rank exactly where the
// wrong text sat — measured on a ten-entry store, that is last, and in no brief
// at all. The operator would have fixed the thing the fleet keeps acting on and
// changed nothing about what the fleet is told, with no way to tell that from a
// fix that took. Ranking an entry as stale because correcting it bought no tier
// is the wrong answer to a different question.
//
// A `raised` takes a position like every other record but moves nothing: a tier
// is the CONSEQUENCE of a reinforcement, and counting it as one too would let a
// single fold that crossed the threshold count twice. A `retired` drops the
// key, because a retired entry is not ranked and its id is never reissued.
//
// The conductor's three records move nothing either, and the SOURCE test above
// is what holds the reword case: an `edited` stamped sourceConductor falls
// through, while the operator's own falls in. That is deliberate and it is R4's
// ruling followed all the way down — a conductor reword counts nothing, so it
// must not buy the rank a fresh position is worth either, which is the one thing
// an operator's edit legitimately does buy. `merged` and `lowered` name no case
// at all, for the same reason.
func (s *Store) noteEventLocked(ev event) {
	s.seq++
	switch {
	case ev.Event == EventSubmitted, ev.Event == EventFolded, ev.Event == EventUserSignal:
		s.lastAt[ev.Id] = s.seq
	case ev.Event == EventEdited && ev.Source == SourceUser:
		// Deliberate, and deliberately unlike reconcileLocked's reword branch,
		// which counts this same event as no reinforcement at all. An operator who
		// corrects a wrong entry must not leave the corrected text ranked where
		// the wrong text sat; see this function's doc before reconciling the two.
		s.lastAt[ev.Id] = s.seq
	case ev.Event == EventRetired:
		delete(s.lastAt, ev.Id)
	}
}

// appendEvents appends every event as its own log line, in one write and one
// fsync, and gives each its position in the log. Batching is what keeps a large
// reconcile off the dispatch path's neck: the cost of a durable append is the
// fsync, so a pass over a thousand changed lines must not pay a thousand of
// them. The caller holds the lock.
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
	if err := appendDurable(s.eventsPath, buf.Bytes()); err != nil {
		return err
	}
	// Positions are taken only now, and only here, because appendDurable is
	// all-or-nothing: a write that landed no bytes must leave the store's idea of
	// the log's length exactly where a re-Open would find it.
	for _, ev := range evs {
		s.noteEventLocked(ev)
	}
	return nil
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
// survives a crash. Same idiom as internal/state's state file — copied, not
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
// which caps at the working-set budget and withholds anything over the weight
// cap. Zero on the disabled (nil) store.
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
