package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
)

// statusReply is the decoded score.status payload. Named for what it is rather
// than for the subsystem, so it cannot be mistaken for the server's own
// scoreState — and so `status`, the helper below, stays reachable from every
// test in the package.
type statusReply struct {
	Enabled       bool       `json:"enabled"`
	Available     bool       `json:"available"`
	Unlocked      bool       `json:"unlocked"`
	Reason        string     `json:"reason"`
	Entries       int        `json:"entries"`
	Rendered      int        `json:"rendered"`
	Oversized     int        `json:"oversized"`
	BlockFull     bool       `json:"block_full"`
	PromoteAt     int        `json:"promote_at"`
	UserSignalsAt int        `json:"user_signals_at"`
	WorkingSet    int        `json:"working_set"`
	Rank          score.Rank `json:"rank"`
	Dir           string     `json:"dir"`
}

// status runs score.status against s and decodes the reply.
func status(t *testing.T, s *Server) statusReply {
	t.Helper()
	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "score.status"})
	msg := reply(t, cc)
	var got statusReply
	if msg.Type != "score" || json.Unmarshal(msg.Score, &got) != nil {
		t.Fatalf("score.status must answer a score object, got %+v", msg)
	}
	return got
}

// scoreStore opens a store in a fresh directory and closes it when the test
// ends, so a test that wants one is three words rather than six lines.
func scoreStore(t *testing.T) (*score.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := score.Open(dir, score.Policy{})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	return st, dir
}

// TestScoreStatusDistinguishesOffFromUnavailable is the operator's only runtime
// way to learn their fleet memory is not running, and why. BATON_SOCK is the
// documented way to run a second fleet, both fleets default to $HOME/.baton, so
// "another daemon holds the directory" is the ordinary case — and answering it
// with the same "disabled" the config knob produces leaves that operator with an
// empty memory and nothing to explain it.
func TestScoreStatusDistinguishesOffFromUnavailable(t *testing.T) {
	st, dir := scoreStore(t)

	held := "score: /home/u/.baton/score.lock is held by another baton daemon; one writer per score directory"
	cases := []struct {
		name   string
		store  *score.Store
		on     bool
		reason string
		want   statusReply
	}{
		{"running", st, true, "", statusReply{
			Enabled: true, Available: true, Entries: 0, Rendered: 0,
			PromoteAt: 3, UserSignalsAt: 2, WorkingSet: 7,
			Rank: score.Rank{Recency: 2, Cwd: 2, Profile: 2, Group: 2}, Dir: dir,
		}},
		{"held by another daemon", nil, true, held, statusReply{Enabled: true, Reason: held}},
		{"switched off", nil, false, "score is switched off in the config (score.enabled: false)",
			statusReply{Reason: "score is switched off in the config (score.enabled: false)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := scoreServer(tc.store)
			WithScore(ScoreState{Store: tc.store, Enabled: tc.on, Reason: tc.reason})(s)

			if got := status(t, s); got != tc.want {
				t.Fatalf("status = %+v, want %+v", got, tc.want)
			}

			// A refusal says the same thing the status does, so an agent's error
			// and an operator's status never disagree about why.
			cc := conn("")
			s.onCommand(cc, proto.Command{Action: "score.submit", Prompt: "anything"})
			msg := reply(t, cc)
			switch {
			case tc.store != nil:
				if msg.Type != "score" {
					t.Fatalf("submit to a running store answered %+v", msg)
				}
			case msg.Type != "error" || msg.Error != tc.reason:
				t.Fatalf("submit refusal = %+v, want the status reason %q", msg, tc.reason)
			}
		})
	}
}

// TestReconcileFailureIsLatched keeps a persistent failure from logging once per
// dispatch forever: the read error returns before the store's fingerprint is
// touched, so the gate stays armed and every dispatch retries. What the operator
// needs is the transition in and the transition out.
func TestReconcileFailureIsLatched(t *testing.T) {
	st, dir := scoreStore(t)
	if _, _, err := st.Submit("still readable", score.Provenance{Source: "user"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, _ := scoreServer(st)

	// score.md as a directory: every read of it fails, for as long as it lasts.
	path := filepath.Join(dir, "score.md")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove score.md: %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir over score.md: %v", err)
	}

	for range 5 {
		s.scoreView(score.Context{})
	}
	if !s.scoreState.failing.Load() {
		t.Fatal("a failing reconcile did not latch, so it logs on every dispatch")
	}
	// The last view read is still served — a stale brief beats no brief.
	cc := conn("")
	s.scoreList(cc, proto.Command{Action: "score.list"})
	if got := string(reply(t, cc).Score); !strings.Contains(got, "still readable") {
		t.Fatalf("score.list = %s, want the last view the store did read", got)
	}
	// And the operator is told, rather than being shown a healthy status while
	// their edits sit inert (invariant I8).
	if got := status(t, s); !got.Available || got.Reason == "" {
		t.Fatalf("status = %+v, want an available store whose file cannot be read", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove the directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("# fixed\n"), 0o600); err != nil {
		t.Fatalf("restore score.md: %v", err)
	}
	s.scoreView(score.Context{})
	if s.scoreState.failing.Load() {
		t.Fatal("the latch did not clear, so recovery would never be reported")
	}
	if got := status(t, s); got.Reason != "" {
		t.Fatalf("status = %+v, want the reason cleared with the latch", got)
	}
}

// TestScoreStatusReportsWithheldLines is invariant I8 on the entry weight cap.
// A line too long to inject is withheld from score.list as well as from every
// brief, so without a count on the wire the operator's own entry is invisible
// everywhere except the file they typed it into — and the entries/rendered gap
// reads exactly like an ordinary render-limit truncation.
func TestScoreStatusReportsWithheldLines(t *testing.T) {
	st, dir := scoreStore(t)
	if _, _, err := st.Submit("a normal note", score.Provenance{Source: "user"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, _ := scoreServer(st)

	if got := status(t, s); got.Entries != 1 || got.Rendered != 1 || got.Oversized != 0 {
		t.Fatalf("status = %+v, want one entry, injected, nothing withheld", got)
	}

	// The operator adds a line far past the weight cap.
	editScoreMD(t, dir, readScoreMD(t, dir)+"- "+strings.Repeat("x", 50_000)+"\n")

	got := status(t, s)
	switch {
	case got.Entries != 2:
		t.Fatalf("status = %+v, want the long line counted as an entry", got)
	case got.Rendered != 1:
		t.Fatalf("status = %+v, want it withheld from the brief", got)
	case got.Oversized != 1:
		t.Fatalf("status = %+v, want the gap explained as a weight cap, not the working-set budget", got)
	}
}

// readScoreMD returns the current score.md, so a test can append to it the way
// an operator would rather than replacing what the store wrote.
func readScoreMD(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "score.md"))
	if err != nil {
		t.Fatalf("read score.md: %v", err)
	}
	return string(data)
}
