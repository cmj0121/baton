package server_test

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/state"
)

// TestDispatchRecordsAndPersistsTask checks the panel.dispatch core action: the
// brief is recorded on the panel (so it reaches every frontend's card) and is
// flushed to the snapshot, so a restart restores it.
func TestDispatchRecordsAndPersistsTask(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID

	const brief = "refactor the auth module"
	if err := c.Send(proto.Command{Action: "panel.dispatch", ID: id, Prompt: brief}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got := recv(t, c)
	if got.Type != "panels" {
		t.Fatalf("dispatch should broadcast the fleet; got %+v", got)
	}
	if got.Panels[0].Task != brief {
		t.Fatalf("brief not recorded on the panel: %q", got.Panels[0].Task)
	}

	// The brief is persisted, not just live: a forced save writes it where
	// state.Load can read it back.
	srv.SaveNow()
	st, err := state.Load(stateF)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(st.Panels) != 1 || st.Panels[0].Task != brief {
		t.Fatalf("brief not persisted: %+v", st.Panels)
	}
}

// TestDispatchRejectsBadInput checks the guard rails on the action: an empty
// prompt, an empty id, and an unknown panel each come back as an error rather
// than silently doing nothing.
func TestDispatchRejectsBadInput(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, _ := listen(t)
	go func() { _ = server.New(ln).Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID

	cases := []struct {
		name   string
		cmd    proto.Command
		expect string
	}{
		{"empty prompt", proto.Command{Action: "panel.dispatch", ID: id, Prompt: ""}, "needs a prompt"},
		{"empty id", proto.Command{Action: "panel.dispatch", ID: "", Prompt: "x"}, "needs an id"},
		{"unknown panel", proto.Command{Action: "panel.dispatch", ID: "999", Prompt: "x"}, "no panel"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := c.Send(tc.cmd); err != nil {
				t.Fatalf("send: %v", err)
			}
			got := recv(t, c)
			if got.Type != "error" || !strings.Contains(got.Error, tc.expect) {
				t.Fatalf("want error containing %q, got %+v", tc.expect, got)
			}
		})
	}
}

// TestDispatchRestoresTask checks that a restored panel carries its persisted
// brief back, so the card still shows what the agent was last asked to do after a
// daemon restart.
func TestDispatchRestoresTask(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)

	if err := (state.State{Panels: []state.PanelState{
		{ID: "1", Kind: "agent", Title: "claude #1", Task: "land the login fix", Spec: state.Spec{Command: "/bin/sh"}},
	}}).Save(stateF); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	srv := server.New(ln, server.WithStateFile(stateF))
	srv.Restore()
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	got := recv(t, c)
	if len(got.Panels) != 1 || got.Panels[0].Task != "land the login fix" {
		t.Fatalf("restored panel lost its brief: %+v", got.Panels)
	}
}

// TestConductorCannotDispatchSelf checks that the queue surface honours the same
// fence as panel.input: a conductor may dispatch a task to a peer panel but not
// onto its own, so it cannot feed itself a task loop.
func TestConductorCannotDispatchSelf(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, _ := listen(t)
	go func() { _ = server.New(ln).Serve() }()

	c := dial(t, sock)

	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create self: %v", err)
	}
	selfID := recv(t, c).Panels[0].ID
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create peer: %v", err)
	}
	peerID := recv(t, c).Panels[1].ID

	if err := c.Send(proto.Command{Action: "hello", Role: "conductor", Self: selfID}); err != nil {
		t.Fatalf("hello conductor: %v", err)
	}
	recv(t, c) // welcome
	recv(t, c) // panels snapshot

	// A peer dispatch is allowed and broadcasts the fleet.
	if err := c.Send(proto.Command{Action: "panel.dispatch", ID: peerID, Prompt: "do the thing"}); err != nil {
		t.Fatalf("dispatch peer: %v", err)
	}
	if got := recv(t, c); got.Type != "panels" {
		t.Fatalf("conductor should dispatch a peer; got %+v", got)
	}

	// A self dispatch is refused.
	if err := c.Send(proto.Command{Action: "panel.dispatch", ID: selfID, Prompt: "loop"}); err != nil {
		t.Fatalf("dispatch self: %v", err)
	}
	if got := recv(t, c); got.Type != "error" || !strings.Contains(got.Error, "own panel") {
		t.Fatalf("expected self-dispatch denial, got %+v", got)
	}
}

// TestDispatchTaskFilter wires the synchronous task.pre filter and checks both of
// its powers over the panel.dispatch path: a rewrite changes the recorded brief,
// and a veto turns the dispatch into an error with nothing recorded.
func TestDispatchTaskFilter(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, _ := listen(t)
	srv := server.New(ln)
	// Rewrite a "tag" brief, veto anything mentioning a secret, pass the rest.
	srv.SetTaskFilter(func(prompt, group string) (string, bool) {
		if strings.Contains(prompt, "secret") {
			return "", false
		}
		if strings.HasPrefix(prompt, "tag ") {
			return "[build] " + prompt, true
		}
		return prompt, true
	})
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID

	// A rewritten brief reaches the panel in its filtered form.
	if err := c.Send(proto.Command{Action: "panel.dispatch", ID: id, Prompt: "tag the release"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := recv(t, c); got.Type != "panels" || got.Panels[0].Task != "[build] tag the release" {
		t.Fatalf("filter rewrite not applied, got %+v", got)
	}

	// A vetoed brief is refused and never recorded.
	if err := c.Send(proto.Command{Action: "panel.dispatch", ID: id, Prompt: "leak the secret"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got := recv(t, c)
	if got.Type != "error" || !strings.Contains(got.Error, "vetoed") {
		t.Fatalf("a vetoed task should error, got %+v", got)
	}
}
