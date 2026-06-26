package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/server"
)

type testResp struct {
	ID     json.RawMessage `json:"id"`
	Result map[string]any  `json:"result"`
	Error  *rpcError       `json:"error"`
}

// run feeds the newline-joined requests through a server wired to a live baton
// over sock, and returns the decoded responses in order.
func run(t *testing.T, sock string, reqs ...string) []testResp {
	t.Helper()
	s := New("9.9.9")
	s.dial = func() (*control.Client, error) { return control.DialSocket(sock, "", "") }

	in := strings.NewReader(strings.Join(reqs, "\n"))
	var out bytes.Buffer
	if err := s.Serve(in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}

	var resps []testResp
	dec := json.NewDecoder(&out)
	for {
		var r testResp
		if err := dec.Decode(&r); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode response: %v", err)
		}
		resps = append(resps, r)
	}
	return resps
}

func startServer(t *testing.T) string {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = server.New(ln).Serve() }()
	return sock
}

func TestMCPHandshakeAndTools(t *testing.T) {
	sock := startServer(t)

	resps := run(t, sock,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	// The notification draws no response: initialize and tools/list only.
	if len(resps) != 2 {
		t.Fatalf("want 2 responses, got %d: %+v", len(resps), resps)
	}

	// initialize: names baton and advertises the tools capability.
	si, _ := resps[0].Result["serverInfo"].(map[string]any)
	if si["name"] != "baton" {
		t.Fatalf("initialize serverInfo = %+v", resps[0].Result)
	}
	if resps[0].Result["protocolVersion"] != "2024-11-05" {
		t.Fatalf("initialize should echo the protocol version, got %v", resps[0].Result["protocolVersion"])
	}

	// tools/list: the fleet-control tools are present.
	tools, _ := resps[1].Result["tools"].([]any)
	got := map[string]bool{}
	for _, tl := range tools {
		if m, ok := tl.(map[string]any); ok {
			got[m["name"].(string)] = true
		}
	}
	for _, want := range []string{"baton_list", "baton_spawn", "baton_send", "baton_dispatch", "baton_dispatch_group", "baton_enqueue", "baton_queue", "baton_group", "baton_close"} {
		if !got[want] {
			t.Fatalf("tools/list missing %q, got %v", want, got)
		}
	}
}

func TestMCPToolCalls(t *testing.T) {
	sock := startServer(t)

	resps := run(t, sock,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"baton_spawn","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"baton_list","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"frobnicate"}`,
	)
	if len(resps) != 4 {
		t.Fatalf("want 4 responses, got %d", len(resps))
	}

	// spawn returns a text block naming the new panel.
	if txt := contentText(t, resps[0].Result); !strings.Contains(txt, "spawned panel") {
		t.Fatalf("spawn result = %q", txt)
	}

	// list returns the fleet JSON, which now includes the spawned panel.
	if txt := contentText(t, resps[1].Result); !strings.Contains(txt, `"id"`) {
		t.Fatalf("list result = %q", txt)
	}

	// an unknown tool is a tool-level error (isError), not a transport error.
	if resps[2].Result["isError"] != true {
		t.Fatalf("unknown tool should be an error result, got %+v", resps[2].Result)
	}

	// an unknown JSON-RPC method is a transport-level error.
	if resps[3].Error == nil || resps[3].Error.Code != -32601 {
		t.Fatalf("unknown method should be -32601, got %+v", resps[3].Error)
	}
}

// TestMCPAllTools calls every tool so each handler closure and the arg accessors
// (str, boolDefault, strSlice) run. The first spawn is panel id "1", which the
// later calls target; whether the server accepts or rejects each, the tool
// handler executes and returns a result (never a protocol error).
func TestMCPAllTools(t *testing.T) {
	sock := startServer(t)

	resps := run(t, sock,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"baton_spawn","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"baton_spawn","arguments":{"agent":"/bin/cat","args":["-u"],"dir":"/tmp"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"baton_list","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"baton_send","arguments":{"id":"1","text":"hi","submit":false}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"baton_send","arguments":{"id":"1","text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"baton_send","arguments":{"text":"no id"}}}`,
		`{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"baton_dispatch","arguments":{"id":"1","prompt":"do the thing"}}}`,
		`{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"baton_dispatch","arguments":{"prompt":"no id"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"baton_group","arguments":{"name":"g","ids":["1"]}}}`,
		`{"jsonrpc":"2.0","id":15,"method":"tools/call","params":{"name":"baton_dispatch_group","arguments":{"group":"g","prompt":"go"}}}`,
		`{"jsonrpc":"2.0","id":16,"method":"tools/call","params":{"name":"baton_dispatch_group","arguments":{"prompt":"no group"}}}`,
		`{"jsonrpc":"2.0","id":17,"method":"tools/call","params":{"name":"baton_enqueue","arguments":{"prompt":"queued work","group":"g"}}}`,
		`{"jsonrpc":"2.0","id":18,"method":"tools/call","params":{"name":"baton_queue","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"baton_rename","arguments":{"id":"1","name":"r"}}}`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"baton_pin","arguments":{"ids":["1"]}}}`,
		`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"baton_unpin","arguments":{"ids":["1"]}}}`,
		`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"baton_signal","arguments":{"signal":"SIGCONT","ids":["1"]}}}`,
		`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"baton_close","arguments":{"ids":["1"]}}}`,
	)
	if len(resps) != 18 {
		t.Fatalf("want 18 responses, got %d", len(resps))
	}
	for i, r := range resps {
		if r.Error != nil {
			t.Fatalf("call %d returned a protocol error: %+v", i+1, r.Error)
		}
		if r.Result == nil {
			t.Fatalf("call %d returned no result", i+1)
		}
	}
	// The missing-id send reports a tool-level error the model can read.
	if resps[5].Result["isError"] != true {
		t.Fatalf("baton_send without an id should be a tool error, got %+v", resps[5].Result)
	}
	// baton_dispatch with an id succeeds; without one it is a tool-level error.
	if txt := contentText(t, resps[6].Result); !strings.Contains(txt, "dispatched to panel") {
		t.Fatalf("baton_dispatch result = %q", txt)
	}
	if resps[7].Result["isError"] != true {
		t.Fatalf("baton_dispatch without an id should be a tool error, got %+v", resps[7].Result)
	}
	// baton_dispatch_group with a group succeeds; without one it is a tool error.
	if txt := contentText(t, resps[9].Result); !strings.Contains(txt, "dispatched to group") {
		t.Fatalf("baton_dispatch_group result = %q", txt)
	}
	if resps[10].Result["isError"] != true {
		t.Fatalf("baton_dispatch_group without a group should be a tool error, got %+v", resps[10].Result)
	}
}

func contentText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("result has no content: %+v", result)
	}
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)
	return text
}
