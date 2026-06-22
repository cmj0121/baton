package server_test

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/state"
)

// startDiffServer is a harness like startServer but it returns the *Server too,
// so a test can assert on the ephemeral set, and it dials no client itself.
func startDiffServer(t *testing.T, opts ...server.Option) (*server.Server, string) {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := server.New(ln, opts...)
	go func() { _ = srv.Serve() }()
	return srv, sock
}

// dialReady dials the server and drains the welcome + initial snapshot.
func dialReady(t *testing.T, sock string) *client.Client {
	t.Helper()
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	recv(t, c) // welcome
	recv(t, c) // snapshot
	return c
}

// requireGitDiff skips when git is absent and neutralises the developer's
// global/system config, mirroring the gitdiff package tests.
func requireGitDiff(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

// gitRepoWithChange makes a fresh git repo in a temp dir, commits one file so it
// has a HEAD, then leaves an untracked file so the tree has uncommitted work.
// It returns the repo path.
func gitRepoWithChange(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=baton", "GIT_AUTHOR_EMAIL=baton@example.com",
		"GIT_COMMITTER_NAME=baton", "GIT_COMMITTER_EMAIL=baton@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
	)
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	runGit("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "a.txt")
	runGit("commit", "-q", "-m", "init")
	// An untracked file makes the tree dirty so HasChanges passes.
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("fresh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// createAgentIn spawns an agent panel that sits idle in dir and returns its id.
func createAgentIn(t *testing.T, c *client.Client, dir string) string {
	t.Helper()
	if err := c.Send(proto.Command{
		Action: "panel.create", Kind: proto.KindAgent,
		Path: "/bin/sh", Args: []string{"-c", "sleep 30"}, Dir: dir,
	}); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	snap := recv(t, c)
	if len(snap.Panels) == 0 {
		t.Fatalf("expected an agent panel, got %+v", snap)
	}
	return snap.Panels[len(snap.Panels)-1].ID
}

// recvEvent waits for a control message of one of the wanted types, draining any
// telemetry/stats that slip into Events.
func recvEvent(t *testing.T, c *client.Client) proto.ServerMsg {
	t.Helper()
	return recv(t, c)
}

// TestDiffPanelDoesNotLeak is the headline guarantee: after a successful
// panel.diff, the ephemeral "diff:*" id must appear in NEITHER the dashboard
// snapshot (panelsMsg, observed over the wire via panel.list) NOR the persisted
// state (snapshotState, observed via the on-disk state file).
func TestDiffPanelDoesNotLeak(t *testing.T) {
	requireGitDiff(t)
	repo := gitRepoWithChange(t)
	statePath := filepath.Join(t.TempDir(), "state.json")

	srv, sock := startDiffServer(t, server.WithStateFile(statePath))
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, repo)

	// Open the diff; the reply carries the ephemeral id.
	if err := c.Send(proto.Command{Action: "panel.diff", ID: agentID}); err != nil {
		t.Fatalf("panel.diff: %v", err)
	}
	reply := recvEvent(t, c)
	if reply.Type != "diff" {
		t.Fatalf("expected a diff reply, got %+v", reply)
	}
	if !strings.HasPrefix(reply.ID, "diff:") {
		t.Fatalf("diff reply id should be diff:-prefixed, got %q", reply.ID)
	}
	ephID := reply.ID

	// 1) Not in the dashboard snapshot.
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("panel.list: %v", err)
	}
	snap := recvEvent(t, c)
	if snap.Type != "panels" {
		t.Fatalf("expected a panels snapshot, got %+v", snap)
	}
	for _, p := range snap.Panels {
		if p.ID == ephID {
			t.Fatalf("ephemeral diff panel %q leaked into the dashboard snapshot", ephID)
		}
	}

	// 2) Not in the persisted state. Force a save with a structural mutation (a
	// new shell panel), then load the snapshot from disk and assert the diff id
	// is absent. If openDiff had appended to s.panels, it would persist here.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell"}); err != nil {
		t.Fatalf("create shell: %v", err)
	}
	recvEvent(t, c) // the broadcast snapshot for the new shell

	var st state.State
	deadline := time.Now().Add(2 * time.Second)
	for {
		loaded, err := state.Load(statePath)
		if err == nil && len(loaded.Panels) > 0 {
			st = loaded
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state never persisted: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, p := range st.Panels {
		if p.ID == ephID {
			t.Fatalf("ephemeral diff panel %q leaked into the persisted state", ephID)
		}
	}

	// The server still tracks exactly the one ephemeral panel.
	if got := srv.EphemeralCount(); got != 1 {
		t.Fatalf("expected 1 tracked ephemeral panel, got %d", got)
	}
}

// TestDiffMissingTarget checks panel.diff for an unknown id is an error.
func TestDiffMissingTarget(t *testing.T) {
	_, sock := startDiffServer(t)
	c := dialReady(t, sock)

	if err := c.Send(proto.Command{Action: "panel.diff", ID: "nope"}); err != nil {
		t.Fatalf("panel.diff: %v", err)
	}
	msg := recvEvent(t, c)
	if msg.Type != "error" {
		t.Fatalf("a diff on a missing panel should error, got %+v", msg)
	}
	if !strings.Contains(msg.Error, "no panel with id") {
		t.Fatalf("unexpected error text: %q", msg.Error)
	}
}

// TestDiffOnShellRejected checks the agent-only gate: a shell panel cannot be
// diffed, and crucially no PTY is spawned for it.
func TestDiffOnShellRejected(t *testing.T) {
	srv, sock := startDiffServer(t)
	c := dialReady(t, sock)

	id := createShells(t, c, 1)[0]
	if err := c.Send(proto.Command{Action: "panel.diff", ID: id}); err != nil {
		t.Fatalf("panel.diff: %v", err)
	}
	msg := recvEvent(t, c)
	if msg.Type != "error" {
		t.Fatalf("a diff on a shell should error, got %+v", msg)
	}
	if msg.Error != "diff is available on agent panels" {
		t.Fatalf("unexpected gate error: %q", msg.Error)
	}
	if got := srv.EphemeralCount(); got != 0 {
		t.Fatalf("the gate should spawn no PTY, but %d ephemeral panels exist", got)
	}
}

// TestDiffNotAGitRepo checks an agent pointed at a non-repo dir reports the
// not-a-git-repository error and spawns nothing.
func TestDiffNotAGitRepo(t *testing.T) {
	requireGitDiff(t)
	plain := t.TempDir() // a dir that is not a git work tree

	srv, sock := startDiffServer(t)
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, plain)
	if err := c.Send(proto.Command{Action: "panel.diff", ID: agentID}); err != nil {
		t.Fatalf("panel.diff: %v", err)
	}
	msg := recvEvent(t, c)
	if msg.Type != "error" {
		t.Fatalf("a diff outside a git repo should error, got %+v", msg)
	}
	if !strings.Contains(msg.Error, "not a git repository") {
		t.Fatalf("unexpected error text: %q", msg.Error)
	}
	if got := srv.EphemeralCount(); got != 0 {
		t.Fatalf("a failed diff should spawn no PTY, but %d ephemeral panels exist", got)
	}
}

// TestDiffNoChanges checks a clean git repo reports "no uncommitted changes".
func TestDiffNoChanges(t *testing.T) {
	requireGitDiff(t)
	repo := gitRepoWithChange(t)
	// Remove the untracked file so the tree is clean.
	if err := os.Remove(filepath.Join(repo, "new.txt")); err != nil {
		t.Fatal(err)
	}

	srv, sock := startDiffServer(t)
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, repo)
	if err := c.Send(proto.Command{Action: "panel.diff", ID: agentID}); err != nil {
		t.Fatalf("panel.diff: %v", err)
	}
	msg := recvEvent(t, c)
	if msg.Type != "error" {
		t.Fatalf("a clean repo diff should error, got %+v", msg)
	}
	if !strings.Contains(msg.Error, "no uncommitted changes") {
		t.Fatalf("unexpected error text: %q", msg.Error)
	}
	if got := srv.EphemeralCount(); got != 0 {
		t.Fatalf("a clean diff should spawn no PTY, but %d ephemeral panels exist", got)
	}
}

// TestDiffCloseRemovesEphemeral checks the close path accepts an ephemeral id:
// panel.close on a diff:* id succeeds and drops it from the tracked set.
func TestDiffCloseRemovesEphemeral(t *testing.T) {
	requireGitDiff(t)
	repo := gitRepoWithChange(t)

	srv, sock := startDiffServer(t)
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, repo)
	if err := c.Send(proto.Command{Action: "panel.diff", ID: agentID}); err != nil {
		t.Fatalf("panel.diff: %v", err)
	}
	reply := recvEvent(t, c)
	if reply.Type != "diff" {
		t.Fatalf("expected a diff reply, got %+v", reply)
	}
	ephID := reply.ID
	if got := srv.EphemeralCount(); got != 1 {
		t.Fatalf("expected 1 ephemeral panel, got %d", got)
	}

	// Close the diff panel. closePanels reports success for it (broadcastFleet
	// follows, so a panels snapshot comes back), and it leaves the set.
	if err := c.Send(proto.Command{Action: "panel.close", ID: ephID}); err != nil {
		t.Fatalf("panel.close: %v", err)
	}
	got := recvEvent(t, c)
	if got.Type != "panels" {
		t.Fatalf("closing the diff panel should broadcast a snapshot, got %+v", got)
	}

	// The ephemeral set is empty (allow a brief moment for the Stop to settle).
	deadline := time.Now().Add(time.Second)
	for srv.EphemeralCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("closing the diff panel left %d ephemeral panels", srv.EphemeralCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDiffDisconnectReapsEphemeral checks a client that drops mid-diff leaves no
// orphan: the conn's ephemeral panels are closed on disconnect.
func TestDiffDisconnectReapsEphemeral(t *testing.T) {
	requireGitDiff(t)
	repo := gitRepoWithChange(t)

	srv, sock := startDiffServer(t)

	// A dedicated client we close by hand mid-diff.
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	recv(t, c) // welcome
	recv(t, c) // snapshot

	agentID := createAgentIn(t, c, repo)
	if err := c.Send(proto.Command{Action: "panel.diff", ID: agentID}); err != nil {
		t.Fatalf("panel.diff: %v", err)
	}
	reply := recvEvent(t, c)
	if reply.Type != "diff" {
		t.Fatalf("expected a diff reply, got %+v", reply)
	}
	if got := srv.EphemeralCount(); got != 1 {
		t.Fatalf("expected 1 ephemeral panel before disconnect, got %d", got)
	}

	// Drop the connection; the server's per-conn teardown reaps the diff panel.
	_ = c.Close()

	deadline := time.Now().Add(2 * time.Second)
	for srv.EphemeralCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("a dropped client left %d orphan ephemeral panels", srv.EphemeralCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
