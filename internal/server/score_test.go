package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
	"github.com/cmj0121/baton/internal/task"
)

// scoreServer builds a socketless Server around st with one idle agent panel,
// capturing every byte dispatch delivers. White-box on purpose: the seam under
// test is the byte stream handed to the PTY, which the wire tests cannot see.
// (No unix sockets here at all, so the ~104-byte path cap is moot.)
//
// It is gateServer plus a store and a byte collector, rather than a second
// Server literal beside it. The two had drifted into the same thing: score tests
// drive monitorTick now, so every map gateServer already fills had to be added
// here one failure at a time, and the injected clock arrived with them.
func scoreServer(st *score.Store) (*Server, *fakeClock, *[]byte) {
	s, clk, _ := gateServer(panel.Panel{
		ID: "p1", Kind: panel.Agent, Title: "claude #1", State: panel.Idle,
		Group: "auth", Cwd: "/work/auth",
	})
	s.specs["p1"] = spawnSpec{Profile: "claude"}
	s.taskDirty = make(chan string, 8)
	s.dirty = make(chan struct{}, 1)
	// Through the real option rather than by setting the fields: a test store
	// stands for a daemon whose config said yes and whose Open succeeded, and the
	// option is what keeps the store and the knob from disagreeing.
	WithScore(ScoreState{Store: st, Enabled: st != nil})(s)
	var delivered []byte
	s.writeInput = func(id string, data []byte) { delivered = append(delivered, data...) }
	return s, clk, &delivered
}

// conn is a throwaway client connection whose replies the test can drain.
func conn(self string) *clientConn {
	return &clientConn{out: make(chan proto.ServerMsg, 8), self: self}
}

// reply pops the next queued message, failing the test when there is none.
func reply(t *testing.T, cc *clientConn) proto.ServerMsg {
	t.Helper()
	select {
	case msg := <-cc.out:
		return msg
	default:
		t.Fatal("no reply queued")
		return proto.ServerMsg{}
	}
}

// noError fails the test when an error reply is queued — success on the
// dispatch path answers with a fleet broadcast, not a direct reply.
func noError(t *testing.T, cc *clientConn) {
	t.Helper()
	select {
	case msg := <-cc.out:
		t.Fatalf("unexpected reply: %+v", msg)
	default:
	}
}

// TestDirectDispatchInjectsScore checks the shape every injection path shares
// (#39): a dispatch renders the score block into the DELIVERED bytes ahead of
// the prompt, hands the task.pre filter the panel's full context, and still
// records the bare prompt as the panel's brief — cards and restarts never carry
// the block.
func TestDirectDispatchInjectsScore(t *testing.T) {
	st, _ := scoreStore(t) // a fresh store holds no entries
	if _, _, err := st.Submit("prefer table-driven tests", score.Provenance{Source: "user"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, _, delivered := scoreServer(st)

	var seen TaskBrief
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) { seen = b; return b, true }

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "panel.dispatch", ID: "p1", Prompt: "fix the login flow"})
	noError(t, cc)

	got := string(*delivered)
	if !strings.HasPrefix(got, "── Score ──\n") {
		t.Fatalf("delivered bytes must lead with the score block, got %q", got)
	}
	if !strings.Contains(got, "prefer table-driven tests") {
		t.Fatalf("delivered bytes must carry the real entry, got %q", got)
	}
	if strings.Contains(got, "#") {
		t.Fatalf("the seeded score.md header must never reach a brief, got %q", got)
	}
	if !strings.Contains(got, "\n\nfix the login flow") {
		t.Fatalf("prompt must follow the block after a blank line, got %q", got)
	}
	if s.panels[0].Task != "fix the login flow" {
		t.Fatalf("recorded brief must stay the bare prompt, got %q", s.panels[0].Task)
	}
	want := TaskBrief{Panel: "p1", Group: "auth", Cwd: "/work/auth", Profile: "claude"}
	if seen.Panel != want.Panel || seen.Group != want.Group || seen.Cwd != want.Cwd || seen.Profile != want.Profile {
		t.Fatalf("filter brief missing the panel context: %+v", seen)
	}
	if seen.Score == "" {
		t.Fatal("filter brief must carry the rendered score block")
	}
}

// TestDirectDispatchNilStore checks the disabled contract: with no store wired
// the dispatch neither errors nor injects — the delivered bytes are exactly the
// plain prompt plus the submit sequence.
func TestDirectDispatchNilStore(t *testing.T) {
	s, _, delivered := scoreServer(nil)

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "panel.dispatch", ID: "p1", Prompt: "fix the login flow"})
	noError(t, cc)

	if got, want := *delivered, dispatchData("fix the login flow", ""); string(got) != string(want) {
		t.Fatalf("nil store must deliver the bare prompt, got %q want %q", got, want)
	}
}

// scoreFleet is scoreServer with a second agent beside the first, in the same
// group but its own directory and profile — the smallest fleet a fan-out can
// tell apart. It records what each member received rather than one concatenated
// stream, because "per member" is the whole assertion.
func scoreFleet(st *score.Store) (*Server, *fakeClock, map[string]string) {
	s, clk, _ := scoreServer(st)
	s.panels = append(s.panels, panel.Panel{
		ID: "p2", Kind: panel.Agent, Title: "codex #1", State: panel.Idle,
		Group: "auth", Cwd: "/work/api",
	})
	s.specs["p2"] = spawnSpec{Profile: "codex"}
	got := map[string]string{}
	s.writeInput = func(id string, data []byte) { got[id] += string(data) }
	return s, clk, got
}

// TestGroupDispatchBindsEachMember is the fan-out half of #44. One prompt racing
// two agents is two deliveries, not one, so each member's brief is bound to the
// panel it lands on: its own cwd and profile in the hook table, and a score
// block ranked against that panel rather than against whichever member happened
// to come first.
func TestGroupDispatchBindsEachMember(t *testing.T) {
	st, _ := scoreStore(t)
	if _, _, err := st.Submit("prefer table-driven tests", score.Provenance{Source: score.SourceUser}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, _, got := scoreFleet(st)

	seen := map[string]TaskBrief{}
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) { seen[b.Panel] = b; return b, true }

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "panel.dispatch-group", Group: "auth", Prompt: "race it"})
	noError(t, cc)

	for _, want := range []TaskBrief{
		{Panel: "p1", Group: "auth", Cwd: "/work/auth", Profile: "claude"},
		{Panel: "p2", Group: "auth", Cwd: "/work/api", Profile: "codex"},
	} {
		b, ok := seen[want.Panel]
		if !ok {
			t.Fatalf("no brief for %s; the chain saw %+v", want.Panel, seen)
		}
		if b.Cwd != want.Cwd || b.Profile != want.Profile || b.Group != want.Group {
			t.Fatalf("brief for %s = %+v, want it bound to that member", want.Panel, b)
		}
		if b.Score == "" {
			t.Fatalf("brief for %s carries no score block", want.Panel)
		}
		if !strings.Contains(got[want.Panel], "prefer table-driven tests") {
			t.Fatalf("%s received %q, want the block ahead of the prompt", want.Panel, got[want.Panel])
		}
	}
}

// TestGroupDispatchDropsOnlyTheVetoedMember pins the veto's reach. A fan-out is
// N deliveries, so a hook that refuses one panel refuses that panel — the rest
// of the race still runs. Only a hook that refuses every member fails the
// command, which is what a single-member group did when the chain ran once for
// the whole fan-out.
func TestGroupDispatchDropsOnlyTheVetoedMember(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, got := scoreFleet(st)
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) { return b, b.Panel != "p1" }

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "panel.dispatch-group", Group: "auth", Prompt: "race it"})

	// The caller is TOLD. A partial veto succeeds, so without the notice seven of
	// ten members refused reads exactly like ten delivered.
	if msg := reply(t, cc); msg.Type != "notice" || !strings.Contains(msg.Notice, "1 of 2") {
		t.Fatalf("reply = %+v, want a notice counting what the fan-out reached", msg)
	}

	if _, sent := got["p1"]; sent {
		t.Fatalf("the vetoed member received %q", got["p1"])
	}
	if got["p2"] != "race it\n" {
		t.Fatalf("p2 received %q, want the fan-out to have reached it anyway", got["p2"])
	}

	// And with every member refused there is nobody left to reach, so the command
	// answers the caller rather than reporting a race that never started.
	s.onFilterTask = func(TaskBrief) (TaskBrief, bool) { return TaskBrief{}, false }
	cc = conn("")
	s.onCommand(cc, proto.Command{Action: "panel.dispatch-group", Group: "auth", Prompt: "race it"})
	if msg := reply(t, cc); msg.Type != "error" {
		t.Fatalf("a wholly vetoed fan-out replied %+v, want an error", msg)
	}
}

// TestTheFanoutBudgetFailsClosed is the whole of what the budget is allowed to
// do. The chain runs per member on the caller's goroutine, so a wide group needs
// a ceiling; but the budget is cumulative across the group, which means the
// members past it were never ASKED — unlike the plugin's own per-invocation
// fail-open, where a hook was asked and did not answer in time. Dispatching them
// anyway would send work no hook examined, and a hook exists to refuse.
//
// So: reached fewer panels, said so, and delivered nothing unfiltered. Three
// assertions carry that, and each pins a different half.
//
// The panels REACHED are what catch a fail-open regression: a member past the
// budget would receive bytes. The hook COUNT catches the opposite mistake, a
// budget that never engages — without it the chain runs for every member, so a
// count of one is what says the ceiling did something. It does not distinguish
// fail-open from fail-closed; the same one hook runs either way.
//
// The notice is the third, and it is the only one that can speak to the
// operator's next move. A member left unreached is unreached whichever way it
// happened, and only the words say whether a hook decided that or whether nobody
// looked — and only the panel id says which panel to go and reach. The cut falls
// in the same place on every attempt, so a count alone would be an instruction to
// loop.
func TestTheFanoutBudgetFailsClosed(t *testing.T) {
	st, _ := scoreStore(t)
	s, clk, got := scoreFleet(st)
	// The real option, and the monitor's clock: the first member's hook advances
	// time past the whole group's budget, so the cut is exact rather than a race
	// between a sleep and a scheduler.
	WithFanoutFilterBudget(time.Second)(s)

	asked := 0
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) {
		asked++
		clk.add(2 * time.Second) // the first member alone spends the group's budget
		return b, true
	}

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "panel.dispatch-group", Group: "auth", Prompt: "race it"})

	if asked != 1 {
		t.Fatalf("the chain was run %d times, want it to stop at the budget", asked)
	}
	if len(got) != 1 {
		t.Fatalf("panels reached = %v, want only the member that was filtered", got)
	}
	if got["p1"] == "" {
		t.Fatalf("panels reached = %v, want the filtered member to have been dispatched", got)
	}
	msg := reply(t, cc)
	if msg.Type != "notice" || !strings.Contains(msg.Notice, "not filtered in time") {
		t.Fatalf("reply = %+v, want a notice saying the rest were not dispatched", msg)
	}
	if !strings.Contains(msg.Notice, "1 of 2") {
		t.Fatalf("notice = %q, want it to count what the fan-out reached", msg.Notice)
	}
	// And NAMES them. The cut falls in the same place on every attempt, so a
	// notice that only counted them would send the operator round a loop that
	// re-delivers to p1 for ever and never reaches p2.
	if !strings.Contains(msg.Notice, "p2") {
		t.Fatalf("notice = %q, want the panel that went unreached named", msg.Notice)
	}
	// A veto reads differently, because the operator's move differs: a refusal is
	// a decision that a retry will make again, a skip is one nobody has made yet.
	if strings.Contains(msg.Notice, "refused") {
		t.Fatalf("notice = %q, want a spent budget not reported as a veto", msg.Notice)
	}
}

// TestAnUnknownPanelIsNeverRenderedFor is #38 §2's standing rule, and it earns a
// test now that every delivery binds through one builder: a queued task's panel
// can leave the fleet between the assignment and the write, so "the id names no
// panel" is reachable from more than a mistyped dispatch. Every context factor
// reads 1.0 against a panel that is not there, so a block rendered for one would
// be the contextless ranking wearing that panel's name.
func TestAnUnknownPanelIsNeverRenderedFor(t *testing.T) {
	st, _ := scoreStore(t)
	if _, _, err := st.Submit("prefer table-driven tests", score.Provenance{Source: score.SourceUser}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, _, _ := scoreServer(st)

	if b := s.dispatchBrief("nosuchpanel", "fix the login flow"); b.Score != "" {
		t.Fatalf("brief for an absent panel = %+v, want no block rendered for it", b)
	}
	// And the entry really is renderable, so the check above is the rule and not
	// an empty store.
	if b := s.dispatchBrief("p1", "fix the login flow"); b.Score == "" {
		t.Fatal("the same store rendered nothing for a real panel, so nothing was tested")
	}
}

// TestEnqueueConsultsNoHook is the score half of the move: task.enqueue reaches
// the store for nothing at all now, because there is no panel to render against
// and no hook to hand a block to. The pass happens when the scheduler drains the
// task; see TestEnqueueDefersTheFilterToDelivery for where it lands.
func TestEnqueueConsultsNoHook(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)

	var seen []TaskBrief
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) { seen = append(seen, b); return b, true }

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "task.enqueue", Group: "auth", Prompt: "later"})
	noError(t, cc)

	if len(seen) != 0 {
		t.Fatalf("task.enqueue handed the filter %+v, want nothing until delivery", seen)
	}
	if got := taskPrompts(s); len(got) != 1 || got[0] != "later" {
		t.Fatalf("backlog = %v, want the one queued prompt", got)
	}
}

// TestAQueuedTaskCarriesTheSameBlockAsADirectDispatch is #44's done-when, checked
// the only way that settles it: the bytes a queued task delivers onto a panel are
// compared against the bytes a direct dispatch to that same panel produces, and
// the two hook tables are compared field for field. One brief builder serves both
// paths, so the equality is structural rather than a coincidence the next change
// could break quietly.
//
// Both dispatches arrive on an AGENT connection. A cockpit's brief would record a
// user signal on the first of the two and leave the second ranking against a
// store the test itself had moved (#38 §4); an agent's brief counts for nothing,
// so the memory the two paths see is the same memory.
func TestAQueuedTaskCarriesTheSameBlockAsADirectDispatch(t *testing.T) {
	st, _ := scoreStore(t)
	if _, _, err := st.Submit("prefer table-driven tests", score.Provenance{Source: score.SourceUser}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	direct, _, directBytes := scoreServer(st)
	var seenDirect TaskBrief
	direct.onFilterTask = func(b TaskBrief) (TaskBrief, bool) { seenDirect = b; return b, true }
	cc := conn("p1")
	direct.onCommand(cc, proto.Command{Action: "panel.dispatch", ID: "p1", Prompt: "fix the login flow"})
	noError(t, cc)

	queued, _, queuedBytes := scoreServer(st)
	var seenQueued TaskBrief
	queued.onFilterTask = func(b TaskBrief) (TaskBrief, bool) { seenQueued = b; return b, true }
	cc = conn("p1")
	queued.onCommand(cc, proto.Command{Action: "task.enqueue", Prompt: "fix the login flow"})
	noError(t, cc)
	queued.monitorTick() // the scheduler drains it onto p1 and the monitor delivers

	if seenDirect.Score == "" {
		t.Fatal("the direct dispatch carried no block, so the comparison is vacuous")
	}
	if seenQueued != seenDirect {
		t.Fatalf("hook table at a queued delivery = %+v, want the direct dispatch's %+v", seenQueued, seenDirect)
	}
	if got, want := string(*queuedBytes), string(*directBytes); got != want {
		t.Fatalf("queued delivery sent %q, want the direct dispatch's %q", got, want)
	}
	if !strings.Contains(string(*queuedBytes), "prefer table-driven tests") {
		t.Fatalf("queued delivery sent %q, want the entry in it", string(*queuedBytes))
	}
}

// TestASpawnOnDemandTaskScoresAtItsHeldDelivery covers the one delivery that is
// neither immediate nor scheduled: a spawn-on-demand task provisions its panel
// and then waits, because a panel that has not settled cannot take a prompt. Its
// brief is bound when the monitor finally releases it — against the panel that
// was created for it, which did not exist when the task was queued.
func TestASpawnOnDemandTaskScoresAtItsHeldDelivery(t *testing.T) {
	st, _ := scoreStore(t)
	if _, _, err := st.Submit("prefer table-driven tests", score.Provenance{Source: score.SourceUser}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, clk, written := gateServer() // no standing agent, so the task provisions its own
	WithScore(ScoreState{Store: st, Enabled: true})(s)
	var seen TaskBrief
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) { seen = b; return b, true }

	if _, err := s.enqueueTask("hi", "", &task.SpawnSpec{Command: "cat"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	_, spawns := schedule(s)
	if len(spawns) != 1 {
		t.Fatalf("expected one spawn request, got %d", len(spawns))
	}
	if !s.applyScheduledSpawns(spawns) {
		t.Fatal("provisioning a panel should report a fleet change")
	}
	pid := s.panels[0].ID
	if seen != (TaskBrief{}) {
		t.Fatalf("provisioning bound the brief early: %+v", seen)
	}

	clk.add(idleAfter) // the fresh panel goes quiet
	s.monitorTick()

	if seen.Panel != pid {
		t.Fatalf("brief = %+v, want it bound to the panel provisioned for the task", seen)
	}
	if seen.Score == "" || len(*written) != 1 || !strings.Contains((*written)[0], "prefer table-driven tests") {
		t.Fatalf("brief = %+v delivered %v, want the block rendered at the held delivery", seen, *written)
	}
	_ = s.closePanel(pid) // reap the real process
}

// taskPrompts lists what the backlog is holding, in no particular order.
func taskPrompts(s *Server) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t.Prompt)
	}
	return out
}

// TestScoreSubmitProvenance checks the #38 §4 stamping: a connection that
// declared a self submits as that agent panel (id, profile, cwd joined from the
// fleet), while a self-less cockpit submits as the user.
func TestScoreSubmitProvenance(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)

	find := func(id string) score.Entry {
		for _, e := range st.Render(score.Context{}) {
			if e.Id == id {
				return e
			}
		}
		t.Fatalf("no entry %q in the store", id)
		return score.Entry{}
	}
	submit := func(cc *clientConn, text string) string {
		s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: text})
		msg := reply(t, cc)
		if msg.Type != "score" {
			t.Fatalf("want a score reply, got %+v", msg)
		}
		var out struct {
			Id     string `json:"id"`
			Folded bool   `json:"folded"`
		}
		if err := json.Unmarshal(msg.Score, &out); err != nil || out.Id == "" {
			t.Fatalf("reply must carry the entry id: %s (%v)", msg.Score, err)
		}
		if out.Folded {
			t.Fatalf("a first submission was reported as a fold: %s", msg.Score)
		}
		return out.Id
	}

	agent := find(submit(conn("p1"), "always run make lint"))
	want := score.Provenance{
		Source: "agent", SourcePanel: "p1",
		SourceProfile: "claude", SourceCwd: "/work/auth", SourceGroup: "auth",
	}
	if agent.Provenance != want {
		t.Fatalf("agent provenance mis-stamped: %+v", agent.Provenance)
	}

	user := find(submit(conn(""), "ship on fridays never"))
	if want := (score.Provenance{Source: "user"}); user.Provenance != want {
		t.Fatalf("user provenance mis-stamped: %+v", user.Provenance)
	}
}

// TestScoreSubmitReportsAFold is #38's "new or folded into id" on the wire. Four
// identical submissions answer with one id, so without the flag the caller
// cannot tell the submission that created the entry from the three that were
// counted into it — and an agent never learns the fleet already knew.
func TestScoreSubmitReportsAFold(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)

	var ids []string
	var folds []bool
	for range 4 {
		cc := conn("p1")
		s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: "always run make lint"})
		msg := reply(t, cc)
		var out struct {
			Id     string `json:"id"`
			Folded bool   `json:"folded"`
		}
		if msg.Type != "score" || json.Unmarshal(msg.Score, &out) != nil {
			t.Fatalf("want a score reply, got %+v", msg)
		}
		ids, folds = append(ids, out.Id), append(folds, out.Folded)
	}

	for i, id := range ids {
		if id != ids[0] {
			t.Fatalf("submission %d landed in %q, want the one entry %q", i, id, ids[0])
		}
	}
	if want := []bool{false, true, true, true}; !reflect.DeepEqual(folds, want) {
		t.Fatalf("folded = %v, want %v — the first records, the rest fold", folds, want)
	}
	if st.Len() != 1 {
		t.Fatalf("entries = %d, want the repeats folded", st.Len())
	}

	// A submission that folded leaves score.md untouched, so it is the mutation
	// an operator cannot see by looking — which is why the store records it on
	// the same buffer a folded LINE lands on, and why the next view is where
	// either becomes #38's one log line per fold. Draining them through the
	// server is what proves there is a single producer.
	s.onCommand(conn(""), proto.Command{Action: "score.list"})
	v, err := st.View(score.Context{})
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if len(v.Folds) != 0 {
		t.Fatalf("folds = %+v, want the view that logged them to have drained them", v.Folds)
	}

	// One more, so the record itself can be read rather than inferred: a
	// submission fold names the panel that repeated the wording, and claims no
	// removal, because no line ever existed to remove.
	s.onCommand(conn("p2"), proto.Command{Action: "score.submit", Prompt: "Always run make lint!"})
	if v, err = st.View(score.Context{}); err != nil {
		t.Fatalf("View: %v", err)
	}
	if len(v.Folds) != 1 {
		t.Fatalf("folds = %+v, want the one fold that submission made", v.Folds)
	}
	switch f := v.Folds[0]; {
	case f.Id != ids[0], f.Text != "always run make lint", f.Repeat != "Always run make lint!":
		t.Fatalf("fold = %+v, want the surviving wording beside the repeat", f)
	case !f.Counted, f.FromFile, f.Removed:
		t.Fatalf("fold = %+v, want a counted submission fold that removed nothing", f)
	case f.Prov.Source != "agent", f.Prov.SourcePanel != "p2":
		t.Fatalf("fold = %+v, want the panel that repeated it", f)
	}
}

// TestScoreSubmitDisabled checks the refusal: with no store the submission is
// turned away plainly, and nothing pretends to have recorded it.
func TestScoreSubmitDisabled(t *testing.T) {
	s, _, _ := scoreServer(nil)

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: "remember me"})
	msg := reply(t, cc)
	if msg.Type != "error" || !strings.Contains(msg.Error, "score is disabled") {
		t.Fatalf("want a plain disabled refusal, got %+v", msg)
	}
}

// TestScoreListAndStatus checks both read verbs' reply shapes, enabled and
// disabled: list is always a JSON array, status reports
// enabled/entries/rendered/dir honestly in each state. The two counts are both
// reported so status can explain its own gap when the working-set budget bites.
func TestScoreListAndStatus(t *testing.T) {
	st, dir := scoreStore(t)
	if _, _, err := st.Submit("one real entry", score.Provenance{Source: "user"}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	cases := []struct {
		name   string
		st     *score.Store
		want   statusReply
		listed int // entries score.list returns
	}{
		{"enabled", st, statusReply{
			Enabled: true, Available: true, Entries: 1, Rendered: 1,
			PromoteAt: 3, UserSignalsAt: 2, WorkingSet: 7,
			Rank: score.Rank{Recency: 2, Cwd: 2, Profile: 2, Group: 2}, Dir: dir,
		}, 1},
		{"disabled", nil, statusReply{Reason: "score is disabled"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := scoreServer(tc.st)

			if entries := listed(t, s, "").Entries; len(entries) != tc.listed {
				t.Fatalf("want %d listed entries, got %+v", tc.listed, entries)
			}

			if got := status(t, s); got != tc.want {
				t.Fatalf("status %+v, want %+v", got, tc.want)
			}
		})
	}
}
