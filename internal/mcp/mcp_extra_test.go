package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// errReader always fails, so Serve's read loop hits a non-EOF error and returns
// it — the branch that distinguishes a real transport fault from a clean EOF.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom read") }

// errWriter fails every Write, so Serve's encode step returns the encoder error
// instead of looping — the write-fault branch.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("boom write") }

// TestServeReadError covers Serve's non-EOF read-error branch: a reader that
// faults (not EOF) makes Serve return the error rather than exit cleanly.
func TestServeReadError(t *testing.T) {
	s := New("9.9.9")
	if err := s.Serve(errReader{}, &bytes.Buffer{}); err == nil {
		t.Fatal("Serve should surface a non-EOF read error")
	}
}

// TestServeEncodeError covers Serve's encode-error branch: when the response
// cannot be written, Serve returns the encoder error. tools/list produces a reply
// without dialing the socket, so no live server is needed.
func TestServeEncodeError(t *testing.T) {
	s := New("9.9.9")
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	if err := s.Serve(in, errWriter{}); err == nil {
		t.Fatal("Serve should surface an encode error")
	}
}

// TestMCPToolErrorBranches drives every reachable tool error path: each command
// is given input the live server rejects, so the handler's `return "", err`
// surfaces as an isError tool result. This exercises the failure side of the tool
// closures that TestMCPAllTools only covers on the success side.
func TestMCPToolErrorBranches(t *testing.T) {
	sock := startServer(t)

	resps := run(t, sock,
		// spawn an agent whose binary cannot exec, so SpawnPanel fails.
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"baton_spawn","arguments":{"agent":"/nonexistent/definitely-not-a-binary"}}}`,
		// dispatch to an unknown panel -> server error.
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"baton_dispatch","arguments":{"id":"999","prompt":"x"}}}`,
		// dispatch-group to an unknown group -> server error.
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"baton_dispatch_group","arguments":{"group":"nope","prompt":"x"}}}`,
		// enqueue with an empty prompt -> server error.
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"baton_enqueue","arguments":{"prompt":""}}}`,
		// rename with an empty name -> server error (name is required by the server).
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"baton_rename","arguments":{"id":"1","name":""}}}`,
		// group with an invalid (empty) name -> server error.
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"baton_group","arguments":{"name":"","ids":["1"]}}}`,
		// pin an unknown panel -> server error.
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"baton_pin","arguments":{"ids":["999"]}}}`,
		// unpin an unknown panel -> server error.
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"baton_unpin","arguments":{"ids":["999"]}}}`,
		// signal with an unknown signal name -> server error.
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"baton_signal","arguments":{"signal":"NOTASIGNAL","ids":["1"]}}}`,
		// close with no ids -> server error.
		`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"baton_close","arguments":{"ids":[]}}}`,
	)
	if len(resps) != 10 {
		t.Fatalf("want 10 responses, got %d", len(resps))
	}
	for i, r := range resps {
		if r.Error != nil {
			t.Fatalf("call %d returned a protocol error, want a tool result: %+v", i+1, r.Error)
		}
		if r.Result["isError"] != true {
			t.Fatalf("call %d should be a tool-level error, got %+v", i+1, r.Result)
		}
	}
}

// TestMCPEnqueueSpawnOnDemand covers the spawn-on-demand branch of baton_enqueue
// (command != ""): a successful EnqueueSpawn, then the same with an empty prompt
// to hit its error return.
func TestMCPEnqueueSpawnOnDemand(t *testing.T) {
	sock := startServer(t)

	resps := run(t, sock,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"baton_enqueue","arguments":{"prompt":"burst work","command":"/bin/cat","args":["-u"],"dir":"/tmp","close":true}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"baton_enqueue","arguments":{"prompt":"","command":"/bin/cat"}}}`,
	)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses, got %d", len(resps))
	}
	if txt := contentText(t, resps[0].Result); !strings.Contains(txt, "spawn-on-demand") {
		t.Fatalf("spawn-on-demand enqueue = %q", txt)
	}
	if resps[1].Result["isError"] != true {
		t.Fatalf("spawn-on-demand enqueue with empty prompt should be a tool error, got %+v", resps[1].Result)
	}
}

// TestMCPReorder covers baton_reorder end to end: the id-required guard, the
// invalid-`to` default, both head and tail success paths against a real queued
// task, and the head/tail error paths against an unknown task id.
func TestMCPReorder(t *testing.T) {
	sock := startServer(t)

	// Enqueue one task with no free agent to drain it, so it stays queued and can
	// be reordered, then read the backlog to learn its id.
	seed := run(t, sock,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"baton_enqueue","arguments":{"prompt":"queued work"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"baton_queue","arguments":{}}}`,
	)
	var tasks []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(contentText(t, seed[1].Result)), &tasks); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatalf("expected a queued task, got %s", contentText(t, seed[1].Result))
	}
	id := tasks[0].ID

	call := func(args string) map[string]any {
		return run(t, sock, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"baton_reorder","arguments":`+args+`}}`)[0].Result
	}

	// Missing id -> the handler's own guard, a tool error.
	if r := call(`{"to":"head"}`); r["isError"] != true {
		t.Fatalf("reorder without id should be a tool error, got %+v", r)
	}
	// Invalid destination -> the default branch, a tool error.
	if r := call(`{"id":"` + id + `","to":"sideways"}`); r["isError"] != true {
		t.Fatalf("reorder with a bad `to` should be a tool error, got %+v", r)
	}
	// Promote then demote the real queued task -> both success paths.
	if txt := contentText(t, call(`{"id":"`+id+`","to":"head"}`)); !strings.Contains(txt, "promoted") {
		t.Fatalf("reorder head = %q", txt)
	}
	if txt := contentText(t, call(`{"id":"`+id+`","to":"tail"}`)); !strings.Contains(txt, "demoted") {
		t.Fatalf("reorder tail = %q", txt)
	}
	// Unknown id -> the head and tail server-error branches.
	if r := call(`{"id":"no-such-task","to":"head"}`); r["isError"] != true {
		t.Fatalf("reorder head of an unknown task should be a tool error, got %+v", r)
	}
	if r := call(`{"id":"no-such-task","to":"tail"}`); r["isError"] != true {
		t.Fatalf("reorder tail of an unknown task should be a tool error, got %+v", r)
	}
}
