package mcp

import (
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/score"
	"github.com/cmj0121/baton/internal/server"
)

// startScoredServer is startServer with a live score store attached, so
// score_submit lands on a store that accepts instead of refusing. The store comes
// back with the socket: a test that seeds or reads it needs the same one the
// daemon was given, and opening a second in the same directory is a lock error.
func startScoredServer(t *testing.T) (string, *score.Store) {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	st, err := score.Open(t.TempDir(), score.Policy{})
	if err != nil {
		t.Fatalf("open score store: %v", err)
	}
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// One option carries the store and the config knob together, so a stand-in
	// daemon cannot report a live store as a disabled subsystem.
	state := server.ScoreState{Store: st, Enabled: true}
	go func() { _ = server.New(ln, server.WithScore(state)).Serve() }()
	return sock, st
}

// TestMCPScoreSubmit covers the one score tool this surface deliberately has:
// it is listed, a submission calls through to the store and answers with the
// created id, and an empty text is the handler's own guard, a tool error.
func TestMCPScoreSubmit(t *testing.T) {
	sock, _ := startScoredServer(t)

	resps := run(t, sock,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"score_submit","arguments":{"text":"this fleet races agents on the same brief"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"score_submit","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"score_submit","arguments":{"text":"This fleet races agents on the same brief."}}}`,
	)
	if len(resps) != 4 {
		t.Fatalf("want 4 responses, got %d", len(resps))
	}

	tools, _ := resps[0].Result["tools"].([]any)
	found := false
	for _, tl := range tools {
		if m, ok := tl.(map[string]any); ok && m["name"] == "score_submit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tools/list should include score_submit, got %+v", resps[0].Result)
	}

	if txt := contentText(t, resps[1].Result); !strings.Contains(txt, "recorded as ") {
		t.Fatalf("score_submit result = %q", txt)
	}
	if resps[2].Result["isError"] != true {
		t.Fatalf("score_submit without text should be a tool error, got %+v", resps[2].Result)
	}
	// The same observation again is FOLDED, and the tool says so: an agent that
	// submits freely learns which of its notes the fleet already had.
	if txt := contentText(t, resps[3].Result); !strings.Contains(txt, "folded into ") {
		t.Fatalf("a repeat should come back as a fold, got %q", txt)
	}
}

// TestMCPScoreSubmitDisabled proves the server's plain refusal reaches the
// model as a tool-level error when no score store is wired.
func TestMCPScoreSubmitDisabled(t *testing.T) {
	sock := startServer(t)

	resps := run(t, sock,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"score_submit","arguments":{"text":"remember me"}}}`,
	)
	if len(resps) != 1 || resps[0].Result["isError"] != true {
		t.Fatalf("score_submit on a disabled store should be a tool error, got %+v", resps)
	}
	if txt := contentText(t, resps[0].Result); !strings.Contains(txt, "disabled") {
		t.Fatalf("the refusal should say the store is disabled, got %q", txt)
	}
}
