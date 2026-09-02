package server

import (
	"fmt"
	"iter"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
)

// The two rates minSubmitGap is derived from, as numbers a test can do
// arithmetic with. Both were measured rather than estimated — see #56 — and the
// constant's own comment is the argument they support.
const (
	// healthyFleetPerDay is what a well-behaved fleet writes across ALL its
	// agents in a day.
	healthyFleetPerDay = 2000
	// retryLoopPerSecond is the pathology: one actor with no reason to keep the
	// reply and every reason to retry past it, sustained.
	retryLoopPerSecond = 73
)

// submitAs sends one score.submit from a FRESH connection declaring self, and
// returns the daemon's reply. A new connection each time is the shape that
// matters throughout this file: `baton mcp` dials per tool call and `baton ctl`
// is a process per command, so a cap keyed on the connection would be dead on
// every path an agent actually uses.
func submitAs(t *testing.T, s *Server, self, text string) proto.ServerMsg {
	t.Helper()
	cc := conn(self)
	s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: text})
	return reply(t, cc)
}

// spendBurst empties one actor's burst allowance with repeats of text, so what
// follows is testing the SUSTAINED cap rather than the handful the bucket hands
// out. The text is the caller's because a test that then counts entries needs
// the burst to fold into the one it is about.
func spendBurst(t *testing.T, s *Server, self, text string) {
	t.Helper()
	for i := range submitBurst {
		if got := submitAs(t, s, self, text); got.Type != "score" {
			t.Fatalf("%q's submission %d of the burst answered %+v, want it recorded", self, i, got)
		}
	}
}

// TestTheSubmitCapIsWorthItsName pins minSubmitGap to the derivation its comment
// gives, in BOTH directions — which is the whole reason this test exists rather
// than the firing test below alone. #46's four tuned constants were each argued
// at length and asserted only from the tightening side, so every loosening
// mutation passed while the prose beside them went on claiming something else.
//
// The derivation: legitimate use and the pathology are four orders of magnitude
// apart, so the cap stands at their geometric mean — the middle of that range on
// the scale it spans. Anything else needs the comment rewritten, so the two fail
// together.
func TestTheSubmitCapIsWorthItsName(t *testing.T) {
	const (
		fleetPerSecond = healthyFleetPerDay / 86400.0
		// The band is generous — the claim is "the middle of four orders of
		// magnitude", not a fifth significant figure — and still narrow enough
		// that a quarter-second (the other two caps' figure) and two seconds both
		// fall outside it.
		band = 1.5
	)
	want := math.Sqrt(fleetPerSecond * retryLoopPerSecond)
	got := 1 / minSubmitGap.Seconds()
	if ratio := math.Max(got/want, want/got); ratio > band {
		t.Errorf("minSubmitGap is %v, which admits %.2f submissions a second; its comment derives "+
			"%.2f a second from the geometric mean of %.4f/s (a healthy fleet's whole day) and %d/s "+
			"(the retry loop). Retune the constant or rewrite the derivation — they are one claim",
			minSubmitGap, got, want, fleetPerSecond, retryLoopPerSecond)
	}
}

// refusedUnder drives one cap through an arrival process and returns the share
// of submissions it refused, as a percentage. The process yields the actor and
// the instant of each submission.
func refusedUnder(limit rateBuckets, arrivals iter.Seq2[string, time.Time]) float64 {
	total, refused := 0, 0
	for who, at := range arrivals {
		total++
		if _, tooSoon := limit.tooSoon(who, at); tooSoon {
			refused++
		}
	}
	return 100 * float64(refused) / float64(total)
}

// exponential is a fleet of agents each submitting with exponential
// inter-arrivals at lambda a second — a Poisson process, which is the arrival
// process real traffic has and the one an EVENLY SPACED sequence cannot stand in
// for. Seeded, so the percentages the tests quote are reproducible.
func exponential(agents int, lambda float64, each int) iter.Seq2[string, time.Time] {
	return func(yield func(string, time.Time) bool) {
		rnd := rand.New(rand.NewSource(7)) //nolint:gosec // a reproducible arrival process, not a secret
		base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		for a := range agents {
			who, at := strconv.Itoa(a), base
			for range each {
				at = at.Add(time.Duration(rnd.ExpFloat64() / lambda * float64(time.Second)))
				if !yield(who, at) {
					return
				}
			}
		}
	}
}

// turns is the third arrival process the cap's comments quote and nothing
// encoded: an agent that finishes a task and writes the several DISTINCT
// observations that task taught it, back to back, then goes quiet until the next
// one. lambda is the per-agent observation rate, so the volume matches
// exponential's at the same figure and the two are comparable — what differs is
// only the shape.
//
// The observations are 10 ms apart, which is what "back to back" means here: two
// MCP tool calls in a row, not two instants that happen to share a nanosecond.
// Seeded, so the percentages the tests quote are reproducible.
func turns(agents int, perTurn int, lambda float64, each int) iter.Seq2[string, time.Time] {
	return func(yield func(string, time.Time) bool) {
		const writeGap = 10 * time.Millisecond
		rnd := rand.New(rand.NewSource(11)) //nolint:gosec // a reproducible arrival process, not a secret
		base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		for a := range agents {
			who, at := strconv.Itoa(a), base
			for range each {
				at = at.Add(time.Duration(rnd.ExpFloat64() / (lambda / float64(perTurn)) * float64(time.Second)))
				for i := range perTurn {
					if !yield(who, at.Add(time.Duration(i)*writeGap)) {
						return
					}
				}
			}
		}
	}
}

// TestTheSubmitCapIsSilentForRealisticTraffic is the direction a firing test
// cannot see: a cap that refused honest work would be worse than the growth it
// stops, because a refused submission that is not a repeat is an observation the
// fleet never gets back.
//
// IT IS WRITTEN AROUND THE ARRIVAL PROCESS AND NOT AROUND THE RATE, which is the
// whole reason it replaces the evenly-spaced version. A minimum gap is refused by
// JITTER, so perfectly even arrivals are the one distribution that cannot fail
// one — the old test asserted "a healthy fleet at 216x refuses nothing" against
// the only input for which that answer was guaranteed, and passed identically
// with the cap tuned to anything at all. Exponential inter-arrivals refuse 33% of
// the very same volume.
//
// Both directions, in one test, because either alone is passed by a change that
// breaks the other:
//
//   - drop the burst and the honest fleet is refused a third of the time
//   - drop the sustained refill and the retry loop is admitted in full
//
// ALL THREE PROCESSES, because the mechanism's comment quotes three and only two
// were ever encoded. The turn shape — the one the burst was chosen for — is the
// one that went unmeasured, and its figure was not what the comments claimed.
//
// The bands are wide on purpose, and the BANDS are the claim: "honest traffic is
// essentially never refused, and a machine-speed loop essentially always is",
// not a third significant figure of a seeded sample. rateBuckets is where the
// run these bands were drawn around is written down.
func TestTheSubmitCapIsSilentForRealisticTraffic(t *testing.T) {
	const (
		// Ten agents at 0.5/s each is 5/s, which is 216x a healthy fleet's whole
		// day — the load #56's review measured, chosen so this is not a claim
		// about a quiet fleet.
		agents, busy = 10, 0.5
		honestMax    = 5.0
		loopMin      = 90.0
		// A turn's worth: three distinct observations as a task finishes. The two
		// bands say what the burst BUYS on that shape rather than that it makes it
		// free, because it does not.
		perTurn     = 3
		turnMax     = 25.0
		turnGapOnly = 60.0
	)
	fresh := func() rateBuckets { return rateBuckets{gap: minSubmitGap, burst: submitBurst} }

	if got := refusedUnder(fresh(), exponential(agents, busy, 2000)); got > honestMax {
		t.Errorf("%.1f%% of an honest fleet's submissions were refused (%d agents, exponential "+
			"inter-arrivals at %.2f/s each = %.0fx a healthy fleet's day), want under %.0f%%. Every "+
			"one of those is a distinct observation the fleet does not get back — a repeat would have "+
			"folded. Remove the burst allowance and this reads about 33%%",
			got, agents, busy, agents*busy/(healthyFleetPerDay/86400.0), honestMax)
	}
	if got := refusedUnder(fresh(), exponential(1, retryLoopPerSecond, 20000)); got < loopMin {
		t.Errorf("only %.1f%% of a %d/s retry loop's submissions were refused, want over %.0f%%: the "+
			"burst allowance must not have widened the SUSTAINED rate, which is the whole bound on "+
			"the log", got, retryLoopPerSecond, loopMin)
	}

	// The turn shape, and both figures the constants quote for it. The first is
	// what the burst leaves; the second is what it took away, and without it the
	// claim that the burst is worth having is a claim about nothing.
	if got := refusedUnder(fresh(), turns(agents, perTurn, busy, 700)); got > turnMax {
		t.Errorf("%.1f%% of a turn-shaped fleet's submissions were refused (%d agents writing %d "+
			"observations as each task finishes, at the same %.0fx volume), want under %.0f%%. That "+
			"is the shape the burst exists for: a turn's observations are distinct lessons, and one "+
			"kept out of three is two the fleet never gets back",
			got, agents, perTurn, agents*busy/(healthyFleetPerDay/86400.0), turnMax)
	}
	if got := refusedUnder(rateBuckets{gap: minSubmitGap}, turns(agents, perTurn, busy, 700)); got < turnGapOnly {
		t.Errorf("a plain minimum gap refused %.1f%% of the same turn-shaped fleet, want over %.0f%%. "+
			"The measured 71%% is the whole case for the bucket; if it is no longer true, the burst's "+
			"argument is being made against a shape that never failed", got, turnGapOnly)
	}
}

// TestTheSubmitBurstIsWorthItsName pins submitBurst the way
// TestTheSubmitCapIsWorthItsName pins the gap: in the direction nothing else
// asserts. A burst of four is chosen because an agent's turn carries three to
// four DISTINCT observations and all of them have to land; one is the minimum
// gap this replaced, and a large one hands a looping actor a free run every time
// it goes quiet.
func TestTheSubmitBurstIsWorthItsName(t *testing.T) {
	if submitBurst < 3 {
		t.Errorf("submitBurst is %d, which clips the three-to-four observations one agent turn "+
			"writes — the case the cap was measured failing", submitBurst)
	}
	if submitBurst > 8 {
		t.Errorf("submitBurst is %d: an idle actor comes back holding that many free records, and "+
			"the bound on the log is the sustained rate plus this", submitBurst)
	}
}

// TestTheCappingLinesPacingIsWorthItsName pins saySubmitCappedEvery, which was
// pinned in NEITHER direction. TestTheRateCapsAreWorthTheirNames does this for
// the other two constants and this one was never added to it; measured, a minute
// could be retuned to an hour, to a day, or to a millisecond and the whole suite
// stayed green. Only a nanosecond died, and only because a hundred refusals
// outrun a window that short.
//
// The pacing test below cannot do it: it rewinds by 2*saySubmitCappedEvery, so
// it is self-relative and holds at any value at all. What is asserted here is
// the constant's own two claims, which are both falsifiable.
//
// THE FLOOR is the log not becoming the thing that grows. A refused loop
// produces a refusal per attempt, so the line is what stands between this cap
// and moving score-events.jsonl's growth into baton.log — measured at 78 KB/s
// uncapped, and baton.log has no rotation (#64). Ten times minSubmitGap says it
// plainly: even an actor being refused at machine speed writes lines at a tenth
// of the rate the cap admits submissions.
//
// THE CEILING is the other claim: an operator tailing the log has to learn the
// capping is STILL happening, not that it once did. Past ten minutes a collapse
// is announced once and then silent for the rest of the incident, which reads as
// resolved.
func TestTheCappingLinesPacingIsWorthItsName(t *testing.T) {
	if floor := 10 * minSubmitGap; saySubmitCappedEvery < floor {
		t.Errorf("saySubmitCappedEvery is %v, under %v (ten times minSubmitGap): a loop refused at "+
			"machine speed then writes a warning line per actor that often, which moves the growth "+
			"this cap removes into a log that nothing rotates", saySubmitCappedEvery, floor)
	}
	if ceiling := 10 * time.Minute; saySubmitCappedEvery > ceiling {
		t.Errorf("saySubmitCappedEvery is %v, over %v: an operator tailing baton.log through a "+
			"collapse is told once and then hears nothing, and its own comment says they must learn "+
			"the capping is still happening", saySubmitCappedEvery, ceiling)
	}
}

// TestABurstOfDistinctObservationsAllLands is the measured failure, at the door
// and against a real store: one agent finishing a turn writes several DIFFERENT
// lessons back to back, and under a minimum gap the store kept the first and
// refused the rest. Nothing there folded — the entry count is the assertion,
// because it is the number that says the lost ones were not repeats.
//
// The second half is what keeps the first from being an exemption: one more
// inside the same instant IS refused, so the allowance is spent rather than
// waived. Both directions again, and neither passes a cap that lost the other.
func TestABurstOfDistinctObservationsAllLands(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)

	notes := make([]string, submitBurst)
	for i := range notes {
		notes[i] = fmt.Sprintf("the fleet forgets step %d of the release checklist", i)
	}
	for i, note := range notes {
		if got := submitAs(t, s, "p1", note); got.Type != "score" {
			t.Fatalf("observation %d of %d written in one turn answered %+v, want it recorded. These "+
				"are distinct lessons, not a repeat: a refusal here is one the fleet never gets back",
				i+1, len(notes), got)
		}
	}
	if n := st.Len(); n != len(notes) {
		t.Errorf("the store holds %d entries after %d distinct observations in one turn, want %d",
			n, len(notes), len(notes))
	}
	if got := submitAs(t, s, "p1", "and the changelog too"); got.Type != "error" ||
		!strings.Contains(got.Error, "too fast") {
		t.Errorf("submission %d in the same instant answered %+v, want the rate refusal: the burst is "+
			"an allowance to spend, and past it the sustained cap is the only thing bounding the log",
			len(notes)+1, got)
	}
}

// TestTheSubmitCapFiresOnTheRetryLoop is the other direction, over the door
// itself rather than the type: a loop submitting at machine speed gets exactly
// one through per gap, and — the part that makes this a bound on the LOG rather
// than on the wire — the refusals reach no store at all.
//
// A retry loop's submissions FOLD. Folding is exact after normalisation, so the
// loop creates no entries; it creates one `folded` record per attempt, and those
// records are the growth. A cap that admitted the write and merely refused to
// answer would have stopped nothing.
func TestTheSubmitCapFiresOnTheRetryLoop(t *testing.T) {
	st, dir := scoreStore(t)
	s, _, _ := scoreServer(st)
	events := filepath.Join(dir, "score-events.jsonl")

	const retried = "the fleet keeps retrying a failed submission"
	submit := func() proto.ServerMsg { return submitAs(t, s, "p1", retried) }
	// The burst is spent first, and spending it is part of the assertion: the
	// loop's opening handful IS admitted, because nothing on this door can tell
	// them from four distinct observations until the store folds them. The SAME
	// text, so the store still holds one entry at the end.
	spendBurst(t, s, "p1", retried)
	grown, err := os.Stat(events)
	if err != nil {
		t.Fatalf("stat the event log: %v", err)
	}
	after := grown.Size()

	for i := range 200 {
		got := submit()
		if got.Type != "error" || !strings.Contains(got.Error, "too fast") {
			t.Fatalf("attempt %d answered %+v, want the rate refusal: 200 submissions inside one %v "+
				"gap is the loop this cap exists for", i, got, minSubmitGap)
		}
	}
	fi, err := os.Stat(events)
	if err != nil {
		t.Fatalf("stat the event log: %v", err)
	}
	if fi.Size() != after {
		t.Errorf("the event log grew %d B across 200 refused submissions; a refusal must reach the "+
			"store not at all, or the cap bounds how fast the log may be written rather than the log",
			fi.Size()-after)
	}
	if n := st.Len(); n != 1 {
		t.Errorf("the store holds %d entries, want 1: the loop repeats one observation", n)
	}
}

// TestTheSubmitCapKeysOnTheActor is #46's bug asserted for the third cap before
// it can be written a third time. Both other caps kept their stamp on the
// clientConn, which reads as the obvious place and throttled nothing an agent
// drives — MCP dials a fresh connection per tool call, so the state the cap
// depended on was destroyed between two calls of a loop.
//
// The half a single-stamp cap cannot do is the second assertion here: submission
// is open to EVERY panel, so a busy neighbour must not clear this actor's stamp.
// One stamp in total would be handed from panel to panel and the cap would be
// dead exactly when the fleet is busy.
func TestTheSubmitCapKeysOnTheActor(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)
	const linter = "the fleet forgets to run the linter"

	spendBurst(t, s, "p1", linter)
	// Another panel is admitted at once — the cap fences an actor, not the verb.
	if got := submitAs(t, s, "p2", "the fleet forgets to tidy its worktrees"); got.Type != "score" {
		t.Fatalf("p2 was refused by p1's allowance: %+v", got)
	}
	// And p1 is still held, over a connection p1 has never used before. This is
	// the assertion that fails for a cap on the clientConn AND for a cap with one
	// allowance in total.
	if got := submitAs(t, s, "p1", linter); got.Type != "error" ||
		!strings.Contains(got.Error, "too fast") {
		t.Fatalf("p1 was admitted again over a fresh connection after p2 submitted (%+v); the cap "+
			"must be keyed on the actor and must hold one allowance per actor", got)
	}
	// The operator's own door — `baton ctl score submit`, which declares no panel
	// — is capped too. Exempting it would leave a shell loop walking straight
	// through the only cap on this door.
	spendBurst(t, s, "", linter)
	if got := submitAs(t, s, "", "operators drain the queue before lunch"); got.Type != "error" {
		t.Fatalf("the operator's door answered %+v once its burst was spent, want the rate refusal: "+
			"an uncapped door is the loop's way round the cap", got)
	}
}

// TestClientsOutsideAPanelDoNotShareOneSlot is the identity half of the cap,
// over the wire the identity actually arrives on.
//
// A panel id reaches the daemon because the daemon injected it. Outside a panel
// there is none, so `baton ctl score submit` from the operator's shell and every
// MCP server started outside the fleet all declared the empty string — and a cap
// keyed on that gave them ONE slot between them. Measured before this: three
// different observations back to back, from clients with nothing to do with each
// other, reached the store as one record and two refusals.
//
// The refusal's line names the actor as well, for the same reason: `panel=` beside
// `source=user` told an operator asking "who is filling my log" precisely nothing,
// on exactly the clients that have no panel to go and look at.
func TestClientsOutsideAPanelDoNotShareOneSlot(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)
	logged := captureLog(t)

	// Through hello, not by setting the field: the plumbing from the wire to the
	// cap key is half of what this is asserting.
	greet := func(actor string) *clientConn {
		cc := conn("")
		s.onCommand(cc, proto.Command{Action: "hello", Actor: actor})
		for len(cc.out) > 0 {
			<-cc.out
		}
		return cc
	}
	submit := func(cc *clientConn, text string) proto.ServerMsg {
		s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: text})
		return reply(t, cc)
	}

	// An MCP server outside the fleet spends its whole allowance...
	const mcp, shell = "ppid:4242", "ppid:9910"
	for i := range submitBurst {
		cc := greet(mcp) // a fresh connection per tool call, which is its real shape
		if got := submit(cc, fmt.Sprintf("the fleet mislabels error %d", i)); got.Type != "score" {
			t.Fatalf("the out-of-panel MCP server's submission %d answered %+v, want it recorded", i, got)
		}
	}
	// ...and the operator's own shell, a different process entirely, is untouched.
	if got := submit(greet(shell), "operators drain the queue before lunch"); got.Type != "score" {
		t.Fatalf("the operator's own `baton ctl score submit` answered %+v while an unrelated "+
			"out-of-panel client was busy; distinct clients must not share one slot", got)
	}
	// The one that is actually over IS refused — the slots are separate, not absent.
	if got := submit(greet(mcp), "the fleet mislabels error 99"); got.Type != "error" {
		t.Fatalf("the out-of-panel MCP server answered %+v past its burst, want the rate refusal: "+
			"giving each client its own slot must not have given any of them an uncapped one", got)
	}
	if line := logged(); !strings.Contains(line, `"actor":"`+mcp+`"`) {
		t.Errorf("the capping line does not name %q:\n%s\nA client with no panel is the only kind "+
			"this cap now separates, and the only kind the old line could not identify", mcp, line)
	}
}

// TestAConnectionIsNotAnInfoLine is the half of #56 the rate cap made WORSE.
//
// A refusal is cheap, so a capped loop reconnects faster than an uncapped one —
// and `baton mcp` dials a fresh connection per tool call. Every one of those
// wrote two ~52 B Info lines, so the cap that removed 6.7 GB a day from
// score-events.jsonl raised the daemon's TOTAL writes: measured on a live daemon,
// 19.6 MB/day of event log against 40 GB/day of baton.log.
//
// The assertion is the level and not the absence, because the lines are still
// worth having for an operator who asked for them. What must not happen is a
// transport deciding how big the daemon log is at the DEFAULT level.
func TestAConnectionIsNotAnInfoLine(t *testing.T) {
	s := New(nil)
	logged := captureLog(t)

	for range 3 {
		cc := conn("")
		s.addClient(cc)
		s.removeClient(cc)
	}

	line := logged()
	for _, what := range []string{"client attached", "client detached"} {
		if !strings.Contains(line, what) {
			t.Fatalf("nothing logged %q at all:\n%s", what, line)
		}
		if strings.Contains(line, `"level":"info","clients"`) {
			t.Errorf("%q is still an Info line:\n%s\nAt one connection per MCP tool call these are "+
				"the largest writer in the daemon log — 40 GB a day measured against 19.6 MB of the "+
				"event log the cap bounded", what, line)
		}
	}
}

// TestACappedSubmissionIsToldApartFromABrokenStore is invariant I8 for the third
// thing that can go wrong on this door. #46 spent a commit separating the other
// two — a store that will not take the write, and text the store itself refused
// — because an operator who cannot tell them apart debugs the wrong one.
//
// So the caller's error names the pacing and the daemon's line says the store is
// HEALTHY, which is the question an operator asks on seeing submissions fail.
func TestACappedSubmissionIsToldApartFromABrokenStore(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)
	logged := captureLog(t)
	const retried = "the fleet keeps retrying"
	spendBurst(t, s, "p1", retried)
	got := submitAs(t, s, "p1", retried)

	// The reply is the submitter's answer and says what the submitter can do.
	if !strings.Contains(got.Error, "too fast") || !strings.Contains(got.Error, "slow down") {
		t.Errorf("the refusal reads %q; an agent has to be able to tell being paced from a store "+
			"that cannot take its note, and to know that saying it again later will work", got.Error)
	}
	// The daemon's line is the operator's, and it is the one that has to rule out
	// the failure it looks like.
	line := logged()
	if !strings.Contains(line, "rate-capped") {
		t.Fatalf("nothing in the daemon log names the capping:\n%s", line)
	}
	if !strings.Contains(line, "healthy") {
		t.Errorf("the capping line does not say the store is healthy:\n%s\nAn operator seeing "+
			"submissions refused has two other candidates — a full disk and a read-only mount — "+
			"and this line is what rules them out", line)
	}
	// Every duration on the line is rendered the way its constant is written.
	// zerolog's Dur emits bare milliseconds, so the line once read `gap=1000
	// every=60000` beside a cap documented as one second and a window documented
	// as a minute — the defect openScore's `waited` field already fixed once.
	for field, want := range map[string]string{
		"gap":   minSubmitGap.String(),
		"every": saySubmitCappedEvery.String(),
	} {
		if pair := `"` + field + `":"` + want + `"`; !strings.Contains(line, pair) {
			t.Errorf("the capping line does not carry %s:\n%s\nA duration logged as a bare number "+
				"reaches the operator as a different number from the one the docs and the reason "+
				"string give", pair, line)
		}
	}
}

// TestTheCappedWarningIsPacedToo is the defect this cap would otherwise BE. A
// loop produces a refusal per attempt, so a Warn each would move the growth out
// of score-events.jsonl and into baton.log — measured at 78 KB/s uncapped, which
// is the same order as the thing being fixed.
//
// One line per actor per saySubmitCappedEvery, and the direction asserted is the
// one a naive implementation gets wrong: not that a line appears, but that a
// hundred refusals do not leave a hundred of them.
func TestTheCappedWarningIsPacedToo(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)
	logged := captureLog(t)

	for range 100 {
		submitAs(t, s, "p1", "the fleet keeps retrying")
	}
	if n := strings.Count(logged(), "rate-capped"); n != 1 {
		t.Errorf("99 refusals left %d capping lines, want exactly 1 per %v: a line each writes the "+
			"growth this cap removes into the daemon log instead", n, saySubmitCappedEvery)
	}

	// It says so again once the pacing window has passed, so an operator tailing
	// the log learns the capping is still happening rather than that it once did.
	rewindAll(s, &s.sayCapped, 2*saySubmitCappedEvery)
	submitAs(t, s, "p1", "the fleet keeps retrying")
	if n := strings.Count(logged(), "rate-capped"); n != 2 {
		t.Errorf("after a full %v the log holds %d capping lines, want a second: a collapse that "+
			"outlasts the window must not be reported once and forgotten", saySubmitCappedEvery, n)
	}
}
