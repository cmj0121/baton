package server_test

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// TestConductorGuardrails verifies the scoped conductor role: it may drive
// other panels but cannot close/signal itself, cannot reload the server, and
// cannot spawn faster than the rate cap. A plain (cockpit) connection is never
// fenced — the same self-close it would perform is allowed.
func TestConductorGuardrails(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	// Two shell panels: one stands in as the conductor's own panel, one as a
	// peer it is allowed to drive.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create self: %v", err)
	}
	selfID := recv(t, c).Panels[0].ID
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create peer: %v", err)
	}
	peerID := recv(t, c).Panels[1].ID

	// Upgrade this connection to the conductor role, declaring selfID as its own.
	if err := c.Send(proto.Command{Action: "hello", Role: "conductor", Self: selfID}); err != nil {
		t.Fatalf("hello conductor: %v", err)
	}
	recv(t, c) // welcome
	recv(t, c) // panels snapshot

	// Driving a peer is allowed: closing it broadcasts the smaller fleet.
	if err := c.Send(proto.Command{Action: "panel.close", ID: peerID}); err != nil {
		t.Fatalf("close peer: %v", err)
	}
	if got := recv(t, c); got.Type != "panels" {
		t.Fatalf("conductor should close a peer; got %+v", got)
	}

	// Self-close is refused.
	if err := c.Send(proto.Command{Action: "panel.close", ID: selfID}); err != nil {
		t.Fatalf("close self: %v", err)
	}
	if got := recv(t, c); got.Type != "error" || !strings.Contains(got.Error, "own panel") {
		t.Fatalf("expected self-close denial, got %+v", got)
	}

	// Self-signal is refused.
	if err := c.Send(proto.Command{Action: "panel.signal", ID: selfID, Signal: "SIGINT"}); err != nil {
		t.Fatalf("signal self: %v", err)
	}
	if got := recv(t, c); got.Type != "error" || !strings.Contains(got.Error, "own panel") {
		t.Fatalf("expected self-signal denial, got %+v", got)
	}

	// Reloading the server is refused.
	if err := c.Send(proto.Command{Action: "server.reload"}); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := recv(t, c); got.Type != "error" || !strings.Contains(got.Error, "reload") {
		t.Fatalf("expected reload denial, got %+v", got)
	}

	// Spawn-rate cap: the first create is admitted (a panels broadcast), the
	// immediate second is refused for spawning too fast.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create one: %v", err)
	}
	if got := recv(t, c); got.Type != "panels" {
		t.Fatalf("first conductor spawn should be admitted; got %+v", got)
	}
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create two: %v", err)
	}
	if got := recv(t, c); got.Type != "error" || !strings.Contains(got.Error, "too fast") {
		t.Fatalf("expected spawn-rate denial, got %+v", got)
	}
}

// TestConductorPanelSpawn checks the conductor panel: it is the singleton, runs
// in a server-managed workspace (not the requested dir) seeded with a primer and
// the identity env, and that workspace outlives the panel that used it.
func TestConductorPanelSpawn(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // keep the workspace out of the real runtime dir
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	srv := server.New(ln)
	go func() { _ = srv.Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	// Spawn the conductor. /bin/cat stands in for the agent CLI: it stays alive
	// reading its pty, so the panel does not exit out from under the assertions.
	// The requested dir (/tmp) must be overridden by the managed workspace.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "agent", Path: "/bin/cat", Dir: "/tmp", Conductor: true}); err != nil {
		t.Fatalf("create conductor: %v", err)
	}
	snap := recv(t, c)
	var cid string
	var found proto.Panel
	for _, p := range snap.Panels {
		if p.Conductor {
			cid, found = p.ID, p
		}
	}
	if cid == "" {
		t.Fatalf("no conductor panel in snapshot %+v", snap.Panels)
	}
	if !strings.HasPrefix(found.Title, "conductor · ") {
		t.Fatalf("conductor title = %q, want a conductor label", found.Title)
	}

	// Workspace: a real dir under the runtime base, not the requested /tmp, holding
	// the primer.
	dir := srv.PanelDir(cid)
	if dir == "" || dir == "/tmp" {
		t.Fatalf("conductor dir = %q, want a managed workspace", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "BATON.md")); err != nil {
		t.Fatalf("workspace primer missing: %v", err)
	}
	// The briefing is also written as CLAUDE.md so the default Claude conductor
	// auto-reads it as project instructions.
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); err != nil {
		t.Fatalf("workspace CLAUDE.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); err != nil {
		t.Fatalf("workspace .mcp.json missing: %v", err)
	}

	// Identity env: socket, scoped role, own panel id.
	env := srv.PanelEnv(cid)
	want := []string{
		paths.EnvSocket + "=" + sock,
		paths.EnvRole + "=conductor",
		paths.EnvPanelID + "=" + cid,
	}
	for _, w := range want {
		if !slices.Contains(env, w) {
			t.Fatalf("conductor env %v missing %q", env, w)
		}
	}

	// Singleton: a second conductor.create is refused.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "agent", Path: "/bin/cat", Conductor: true}); err != nil {
		t.Fatalf("second conductor: %v", err)
	}
	if got := recv(t, c); got.Type != "error" || !strings.Contains(got.Error, "already exists") {
		t.Fatalf("second conductor should be refused, got %+v", got)
	}

	// Closing the conductor keeps its workspace: it belongs to the socket, and the
	// next conductor opens straight back into the settings this one collected.
	if err := c.Send(proto.Command{Action: "panel.close", ID: cid}); err != nil {
		t.Fatalf("close conductor: %v", err)
	}
	recv(t, c) // panels snapshot without it
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("workspace %s should survive the close, stat err = %v", dir, err)
	}

	// …and the next conductor lands in that very directory rather than a new one.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "agent", Path: "/bin/cat", Conductor: true}); err != nil {
		t.Fatalf("second conductor: %v", err)
	}
	var next string
	for range 10 {
		got := recv(t, c)
		if got.Type != "panels" {
			continue
		}
		for _, p := range got.Panels {
			if p.Conductor {
				next = p.ID
			}
		}
		if next != "" {
			break
		}
	}
	if next == "" {
		t.Fatal("the conductor did not come back")
	}
	if again := srv.PanelDir(next); again != dir {
		t.Fatalf("reopened conductor dir = %q, want the retained workspace %q", again, dir)
	}
}

// TestConductorOperatorBrief checks that an operator's $HOME/.baton/CONDUCTOR.md
// is appended to the conductor's BATON.md briefing.
func TestConductorOperatorBrief(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".baton"), 0o700); err != nil {
		t.Fatalf("mkdir .baton: %v", err)
	}
	const mission = "Keep two reviewers running on the api repo at all times."
	if err := os.WriteFile(filepath.Join(home, ".baton", "CONDUCTOR.md"), []byte("# Mission\n\n"+mission+"\n"), 0o600); err != nil {
		t.Fatalf("write CONDUCTOR.md: %v", err)
	}

	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	srv := server.New(ln)
	go func() { _ = srv.Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	if err := c.Send(proto.Command{Action: "panel.create", Kind: "agent", Path: "/bin/cat", Conductor: true}); err != nil {
		t.Fatalf("create conductor: %v", err)
	}
	snap := recv(t, c)
	var cid string
	for _, p := range snap.Panels {
		if p.Conductor {
			cid = p.ID
		}
	}
	if cid == "" {
		t.Fatalf("no conductor panel in snapshot %+v", snap.Panels)
	}

	// The same briefing is written to both names; CLAUDE.md is what the default
	// Claude conductor auto-reads, so assert the brief landed in both.
	for _, name := range []string{"BATON.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(srv.PanelDir(cid), name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(data), "Operator's brief") {
			t.Fatalf("%s should carry the operator brief heading, got:\n%s", name, data)
		}
		if !strings.Contains(string(data), mission) {
			t.Fatalf("%s should include the operator's mission, got:\n%s", name, data)
		}
		// The built-in primer is still present — the brief augments, never replaces it.
		if !strings.Contains(string(data), "You are the baton conductor") {
			t.Fatalf("%s should keep the built-in primer, got:\n%s", name, data)
		}
	}
}

// TestConductorResetWorkspace covers the escape hatch behind
// `baton ctl conductor reset`: it is refused while a conductor still exists in
// the fleet, it clears the workspace once that conductor is closed, and the
// conductor role can never reach for it.
func TestConductorResetWorkspace(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // keep the workspace out of the real runtime dir
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	srv := server.New(ln)
	go func() { _ = srv.Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	if err := c.Send(proto.Command{Action: "panel.create", Kind: "agent", Path: "/bin/cat", Conductor: true}); err != nil {
		t.Fatalf("create conductor: %v", err)
	}
	cid := recv(t, c).Panels[0].ID
	ws := srv.PanelDir(cid)
	if ws == "" {
		t.Fatal("the conductor has no workspace")
	}
	marker := filepath.Join(ws, "settings.local.json")
	if err := os.WriteFile(marker, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	// Refused while the conductor is there: deleting the directory under a live
	// agent leaves it running in an unlinked cwd.
	if err := c.Send(proto.Command{Action: "conductor.reset"}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := recv(t, c); got.Type != "error" || !strings.Contains(got.Error, "close the conductor first") {
		t.Fatalf("reset should be refused while the conductor is live, got %+v", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("a refused reset must not touch the workspace: %v", err)
	}

	// Closed, then reset: the workspace and everything in it is gone.
	if err := c.Send(proto.Command{Action: "panel.close", ID: cid}); err != nil {
		t.Fatalf("close conductor: %v", err)
	}
	recv(t, c) // panels snapshot without it
	if err := c.Send(proto.Command{Action: "conductor.reset"}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil { // barrier
		t.Fatalf("list: %v", err)
	}
	if got := recv(t, c); got.Type != "panels" {
		t.Fatalf("reset should have been accepted, got %+v", got)
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Fatalf("workspace %s should be gone after a reset, stat err = %v", ws, err)
	}
}

// TestConductorResetFenced checks the fence: an agent whose state has gone bad is
// exactly the one that must not be able to erase the record of it. (The name is
// kept short on purpose — t.TempDir() plus a long test name overflows the ~104
// byte cap on a unix socket path.)
func TestConductorResetFenced(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() { _ = server.New(ln).Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	if err := c.Send(proto.Command{Action: "hello", Role: "conductor", Self: "1"}); err != nil {
		t.Fatalf("hello conductor: %v", err)
	}
	recv(t, c) // welcome
	recv(t, c) // panels snapshot

	if err := c.Send(proto.Command{Action: "conductor.reset"}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := recv(t, c); got.Type != "error" || !strings.Contains(got.Error, "conductor role") {
		t.Fatalf("the conductor role should be fenced from conductor.reset, got %+v", got)
	}
}

// TestConductorBriefReloads checks the reload path rewrites the live conductor's
// wiring from an edited $HOME/.baton/CONDUCTOR.md, and tells whoever is attached
// that there is something new to read. (Short name: t.TempDir() plus a long test
// name overflows the ~104 byte cap on a unix socket path.)
func TestConductorBriefReloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // keep the workspace out of the real runtime dir
	if err := os.MkdirAll(filepath.Join(home, ".baton"), 0o700); err != nil {
		t.Fatalf("mkdir .baton: %v", err)
	}
	const first = "Keep two reviewers running on the api repo."
	brief := filepath.Join(home, ".baton", "CONDUCTOR.md")
	if err := os.WriteFile(brief, []byte("# Mission\n\n"+first+"\n"), 0o600); err != nil {
		t.Fatalf("write CONDUCTOR.md: %v", err)
	}

	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	srv := server.New(ln)
	go func() { _ = srv.Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	if err := c.Send(proto.Command{Action: "panel.create", Kind: "agent", Path: "/bin/cat", Conductor: true}); err != nil {
		t.Fatalf("create conductor: %v", err)
	}
	cid := recv(t, c).Panels[0].ID
	if err := c.Send(proto.Command{Action: "panel.attach", ID: cid}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// Barrier: the connection is served in order, so a panels snapshot answering
	// this proves the attach above has been processed and the notice below will
	// have somewhere to go.
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	for range 10 {
		if recv(t, c).Type == "panels" {
			break
		}
	}

	// The operator edits the brief while the conductor runs. Nothing has told the
	// server yet, so the workspace still carries the old one.
	const second = "Stop spawning reviewers; drain the backlog instead."
	if err := os.WriteFile(brief, []byte("# Mission\n\n"+second+"\n"), 0o600); err != nil {
		t.Fatalf("rewrite CONDUCTOR.md: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(srv.PanelDir(cid), "BATON.md"))
	if err != nil {
		t.Fatalf("read BATON.md: %v", err)
	}
	if strings.Contains(string(data), second) {
		t.Fatal("the workspace picked up the edit before any reload")
	}

	srv.Reload(server.Settings{}) // what SIGHUP and the cockpit reload both call

	for _, name := range []string{"BATON.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(srv.PanelDir(cid), name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(data), second) {
			t.Fatalf("%s should carry the edited brief after a reload, got:\n%s", name, data)
		}
		if strings.Contains(string(data), first) {
			t.Fatalf("%s should no longer carry the superseded brief, got:\n%s", name, data)
		}
		if !strings.Contains(string(data), "You are the baton conductor") {
			t.Fatalf("%s should keep the built-in primer, got:\n%s", name, data)
		}
	}

	// …and the panel says so, so a watching human knows there is something new. The
	// client routes "output" to its own channel, so read that one.
	deadline := time.After(5 * time.Second)
	var notice string
	for notice == "" {
		select {
		case msg := <-c.Output:
			if strings.Contains(string(msg.Data), "brief updated") {
				notice = string(msg.Data)
			}
		case <-deadline:
			t.Fatal("a reload should tell an attached cockpit the brief changed")
		}
	}
}
