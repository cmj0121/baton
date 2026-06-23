package server_test

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
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

// TestDiffReturnsStructuredFiles checks the default panel.diff replies with the
// structured per-file diff (type "diff", keyed to the target panel) and spawns no
// ephemeral panel at all — the strongest form of the no-leak guarantee, since
// there is nothing transient to reach the dashboard or the persisted state.
func TestDiffReturnsStructuredFiles(t *testing.T) {
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
		t.Fatalf("expected a structured diff reply, got %+v", reply)
	}
	if reply.ID != agentID {
		t.Fatalf("diff reply id = %q, want the target %q", reply.ID, agentID)
	}
	if len(reply.Files) == 0 {
		t.Fatalf("diff reply carried no files for a dirty repo")
	}
	// The untracked new.txt from gitRepoWithChange must surface as an added file.
	var found bool
	for _, f := range reply.Files {
		if f.Path == "new.txt" && f.Work == "?" && strings.Contains(f.Unstaged, "+") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected new.txt as an untracked added file, got %+v", reply.Files)
	}
	// Nothing is spawned, so no ephemeral can leak into the dashboard or state.
	if got := srv.EphemeralCount(); got != 0 {
		t.Fatalf("the structured diff should spawn no PTY, but %d ephemeral panels exist", got)
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
// panel.close on a diff:* id succeeds and drops it from the tracked set. It uses
// an explicit, long-lived diff-command so panel.diff takes the ephemeral-PTY path
// (the default structured path spawns nothing to close).
func TestDiffCloseRemovesEphemeral(t *testing.T) {
	requireGitDiff(t)
	repo := gitRepoWithChange(t)

	srv, sock := startDiffServer(t, server.WithDiffCommand("sleep 300"))
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, repo)
	if err := c.Send(proto.Command{Action: "panel.diff", ID: agentID}); err != nil {
		t.Fatalf("panel.diff: %v", err)
	}
	reply := recvEvent(t, c)
	if reply.Type != "ephemeral" {
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

// TestDiffPerConnCap checks the per-connection cap: a single client may hold at
// most maxEphemeralPerConn (8) diff panels open; the next panel.diff is rejected
// with an error naming the max and spawns nothing, leaving the count at the cap.
// Closing one then frees a slot for a fresh diff.
func TestDiffPerConnCap(t *testing.T) {
	requireGitDiff(t)
	repo := gitRepoWithChange(t)

	// Explicit long-lived diff-command so each panel.diff holds an ephemeral PTY
	// open (the default structured path spawns nothing to count against the cap).
	srv, sock := startDiffServer(t, server.WithDiffCommand("sleep 300"))
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, repo)

	const cap = 8 // mirrors maxEphemeralPerConn
	ephIDs := make([]string, 0, cap)
	for i := 0; i < cap; i++ {
		if err := c.Send(proto.Command{Action: "panel.diff", ID: agentID}); err != nil {
			t.Fatalf("panel.diff %d: %v", i, err)
		}
		reply := recvEvent(t, c)
		if reply.Type != "ephemeral" {
			t.Fatalf("diff %d: expected a diff reply, got %+v", i, reply)
		}
		ephIDs = append(ephIDs, reply.ID)
	}
	if got := srv.EphemeralCount(); got != cap {
		t.Fatalf("expected %d ephemeral panels at the cap, got %d", cap, got)
	}

	// The (cap+1)th diff is rejected and spawns nothing.
	if err := c.Send(proto.Command{Action: "panel.diff", ID: agentID}); err != nil {
		t.Fatalf("panel.diff over cap: %v", err)
	}
	msg := recvEvent(t, c)
	if msg.Type != "error" {
		t.Fatalf("a diff past the cap should error, got %+v", msg)
	}
	if !strings.Contains(msg.Error, "too many open panels") || !strings.Contains(msg.Error, "8") {
		t.Fatalf("unexpected cap error text: %q", msg.Error)
	}
	if got := srv.EphemeralCount(); got != cap {
		t.Fatalf("a rejected diff should spawn nothing; count moved off the cap to %d", got)
	}

	// Close one and confirm a new diff now succeeds.
	if err := c.Send(proto.Command{Action: "panel.close", ID: ephIDs[0]}); err != nil {
		t.Fatalf("panel.close: %v", err)
	}
	got := recvEvent(t, c)
	if got.Type != "panels" {
		t.Fatalf("closing a diff panel should broadcast a snapshot, got %+v", got)
	}
	deadline := time.Now().Add(time.Second)
	for srv.EphemeralCount() != cap-1 {
		if time.Now().After(deadline) {
			t.Fatalf("after one close, expected %d ephemeral panels, got %d", cap-1, srv.EphemeralCount())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := c.Send(proto.Command{Action: "panel.diff", ID: agentID}); err != nil {
		t.Fatalf("panel.diff after freeing a slot: %v", err)
	}
	reply := recvEvent(t, c)
	if reply.Type != "ephemeral" {
		t.Fatalf("a diff after freeing a slot should succeed, got %+v", reply)
	}
	if got := srv.EphemeralCount(); got != cap {
		t.Fatalf("after re-opening, expected %d ephemeral panels, got %d", cap, got)
	}
}

// TestDiffHardKillsProcessGroup proves the ephemeral teardown SIGKILLs the whole
// process group, not just the PTY's foreground shell: a plain PTY close (SIGHUP)
// could leave a backgrounded grandchild (a GUI difftool, a pager) alive. The diff
// command is pinned via WithDiffCommand to spawn a long-lived `sleep`, record its
// pid, and wait; after panel.close the recorded pid must be gone.
func TestDiffHardKillsProcessGroup(t *testing.T) {
	requireGitDiff(t)
	repo := gitRepoWithChange(t)

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	// Background a sleep, publish its pid, then wait — so the diff PTY stays alive
	// and there is a descendant that only a process-group kill reaches.
	diffCmd := "sleep 300 & echo $! > " + pidFile + "; wait"

	srv, sock := startDiffServer(t, server.WithDiffCommand(diffCmd))
	c := dialReady(t, sock)

	agentID := createAgentIn(t, c, repo)
	if err := c.Send(proto.Command{Action: "panel.diff", ID: agentID}); err != nil {
		t.Fatalf("panel.diff: %v", err)
	}
	reply := recvEvent(t, c)
	if reply.Type != "ephemeral" {
		t.Fatalf("expected a diff reply, got %+v", reply)
	}
	ephID := reply.ID

	// Wait for the backgrounded sleep to publish its pid.
	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for {
		if b, err := os.ReadFile(pidFile); err == nil {
			if n, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil && n > 0 {
				childPID = n
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the diff child never published its pid")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Sanity: the child is alive right now (signal 0 probes existence).
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("the diff child %d should be alive before close: %v", childPID, err)
	}

	// Close the diff panel: this runs the SIGKILL-then-Stop path on the group.
	if err := c.Send(proto.Command{Action: "panel.close", ID: ephID}); err != nil {
		t.Fatalf("panel.close: %v", err)
	}
	recvEvent(t, c) // the broadcast snapshot

	// The whole group is gone, so the backgrounded sleep must be reaped too. A
	// SIGHUP-only close would leave it orphaned and alive.
	deadline = time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(childPID, 0); err != nil {
			break // ESRCH (or EPERM after reap) — the child is gone
		}
		if time.Now().After(deadline) {
			t.Fatalf("the diff child %d outlived its panel — process group was not hard-killed", childPID)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got := srv.EphemeralCount(); got != 0 {
		t.Fatalf("closing the diff left %d ephemeral panels", got)
	}
}

// TestDiffDisconnectReapsEphemeral checks a client that drops mid-diff leaves no
// orphan: the conn's ephemeral panels are closed on disconnect.
func TestDiffDisconnectReapsEphemeral(t *testing.T) {
	requireGitDiff(t)
	repo := gitRepoWithChange(t)

	// Explicit long-lived diff-command so the diff holds an ephemeral PTY the
	// disconnect path must reap (the default structured path spawns nothing).
	srv, sock := startDiffServer(t, server.WithDiffCommand("sleep 300"))

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
	if reply.Type != "ephemeral" {
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
