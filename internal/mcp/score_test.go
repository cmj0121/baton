package mcp

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	)
	// The repeat comes from a SECOND panel, because the daemon caps submissions
	// per actor and these calls run at machine speed: two agents saying the same
	// thing is what folding is for, and it keeps this test about the tool's
	// wording rather than about the pacing. TestMCPSubmitIsCapped is where the
	// cap itself is measured on this same path.
	resps = append(resps, runAs(t, sock, "p2",
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"score_submit","arguments":{"text":"This fleet races agents on the same brief."}}}`,
	)...)
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

// TestMCPSubmitIsCapped measures the submit cap on the path it has to hold, and
// the only path a looping agent ever uses. Everything above drives score_submit
// through a socket a test holds open; THIS is the shape `baton mcp` really has —
// a fresh connection dialled and closed for every tool call (callTool).
//
// That shape is what killed the first version of the other two caps: their stamp
// lived on the clientConn, so the state each depended on was destroyed between
// two calls of a loop and both were inert on exactly the path an LLM drives. The
// submit cap is keyed on the panel from the day it was written, and this is what
// says so rather than the comment claiming it.
//
// The growth it stops was measured on real daemons: eight concurrent connections
// sustained 73 submissions a second against a HEALTHY store, 222 B of fold
// record each, 1.47 GB of log a day.
//
// What lands here is the burst allowance and then one a second. That the opening
// handful IS admitted is the point rather than a leak: nothing on this path can
// tell four repeats from four distinct observations until the store folds them,
// and the sustained rate — which is the whole bound on the log — is unchanged.
func TestMCPSubmitIsCapped(t *testing.T) {
	sock, st := startScoredServer(t)

	const calls = 60
	var reqs []string
	for i := range calls {
		// The retry loop's own shape: the SAME observation every time, which folds
		// rather than creating an entry. The growth is one fold record per
		// attempt, so a cap watching new entries would stop nothing.
		reqs = append(reqs, fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"score_submit",`+
				`"arguments":{"text":"the fleet keeps retrying a failed submission"}}}`, i+1))
	}
	start := time.Now()
	resps := runAs(t, sock, "p1", reqs...)
	elapsed := time.Since(start)

	if len(resps) != calls {
		t.Fatalf("want %d responses, got %d", calls, len(resps))
	}
	var recorded, capped int
	for i, r := range resps {
		txt := contentText(t, r.Result)
		switch {
		case strings.Contains(txt, "recorded as "), strings.Contains(txt, "folded into "):
			recorded++
		case strings.Contains(txt, "too fast"):
			capped++
		default:
			t.Fatalf("call %d answered %q, want a submission or the rate refusal", i+1, txt)
		}
	}
	// The burst allowance, then one per gap — bounded by the elapsed time rather
	// than by a bare figure, because a slow machine may legitimately let another
	// second through. submitBurst is unexported next to the cap it belongs to;
	// this is a BOUND, so a server that widened its burst fails here loudly
	// rather than drifting past a stale number in silence.
	const burst = 4
	if want := int(elapsed/time.Second) + burst; recorded > want {
		t.Fatalf("%d of %d submissions landed in %v, want no more than %d (a burst of %d plus one a "+
			"second): the cap is dead on the MCP path, which is the only path an agent's retry loop "+
			"uses", recorded, calls, elapsed, want, burst)
	}
	if capped == 0 {
		t.Fatalf("all %d submissions landed: the cap is not holding on the MCP path", calls)
	}
	if got := st.Len(); got != 1 {
		t.Fatalf("store holds %d entries, want 1: a repeat folds, so the loop never makes a second", got)
	}
	t.Logf("MCP path: %d calls in %v, %d recorded, %d capped", calls, elapsed, recorded, capped)
}
