package server_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/queue"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/task"
)

// queueDirFor mirrors the server's own derivation of the backlog directory from
// the state file path.
func queueDirFor(stateF string) string {
	return strings.TrimSuffix(stateF, ".state.json") + ".queue"
}

// TestEnqueuePersistsAndLists drives the wire path: a task.enqueue writes a file
// to the on-disk backlog and shows up in task.list.
func TestEnqueuePersistsAndLists(t *testing.T) {
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	go func() { _ = srv.Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "task.enqueue", Prompt: "ship it", Group: "api"}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	recv(t, c) // the broadcast fleet snapshot after enqueue

	// The backlog file lands (the saver is async, so poll for it).
	qdir := queueDirFor(stateF)
	deadline := time.Now().Add(2 * time.Second)
	for {
		entries, _ := os.ReadDir(qdir)
		if len(entries) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the enqueued task should persist a file in %s, got %v", qdir, entries)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// task.list reports it.
	if err := c.Send(proto.Command{Action: "task.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	var msg proto.ServerMsg
	for {
		msg = recv(t, c)
		if msg.Type == "tasks" {
			break
		}
	}
	if len(msg.Tasks) != 1 || msg.Tasks[0].Prompt != "ship it" || msg.Tasks[0].Status != "queued" {
		t.Fatalf("task.list should report the queued task, got %+v", msg.Tasks)
	}
}

// TestRestoreReQueuesOrphans checks that on boot the backlog reloads, a task that
// was in flight on a now-dead panel is re-queued (kept id, dropped panel), and a
// terminal leftover file is ignored.
func TestRestoreReQueuesOrphans(t *testing.T) {
	ln, sock, stateF := listen(t)
	qdir := queueDirFor(stateF)

	// Seed the backlog directly with the store: a waiting task, an orphaned
	// in-flight task, and a stale terminal file.
	st := queue.New(qdir, time.Now)
	if err := st.Save(task.Task{ID: "t1", Prompt: "waiting", Status: task.Queued}); err != nil {
		t.Fatalf("seed t1: %v", err)
	}
	if err := st.Save(task.Task{ID: "t2", Prompt: "in flight", Status: task.Running, Panel: "9", Attempts: 1}); err != nil {
		t.Fatalf("seed t2: %v", err)
	}
	if err := st.Save(task.Task{ID: "t3", Prompt: "old", Status: task.Done}); err != nil {
		t.Fatalf("seed t3: %v", err)
	}

	srv := server.New(ln, server.WithStateFile(stateF))
	srv.Restore()
	go func() { _ = srv.Serve() }()
	_ = dial(t, sock)

	if srv.TaskCount() != 2 {
		t.Fatalf("restore should load the two live tasks (terminal dropped), got %d", srv.TaskCount())
	}
	if tk, ok := srv.TaskByID("t1"); !ok || tk.Status != task.Queued {
		t.Fatalf("the waiting task should restore queued, got %+v ok=%v", tk, ok)
	}
	if tk, ok := srv.TaskByID("t2"); !ok || tk.Panel != "" || tk.Status != task.Queued {
		t.Fatalf("the orphaned task should be re-queued with no panel, got %+v ok=%v", tk, ok)
	}
}

// TestConductorCannotDrain checks the fence: a conductor may enqueue, but draining
// the backlog is an operator-only action.
func TestConductorCannotDrain(t *testing.T) {
	ln, sock, _ := listen(t)
	go func() { _ = server.New(ln).Serve() }()

	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "hello", Role: "conductor", Self: ""}); err != nil {
		t.Fatalf("hello conductor: %v", err)
	}
	recv(t, c) // welcome
	recv(t, c) // panels snapshot

	if err := c.Send(proto.Command{Action: "task.drain"}); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if got := recv(t, c); got.Type != "error" || !strings.Contains(got.Error, "operator") {
		t.Fatalf("a conductor draining the backlog should be refused, got %+v", got)
	}
}
