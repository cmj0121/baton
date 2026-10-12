package main

import (
	"encoding/json"
	"io"
	"os"
	"slices"
	"testing"

	"github.com/cmj0121/baton/internal/mcp"
)

// TestMcpMainWriteError covers mcpMain's failure branch (exit code 1): stdin
// carries one framed message that produces a reply, but stdout's read end is
// closed, so the server's reply write fails and Serve returns an error. Using a
// fresh pipe (not fd 1) means the failed write yields EPIPE rather than the
// SIGPIPE crash the runtime reserves for the real stdout.
func TestMcpMainWriteError(t *testing.T) {
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	go func() {
		_, _ = inW.Write([]byte("{malformed json}\n")) // a frame that draws a parse-error reply
		_ = inW.Close()
	}()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	_ = outR.Close() // the reply write now fails with EPIPE

	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() {
		os.Stdin, os.Stdout = oldIn, oldOut
		_ = inR.Close()
		_ = outW.Close()
	}()

	if code := mcpMain(false); code != 1 {
		t.Fatalf("mcpMain with a broken stdout = %d, want 1", code)
	}
}

// mcpToolNames runs mcpMain over one tools/list request and returns the names it
// lists, in order.
func mcpToolNames(t *testing.T, worker bool) []string {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	go func() {
		_, _ = inW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"))
		_ = inW.Close()
	}()
	read := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(outR)
		read <- data
	}()

	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	code := mcpMain(worker)
	os.Stdin, os.Stdout = oldIn, oldOut
	_ = outW.Close()
	_ = inR.Close()
	if code != 0 {
		t.Fatalf("mcpMain(%v) = %d, want 0", worker, code)
	}

	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(<-read, &resp); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	var names []string
	for _, tl := range resp.Result.Tools {
		names = append(names, tl.Name)
	}
	return names
}

// TestMcpMainSelectsTheTable: `baton mcp --score-only` — the flag main.go turns
// into worker=true, and the one written into every worker panel's MCP config —
// serves the worker table, and a bare `baton mcp` serves the conductor's, which
// has no peer tool in it.
func TestMcpMainSelectsTheTable(t *testing.T) {
	want := []string{"score_submit", "baton_peers", "baton_peek", "baton_tell"}
	if got := mcpToolNames(t, true); !slices.Equal(got, want) {
		t.Errorf("worker tools/list = %v, want %v", got, want)
	}
	full := mcpToolNames(t, false)
	if len(full) != len(mcp.ToolNames()) || !slices.Contains(full, "baton_spawn") {
		t.Errorf("the conductor's tools/list should be the fleet-control table, got %v", full)
	}
	for _, name := range want[1:] {
		if slices.Contains(full, name) {
			t.Errorf("the conductor's table must not carry %q", name)
		}
	}
}
