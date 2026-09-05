package server_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// plantWorktreeRecord writes the stamped set directly, bypassing every path that
// normally fills it. It is how this file asks the question the record cannot
// answer for itself: given an entry naming something baton never opened, what
// actually stops the sweep from removing it?
//
// The store lives beside the fleet snapshot, and the name is derived here exactly
// as the server derives it, so a rename there fails this test rather than
// silently leaving it planting a file nothing reads.
func plantWorktreeRecord(t *testing.T, stateF string, paths ...string) {
	t.Helper()
	store := strings.TrimSuffix(stateF, ".state.json") + ".worktrees.json"
	body, err := json.Marshal(map[string]any{"schema": 1, "paths": paths})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, body, 0o600); err != nil {
		t.Fatalf("plant %s: %v", store, err)
	}
}

// TestSweepCannotRemoveWhatGitDoesNotCallAWorktree is the answer to "what stops a
// sweep from removing something the operator did not open".
//
// It is not the record — the record is a plain JSON file beside the socket, and
// this test plants one naming a directory of the operator's notes. It is not the
// classifier either: with no panel claiming the path, that directory comes back
// an orphan, which is the one status a sweep acts on. What stops it is that the
// removal is delegated WHOLE to `git worktree remove`, which refuses a path git
// has no worktree registered at. The refusal is recorded as a skip and the sweep
// goes on, so the directory and its file are still there afterwards.
//
// The second path is the honest boundary beside it: a worktree the OPERATOR made
// by hand, which baton never opened, IS removed once its path is in the record.
// Git's registration is the gate, not baton's authorship — so what protects an
// operator's work is that nothing on the wire can put a path into that record
// (worktreeSpawn stamps only a path git has just created), not that the sweep
// second-guesses what it reads.
func TestSweepCannotRemoveWhatGitDoesNotCallAWorktree(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	repo, env := wtRepo(t)

	notes := filepath.Join(t.TempDir(), "notes")
	if err := os.Mkdir(notes, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(notes, "NOTES.txt")
	if err := os.WriteFile(keep, []byte("irreplaceable\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	handmade := filepath.Join(t.TempDir(), "handmade")
	gitIn(t, env, repo, "worktree", "add", "-b", "handmade", handmade)

	ln, sock, stateF := listen(t)
	plantWorktreeRecord(t, stateF, notes, handmade)
	serve(t, server.New(ln, server.WithStateFile(stateF)))
	c := dial(t, sock)

	// Both are orphans: nothing in the fleet claims either, which is exactly the
	// state a sweep acts on. If this stops being true the test below proves nothing.
	for _, p := range []string{notes, handmade} {
		if got := statusOfPath(t, listTrees(t, c), p); got != "orphan" {
			t.Fatalf("planted %s classified as %q, want orphan — the sweep would not have reached it", p, got)
		}
	}

	got := sweepTrees(t, c)
	if len(got.Skipped) != 1 || got.Skipped[0].Path != notes {
		t.Fatalf("sweep skipped %+v, want the plain directory refused by git", got.Skipped)
	}
	if body, err := os.ReadFile(keep); err != nil || string(body) != "irreplaceable\n" {
		t.Fatalf("the operator's file is %q (%v), want it untouched", body, err)
	}
	if len(got.Removed) != 1 || got.Removed[0] != handmade {
		t.Fatalf("sweep removed %+v, want the registered worktree and only it", got.Removed)
	}
	if _, err := os.Stat(handmade); !os.IsNotExist(err) {
		t.Fatalf("stat of the removed worktree = %v, want it gone", err)
	}
}

// TestNothingOnTheWireCanStampAPath is the property the test above leans on: the
// stamped set only ever gains a path git has just created a worktree at. Every
// spawn verb is driven with a repository that is not one, and the record stays
// empty — the tree has to exist before it can be recorded, so an entry naming a
// directory of the operator's cannot be arranged from the socket.
func TestNothingOnTheWireCanStampAPath(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	notARepo := t.TempDir()

	ln, sock, stateF := listen(t)
	serve(t, server.New(ln, server.WithStateFile(stateF)))
	c := dial(t, sock)

	if err := c.Send(proto.Command{
		Action: "panel.git", Git: "worktree-add",
		Dir: notARepo, Name: "feature/x", Path: "/bin/cat",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg := recvWorktree(t, c); msg.Type != "error" {
		t.Fatalf("worktree-add on a non-repo = %+v, want a refusal", msg)
	}
	if e := listTrees(t, c); len(e) != 0 {
		t.Fatalf("the record gained %+v from a refused worktree-add, want nothing stamped", e)
	}
}
