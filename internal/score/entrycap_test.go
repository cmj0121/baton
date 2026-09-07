package score

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the ENTRY CAP's (#83): the bound on how many entries the store
// will take from its agents, and — far more of it — the bound on WHO that
// refusal reaches.
//
// A test that submits past the cap and asserts a refusal proves the counter and
// nothing else. What the design turns on is the asymmetry: an agent at the
// ceiling is refused, the operator's own line at the same ceiling is admitted,
// a repeat still folds, and a store already past the cap still opens, still
// reconciles and still compacts. Every one of those is written here in BOTH
// directions, because a cap that also froze the file, the folding or the boot
// would pass every test that only checks the refusal fires.

// fillMD writes n bullets the operator could have typed and reconciles them in,
// returning the file's lines so a caller can put some of them back.
//
// It goes through score.md rather than through Submit deliberately: it is the
// only door that is not capped, so it is the one that can seed a store AT or
// PAST the ceiling for the tests that need to look at one.
func fillMD(t *testing.T, s *Store, dir string, n int) []string {
	t.Helper()
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("- the fleet keeps doing the thing number %d", i)
	}
	writeMD(t, dir, strings.Join(lines, "\n")+"\n")
	if _, err := s.Reconcile(); err != nil {
		t.Fatalf("reconcile %d bullets: %v", n, err)
	}
	if s.Len() != n {
		t.Fatalf("entries after seeding = %d, want %d", s.Len(), n)
	}
	return readMDLines(t, dir)
}

// readMDLines is score.md as the store has just rewritten it, ids and all.
func readMDLines(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, scoreMD))
	if err != nil {
		t.Fatalf("read score.md: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// logSize is score-events.jsonl's size, for the assertions that a refusal costs
// the log nothing. A bound that appended a record per refusal would be the
// growth #83 is about, arriving through the door built to stop it.
func logSize(t *testing.T, dir string) int64 {
	t.Helper()
	fi, err := os.Stat(filepath.Join(dir, scoreEvents))
	if err != nil {
		t.Fatalf("stat the event log: %v", err)
	}
	return fi.Size()
}

// TestAnAgentIsRefusedAtTheCapAndTheOperatorIsNot is the whole policy in one
// test, and it is the one that must fail if either half is removed.
//
// The bound exists to stop UNATTENDED growth, so the door an unattended fleet
// grows through is shut and the door a person curates through is not. Freezing
// the operator's file at the ceiling would freeze curation at exactly the moment
// curation is what the store needs, which is the failure #83 names in its own
// "what happens at the ceiling" question.
func TestAnAgentIsRefusedAtTheCapAndTheOperatorIsNot(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Policy{MaxEntries: 5})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	fillMD(t, s, dir, 5)

	before := logSize(t, dir)
	_, _, err = s.Submit("an agent has something new to say", Provenance{Source: SourceAgent})
	if !errors.Is(err, ErrStoreFull) {
		t.Fatalf("submit at the cap: err = %v, want ErrStoreFull", err)
	}
	// Actionable, not merely accurate: both numbers, because the agent cannot
	// act on either and the operator can only act on knowing which is which.
	for _, want := range []string{"5 entries", "limit is 5", "score.md", "score.max-entries"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
	if s.Len() != 5 {
		t.Errorf("entries after the refusal = %d, want 5", s.Len())
	}
	if after := logSize(t, dir); after != before {
		t.Errorf("the refusal grew the log by %d bytes; a refused submission must append nothing", after-before)
	}

	// The other half, and it is the half a counter alone would get wrong. The
	// operator types a sixth line into a store that has just refused an agent a
	// sixth entry, and the store takes it.
	lines := append(readMDLines(t, dir), "- the operator has something to add at the ceiling")
	writeMD(t, dir, strings.Join(lines, "\n")+"\n")
	d, err := s.Reconcile()
	if err != nil {
		t.Fatalf("reconcile the operator's line at the cap: %v", err)
	}
	if d.Admitted != 1 || s.Len() != 6 {
		t.Fatalf("the operator's line at the cap: admitted = %d, entries = %d; want 1 and 6", d.Admitted, s.Len())
	}
	// And the agent is still refused, now against the larger count — so the
	// refusal reports the store as it stands rather than the ceiling it was
	// configured with.
	_, _, err = s.Submit("an agent tries again past the ceiling", Provenance{Source: SourceAgent})
	if !errors.Is(err, ErrStoreFull) {
		t.Fatalf("submit past the cap: err = %v, want ErrStoreFull", err)
	}
	if !strings.Contains(err.Error(), "6 entries") {
		t.Errorf("refusal %q does not report the store's actual count of 6", err)
	}
}

// TestTheCapStillFoldsARepeat pins the second half of "what happens at the
// ceiling": the store stops LEARNING NEW THINGS, not learning.
//
// #37's whole model is that recurrence earns a tier, and a repeat adds no entry
// — so a cap asked before the fold would take the store's one remaining useful
// act away at the moment its memory is most worth ranking. The reinforcement is
// asserted as well as the fold, because a fold that counted nothing would pass
// a test that only looked at the returned flag.
func TestTheCapStillFoldsARepeat(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Policy{MaxEntries: 3})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	fillMD(t, s, dir, 3)

	e, folded, err := s.Submit("the fleet keeps doing the thing number 1", Provenance{Source: SourceAgent})
	if err != nil {
		t.Fatalf("a repeat at the cap was refused: %v", err)
	}
	if !folded {
		t.Fatalf("a repeat at the cap did not fold")
	}
	if e.Reinforcements != 1 {
		t.Errorf("reinforcements after the fold = %d, want 1", e.Reinforcements)
	}
	if s.Len() != 3 {
		t.Errorf("entries after a fold = %d, want 3", s.Len())
	}
}

// TestTheCapRefusesTheOperatorsOwnSubmitToo pins the decision that is easiest to
// reverse by accident and hardest to notice: the refusal does NOT read
// Provenance.Source.
//
// #38 §4 openly calls the source a self-declaration rather than a boundary, and
// #52 declined to hang the operator's merge on it — giving them score.md
// instead. Exempting SourceUser here would hang a WRITE SURFACE on exactly that
// declaration, and it would buy nothing, because the exemption the operator is
// owed already exists unconditionally one test up. If a later change adds the
// branch, this fails and points at that ruling.
func TestTheCapRefusesTheOperatorsOwnSubmitToo(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Policy{MaxEntries: 2})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	fillMD(t, s, dir, 2)

	for _, src := range []string{SourceAgent, SourceUser} {
		_, _, err := s.Submit("a wording neither of them has used before "+src, Provenance{Source: src})
		if !errors.Is(err, ErrStoreFull) {
			t.Errorf("submit at the cap as %q: err = %v, want ErrStoreFull", src, err)
		}
	}
}

// TestASubmissionIsAdmittedOnceTheOperatorRetiresALine is the refusal's other
// direction: the cap is a ceiling on what the store HOLDS, never a latch on what
// it has ever held.
//
// It also pins where the count is taken. The submission below runs against a
// file the store has not read yet, so a cap asked BEFORE Submit's reconcile
// would refuse it against a store that no longer exists — an operator freeing a
// slot and watching the fleet stay frozen until something else happened to
// re-read the file.
func TestASubmissionIsAdmittedOnceTheOperatorRetiresALine(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Policy{MaxEntries: 4})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	lines := fillMD(t, s, dir, 4)

	if _, _, err := s.Submit("something new", Provenance{Source: SourceAgent}); !errors.Is(err, ErrStoreFull) {
		t.Fatalf("submit at the cap: err = %v, want ErrStoreFull", err)
	}
	// The operator deletes one line and does nothing else — no reconcile, no
	// view. The next submission is what reads the file.
	writeMD(t, dir, strings.Join(lines[1:], "\n")+"\n")
	e, folded, err := s.Submit("something new", Provenance{Source: SourceAgent})
	if err != nil {
		t.Fatalf("submit after the operator freed a slot: %v", err)
	}
	if folded {
		t.Fatalf("the submission folded; it repeats nothing in the file")
	}
	if s.Len() != 4 {
		t.Errorf("entries = %d, want 4 — three the operator kept plus the one just admitted", s.Len())
	}
	if e.Id == "" {
		t.Errorf("the admitted entry carries no id")
	}
}

// TestTheCapCountsLiveEntriesAndNotBurnedIds is the assertion that separates the
// bound this change makes from the one it deliberately does not.
//
// Store.burned holds every id the log has ever named and grows monotonically;
// s.entries holds what the store currently remembers. A cap counting the wrong
// one would refuse a store that has churned its way through the ceiling while
// holding almost nothing — which is a store working exactly as #38 intends,
// permanently frozen by its own history.
func TestTheCapCountsLiveEntriesAndNotBurnedIds(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Policy{MaxEntries: 3})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)

	// Three rounds of filling and emptying the file: nine ids burned, none live.
	for range 3 {
		fillMD(t, s, dir, 3)
		writeMD(t, dir, "")
		if _, err := s.Reconcile(); err != nil {
			t.Fatalf("reconcile the emptied file: %v", err)
		}
	}
	s.mu.Lock()
	burned := s.burned.len()
	s.mu.Unlock()
	if burned < 9 {
		t.Fatalf("burned ids = %d, want at least 9 — the churn did not happen", burned)
	}
	if s.Len() != 0 {
		t.Fatalf("entries after emptying the file = %d, want 0", s.Len())
	}

	if _, _, err := s.Submit("the store is empty and must take this", Provenance{Source: SourceAgent}); err != nil {
		t.Fatalf("submit into an empty store that has burned %d ids: %v", burned, err)
	}
}

// TestAStoreOverTheCapOpensReconcilesAndCompacts is the boot half, and it is
// the one a cap enforced in the wrong place would break outright.
//
// A store can be past its cap for two ordinary reasons — the operator curated a
// large score.md, or they lowered score.max-entries under a store that was
// already larger — and neither may cost them their memory. So every path that
// is not Submit has to go on working at any count: the replay, the recovery
// pass, the render, and the rewrite that bounds the log.
func TestAStoreOverTheCapOpensReconcilesAndCompacts(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Policy{MaxEntries: 100})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	fillMD(t, s, dir, 40)
	s.Close()

	// Reopened under a cap a quarter the size of the store already on disk.
	s, err = Open(dir, Policy{MaxEntries: 10})
	if err != nil {
		t.Fatalf("reopen a store past its cap: %v", err)
	}
	t.Cleanup(s.Close)
	if s.Len() != 40 {
		t.Fatalf("entries after reopening past the cap = %d, want 40", s.Len())
	}
	if h := s.Health(); h.TornEvents != 0 {
		t.Errorf("torn events = %d, want 0", h.TornEvents)
	}
	// The render is unaffected: the cap bounds admission, never injection.
	if got := len(s.Render(Context{})); got != defaultWorkingSet {
		t.Errorf("rendered = %d, want the working set of %d", got, defaultWorkingSet)
	}
	// And the operator can still curate downward, which is the act the refusal
	// tells them to perform.
	lines := readMDLines(t, dir)
	writeMD(t, dir, strings.Join(lines[:5], "\n")+"\n")
	d, err := s.Reconcile()
	if err != nil {
		t.Fatalf("reconcile a store past its cap: %v", err)
	}
	if d.Retired != 35 || s.Len() != 5 {
		t.Fatalf("curating past the cap: retired = %d, entries = %d; want 35 and 5", d.Retired, s.Len())
	}

	// The rewrite still lands. maxBytes 0 forces it past the size refusal, so
	// what is being asserted is the compaction itself rather than the trigger.
	n, err := s.compact(0)
	if err != nil {
		t.Fatalf("compact a store that has been past its cap: %v", err)
	}
	if n == 0 {
		t.Fatalf("compaction wrote nothing")
	}
	s.Close()
	s, err = Open(dir, Policy{MaxEntries: 10})
	if err != nil {
		t.Fatalf("reopen a compacted store: %v", err)
	}
	t.Cleanup(s.Close)
	if s.Len() != 5 {
		t.Errorf("entries after compaction and reboot = %d, want 5", s.Len())
	}
}

// TestClampMaxEntriesReadsUnsetAsTheDefault pins clampMaxEntries against
// clampWorkingSet's rule, which is the rule it is deliberately a copy of: a key
// nobody wrote and a count below one are the same instruction, and neither is a
// way to switch the memory off. There is no ceiling — an operator who asks for
// a hundred thousand gets it.
func TestClampMaxEntriesReadsUnsetAsTheDefault(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, defaultMaxEntries},
		{-1, defaultMaxEntries},
		{-100000, defaultMaxEntries},
		{1, 1},
		{7, 7},
		{defaultMaxEntries, defaultMaxEntries},
		{100000, 100000},
	} {
		if got := clampMaxEntries(tc.in); got != tc.want {
			t.Errorf("clampMaxEntries(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
	// And it is reached from the policy the store actually runs on, not only
	// from the helper: a Policy with the field unset must land on the default.
	s, err := Open(t.TempDir(), Policy{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	if got := s.Policy().MaxEntries; got != defaultMaxEntries {
		t.Errorf("an unset policy runs on MaxEntries = %d, want %d", got, defaultMaxEntries)
	}
}

// TestTheCapReloadsOnSetPolicy pins the knob against the reload path. Every
// other number in Policy is one the live store compares and reloads on SIGHUP;
// a cap that only took effect at Open would be the one score key an operator
// could not fix without restarting the fleet, and nothing would say so.
func TestTheCapReloadsOnSetPolicy(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Policy{MaxEntries: 2})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	fillMD(t, s, dir, 2)

	if _, _, err := s.Submit("refused under the small cap", Provenance{Source: SourceAgent}); !errors.Is(err, ErrStoreFull) {
		t.Fatalf("submit at the cap: err = %v, want ErrStoreFull", err)
	}
	if changed := s.SetPolicy(Policy{MaxEntries: 4}); !changed {
		t.Fatalf("SetPolicy reported no change after raising the entry cap")
	}
	if _, _, err := s.Submit("admitted under the larger cap", Provenance{Source: SourceAgent}); err != nil {
		t.Fatalf("submit after raising the cap: %v", err)
	}
	// And downward, which is the direction that must not retire anything: an
	// operator lowering the key past a store that is already larger bounds what
	// it will TAKE, never what it holds.
	if changed := s.SetPolicy(Policy{MaxEntries: 1}); !changed {
		t.Fatalf("SetPolicy reported no change after lowering the entry cap")
	}
	if s.Len() != 3 {
		t.Errorf("entries after lowering the cap = %d, want 3 — lowering it retires nothing", s.Len())
	}
	if _, _, err := s.Submit("refused again", Provenance{Source: SourceAgent}); !errors.Is(err, ErrStoreFull) {
		t.Errorf("submit under the lowered cap: err = %v, want ErrStoreFull", err)
	}
}

// TestDefaultMaxEntriesIsBoundedBothWays pins the NUMBER against what it is
// derived from rather than against itself, in the shape
// TestCompactAtBytesIsBoundedBothWays uses. The derivation lives on
// defaultMaxEntries; this is what fails when the number drifts away from it.
//
// Too SMALL and the cap binds on stores #38 calls ordinary — a fleet's curated
// memory is meant to be small, but "small" there is tens of entries, and a cap
// within reach of what a single brief can carry would refuse a store that is
// doing exactly what it exists to do.
//
// Too LARGE and it is not a bound. #83's own words: a store with 100,000
// entries is not a store anyone is reading, it is a leak — and the 60 MiB log
// describing 200,000 entries at 262 MiB of live heap is the shape the cap has
// to be under.
func TestDefaultMaxEntriesIsBoundedBothWays(t *testing.T) {
	// What one brief can carry at maximum entry length, which is the store's
	// whole purpose expressed as a number; see maxBlockRunes.
	perBrief := maxBlockRunes / maxEntryRunes
	if defaultMaxEntries < 20*perBrief {
		t.Errorf("defaultMaxEntries = %d, which is under twenty times the %d entries one brief can carry; "+
			"a cap that close to the working set refuses ordinary stores", defaultMaxEntries, perBrief)
	}
	if defaultMaxEntries < 50*defaultWorkingSet {
		t.Errorf("defaultMaxEntries = %d, which is under fifty times the default working set of %d",
			defaultMaxEntries, defaultWorkingSet)
	}
	// The leak side. 100,000 is the figure #83 names as no longer a memory, and
	// the default has to be well clear of it rather than merely under it.
	if defaultMaxEntries > 10_000 {
		t.Errorf("defaultMaxEntries = %d, which is within an order of magnitude of the six-figure "+
			"store #83 calls a leak rather than a memory", defaultMaxEntries)
	}
}
