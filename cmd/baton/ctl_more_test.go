package main

import (
	"path/filepath"
	"testing"

	"github.com/cmj0121/baton/internal/control"
)

// TestCtlMainDialError covers ctlMain's dial-failure path: the args parse
// cleanly, but with no server listening on the socket control.Dial fails, so
// ctlMain returns exit code 1 without ever running a subcommand.
func TestCtlMainDialError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	t.Setenv("BATON_SOCK", filepath.Join(home, "absent.sock"))

	if code := ctlMain([]string{"list"}); code != 1 {
		t.Fatalf("ctlMain(list) with no server = %d, want 1", code)
	}
}

// TestCtlMainRunError covers ctlMain's run-failure path: the command parses and
// dials a live server, but its handler is rejected (cancelling a task that does
// not exist), so kctx.Run returns an error and ctlMain returns exit code 1.
func TestCtlMainRunError(t *testing.T) {
	ctlTestServer(t)
	if code := ctlMain([]string{"queue", "cancel", "no-such-task"}); code != 1 {
		t.Fatalf("ctlMain(queue cancel bogus) = %d, want 1", code)
	}
}

// TestCtlRunClientErrors closes the control client out from under every
// subcommand handler, so each RPC fails on the dead connection. This exercises
// the error-return branch of every Run method — including both branches of
// ctlQueueAdd (plain enqueue and spawn-on-demand) and the promote/demote
// handlers that no success test reaches.
func TestCtlRunClientErrors(t *testing.T) {
	sock := ctlTestServer(t)
	c, err := control.DialSocket(sock, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close() // every subsequent RPC now fails on the closed connection

	runs := []interface {
		Run(*control.Client) error
	}{
		ctlList{},
		ctlSpawn{Agent: "/bin/cat"},
		ctlGroup{Name: "g", IDs: []string{"z"}},
		ctlRename{ID: "z", Name: "n"},
		ctlPin{IDs: []string{"z"}},
		ctlUnpin{IDs: []string{"z"}},
		ctlSignal{Signal: "SIGTERM", IDs: []string{"z"}},
		ctlSend{ID: "z", Text: "t"},
		ctlDispatch{ID: "z", Prompt: "p"},
		ctlDispatchGroup{Group: "g", Prompt: "p"},
		ctlQueueAdd{Prompt: "x"},                      // plain enqueue branch
		ctlQueueAdd{Prompt: "x", Command: "/bin/cat"}, // spawn-on-demand branch
		ctlQueueList{},
		ctlQueueCancel{ID: "z"},
		ctlQueuePromote{ID: "z"},
		ctlQueueDemote{ID: "z"},
		ctlQueueDrain{},
		ctlClose{IDs: []string{"z"}},
	}
	for _, r := range runs {
		if err := r.Run(c); err == nil {
			t.Errorf("%T.Run on a closed client should error", r)
		}
	}
}

// TestCtlQueueReorder drives the spawn-on-demand enqueue and the promote/demote
// reorder handlers against a live server, covering their success paths end to
// end (a queued task is enqueued, bumped to the head, then dropped to the tail).
func TestCtlQueueReorder(t *testing.T) {
	sock := ctlTestServer(t)
	c, err := control.DialSocket(sock, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Spawn-on-demand enqueue: the Command branch of ctlQueueAdd.
	if err := (ctlQueueAdd{Prompt: "spawn work", Command: "/bin/cat", Dir: t.TempDir(), Close: true}).Run(c); err != nil {
		t.Fatalf("ctlQueueAdd spawn: %v", err)
	}
	// A second plain task so the backlog has something to reorder.
	if err := (ctlQueueAdd{Prompt: "plain work"}).Run(c); err != nil {
		t.Fatalf("ctlQueueAdd plain: %v", err)
	}

	tasks, err := c.Tasks()
	if err != nil {
		t.Fatalf("tasks: %v", err)
	}
	var id string
	for _, tk := range tasks {
		if tk.Prompt == "plain work" {
			id = tk.ID
		}
	}
	if id == "" {
		t.Fatalf("the enqueued task should be in the backlog, got %+v", tasks)
	}

	if err := (ctlQueuePromote{ID: id}).Run(c); err != nil {
		t.Fatalf("ctlQueuePromote.Run: %v", err)
	}
	if err := (ctlQueueDemote{ID: id}).Run(c); err != nil {
		t.Fatalf("ctlQueueDemote.Run: %v", err)
	}
}
