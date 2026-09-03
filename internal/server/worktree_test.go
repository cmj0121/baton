package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
	"github.com/cmj0121/baton/internal/worktree"
)

// wtEnv is the git environment the worktree tests run under: the developer's
// global and system config is neutralised so a stray `init.defaultBranch` or
// hook cannot change what the tests observe.
func wtEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=baton", "GIT_AUTHOR_EMAIL=baton@example.com",
		"GIT_COMMITTER_NAME=baton", "GIT_COMMITTER_EMAIL=baton@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
	)
}

// wtGit runs one git command in dir, failing the test on a non-zero exit.
func wtGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, wtEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// wtRepo makes a fresh git repo with one commit, so it has a HEAD to branch off.
func wtRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	dir := t.TempDir()
	wtGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wtGit(t, dir, "add", "a.txt")
	wtGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

// wtList is what git itself calls the worktrees of repo — the vocabulary the
// record has to be comparable with, and what an orphan sweep will read.
func wtList(t *testing.T, repo string) []string {
	t.Helper()
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir, cmd.Env = repo, wtEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	var trees []string
	for _, line := range strings.Split(string(out), "\n") {
		if after, ok := strings.CutPrefix(line, "worktree "); ok {
			trees = append(trees, after)
		}
	}
	return trees
}

// wtServer builds a server with persistence on but nothing serving: the
// primitive is called directly, so no client and no listener traffic is needed.
// It returns the server and the sibling file the worktree record lands in — the
// path the server derives from the state file, spelled out here so the test
// asserts that naming rather than reading it back off the server.
func wtServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	s := newHostServer(t, WithStateFile(filepath.Join(dir, "s.state.json")))
	return s, filepath.Join(dir, "s.worktrees.json")
}

// idleAgent is a spec that spawns and stays put, so the panel survives long
// enough for the assertions. It carries a profile, since the worktree agent is a
// copy of its source and has to land under the same caps.
func idleAgent() spawnSpec {
	return spawnSpec{
		Spec:    ptymgr.Spec{Command: "/bin/sh", Args: []string{"-c", "sleep 30"}},
		Profile: "heavy",
	}
}

// TestWorktreeSpawnWithoutAPanel is the extraction itself: the whole
// add → spawn → group sequence reached with a repo path and a spec, with NO agent
// living in the repo and no panel id anywhere in the call. If the sequence were
// still welded to a live panel this test could not be written.
func TestWorktreeSpawnWithoutAPanel(t *testing.T) {
	repo := wtRepo(t)
	s, _ := wtServer(t)

	if len(s.panels) != 0 {
		t.Fatalf("the fleet should be empty before the call, got %+v", s.panels)
	}
	if err := s.worktreeSpawn(repo, "feature/solo", idleAgent()); err != nil {
		t.Fatalf("worktreeSpawn: %v", err)
	}

	tree := filepath.Join(repo+"-worktrees", "feature-solo")
	if _, err := os.Stat(filepath.Join(tree, ".git")); err != nil {
		t.Fatalf("the worktree should exist at %s: %v", tree, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.panels) != 1 {
		t.Fatalf("expected one spawned agent, got %+v", s.panels)
	}
	p := s.panels[0]
	if p.Kind != panel.Agent {
		t.Fatalf("the spawned panel should be an agent, got %v", p.Kind)
	}
	if p.Group != "feature/solo" {
		t.Fatalf("the agent should be grouped under the branch, got %q", p.Group)
	}
	if got := s.specs[p.ID].Dir; got != tree {
		t.Fatalf("the agent should be rooted in the worktree %q, got %q", tree, got)
	}
	// The worktree agent is a copy of its source, so it has to land under the same
	// caps rather than dropping to the fleet-wide ones.
	if got := s.specs[p.ID].Profile; got != "heavy" {
		t.Fatalf("the spawned agent should keep the spec's profile, got %q", got)
	}
	if got := s.specs[p.ID].Command; got != "/bin/sh" {
		t.Fatalf("the spawned agent should run the spec's command, got %q", got)
	}
}

// TestWorktreeSpawnRecordsOnlyItsOwn is the persisted set, asserted in git's own
// vocabulary: of the three trees git reports for this repo, exactly the one the
// primitive opened is recorded. The repo itself and the tree the operator made
// with plain `git worktree add` are not — the half that can fail, and the half an
// orphan sweep depends on.
func TestWorktreeSpawnRecordsOnlyItsOwn(t *testing.T) {
	repo := wtRepo(t)
	s, recordFile := wtServer(t)

	// A tree the operator made by hand, before baton ever touches this repo.
	byHand := filepath.Join(t.TempDir(), "by-hand")
	wtGit(t, repo, "worktree", "add", "-b", "by-hand", "--", byHand)

	if err := s.worktreeSpawn(repo, "feature/mine", idleAgent()); err != nil {
		t.Fatalf("worktreeSpawn: %v", err)
	}
	mine := filepath.Join(repo+"-worktrees", "feature-mine")

	known := wtList(t, repo)
	if len(known) != 3 {
		t.Fatalf("git should know the repo, the hand-made tree and baton's, got %v", known)
	}

	// Read the record back off disk through the store's own reader, so the test
	// asserts the on-disk contract rather than an in-memory field.
	got := worktree.New(recordFile).Paths()
	if len(got) != 1 {
		t.Fatalf("exactly one of git's %v was opened by baton, recorded %v", known, got)
	}
	// Every recorded path must be one git would name, or a sweep can never match it.
	if !slices.Contains(known, got[0]) {
		t.Fatalf("the record %q is not a path git names in %v", got[0], known)
	}
	if !strings.HasSuffix(got[0], filepath.Join("-worktrees", "feature-mine")) {
		t.Fatalf("the recorded tree should be the one baton opened (%s), got %q", mine, got[0])
	}
	if strings.HasSuffix(got[0], "by-hand") {
		t.Fatalf("a tree made with plain `git worktree add` (%s) must not be recorded", byHand)
	}
}

// TestWorktreeRemoveUnrecordsTheTree is the mirror of the hand-made-tree test:
// the set names the trees baton owns NOW, so a tree retired through `C-t G` `x`
// leaves it again. Without the un-record the set would only ever grow, and every
// removal would leave a path naming a tree that no longer exists.
func TestWorktreeRemoveUnrecordsTheTree(t *testing.T) {
	repo := wtRepo(t)
	s, recordFile := wtServer(t)

	if err := s.worktreeSpawn(repo, "feature/short", idleAgent()); err != nil {
		t.Fatalf("worktreeSpawn: %v", err)
	}
	tree := filepath.Join(repo+"-worktrees", "feature-short")
	if got := worktree.New(recordFile).Paths(); len(got) != 1 {
		t.Fatalf("the tree should be recorded before it is removed, got %v", got)
	}

	// `x` runs against an agent sitting in the repo, not in the tree being removed.
	id, err := s.createPanel(proto.KindAgent, "/bin/sh", []string{"-c", "sleep 30"}, repo, "", false, false)
	if err != nil {
		t.Fatalf("create the repo agent: %v", err)
	}
	if err := s.gitWorktreeRemove(id, tree); err != nil {
		t.Fatalf("gitWorktreeRemove: %v", err)
	}

	if got := worktree.New(recordFile).Paths(); len(got) != 0 {
		t.Fatalf("a tree removed through baton should leave the record, got %v", got)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatalf("the tree should be gone from disk, stat err = %v", err)
	}
}

// TestWorktreeRemoveKeepsTheRecordWhenGitRefuses checks the un-record is tied to
// git actually having removed the tree: a refusal leaves both the tree and its
// entry, so the operator can retry.
func TestWorktreeRemoveKeepsTheRecordWhenGitRefuses(t *testing.T) {
	repo := wtRepo(t)
	s, recordFile := wtServer(t)

	if err := s.worktreeSpawn(repo, "feature/dirty", idleAgent()); err != nil {
		t.Fatalf("worktreeSpawn: %v", err)
	}
	tree := filepath.Join(repo+"-worktrees", "feature-dirty")

	// Uncommitted work makes git refuse the plain (no --force) removal.
	if err := os.WriteFile(filepath.Join(tree, "wip.txt"), []byte("wip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wtGit(t, tree, "add", "wip.txt")

	id, err := s.createPanel(proto.KindAgent, "/bin/sh", []string{"-c", "sleep 30"}, repo, "", false, false)
	if err != nil {
		t.Fatalf("create the repo agent: %v", err)
	}
	if err := s.gitWorktreeRemove(id, tree); err == nil {
		t.Fatal("git should have refused to remove a dirty worktree")
	}

	if got := worktree.New(recordFile).Paths(); len(got) != 1 {
		t.Fatalf("a refused removal must leave the tree recorded, got %v", got)
	}
	if _, err := os.Stat(filepath.Join(tree, ".git")); err != nil {
		t.Fatalf("a refused removal must leave the tree standing: %v", err)
	}
}

// TestWorktreeSpawnKeepsTheTreeOnSpawnFailure checks the documented behaviour when
// the agent will not start: the error says the tree was made, the tree is still on
// disk, and it is still recorded — the record is what a sweep needs to retire it.
func TestWorktreeSpawnKeepsTheTreeOnSpawnFailure(t *testing.T) {
	repo := wtRepo(t)
	s, recordFile := wtServer(t)

	spec := spawnSpec{Spec: ptymgr.Spec{Command: filepath.Join(t.TempDir(), "no-such-agent")}}
	err := s.worktreeSpawn(repo, "feature/dead", spec)
	if err == nil {
		t.Fatal("a spawn that cannot start should have been reported")
	}
	if !strings.Contains(err.Error(), "the agent did not start") {
		t.Fatalf("the error should say the tree outlived the spawn, got %q", err)
	}

	tree := filepath.Join(repo+"-worktrees", "feature-dead")
	if _, statErr := os.Stat(filepath.Join(tree, ".git")); statErr != nil {
		t.Fatalf("a failed spawn must leave the worktree at %s: %v", tree, statErr)
	}
	if got := worktree.New(recordFile).Paths(); len(got) != 1 || !slices.Contains(wtList(t, repo), got[0]) {
		t.Fatalf("a tree that outlived its spawn should still be recorded as one git names, got %v", got)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.panels) != 0 {
		t.Fatalf("a failed spawn should leave no panel behind, got %+v", s.panels)
	}
}

// TestWorktreeSpawnRefusesANonRepo checks the refusal survived the extraction and
// that it costs nothing: no tree, no panel, and nothing added to the record. There
// is no fallback onto a plain spawn.
func TestWorktreeSpawnRefusesANonRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	plain := t.TempDir()
	s, recordFile := wtServer(t)

	err := s.worktreeSpawn(plain, "feature/nope", idleAgent())
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("a non-repo should be refused with the usual shape, got %v", err)
	}
	if _, statErr := os.Stat(plain + "-worktrees"); !os.IsNotExist(statErr) {
		t.Fatalf("a refused add should have made no tree, stat err = %v", statErr)
	}
	if got := worktree.New(recordFile).Paths(); len(got) != 0 {
		t.Fatalf("a refused add should record nothing, got %v", got)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.panels) != 0 {
		t.Fatalf("a refused add should spawn nothing, got %+v", s.panels)
	}
}

// TestWorktreeRecordIsOffWithoutPersistence checks the store shares the fleet
// snapshot's on/off switch: a server with no state file still opens the tree and
// spawns, it simply keeps no record.
func TestWorktreeRecordIsOffWithoutPersistence(t *testing.T) {
	repo := wtRepo(t)
	s := newHostServer(t) // no WithStateFile: persistence off

	if s.wtrees != nil {
		t.Fatal("a server with no state file should keep no worktree record")
	}
	if err := s.worktreeSpawn(repo, "feature/nopersist", idleAgent()); err != nil {
		t.Fatalf("worktreeSpawn: %v", err)
	}
	tree := filepath.Join(repo+"-worktrees", "feature-nopersist")
	if _, err := os.Stat(filepath.Join(tree, ".git")); err != nil {
		t.Fatalf("the tree should still be made without persistence: %v", err)
	}
}
