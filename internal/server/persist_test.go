package server_test

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/state"
)

// listen opens a fresh unix socket and returns it with its path. The socket lives
// in a short-named temp dir, since macOS caps unix socket paths near 104 bytes and
// the default per-test temp dir (named after the test) can overrun it.
func listen(t *testing.T) (ln net.Listener, sock, stateF string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock = filepath.Join(dir, "s.sock")
	stateF = filepath.Join(dir, "state.json")
	ln, err = net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln, sock, stateF
}

// dial attaches a client and drains the handshake (welcome + initial panels).
func dial(t *testing.T, sock string) *client.Client {
	t.Helper()
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	recv(t, c) // welcome
	recv(t, c) // initial panels snapshot
	return c
}

// TestSnapshotCapturesSpec checks that creating a panel freezes its spawn spec and
// that a forced save writes it back in a shape state.Load can read.
func TestSnapshotCapturesSpec(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	dir := t.TempDir()
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "agent", Path: "/bin/echo", Args: []string{"hi"}, Dir: dir}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID

	srv.SaveNow()
	st, err := state.Load(stateF)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(st.Panels) != 1 {
		t.Fatalf("expected 1 persisted panel, got %d", len(st.Panels))
	}
	ps := st.Panels[0]
	if ps.ID != id || ps.Kind != "agent" {
		t.Fatalf("unexpected persisted panel %+v", ps)
	}
	if ps.Spec.Command != "/bin/echo" || len(ps.Spec.Args) != 1 || ps.Spec.Args[0] != "hi" || ps.Spec.Dir != dir {
		t.Fatalf("spec not captured: %+v", ps.Spec)
	}
}

// TestSaveOnMutation checks that a structural mutation flushes the fleet/layout to
// disk on its own (no explicit SaveNow), and that the seq and grouping round-trip.
func TestSaveOnMutation(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{id}, Group: "work"}); err != nil {
		t.Fatalf("group: %v", err)
	}
	recv(t, c) // grouped snapshot

	// The saverLoop writes asynchronously; poll until the file reflects the group.
	var st state.State
	deadline := time.After(3 * time.Second)
	for {
		var err error
		st, err = state.Load(stateF)
		if err == nil && len(st.Panels) == 1 && st.Panels[0].Group == "work" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("mutation never persisted: %+v", st)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if st.Seq < 1 {
		t.Fatalf("seq not persisted, got %d", st.Seq)
	}
	if st.Panels[0].ID != id {
		t.Fatalf("persisted wrong panel: %+v", st.Panels[0])
	}
}

// TestRestoreDeadSlotsAndRespawn checks that Restore loads every persisted panel as
// an exited dead slot, restores seq past the highest id, keeps the spec, and that a
// manual respawn then re-runs the process.
func TestRestoreDeadSlotsAndRespawn(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)

	// Seed a state file with two panels, the higher id being 5.
	seed := state.State{
		Seq: 5,
		Panels: []state.PanelState{
			{ID: "2", Kind: "shell", Title: "shell #2", Spec: state.Spec{Command: "/bin/sh"}},
			{ID: "5", Kind: "agent", Title: "echo", Group: "work", Pinned: true, Spec: state.Spec{Command: "/bin/echo", Args: []string{"hi"}}},
		},
	}
	if err := seed.Save(stateF); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	srv := server.New(ln, server.WithStateFile(stateF))
	srv.Restore()
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	// The initial snapshot the handshake already drained should carry both panels;
	// ask again to assert on a fresh snapshot.
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	snap := recv(t, c)
	if len(snap.Panels) != 2 {
		t.Fatalf("expected 2 restored panels, got %d (%+v)", len(snap.Panels), snap.Panels)
	}
	for _, p := range snap.Panels {
		if p.State != "exited" {
			t.Fatalf("restored panel %s should be a dead slot, got state %q", p.ID, p.State)
		}
	}
	if snap.Panels[1].Group != "work" || !snap.Panels[1].Pinned {
		t.Fatalf("group/pin not restored: %+v", snap.Panels[1])
	}

	// A new panel must not collide with the restored ids: seq was 5, so the next is 6.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	created := recv(t, c).Panels
	newID := created[len(created)-1].ID
	if newID != "6" {
		t.Fatalf("new panel id should clear the restored ids, got %q", newID)
	}

	// Respawn the restored agent (a cheap /bin/echo); it goes live then exits on
	// its own, but it must leave the dead slot at least once.
	if err := c.Send(proto.Command{Action: "panel.respawn", ID: "5"}); err != nil {
		t.Fatalf("respawn: %v", err)
	}
	left := false
	deadline := time.After(3 * time.Second)
	for !left {
		select {
		case msg := <-c.Events:
			if msg.Type != "panels" {
				continue
			}
			for _, p := range msg.Panels {
				if p.ID == "5" && p.State != "exited" {
					left = true
				}
			}
		case <-deadline:
			t.Fatal("respawned panel never left the exited state")
		}
	}
}

// TestRestoreSeqGuardBumpsPastHigherIDs covers the guard's bump path: a state file
// whose panel ids exceed the persisted seq (a hand-edited or partially-written
// file) must still leave seq past the highest id, so a freshly created panel never
// collides with a restored one.
func TestRestoreSeqGuardBumpsPastHigherIDs(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)

	// seq is stale at 1, but the restored ids run up to 5.
	seed := state.State{
		Seq: 1,
		Panels: []state.PanelState{
			{ID: "2", Kind: "shell", Title: "shell #2", Spec: state.Spec{Command: "/bin/sh"}},
			{ID: "5", Kind: "shell", Title: "shell #5", Spec: state.Spec{Command: "/bin/sh"}},
		},
	}
	if err := seed.Save(stateF); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	srv := server.New(ln, server.WithStateFile(stateF))
	srv.Restore()
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	created := recv(t, c).Panels
	if newID := created[len(created)-1].ID; newID != "6" {
		t.Fatalf("seq guard should bump past the highest restored id (5); next id = %q, want 6", newID)
	}
}

// TestRespawnRefusesLivePanel checks that respawn errors for a panel whose process
// is still running.
func TestRespawnRefusesLivePanel(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	id := recv(t, c).Panels[0].ID

	if err := c.Send(proto.Command{Action: "panel.respawn", ID: id}); err != nil {
		t.Fatalf("respawn send: %v", err)
	}
	got := recv(t, c)
	if got.Type != "error" {
		t.Fatalf("expected an error respawning a live panel, got %+v", got)
	}
}
