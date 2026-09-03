package mcp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mcpWTRepo makes a git repo with one commit, so baton_spawn has a HEAD to
// branch off.
func mcpWTRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// Set on the PROCESS, not just on the fixture's own git calls: the git that
	// matters here runs inside the server under test, and it inherits this
	// environment rather than the fixture's. Without it a developer's global
	// init.defaultBranch or hooks reach the code being asserted.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	dir := t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=baton", "GIT_AUTHOR_EMAIL=baton@example.com",
		"GIT_COMMITTER_NAME=baton", "GIT_COMMITTER_EMAIL=baton@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
	)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "init")
	return dir
}

// TestMCPSpawnWorktree is the MCP half of the acceptance: the extra fields on
// baton_spawn — not a sibling tool — build the tree and return the new panel's
// id, and the tool errors rather than half-spawning when a field is missing or
// the directory is not a repository.
func TestMCPSpawnWorktree(t *testing.T) {
	repo := mcpWTRepo(t)
	sock := startServer(t)

	resps := run(t, sock,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"baton_spawn","arguments":{"dir":%q,"worktree":true,"branch":"feat/mcp","agent":"/bin/sh","args":["-c","sleep 30"]}}}`, repo),
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"baton_spawn","arguments":{"dir":%q,"worktree":true,"agent":"/bin/sh"}}}`, repo),
		fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"baton_spawn","arguments":{"dir":%q,"worktree":true,"branch":"feat/nope","agent":"/bin/sh"}}}`, t.TempDir()),
		fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"baton_spawn","arguments":{"dir":%q,"branch":"feat/oops","agent":"/bin/sh"}}}`, repo),
	)
	if len(resps) != 4 {
		t.Fatalf("want 4 responses, got %d", len(resps))
	}

	if resps[0].Result["isError"] == true {
		t.Fatalf("the worktree spawn should succeed, got %q", contentText(t, resps[0].Result))
	}
	if txt := contentText(t, resps[0].Result); !strings.HasPrefix(txt, "spawned panel ") {
		t.Fatalf("the worktree spawn should name the new panel, got %q", txt)
	}
	tree := filepath.Join(repo+"-worktrees", "feat-mcp")
	if _, err := os.Stat(filepath.Join(tree, ".git")); err != nil {
		t.Fatalf("the worktree should exist at %s: %v", tree, err)
	}

	// worktree: true with no branch is a TOOL error, and one raised in the process
	// before anything reaches the socket.
	if resps[1].Result["isError"] != true {
		t.Fatalf("worktree without a branch should be a tool error, got %+v", resps[1].Result)
	}
	if txt := contentText(t, resps[1].Result); !strings.Contains(txt, "branch") {
		t.Fatalf("want a refusal naming the branch, got %q", txt)
	}

	// A non-repo dir is a tool error too — this one from the server, which is where
	// the tree would have been built.
	if resps[2].Result["isError"] != true {
		t.Fatalf("worktree against a non-repo should be a tool error, got %+v", resps[2].Result)
	}
	if txt := contentText(t, resps[2].Result); !strings.Contains(txt, "not a git repository") {
		t.Fatalf("want the non-repo refusal, got %q", txt)
	}

	// branch WITHOUT worktree is refused rather than dropped. Setting one field of
	// a pair is the slip a model makes, and dropping it silently would spawn into
	// the shared checkout — quietly doing the opposite of what was asked. `ctl`
	// refuses the same pairing; this is the surface the conductor actually reaches.
	if resps[3].Result["isError"] != true {
		t.Fatalf("branch without worktree should be a tool error, got %+v", resps[3].Result)
	}
	// Named per branch, not per repo: the first call in this test legitimately made
	// repo-worktrees, so only the refused branch's own tree proves anything.
	if _, err := os.Stat(filepath.Join(repo+"-worktrees", "feat-oops")); !os.IsNotExist(err) {
		t.Fatalf("a refused call must leave no tree behind, stat err = %v", err)
	}
}

// TestMCPPlainSpawn: the new fields are opt-in. Omitting worktree leaves dir
// meaning the workdir, and builds no tree beside a repo.
//
// The name is terse on purpose — a Unix socket address is capped near 104 bytes
// and startServer spells the test's own name into the path, so a descriptive name
// here is a `bind: invalid argument` instead of a test.
func TestMCPPlainSpawn(t *testing.T) {
	repo := mcpWTRepo(t)
	sock := startServer(t)

	resps := run(t, sock,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"baton_spawn","arguments":{"dir":%q,"agent":"/bin/sh","args":["-c","sleep 30"]}}}`, repo),
	)
	if resps[0].Result["isError"] == true {
		t.Fatalf("a plain spawn should succeed, got %q", contentText(t, resps[0].Result))
	}
	if _, err := os.Stat(repo + "-worktrees"); !os.IsNotExist(err) {
		t.Fatalf("a spawn without worktree must call no git worktree, stat err = %v", err)
	}
}
