package server

import (
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/attn"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
	"github.com/cmj0121/baton/internal/restart"
	"github.com/cmj0121/baton/internal/task"
)

// The tests below are the REFUSALS. A third panel kind is only worth having if
// the surfaces that must not accept it demonstrably do not, so each one here
// fails if a command panel is ever treated as an agent again — which is #54's
// defect, and the shape it would come back in.

// TestCreateCommandPanel drives the spawn: the panel records the new kind, takes
// the agent's "<command> · <workdir>" title (the two facts that identify it), and
// carries NO identity env — a plain binary drives nothing, so being told which
// panel it is grants it nothing it could use.
func TestCreateCommandPanel(t *testing.T) {
	s, _, _ := gateServer()
	id, err := s.createPanel(originOperator, proto.KindCommand, "cat", nil, t.TempDir(), "", false, false)
	if err != nil {
		t.Fatalf("spawning a command panel: %v", err)
	}
	defer func() { _ = s.closePanel(id) }()

	i := s.indexLocked(id)
	if i < 0 {
		t.Fatalf("the command panel is not in the fleet")
	}
	p := s.panels[i]
	if !p.IsCommand() {
		t.Fatalf("kind = %v, want Command", p.Kind)
	}
	if p.IsAgent() {
		t.Fatal("a command panel must not report as an agent")
	}
	if !strings.HasPrefix(p.Title, "cat · ") {
		t.Errorf("title = %q, want the agent form %q", p.Title, "cat · <workdir>")
	}
	for _, e := range s.specs[id].Env {
		if strings.HasPrefix(e, "BATON_PANEL_ID") {
			t.Errorf("a command panel was handed the identity env: %q", e)
		}
	}
}

// TestCreateCommandPanelNeedsACommand: there is no default binary to fall back
// on, so an empty command is refused rather than silently spawning a shell —
// which is the misread the kind exists to prevent.
func TestCreateCommandPanelNeedsACommand(t *testing.T) {
	s, _, _ := gateServer()
	if _, err := s.createPanel(originOperator, proto.KindCommand, "", nil, t.TempDir(), "", false, false); err == nil {
		t.Fatal("a command panel with no command should be refused")
	}
	if len(s.panels) != 0 {
		t.Fatalf("a refused spawn must leave no panel behind, got %d", len(s.panels))
	}
}

// TestSchedulerNeverPicksACommandPanel is the refusal the issue opens with. The
// scheduler hands queued work to a free idle agent by typing the brief at it; a
// plain binary will never read those keystrokes, so it must not be a candidate
// even when it is the only free panel in the fleet.
func TestSchedulerNeverPicksACommandPanel(t *testing.T) {
	s, _, _ := gateServer(
		panel.Panel{ID: "c1", Kind: panel.Command, State: panel.Idle},
		panel.Panel{ID: "s1", Kind: panel.Shell, State: panel.Idle},
	)
	s.mu.Lock()
	got, ok := s.freeIdleAgentLocked("")
	s.mu.Unlock()
	if ok {
		t.Fatalf("the scheduler offered work to %q; only an agent may take a queued task", got)
	}

	// …and it still picks the agent when one is there, so the test above is
	// refusing the command panel rather than refusing everything.
	s.mu.Lock()
	s.panels = append(s.panels, panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Idle})
	got, ok = s.freeIdleAgentLocked("")
	s.mu.Unlock()
	if !ok || got != "a1" {
		t.Fatalf("the scheduler should still pick the agent, got %q ok=%v", got, ok)
	}
}

// TestCommandPanelIsOffTheAttentionLadder: the rungs above idle are agent-only,
// and they say so through one flag. A command panel quiet for half an hour has
// neither a turn that could be over nor work it could be stuck on — the ladder is
// deciding nothing about it, not deciding calmly.
func TestCommandPanelIsOffTheAttentionLadder(t *testing.T) {
	s, clk, _ := gateServer(panel.Panel{ID: "c1", Kind: panel.Command, State: panel.Running})
	s.specs["c1"] = spawnSpec{Profile: "claude"}
	s.agentAttention = map[string]attn.Policy{"claude": {StuckAfter: 30 * time.Minute}}

	clk.add(idleAfter + attn.DefaultDoneAfter + 30*time.Minute)
	sig := s.signalsLocked(s.panels[0], true)
	if sig.agent {
		t.Fatal("a command panel must not raise the agent flag")
	}
	if sig.doneDue || sig.stuckDue {
		t.Fatalf("the agent-only rungs armed for a command panel: %+v", sig)
	}
	// The quiet clock still runs — it is what settles the panel to idle — so this
	// asserts an exclusion from the upper rungs, not a panel the Monitor forgot.
	if !sig.quiet {
		t.Fatal("a command panel should still settle on the quiet clock")
	}
	if st, _ := nextState(sig); st != panel.Idle {
		t.Fatalf("a quiet command panel should settle to idle, got %v", st)
	}
}

// TestCommandPanelRefusesTheAgentOnlyOps: diff, the git menu and both worktree
// verbs all route through agentTargetSpec, which is the server's authoritative
// gate. A command panel has no work tree to reason about — it is a process, not a
// worker in a checkout — so it is refused there exactly as a shell is.
func TestCommandPanelRefusesTheAgentOnlyOps(t *testing.T) {
	s, _, _ := gateServer(
		panel.Panel{ID: "c1", Kind: panel.Command, State: panel.Running},
		panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Running},
	)
	for _, label := range []string{"diff", "git"} {
		if _, err := s.agentTargetSpec("c1", label); err == nil {
			t.Errorf("%s was allowed on a command panel", label)
		}
		if _, err := s.agentTargetSpec("a1", label); err != nil {
			t.Errorf("%s should still be allowed on an agent panel: %v", label, err)
		}
	}
}

// TestCommandPanelIsNotRestarted is the one refusal the third kind did NOT get
// for free, because supervision was never agent-only. For a plain binary a
// non-zero exit is the RESULT — a test run that failed, a build that did not
// compile — and on-failure would re-run that failure on a backoff and then
// announce it had given up: a crash loop assembled out of a program working
// correctly.
func TestCommandPanelIsNotRestarted(t *testing.T) {
	s := supervisorServer(restart.Policy{Mode: restart.OnFailure})
	s.panels[0].Kind = panel.Command

	if got := supervise(s, 1, time.Now()); got != "" {
		t.Fatalf("a command panel's failure was supervised: %q", got)
	}
	if st := s.restarts["p1"]; st != nil && st.failures != 0 {
		t.Fatalf("a command panel's exit spent the failure budget: %d", st.failures)
	}

	// The same policy on the same server still restarts a shell, so the test above
	// is refusing the kind rather than reading an inert policy.
	s.panels[0].Kind = panel.Shell
	if got := supervise(s, 1, time.Now()); got == "" {
		t.Fatal("the on-failure policy should still restart a shell panel")
	}
}

// TestExitActivitySaysFinishedForACommand covers the answer to the issue's open
// question, in the one place it is visible. A command panel that exits HOLDS —
// nothing closes it, and the operator dismisses it once they have read it, which
// is why there is no new lifecycle machinery here. What changes is the word: it
// finished, rather than died, and a non-zero code is its answer rather than a
// fault.
func TestExitActivitySaysFinishedForACommand(t *testing.T) {
	cases := []struct {
		kind panel.Kind
		code int
		want string
	}{
		{panel.Command, 0, "finished"},
		{panel.Command, 2, "finished · code 2"},
		{panel.Agent, 0, "exited"},
		{panel.Shell, 1, "exited"},
	}
	for _, c := range cases {
		if got := exitActivity(c.kind, c.code); got != c.want {
			t.Errorf("exitActivity(%v, %d) = %q, want %q", c.kind, c.code, got, c.want)
		}
	}
}

// TestCommandPanelHoldsAfterItExits: the panel stays in the fleet as a slot the
// operator reads and then closes, carrying its code — the same durable dead slot
// an agent gets, reached by a different route and meaning something else.
func TestCommandPanelHoldsAfterItExits(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "c1", Kind: panel.Command, State: panel.Running})
	s.specs["c1"] = spawnSpec{Spec: ptymgr.Spec{Command: "true"}}

	s.onPanelExit("c1", 0)

	i := s.indexLocked("c1")
	if i < 0 {
		t.Fatal("a finished command panel must hold until the operator closes it")
	}
	if s.panels[i].State != panel.Exited {
		t.Fatalf("state = %v, want Exited", s.panels[i].State)
	}
	if s.panels[i].Activity != "finished" {
		t.Fatalf("activity = %q, want %q", s.panels[i].Activity, "finished")
	}
}

// TestCommandPanelTakesADispatchLikeAShell is the refusal this file does NOT
// have, pinned as the acceptance it actually is.
//
// The gate the refusals above go through is agentTargetSpec, and dispatch does
// not go through it: `baton ctl dispatch`, the baton_dispatch tool and a
// dispatch-group fan-out all land on a command panel, and the brief carries the
// score block a fan-out would put on an agent. SPEC.md said the opposite of both
// halves for a while, and it is the doc that was wrong.
//
// The shell panel is why. Dispatch is "write these bytes into this PTY", and a
// shell is no more an agent than a command panel is — it takes a dispatch, and
// it takes the block with it. Refusing the command panel alone would split the
// two non-agent kinds on a rule with nothing behind it, and would take away the
// one way to answer a binary that is sitting on a prompt. The agent-only ops are
// refused because they need a checkout to reason about; writing to a PTY needs
// nothing but the PTY.
//
// So both kinds are asserted together, and asserted to behave the SAME. A future
// change that gates dispatch on kind has to break this pair, not one of them.
func TestCommandPanelTakesADispatchLikeAShell(t *testing.T) {
	st, _ := scoreStore(t)
	seedEntry(t, st, "run the linter before claiming a task is done")
	s, _, written := gateServer(
		panel.Panel{ID: "c1", Kind: panel.Command, State: panel.Idle},
		panel.Panel{ID: "s1", Kind: panel.Shell, State: panel.Idle},
	)
	WithScore(ScoreState{Store: st, Enabled: true})(s)

	for _, id := range []string{"c1", "s1"} {
		if _, err := s.dispatchScored(id, "status", "", task.AuthorUser); err != nil {
			t.Fatalf("dispatch to %s: %v", id, err)
		}
	}

	if len(*written) != 2 {
		t.Fatalf("delivered %v, want one write to each of the two non-agent panels", *written)
	}
	for i, id := range []string{"c1", "s1"} {
		got := (*written)[i]
		if !strings.HasPrefix(got, id+":") {
			t.Fatalf("write %d = %q, want it addressed to %s", i, got, id)
		}
		if !strings.Contains(got, "run the linter before claiming a task is done") {
			t.Fatalf("the brief to %s carries no score block: %q", id, got)
		}
	}
	if (*written)[0][len("c1"):] != (*written)[1][len("s1"):] {
		t.Fatalf("the command panel and the shell were briefed differently:\n%q\n%q", (*written)[0], (*written)[1])
	}
}
