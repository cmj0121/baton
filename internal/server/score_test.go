package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
	"github.com/cmj0121/baton/internal/score"
	"github.com/cmj0121/baton/internal/task"
)

// scoreServer builds a socketless Server around st with one idle agent panel,
// capturing every byte dispatch delivers. White-box on purpose: the seam under
// test is the byte stream handed to the PTY, which the wire tests cannot see.
// (No unix sockets here at all, so the ~104-byte path cap is moot.)
func scoreServer(st *score.Store) (*Server, *[]byte) {
	mo, _ := newTestMonitor()
	var delivered []byte
	s := &Server{
		pty:     ptymgr.New(),
		clients: map[*clientConn]struct{}{},
		mon:     mo,
		panels: []panel.Panel{{
			ID: "p1", Kind: panel.Agent, Title: "claude #1", State: panel.Idle,
			Group: "auth", Cwd: "/work/auth",
		}},
		specs:           map[string]spawnSpec{"p1": {Profile: "claude"}},
		pendingDispatch: map[string][]byte{},
		tasks:           map[string]*task.Task{},
		panelTask:       map[string]string{},
		spawning:        map[string]bool{},
		taskDirty:       make(chan string, 8),
		dirty:           make(chan struct{}, 1),
	}
	// Through the real option rather than by setting the fields: a test store
	// stands for a daemon whose config said yes and whose Open succeeded, and the
	// option is what keeps the store and the knob from disagreeing.
	WithScore(ScoreState{Store: st, Enabled: st != nil})(s)
	s.writeInput = func(id string, data []byte) { delivered = append(delivered, data...) }
	return s, &delivered
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

// TestDirectDispatchInjectsScore checks the one injection path (#39): a direct
// panel.dispatch renders the score block into the DELIVERED bytes ahead of the
// prompt, hands the task.pre filter the panel's full context, and still records
// the bare prompt as the panel's brief — cards and restarts never carry the block.
func TestDirectDispatchInjectsScore(t *testing.T) {
	st, _ := scoreStore(t) // a fresh store holds no entries
	if _, _, err := st.Submit("prefer table-driven tests", score.Provenance{Source: "user"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, delivered := scoreServer(st)

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
	s, delivered := scoreServer(nil)

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "panel.dispatch", ID: "p1", Prompt: "fix the login flow"})
	noError(t, cc)

	if got, want := *delivered, dispatchData("fix the login flow", ""); string(got) != string(want) {
		t.Fatalf("nil store must deliver the bare prompt, got %q want %q", got, want)
	}
}

// TestGroupDispatchCarriesNoScore checks that fan-out stays uninjected in S0
// (per-member delivery is R5): the filter sees an empty-context brief and the
// member receives the bare prompt even with a live store holding entries.
func TestGroupDispatchCarriesNoScore(t *testing.T) {
	st, _ := scoreStore(t)
	s, delivered := scoreServer(st)

	var seen TaskBrief
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) { seen = b; return b, true }

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "panel.dispatch-group", Group: "auth", Prompt: "race it"})
	noError(t, cc)

	if seen.Score != "" || seen.Panel != "" || seen.Cwd != "" || seen.Profile != "" {
		t.Fatalf("fan-out brief must ride empty in S0: %+v", seen)
	}
	if got := string(*delivered); strings.Contains(got, "── Score ──") {
		t.Fatalf("fan-out delivery must not carry the block, got %q", got)
	}
}

// TestEnqueueBriefCarriesNoScore checks the queued path: task.enqueue hands the
// filter an empty-context brief — a queued task carries no score by design (R5).
func TestEnqueueBriefCarriesNoScore(t *testing.T) {
	st, _ := scoreStore(t)
	s, _ := scoreServer(st)

	var seen TaskBrief
	s.onFilterTask = func(b TaskBrief) (TaskBrief, bool) { seen = b; return b, true }

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "task.enqueue", Group: "auth", Prompt: "later"})
	noError(t, cc)

	if seen.Prompt != "later" || seen.Group != "auth" {
		t.Fatalf("enqueue brief lost its prompt/group: %+v", seen)
	}
	if seen.Score != "" || seen.Panel != "" || seen.Cwd != "" || seen.Profile != "" {
		t.Fatalf("enqueue brief must ride empty in S0: %+v", seen)
	}
}

// TestScoreSubmitProvenance checks the #38 §4 stamping: a connection that
// declared a self submits as that agent panel (id, profile, cwd joined from the
// fleet), while a self-less cockpit submits as the user.
func TestScoreSubmitProvenance(t *testing.T) {
	st, _ := scoreStore(t)
	s, _ := scoreServer(st)

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
	s, _ := scoreServer(st)

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
	s, _ := scoreServer(nil)

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
			PromoteAt: 3, WorkingSet: 7,
			Rank: score.Rank{Recency: 2, Cwd: 2, Profile: 2, Group: 2}, Dir: dir,
		}, 1},
		{"disabled", nil, statusReply{Reason: "score is disabled"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := scoreServer(tc.st)

			if entries := listed(t, s, "").Entries; len(entries) != tc.listed {
				t.Fatalf("want %d listed entries, got %+v", tc.listed, entries)
			}

			if got := status(t, s); got != tc.want {
				t.Fatalf("status %+v, want %+v", got, tc.want)
			}
		})
	}
}
