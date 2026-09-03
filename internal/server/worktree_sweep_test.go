package server_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/worktree"
)

// gitEnv is the environment every git call in this file runs under, fixture and
// daemon alike. It is set on the PROCESS rather than on the fixture's own
// commands because the git that matters here runs inside the server under test
// and inherits this, not the fixture's: without it a developer's global
// init.defaultBranch or hooks reach the code being asserted.
func gitEnv(t *testing.T) []string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=baton", "GIT_AUTHOR_EMAIL=baton@example.com",
		"GIT_COMMITTER_NAME=baton", "GIT_COMMITTER_EMAIL=baton@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
	)
}

// gitIn runs one git command in dir and fails the test if it does not succeed.
func gitIn(t *testing.T, env []string, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// wtRepo makes a git repo with one commit, so a worktree spawn has a HEAD to
// branch off. It returns the repo and the environment its git runs under.
func wtRepo(t *testing.T) (string, []string) {
	t.Helper()
	env := gitEnv(t)
	// A short temp root: the control socket beside it is capped near 104 bytes on
	// macOS, and a dir named after the test overruns that.
	dir, err := os.MkdirTemp("", "wtrepo")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	gitIn(t, env, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, env, dir, "add", "a.txt")
	gitIn(t, env, dir, "commit", "-q", "-m", "init")
	return dir, env
}

// recvWorktree waits for a worktree reply or a refusal, tolerating the panels and
// stats pushes interleaved with them. The refusal is returned rather than failed
// on, because half of what these tests assert about IS the refusal.
func recvWorktree(t *testing.T, c *client.Client) proto.ServerMsg {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case msg, ok := <-c.Events:
			if !ok {
				t.Fatal("event channel closed unexpectedly")
			}
			if msg.Type == "worktree" || msg.Type == "error" {
				return msg
			}
		case <-deadline:
			t.Fatal("timed out waiting for a worktree reply")
			return proto.ServerMsg{}
		}
	}
}

// listTrees runs worktree.list and decodes the classified set.
func listTrees(t *testing.T, c *client.Client) []worktree.Entry {
	t.Helper()
	if err := c.Send(proto.Command{Action: "worktree.list"}); err != nil {
		t.Fatalf("send worktree.list: %v", err)
	}
	msg := recvWorktree(t, c)
	if msg.Type == "error" {
		t.Fatalf("worktree.list was refused: %s", msg.Error)
	}
	var out []worktree.Entry
	if err := json.Unmarshal(msg.Worktree, &out); err != nil {
		t.Fatalf("decode worktree.list %s: %v", msg.Worktree, err)
	}
	return out
}

// sweepReply mirrors the sweep payload as a CLIENT sees it. It is spelled out
// here rather than reusing the server's own struct so the wire shape is pinned
// independently of the type that produces it.
type sweepReply struct {
	Removed []string `json:"removed"`
	Dropped []string `json:"dropped"`
	Skipped []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"skipped"`
}

// sweepTrees runs worktree.sweep and decodes what it did.
func sweepTrees(t *testing.T, c *client.Client) sweepReply {
	t.Helper()
	if err := c.Send(proto.Command{Action: "worktree.sweep"}); err != nil {
		t.Fatalf("send worktree.sweep: %v", err)
	}
	msg := recvWorktree(t, c)
	if msg.Type == "error" {
		t.Fatalf("worktree.sweep was refused: %s", msg.Error)
	}
	var out sweepReply
	if err := json.Unmarshal(msg.Worktree, &out); err != nil {
		t.Fatalf("decode worktree.sweep %s: %v", msg.Worktree, err)
	}
	return out
}

// statusOfPath finds one path in a classification.
func statusOfPath(t *testing.T, entries []worktree.Entry, path string) worktree.Status {
	t.Helper()
	for _, e := range entries {
		if e.Path == path {
			return e.Status
		}
	}
	t.Fatalf("no entry for %q in %+v", path, entries)
	return ""
}

// hasPath reports whether a classification mentions path at all.
func hasPath(entries []worktree.Entry, path string) bool {
	for _, e := range entries {
		if e.Path == path {
			return true
		}
	}
	return false
}

// resolved is the spelling the record keeps: absolute with symlinks resolved,
// which is how git reports a worktree and therefore how a stamped path reads
// back. On macOS a temp dir under /var resolves to /private/var, so a test that
// compared the raw path would find nothing.
func resolved(t *testing.T, path string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks %s: %v", path, err)
	}
	return out
}

// spawnWorktreePanel opens a worktree on branch off repo with an agent that exits
// at once, and returns the panel id and the resolved tree path. The agent exits
// so the panel becomes a DEAD SLOT — the state that separates "the fleet still
// refers to this tree" from "nothing does".
func spawnWorktreePanel(t *testing.T, c *client.Client, repo, branch string) (string, string) {
	t.Helper()
	if err := c.Send(proto.Command{
		Action: "panel.git", Git: "worktree-add",
		Dir: repo, Name: branch,
		Path: "/bin/sh", Args: []string{"-c", "exit 0"},
	}); err != nil {
		t.Fatalf("worktree-add: %v", err)
	}

	leaf := strings.ReplaceAll(branch, "/", "-")
	tree := filepath.Join(repo+"-worktrees", leaf)
	// ONE loop, not two. The agent is `sh -c 'exit 0'`, so the broadcast saying it
	// exited can arrive while a first loop is still waiting for the tree — and a
	// first loop that drained c.Events to pass the time threw that broadcast away,
	// leaving the second waiting forever for news that had already come. That is
	// what failed on CI and never here: two loops raced for one channel and the
	// machine decided who won.
	var id string
	deadline := time.After(15 * time.Second)
	for id == "" {
		select {
		case msg, ok := <-c.Events:
			if !ok {
				t.Fatal("event channel closed unexpectedly")
			}
			for _, p := range msg.Panels {
				if p.State == "exited" && p.Group == branch {
					id = p.ID
				}
			}
		case <-deadline:
			t.Fatalf("the worktree agent for %s never exited; tree expected at %s", branch, tree)
		}
	}
	if _, err := os.Stat(filepath.Join(tree, ".git")); err != nil {
		t.Fatalf("the worktree should have been created at %s: %v", tree, err)
	}
	return id, resolved(t, tree)
}

func TestWorktreeListAndSweepAcceptance(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	repo, env := wtRepo(t)
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	serve(t, srv)
	c := dial(t, sock)

	// A tree the OPERATOR made by hand, in the same repository, before baton opens
	// any of its own. Nothing baton does may ever name or touch it.
	byHand := filepath.Join(repo+"-byhand", "theirs")
	gitIn(t, env, repo, "worktree", "add", "-q", "-b", "hand/made", byHand)
	byHandResolved := resolved(t, byHand)

	_, tree := spawnWorktreePanel(t, c, repo, "feat/x")

	// 1. The agent has exited, so the slot is dead — and a dead slot is NOT an
	//    orphan. The fleet still names this tree; the operator has not said they
	//    are done with it.
	entries := listTrees(t, c)
	if got := statusOfPath(t, entries, tree); got != worktree.StatusDeadSlot {
		t.Fatalf("a tree an exited panel still names should be a dead slot, got %q", got)
	}
	if hasPath(entries, byHandResolved) {
		t.Fatalf("a worktree the test made by hand must never be listed, got %+v", entries)
	}

	// 2. A sweep now removes nothing: the only stamped tree is a dead slot.
	if got := sweepTrees(t, c); len(got.Removed) != 0 || len(got.Dropped) != 0 {
		t.Fatalf("a sweep must not touch a dead slot, got %+v", got)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Fatalf("the dead slot's tree must still be there: %v", err)
	}

	// 3. Purging the slot is the operator saying they are done with it. Now — and
	//    only now — nothing in the fleet names the tree, so it is an orphan.
	if err := c.Send(proto.Command{Action: "panel.purge"}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	deadline := time.After(10 * time.Second)
	for {
		entries = listTrees(t, c)
		if statusOfPath(t, entries, tree) == worktree.StatusOrphan {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("purging the slot should have left an orphan, got %+v", entries)
		default:
		}
	}

	// 4. The sweep retires it: off the disk, and out of git's own view. The second
	//    half is what makes this a removal rather than an rm -rf.
	got := sweepTrees(t, c)
	if len(got.Removed) != 1 || got.Removed[0] != tree {
		t.Fatalf("the sweep should have removed exactly the orphan %q, got %+v", tree, got)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatalf("the orphan's directory should be gone, stat err = %v", err)
	}
	if out := gitIn(t, env, repo, "worktree", "list"); strings.Contains(out, tree) {
		t.Fatalf("git should no longer know %q, list says:\n%s", tree, out)
	}
	if got := listTrees(t, c); len(got) != 0 {
		t.Fatalf("the swept path should be out of the record too, got %+v", got)
	}

	// 5. …and the operator's own tree came through untouched, on disk and in git.
	if _, err := os.Stat(byHand); err != nil {
		t.Fatalf("a worktree the test made by hand must survive the sweep: %v", err)
	}
	if out := gitIn(t, env, repo, "worktree", "list"); !strings.Contains(out, byHandResolved) {
		t.Fatalf("git should still know the hand-made tree %q, list says:\n%s", byHandResolved, out)
	}
}

// TestWorktreeSweepSkipsADirtyOrphan is the fourth acceptance: an orphan holding
// uncommitted work is REPORTED and left standing, and does not fail the sweep
// around it. Two orphans are used so "the rest of the sweep goes on" is an
// assertion rather than a hope — with one, a sweep that aborted on the first
// refusal would look identical.
//
// The skip's reason is asserted only to be non-empty. git localises its messages,
// so matching its English wording would pass only on machines that speak it.
func TestWorktreeSweepSkipsADirtyOrphan(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	repo, env := wtRepo(t)
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	serve(t, srv)
	c := dial(t, sock)

	// The branch names are chosen so the DIRTY tree sorts before the clean one.
	// The record is stored sorted, so the sweep walks it in path order — with the
	// clean tree first, git's refusal would be the last thing that happened and a
	// sweep that aborted on the first error would pass this test unchanged.
	_, dirtyTree := spawnWorktreePanel(t, c, repo, "feat/a-dirty")
	_, cleanTree := spawnWorktreePanel(t, c, repo, "feat/z-clean")

	// Uncommitted work in one of them. git refuses to remove it without --force,
	// and no --force is ever passed.
	if err := os.WriteFile(filepath.Join(dirtyTree, "wip.txt"), []byte("unsaved\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.Send(proto.Command{Action: "panel.purge"}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	deadline := time.After(10 * time.Second)
	for statusOfPath(t, listTrees(t, c), cleanTree) != worktree.StatusOrphan {
		select {
		case <-deadline:
			t.Fatal("purging the slots should have left orphans")
		default:
		}
	}

	got := sweepTrees(t, c)
	if len(got.Skipped) != 1 || got.Skipped[0].Path != dirtyTree {
		t.Fatalf("the dirty orphan should be the one skipped, got %+v", got)
	}
	if got.Skipped[0].Reason == "" {
		t.Fatal("a skip must say why, so the operator knows what to deal with")
	}
	if _, err := os.Stat(dirtyTree); err != nil {
		t.Fatalf("a skipped orphan must be left in place: %v", err)
	}
	// The clean tree is swept AFTER the refusal, so this is the assertion that the
	// sweep carried on rather than stopping at the first thing git said no to.
	if len(got.Removed) != 1 || got.Removed[0] != cleanTree {
		t.Fatalf("the sweep must go on past a refusal and still remove %q, got %+v", cleanTree, got)
	}

	// The skipped tree stays STAMPED, so the operator can still see it and a later
	// sweep can finish once they have dealt with the work in it.
	if s := statusOfPath(t, listTrees(t, c), dirtyTree); s != worktree.StatusOrphan {
		t.Fatalf("a skipped orphan must stay in the record, got %q", s)
	}
	if out := gitIn(t, env, repo, "worktree", "list"); !strings.Contains(out, dirtyTree) {
		t.Fatalf("git should still know the skipped tree, list says:\n%s", out)
	}
}

// TestWorktreeSweepDropsARecordWhoseTreeIsGone is the case #65 said the sweep must
// tolerate: an operator removes the tree in their own terminal, or deletes the
// directory outright, and neither reaches baton. The record is then a path naming
// nothing. It is dropped rather than erroring, and — the half that matters — the
// sweep does not treat a missing directory as a reason to give up on the rest.
func TestWorktreeSweepDropsARecordWhoseTreeIsGone(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	repo, _ := wtRepo(t)
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	serve(t, srv)
	c := dial(t, sock)

	_, tree := spawnWorktreePanel(t, c, repo, "feat/vanished")
	if err := c.Send(proto.Command{Action: "panel.purge"}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	deadline := time.After(10 * time.Second)
	for statusOfPath(t, listTrees(t, c), tree) != worktree.StatusOrphan {
		select {
		case <-deadline:
			t.Fatal("purging the slot should have left an orphan")
		default:
		}
	}

	// The operator deletes it behind baton's back.
	if err := os.RemoveAll(tree); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if e := listTrees(t, c); len(e) != 1 || e[0].Exists {
		t.Fatalf("a recorded path naming nothing should still be listed, and not Exists: %+v", e)
	}

	got := sweepTrees(t, c)
	if len(got.Dropped) != 1 || got.Dropped[0] != tree {
		t.Fatalf("a record whose tree is gone should be dropped, got %+v", got)
	}
	if len(got.Removed) != 0 {
		t.Fatalf("there was nothing on disk to remove, got %+v", got)
	}
	if e := listTrees(t, c); len(e) != 0 {
		t.Fatalf("the dropped record should be out of the set, got %+v", e)
	}
}

// TestWorktreeCloseSkipsTheDeadSlotStage records what panel.close actually does,
// because the issue's acceptance says it leaves a dead slot and it does not.
// closePanel deletes the panel AND its spawn spec, so from the sweep's point of
// view the tree is unclaimed the moment the panel goes: it is an orphan straight
// away, with no purge in between.
//
// That is the safe direction — a tree becomes eligible only when nothing in the
// fleet refers to it, and after a close nothing does — but it means "close" and
// "purge" are not two steps toward the same place. Pinned as an assertion so the
// next reader finds the behaviour rather than the issue's wording.
func TestWorktreeCloseSkipsTheDeadSlotStage(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	repo, _ := wtRepo(t)
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	serve(t, srv)
	c := dial(t, sock)

	id, tree := spawnWorktreePanel(t, c, repo, "feat/closed")
	if s := statusOfPath(t, listTrees(t, c), tree); s != worktree.StatusDeadSlot {
		t.Fatalf("before the close it is a dead slot, got %q", s)
	}

	if err := c.Send(proto.Command{Action: "panel.close", IDs: []string{id}}); err != nil {
		t.Fatalf("close: %v", err)
	}
	deadline := time.After(10 * time.Second)
	for statusOfPath(t, listTrees(t, c), tree) != worktree.StatusOrphan {
		select {
		case <-deadline:
			t.Fatal("panel.close drops the spawn spec, so the tree should be an orphan")
		default:
		}
	}
}

// TestWorktreeVerbsWithPersistenceOff is the fail-safe direction as an assertion,
// and the one this feature would be dangerous without.
//
// No state file means no worktree record, which means baton cannot say it opened
// ANY path. list answers with nothing, and sweep removes nothing — including a
// real worktree sitting right there in the repository. The opposite reading of
// the same situation, "no record, so nothing is accounted for, so sweep it all",
// is the one that empties disks, and the tree left standing here is what tells
// the two apart.
func TestWorktreeVerbsWithPersistenceOff(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	repo, env := wtRepo(t)
	ln, sock, _ := listen(t)
	srv := server.New(ln) // no WithStateFile: persistence, and the record, are off
	serve(t, srv)
	c := dial(t, sock)

	tree := filepath.Join(repo+"-worktrees", "unstamped")
	gitIn(t, env, repo, "worktree", "add", "-q", "-b", "feat/unstamped", tree)

	if got := listTrees(t, c); len(got) != 0 {
		t.Fatalf("no record means nothing to list, got %+v", got)
	}
	got := sweepTrees(t, c)
	if len(got.Removed) != 0 || len(got.Dropped) != 0 || len(got.Skipped) != 0 {
		t.Fatalf("no record means SWEEP NOTHING, got %+v", got)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Fatalf("an unstamped worktree must survive a sweep with no record: %v", err)
	}
}

// TestWorktreeSweepIsFencedFromTheConductor is the mass-delete fence. A
// conductor connection may LIST the trees baton opened — seeing the residue its
// own spawns left is exactly what an agent driving the fleet should be able to
// check — but may not sweep them. The operator's unscoped ctl is who sweeps.
//
// Both halves are asserted on ONE connection, because "fenced" is a claim about
// the difference: a daemon that refused everything to a conductor, or nothing,
// would pass half of this and fail the other.
//
// The connection declares the role after the handshake, which the monotone hello
// rule permits from empty — a connection may add a fence, never drop one.
func TestWorktreeSweepIsFencedFromTheConductor(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	repo, _ := wtRepo(t)
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	serve(t, srv)
	c := dial(t, sock)

	_, tree := spawnWorktreePanel(t, c, repo, "feat/fenced")
	if err := c.Send(proto.Command{Action: "panel.purge"}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	deadline := time.After(10 * time.Second)
	for statusOfPath(t, listTrees(t, c), tree) != worktree.StatusOrphan {
		select {
		case <-deadline:
			t.Fatal("purging the slot should have left an orphan")
		default:
		}
	}

	if err := c.Send(proto.Command{Action: "hello", Role: "conductor", Self: "99"}); err != nil {
		t.Fatalf("hello as conductor: %v", err)
	}

	// Listing is still open to it.
	if err := c.Send(proto.Command{Action: "worktree.list"}); err != nil {
		t.Fatalf("send worktree.list: %v", err)
	}
	if msg := recvWorktree(t, c); msg.Type != "worktree" {
		t.Fatalf("a conductor may list the trees, got %q: %s", msg.Type, msg.Error)
	}

	// Sweeping is not, and the orphan is still standing afterwards — the refusal
	// is an effect, not just a message.
	if err := c.Send(proto.Command{Action: "worktree.sweep"}); err != nil {
		t.Fatalf("send worktree.sweep: %v", err)
	}
	msg := recvWorktree(t, c)
	if msg.Type != "error" {
		t.Fatalf("a conductor must not be able to sweep, got a %q reply: %s", msg.Type, msg.Worktree)
	}
	if !strings.Contains(msg.Error, "conductor role") {
		t.Fatalf("want the conductor refusal, got %q", msg.Error)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Fatalf("the refused sweep must have removed nothing: %v", err)
	}
}

// TestWorktreeSweepSkipsALockedOrphanToo covers git's OTHER refusal beside the
// dirty one, and it is here because the two are different messages.
//
// The sweep must treat both the same way — skipped, named, sweep goes on — and
// the way to get that wrong is to recognise one refusal and let the other
// through as a hard failure. The sweep matches on nothing at all: any error from
// WorktreeRemove is a skip. This test is what keeps it that way, and it runs a
// locked orphan, a dirty one and a clean one together so the answer has to be
// "two named, one removed" rather than anything simpler.
func TestWorktreeSweepSkipsALockedOrphanToo(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	repo, env := wtRepo(t)
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	serve(t, srv)
	c := dial(t, sock)

	// Both refusals sort BEFORE the clean tree, since the record is walked in path
	// order: the clean one is what the sweep only reaches by carrying on past two
	// different refusals in a row.
	_, lockedTree := spawnWorktreePanel(t, c, repo, "feat/a-locked")
	_, dirtyTree := spawnWorktreePanel(t, c, repo, "feat/b-dirty")
	_, cleanTree := spawnWorktreePanel(t, c, repo, "feat/z-clean")

	gitIn(t, env, repo, "worktree", "lock", lockedTree)
	if err := os.WriteFile(filepath.Join(dirtyTree, "wip.txt"), []byte("unsaved\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.Send(proto.Command{Action: "panel.purge"}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	deadline := time.After(10 * time.Second)
	for statusOfPath(t, listTrees(t, c), cleanTree) != worktree.StatusOrphan {
		select {
		case <-deadline:
			t.Fatal("purging the slots should have left orphans")
		default:
		}
	}

	got := sweepTrees(t, c)
	skipped := map[string]string{}
	for _, s := range got.Skipped {
		skipped[s.Path] = s.Reason
	}
	for _, want := range []string{lockedTree, dirtyTree} {
		reason, ok := skipped[want]
		if !ok {
			t.Fatalf("git refuses %q, so it must be skipped and named, got %+v", want, got)
		}
		if reason == "" {
			t.Fatalf("the skip of %q must say why", want)
		}
	}
	if len(got.Removed) != 1 || got.Removed[0] != cleanTree {
		t.Fatalf("the sweep must reach %q past both refusals, got %+v", cleanTree, got)
	}
	for _, kept := range []string{lockedTree, dirtyTree} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("a skipped orphan must be left in place: %v", err)
		}
	}
}
