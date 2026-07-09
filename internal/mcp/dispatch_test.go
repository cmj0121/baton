package mcp

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/cmj0121/baton/internal/control"
)

// TestHandlePing covers the JSON-RPC keepalive: ping is answered with an empty
// result and no error, so a client's liveness probe does not read as a failure.
func TestHandlePing(t *testing.T) {
	s := New("9.9.9")
	resp, reply := s.handle(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "ping"})
	if !reply {
		t.Fatal("ping must be answered")
	}
	if resp.Error != nil || resp.Result == nil {
		t.Fatalf("ping should be an empty-result success, got %+v", resp)
	}
}

// TestCallToolBadParams covers the malformed-arguments branch: an unparseable
// tools/call params blob is a tool-level error the model can read, not a crash.
func TestCallToolBadParams(t *testing.T) {
	s := New("9.9.9")
	got := s.callTool(json.RawMessage(`{"name":`))
	if got["isError"] != true {
		t.Fatalf("malformed tool-call params should be a tool error, got %+v", got)
	}
}

// TestCallToolDialError covers the socket-down branch: when the tool cannot reach
// the control socket, the failure surfaces as an isError tool result rather than
// tearing down the call.
func TestCallToolDialError(t *testing.T) {
	s := New("9.9.9")
	s.dial = func() (*control.Client, error) { return nil, errors.New("no socket") }
	got := s.callTool(json.RawMessage(`{"name":"baton_list","arguments":{}}`))
	if got["isError"] != true {
		t.Fatalf("a dial failure should be a tool error, got %+v", got)
	}
}
