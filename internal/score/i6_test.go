package score

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// This file holds invariant I6 by ENUMERATION, and the division of labour is
// worth stating plainly because the tests here are easy to over-read.
//
// A behavioural test can only say "these hundred calls did not reach the top
// tier". It cannot say there is no hundred-and-first way to try. That is what
// the registries below are for: they read the package's own syntax tree and the
// Store's own method set, and they fail on anything they have not been told
// about. So the behavioural suite proves what the code DOES, and this file
// proves there is nothing else to run.
//
// Keep that split in mind, because it is exactly what these registries do not
// give you: THE PROSE IN THEIR VALUES IS NOT ASSERTED. Replace the guard in
// reinforceLocked with a bare constant and tierWrites still passes — the write
// is registered, and its map value is a note for a reader rather than a check.
// Six behavioural tests fail instead, which is the intended answer; a registry
// that tried to check semantics would be a re-implementation of the code it is
// checking. The one structural claim that IS asserted is where the guard lives,
// because that is the mutation most worth catching: see
// TestOnlyReinforceLockedConsultsTheCeiling.
//
// Three registries, one per test:
//
//  1. Every write to a field named Tier, anywhere in the package's non-test
//     files, appears in tierWrites (TestEveryTierWriteIsRegistered). Three of
//     them write an ENTRY's tier; the rest copy one into a record that reports
//     it.
//  2. Exactly two functions name EventRaised, and only one BUILDS the record —
//     replayLocked reads it back and copies the tier it names rather than
//     computing one (same test, raiseMentions). Note how this closes the other
//     registry's obvious hole: a raise built with a bare string instead of the
//     constant still fails as an unregistered tier write.
//  3. Every exported method of Store is classified as reachable by an agent or
//     not, and every agent-reachable one is driven a hundred times without
//     reaching the top rung (TestNoAgentReachableCallReachesTheTopTier). This
//     one goes through reflection rather than the syntax tree, so it is
//     file-agnostic by construction.

// tierWrite is one place the package puts a number in a field named Tier: which
// function, the expression it assigns through, and the struct the field belongs
// to where the syntax names it. Composite literals are named by their type; a
// bare selector assignment cannot be, so the EXPRESSION is what identifies it.
//
// Keyed on the function name alone rather than on the file, so moving a function
// between files does not churn the registry — and so a tier write in a NEW file
// of this package lands here under a name nothing has registered, which is the
// point.
type tierWrite struct {
	Fn   string
	Expr string
	Type string
}

// tierWrites is every one of them. It is a CLOSED list, and closedness is the
// whole of what it checks: a tier written anywhere the registry does not name
// fails the test that reads it, and a registry entry nothing writes any more
// fails it too.
//
// The values say what each write is, for whoever a failure sends here. They are
// not assertions and nothing reads them — see this file's header, which states
// that limit rather than leaving it to be discovered.
//
// Three of them write an ENTRY's tier — newEntry, reinforceLocked and
// replayLocked — and those three are what I6 turns on. The rest copy a tier that
// has already been written into a record that reports it: a raised event, a fold
// note, a ranking factor. None of those can invent a rung, because none computes
// one.
var tierWrites = map[tierWrite]string{
	{Fn: "newEntry", Type: "Entry"}: "the literal 1 — every entry starts on the bottom rung",
	{Fn: "reinforceLocked", Expr: "e.Tier"}: "++ past a guard on Policy.ceiling, so it stops at " +
		"agentEarnedTier until the entry's own UserSignals reach the threshold, and at maxEarnedTier after",
	{Fn: "replayLocked", Expr: "e.Tier"}: "two writes under one key: a tier COPIED from a raised record, guarded " +
		"to 1..maxEarnedTier, and one copied from a lowered record, guarded to strictly below the rung the entry " +
		"is already on. Neither is computed, and a record outside its guard is rejected and counted, never clamped",

	{Fn: "lowerLocked", Expr: "e.Tier"}: "-- past a guard on the bottom of the ladder. It is the package's ONE " +
		"demotion and it takes no target: there is no tier for a caller to name, so the conductor cannot raise " +
		"through it (invariant I6)",
	{Fn: "lowerLocked", Type: "event"}: "the lowered record, carrying the rung the guarded -- above just landed on",

	{Fn: "reinforceLocked", Type: "event"}: "the raised record, carrying the tier the guarded ++ above just produced",
	{Fn: "foldLocked", Type: "Fold"}:       "a report of where the entry stands after the fold",
	{Fn: "reconcileLocked", Type: "Fold"}:  "the same report, for a duplicate line the pass folded",
	{Fn: "reconcileLocked", Expr: "folds[at].Tier"}: "the same report again, updated in place once the " +
		"entry it describes has moved",
	{Fn: "rankLocked", Type: "Factors"}: "the tier as a ranking multiplier — read, never written back",

	// Not a tier at all. It is here because the syntax cannot say what a pointer
	// write targets, so the walker reports every one and each has to be accounted
	// for — which is the point: this is the package's only pointer write today,
	// and a second one would have to be justified on this line before it could
	// pass. See tierWalker.
	{Fn: "alias", Expr: "*evictions"}: "the alias-eviction counter, passed by pointer so a merge and a " +
		"reword can each report what they pushed out of Entry.Aliases into the counters they commit with",
}

// ceilingCallers is every function allowed to consult Policy.ceiling, and it is
// exactly one. This is the file's only structural claim about WHERE the bound is
// applied rather than merely where a tier is written, and it earns its place
// because the cheapest way to break I6 is to leave reinforceLocked's write
// exactly where it is and swap the guard under it for a constant. tierWrites
// cannot see that; this can.
//
// It sees the CALL, not the use made of it, and the difference is worth knowing
// before leaning on it. A vestigial `_ = s.policy.ceiling(*e)` left above a
// constant guard passes here, because the name is still mentioned in the
// function the registry expects to mention it. The behavioural tests fail on
// that mutation, which is the division this whole file rests on — a registry
// says there is nothing else to run, and only running the code says what it
// does. Widening this one to chase the vestigial case would mean deciding, from
// the syntax alone, whether a value is used, which is a type checker.
var ceilingCallers = map[string]string{
	"reinforceLocked": "the one guard on the one path that raises an entry",
}

// raiseMentions is every function allowed to name EventRaised, and why. Two,
// and the asymmetry between them is the whole of step 2: one BUILDS the record
// and one READS it back. A third would mean a second way for an entry to climb.
var raiseMentions = map[string]string{
	"reinforceLocked": "builds the record — the only place an entry is raised",
	"replayLocked":    "reads it back, and copies the tier it names rather than computing one",
}

// TestEveryTierWriteIsRegistered is registries 1 and 2. It reads every non-test
// file in the package, so it holds for code no test happens to exercise and for
// code in a file that did not exist when it was written.
//
// It checks MEMBERSHIP and nothing else. What each registered write then does
// with the number is the behavioural suite's to say; see this file's header,
// which is explicit about that line.
func TestEveryTierWriteIsRegistered(t *testing.T) {
	var (
		writes  []tierWrite
		raisers = map[string]bool{}
	)
	for _, name := range packageFiles(t) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Walk(&tierWalker{fn: fd.Name.Name, writes: &writes, raisers: raisers}, fd.Body)
		}
	}

	if len(writes) == 0 {
		t.Fatal("found no tier writes at all; the walker is broken, not the code")
	}
	for _, w := range writes {
		if _, known := tierWrites[w]; !known {
			t.Errorf("%s writes a tier through %s, and tierWrites does not name it: register it with "+
				"what it writes, or route it through reinforceLocked", w.Fn, describe(w))
		}
	}
	for w := range tierWrites {
		if !slices.Contains(writes, w) {
			t.Errorf("tierWrites still claims %s writes a tier through %s; it does not any more",
				w.Fn, describe(w))
		}
	}

	for fn := range raisers {
		if _, known := raiseMentions[fn]; !known {
			t.Errorf("%s names EventRaised: an entry may be raised in one place only (reinforceLocked), "+
				"because that is where Policy.ceiling is consulted", fn)
		}
	}
	for fn := range raiseMentions {
		if !raisers[fn] {
			t.Errorf("raiseMentions still claims %s names EventRaised; it does not any more", fn)
		}
	}
}

// TestOnlyReinforceLockedConsultsTheCeiling is the one place this file asserts
// something about the bound rather than about the inventory.
//
// tierWrites registers reinforceLocked's `e.Tier++` and would go on registering
// it if the guard above it were replaced by a bare constant — a mutation that
// breaks I6 and that a registry of WRITES structurally cannot see. This sees it:
// Policy.ceiling is where agentEarnedTier and maxEarnedTier are chosen between,
// so a reinforceLocked that has stopped calling it has stopped asking whether
// the user signalled, whatever it then writes.
//
// It is deliberately a two-way check. A SECOND caller would be a second place
// the ceiling is decided, which is the same invariant lost from the other end.
func TestOnlyReinforceLockedConsultsTheCeiling(t *testing.T) {
	callers := map[string]bool{}
	for _, name := range packageFiles(t) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			// Skip ceiling's own declaration: it is the definition, not a consultation.
			if !ok || fd.Body == nil || fd.Name.Name == "ceiling" {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if se, ok := n.(*ast.SelectorExpr); ok && se.Sel.Name == "ceiling" {
					callers[fd.Name.Name] = true
				}
				return true
			})
		}
	}

	for fn := range callers {
		if _, known := ceilingCallers[fn]; !known {
			t.Errorf("%s consults Policy.ceiling: the tier ceiling is chosen in one place only, "+
				"or invariant I6 has two answers", fn)
		}
	}
	for fn := range ceilingCallers {
		if !callers[fn] {
			t.Errorf("%s no longer consults Policy.ceiling: whatever it writes to a tier is no longer "+
				"asking whether the user signalled, which is invariant I6", fn)
		}
	}
}

// packageFiles is every non-test Go file in this package's directory.
//
// The directory rather than a named file, because "score.go" was an evasion: a
// tier written from a new file of the same package was invisible to a registry
// that only ever parsed one. Build tags are deliberately ignored — the lock and
// stat files are per-GOOS, and a tier write hidden behind a tag this build does
// not compile is exactly as unregistered as any other.
func packageFiles(t *testing.T) []string {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var out []string
	for _, name := range names {
		if !strings.HasSuffix(name, "_test.go") {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		t.Fatal("no non-test files found; the test is running somewhere unexpected")
	}
	return out
}

// note records an assignment target that reaches a tier, by the field it names
// or by a pointer that could name any field at all.
func (w *tierWalker) note(lhs ast.Expr) {
	switch t := lhs.(type) {
	case *ast.SelectorExpr:
		if t.Sel.Name == "Tier" {
			*w.writes = append(*w.writes, tierWrite{Fn: w.fn, Expr: exprString(t)})
		}
	case *ast.StarExpr:
		*w.writes = append(*w.writes, tierWrite{Fn: w.fn, Expr: exprString(t)})
	}
}

func describe(w tierWrite) string {
	if w.Expr != "" {
		return w.Expr
	}
	return w.Type + "{…}"
}

// tierWalker collects tier writes and EventRaised mentions out of one function
// body. It carries elide so an ELIDED composite literal — the `{…}` inside a
// []Fold{{…}} — is attributed to the element type rather than reported as
// nameless, which is the one shape a flat inspection cannot resolve without
// type information this package will not pull in.
//
// It reports three shapes as writes, and the last two are about ESCAPE rather
// than about tiers. A write through a pointer (`*p = 3`) names no field the
// syntax can see, and taking a tier's address (`&e.Tier`) hands that pointer to
// anyone; either would let a tier be written past a registry that only watched
// `x.Tier = …`. So both are reported, under expressions no entry in tierWrites
// spells, and the test fails on them by construction. There is no such code in
// the package today, and that is the state this keeps.
type tierWalker struct {
	fn      string
	elide   string
	writes  *[]tierWrite
	raisers map[string]bool
}

func (w *tierWalker) Visit(n ast.Node) ast.Visitor {
	switch x := n.(type) {
	case *ast.Ident:
		if x.Name == "EventRaised" {
			w.raisers[w.fn] = true
		}
	case *ast.AssignStmt:
		for _, lhs := range x.Lhs {
			w.note(lhs)
		}
	case *ast.IncDecStmt:
		w.note(x.X)
	case *ast.UnaryExpr:
		// &e.Tier — the address of a tier is a write anywhere it is dereferenced,
		// and the registry cannot follow it there, so it is reported here.
		if se, ok := x.X.(*ast.SelectorExpr); ok && x.Op == token.AND && se.Sel.Name == "Tier" {
			*w.writes = append(*w.writes, tierWrite{Fn: w.fn, Expr: "&" + exprString(se)})
		}
	case *ast.CompositeLit:
		name := typeName(x.Type)
		if name == "" {
			name = w.elide
		}
		for _, el := range x.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Tier" {
				*w.writes = append(*w.writes, tierWrite{Fn: w.fn, Type: name})
			}
		}
		return &tierWalker{fn: w.fn, elide: elementName(x.Type), writes: w.writes, raisers: w.raisers}
	}
	return w
}

// typeName is the type a composite literal names, or empty when it elides it.
func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

// elementName is what an elided literal one level inside this type stands for.
func elementName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.ArrayType:
		return typeName(t.Elt)
	case *ast.MapType:
		return typeName(t.Value)
	}
	return ""
}

// exprString renders a selector chain the way the source spells it, which is
// what identifies a write no type information names. Anything else is rendered
// as its node kind, which would fail the table and say so.
func exprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.IndexExpr:
		return exprString(t.X) + "[" + exprString(t.Index) + "]"
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	}
	return fmt.Sprintf("%T", e)
}

// agentDoors is every exported Store method an agent's connection can drive with
// a source of its own, and how. #38 §4 is what makes the list this short: the
// server stamps a submission from the connection it arrived on, so an agent
// panel's every mutation carries SourceAgent and cannot claim otherwise.
//
// Each is driven a hundred times below. A method here that stops moving the
// ladder is not a problem; a method that is NOT here and can be reached by an
// agent is invariant I6 lost, which is why the classification is exhaustive
// rather than a list of the interesting ones.
var agentDoors = map[string]func(t *testing.T, s *Store, id string){
	"Submit": func(t *testing.T, s *Store, _ string) {
		t.Helper()
		if _, _, err := s.Submit("keep the build green", Provenance{Source: SourceAgent, SourcePanel: "p1"}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	},
	"Reinforce": func(t *testing.T, s *Store, id string) {
		t.Helper()
		if err := s.Reinforce(id, SourceAgent); err != nil {
			t.Fatalf("Reinforce: %v", err)
		}
	},
	// The server calls Signal only for the user's own dispatches, but the STORE
	// takes the provenance it is handed, so an agent source is a shape it must
	// hold whatever a caller does. Driving it here is what keeps I6 a property of
	// the store rather than of one careful call site.
	"Signal": func(t *testing.T, s *Store, _ string) {
		t.Helper()
		if _, _, err := s.Signal("keep the build green", Provenance{Source: SourceAgent, SourcePanel: "p1"}); err != nil {
			t.Fatalf("Signal: %v", err)
		}
	},
	// The conductor IS an agent panel, so the three corrections are doors an
	// agent's connection can reach. They are the only doors on this list the
	// caller supplies no source for — the store stamps sourceConductor itself —
	// which is a difference in who SAYS it, not in who reaches it, and I6 is about
	// the second.
	//
	// Merge is the one driven here: it is the correction that reads TWO entries,
	// and so the only one that could carry another entry's earned counts across.
	// The scratch entry it absorbs is created here and retired by the merge, so
	// the store's shape after the call is the shape before it and the assertions
	// in the test still read the entry they were handed. Reword and Lower write no
	// counter and no upward tier at all; TestRefineMovesNoCounterAndNoRank drives
	// them and checks the same claim, and closedDoors carries their reason.
	"Merge": func(t *testing.T, s *Store, id string) {
		t.Helper()
		refineScratch++
		other := submitAs(t, s, fmt.Sprintf("a scratch observation %d", refineScratch),
			Provenance{Source: SourceAgent, SourcePanel: "p1"})
		if err := s.Merge(id, other.Id); err != nil {
			t.Fatalf("Merge: %v", err)
		}
	},
}

// refineScratch numbers the throwaway entries the Merge door absorbs, so no two
// of them share a wording and fold into each other instead.
var refineScratch int

// closedDoors is every OTHER exported method, with the reason an agent cannot
// use it to climb. Together with agentDoors it must cover Store's whole exported
// surface, which is what TestEveryStoreMethodIsClassified checks — a method
// added later is unclassified until someone decides which of the two it is.
var closedDoors = map[string]string{
	"Boot":        "reports what Open's recovery pass did; writes nothing",
	"Close":       "releases the directory claim",
	"Dir":         "reports the directory",
	"DrainFolds":  "hands back fold records already made; changes no entry",
	"Explain":     "a read; its reconcile pass acts on score.md, which is the user's file",
	"Health":      "reports counters",
	"Len":         "counts entries",
	"Policy":      "reports the tuning in force",
	"Reconcile":   "folds score.md back in; every reinforcement it counts is the USER's, by definition of whose file it is",
	"Lower":       "the package's one demotion: it takes no target tier at all, so there is no rung for a caller to name",
	"Merge":       "", // in agentDoors
	"Reinforce":   "", // in agentDoors; listed here only so the two maps can be compared by name
	"Render":      "a read",
	"Reword":      "re-spells one statement: the wording changes, and no counter, tier or log position moves with it",
	"RenderBlock": "a read",
	"SetPolicy":   "retunes thresholds; #37 demotes nothing and no threshold grants a tier, only earns one",
	"Signal":      "", // in agentDoors
	"Submit":      "", // in agentDoors
	"Unlocked":    "reports the single-writer claim",
	"View":        "a read, like Explain",
}

// TestEveryStoreMethodIsClassified is what keeps the proof below exhaustive. The
// hundred-submission check is only as good as the claim that there is nothing
// else to submit through, and nothing but this test holds that claim.
func TestEveryStoreMethodIsClassified(t *testing.T) {
	typ := reflect.TypeOf((*Store)(nil))
	for i := range typ.NumMethod() {
		name := typ.Method(i).Name
		_, open := agentDoors[name]
		_, closed := closedDoors[name]
		if !open && !closed {
			t.Errorf("Store.%s is unclassified: add it to agentDoors if an agent's connection can drive it "+
				"with a source of its own, and to closedDoors with the reason if it cannot", name)
		}
	}
	for name := range closedDoors {
		if _, ok := typ.MethodByName(name); !ok {
			t.Errorf("closedDoors names Store.%s, which no longer exists", name)
		}
	}
	for name := range agentDoors {
		if _, ok := closedDoors[name]; !ok {
			t.Errorf("agentDoors names Store.%s but closedDoors does not; keep both lists over the same names", name)
		}
	}
}

// TestNoAgentReachableCallReachesTheTopTier is step 3, and #38's verification
// check 5 taken past the one call it names: every door an agent has, driven a
// hundred times each, under three policies.
//
// The policies matter, because both thresholds are the operator's and an agent
// that cannot climb under the defaults must not be able to climb under the
// numbers an eager operator would reach for. So the run includes the smallest
// promote-at the clamp accepts, beside both ends of the user-signal threshold:
// the smallest, where the fewest manufactured signals would be needed to slip
// past it, and a large one, where the ceiling stays shut on any count at all.
func TestNoAgentReachableCallReachesTheTopTier(t *testing.T) {
	for _, p := range []Policy{{}, {PromoteAt: 2, UserSignalsAt: 2}, {PromoteAt: 2, UserSignalsAt: 100}} {
		t.Run(fmt.Sprintf("promote_at=%d/user_signals_at=%d", p.PromoteAt, p.UserSignalsAt), func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir, p)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(s.Close)

			e := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent, SourcePanel: "p1"})
			for name, drive := range agentDoors {
				for range 100 {
					drive(t, s, e.Id)
				}
				got := s.Render(Context{})[0]
				if got.Tier > agentEarnedTier {
					t.Fatalf("a hundred calls to %s reached tier %d, want no more than %d", name, got.Tier, agentEarnedTier)
				}
				if got.UserSignals != 0 {
					t.Fatalf("%s recorded %d user signals; nothing an agent does is one", name, got.UserSignals)
				}
			}

			// And the log carries no raise the ladder does not allow, so a restart
			// on another machine cannot land higher than this one did.
			for _, ev := range events(t, dir) {
				if ev.Event == EventRaised && ev.Tier > agentEarnedTier {
					t.Fatalf("the log raised %s to tier %d on agent traffic alone", ev.Id, ev.Tier)
				}
			}
			s.Close()
			again := openStore(t, dir)
			if got := again.Render(Context{})[0].Tier; got > agentEarnedTier {
				t.Fatalf("replay reached tier %d on a log of agent traffic, want no more than %d", got, agentEarnedTier)
			}
			if got := again.Health().RejectedTiers; got != 0 {
				t.Fatalf("rejected tier records = %d, want none: this log was written by this build", got)
			}
		})
	}
}

// TestTheLadderIsDeterministic is invariant I1 over the rung R4 added: the same
// sequence of sources, replayed on a second store, yields the same tiers and the
// same user-signal counts. The user signal is a COUNT rebuilt from the log's
// records rather than a flag carried in the snapshot, so this is the assertion
// that the two ways of arriving at it agree.
func TestTheLadderIsDeterministic(t *testing.T) {
	sources := []string{SourceAgent, SourceUser, SourceAgent, SourceUser, SourceUser, SourceAgent}

	run := func(t *testing.T) (dir string, tier, signals, reinforcements int) {
		t.Helper()
		dir = t.TempDir()
		s := openStore(t, dir)
		e := submitAs(t, s, "keep the build green", Provenance{Source: SourceAgent})
		for _, src := range sources {
			if err := s.Reinforce(e.Id, src); err != nil {
				t.Fatalf("Reinforce(%s): %v", src, err)
			}
		}
		got := s.Render(Context{})[0]
		s.Close()
		return dir, got.Tier, got.UserSignals, got.Reinforcements
	}

	dirA, tierA, signalsA, reinfA := run(t)
	_, tierB, signalsB, reinfB := run(t)
	if tierA != tierB || signalsA != signalsB || reinfA != reinfB {
		t.Fatalf("two runs of the same sources diverged: (%d,%d,%d) vs (%d,%d,%d)",
			tierA, signalsA, reinfA, tierB, signalsB, reinfB)
	}
	// Three user signals out of six reinforcements, which is what the sequence
	// says — computed here from the sequence rather than written down, so the
	// figure cannot drift from the input it describes.
	wantSignals := 0
	for _, src := range sources {
		if src == SourceUser {
			wantSignals++
		}
	}
	if signalsA != wantSignals {
		t.Fatalf("user signals = %d over %v, want %d", signalsA, sources, wantSignals)
	}
	if tierA != maxEarnedTier {
		t.Fatalf("tier = %d, want %d: %d user signals clear a threshold of %d",
			tierA, maxEarnedTier, wantSignals, defaultUserSignalsAt)
	}

	// The replay of the first run's own log lands on the same three numbers,
	// which is what makes a restart invisible to the ladder.
	replayed := openStore(t, dirA).Render(Context{})[0]
	if replayed.Tier != tierA || replayed.UserSignals != signalsA || replayed.Reinforcements != reinfA {
		t.Fatalf("replay = %+v, want tier %d with %d user signals and %d reinforcements",
			replayed, tierA, signalsA, reinfA)
	}
}

// TestSourceNamesAreWhatTheWireCarries keeps the two exported source constants
// spelled the way #38 §3's log and every existing record spell them. They are
// persisted in score-events.jsonl and compared against on replay, so renaming
// one silently re-reads every old log as "not the user".
func TestSourceNamesAreWhatTheWireCarries(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{SourceUser, "user"},
		{SourceAgent, "agent"},
		{sourceRecovery, "recovery"},
	} {
		if tc.got != tc.want {
			t.Errorf("source constant = %q, want %q — the log on disk spells it that way", tc.got, tc.want)
		}
	}
	if strings.EqualFold(SourceUser, SourceAgent) {
		t.Fatal("the two sources must be distinguishable")
	}
}
