package control_test

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/worktree"
)

// startStateServer is startServer with PERSISTENCE ON, which is what gives the
// daemon a worktree record to answer from at all. startServer runs without one,
// so every worktree verb against it is correctly empty.
func startStateServer(t *testing.T) string {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	dir := shortDir(t)
	sock := filepath.Join(dir, "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := server.New(ln, server.WithStateFile(filepath.Join(dir, "baton.state.json")))
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { srv.Shutdown() })
	return sock
}

// wtOrphan opens a worktree through the client, waits for its agent to exit, then
// purges the slot — the sequence that leaves a tree nothing in the fleet names.
// It returns the tree path as the record spells it.
func wtOrphan(t *testing.T, c *control.Client, repo, branch string) string {
	t.Helper()
	if _, err := c.SpawnWorktree("/bin/sh", []string{"-c", "exit 0"}, repo, branch); err != nil {
		t.Fatalf("SpawnWorktree: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		panels, err := c.List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		done := len(panels) > 0
		for _, p := range panels {
			if p.State != "exited" {
				done = false
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worktree agent never exited, panels %+v", panels)
		}
	}
	if err := c.Do(proto.Command{Action: "panel.purge"}); err != nil {
		t.Fatalf("purge: %v", err)
	}

	tree, err := filepath.EvalSymlinks(filepath.Join(repo+"-worktrees", strings.ReplaceAll(branch, "/", "-")))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return tree
}

// TestWorktreesAndSweepRoundtrip drives the two verbs through the client: the
// classified list comes back typed, the same answer comes back as JSON for the
// CLI and the MCP tool to print, and a sweep retires the orphan and says so.
func TestWorktreesAndSweepRoundtrip(t *testing.T) {
	repo := wtRepo(t)
	c, err := control.DialSocket(startStateServer(t), "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	tree := wtOrphan(t, c, repo, "feat/round")

	trees, err := c.Worktrees()
	if err != nil {
		t.Fatalf("Worktrees: %v", err)
	}
	if len(trees) != 1 || trees[0].Path != tree {
		t.Fatalf("the tree baton opened should be the one listed, got %+v", trees)
	}
	if trees[0].Status != worktree.StatusOrphan {
		t.Fatalf("a purged worktree panel leaves an orphan, got %q", trees[0].Status)
	}

	// The JSON presentation is the same answer, not a second one.
	out, err := c.WorktreesJSON()
	if err != nil {
		t.Fatalf("WorktreesJSON: %v", err)
	}
	var viaJSON []worktree.Entry
	if err := json.Unmarshal([]byte(out), &viaJSON); err != nil {
		t.Fatalf("WorktreesJSON should be JSON, got %q: %v", out, err)
	}
	if len(viaJSON) != 1 || viaJSON[0] != trees[0] {
		t.Fatalf("the JSON and typed answers should agree, got %+v vs %+v", viaJSON, trees)
	}

	swept, err := c.SweepWorktrees()
	if err != nil {
		t.Fatalf("SweepWorktrees: %v", err)
	}
	if !strings.Contains(swept, tree) {
		t.Fatalf("the sweep should name what it removed, got %q", swept)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatalf("the orphan should be gone, stat err = %v", err)
	}
	if got, err := c.Worktrees(); err != nil || len(got) != 0 {
		t.Fatalf("the record should be empty after the sweep, got %+v (%v)", got, err)
	}
}

// TestWorktreeVerbsSurfaceARefusal checks a daemon refusal reaches the caller as
// an error rather than an empty answer. A conductor connection is refused the
// sweep, and a client that read that as "nothing to sweep" would be the worst
// possible way to be wrong about it — silence where a boundary was hit.
func TestWorktreeVerbsSurfaceARefusal(t *testing.T) {
	c, err := control.DialSocket(startStateServer(t), "conductor", "99", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Listing is open to a conductor…
	if _, err := c.Worktrees(); err != nil {
		t.Fatalf("a conductor may list the trees: %v", err)
	}
	// …and sweeping is not, which must arrive as an error.
	out, err := c.SweepWorktrees()
	if err == nil {
		t.Fatalf("a conductor's sweep should be refused, got %q", out)
	}
	if !strings.Contains(err.Error(), "conductor role") {
		t.Fatalf("want the daemon's own refusal, got %v", err)
	}
}

// TestWorktreeVerbsOnAClosedClient checks the I/O failure path reports rather
// than returning a plausible-looking empty set.
func TestWorktreeVerbsOnAClosedClient(t *testing.T) {
	c, err := control.DialSocket(startStateServer(t), "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()

	if _, err := c.Worktrees(); err == nil {
		t.Fatal("Worktrees on a closed client should fail")
	}
	if _, err := c.WorktreesJSON(); err == nil {
		t.Fatal("WorktreesJSON on a closed client should fail")
	}
	if _, err := c.SweepWorktrees(); err == nil {
		t.Fatal("SweepWorktrees on a closed client should fail")
	}
}
