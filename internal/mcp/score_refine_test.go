package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
)

// TestMCPScoreRefineTools covers the conductor's three corrections on this
// surface: they are listed, their own argument guards are tool errors, and — on
// a connection that is not the conductor panel's, which a test harness and an
// ordinary agent panel both are — the daemon's refusal reaches the model as a
// tool error rather than as silence.
//
// The tool list is built once per binary and cannot know which panel the process
// was started in, so these tools EXIST everywhere and answer with the gate's
// refusal where they do not apply. That is the honest shape, and it is the one
// the model has to be able to read.
func TestMCPScoreRefineTools(t *testing.T) {
	sock, _ := startScoredServer(t)

	resps := run(t, sock,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"score_merge","arguments":{"id":"aaa111","from":"bbb222"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"score_reword","arguments":{"id":"aaa111","text":"a better wording"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"score_lower","arguments":{"id":"aaa111"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"score_merge","arguments":{"id":"aaa111"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"score_lower","arguments":{}}}`,
	)
	if len(resps) != 6 {
		t.Fatalf("want 6 responses, got %d", len(resps))
	}

	listed := map[string]bool{}
	tools, _ := resps[0].Result["tools"].([]any)
	for _, tl := range tools {
		if m, ok := tl.(map[string]any); ok {
			name, _ := m["name"].(string)
			listed[name] = true
		}
	}
	for _, name := range []string{"score_merge", "score_reword", "score_lower"} {
		if !listed[name] {
			t.Errorf("tools/list should include %s", name)
		}
	}

	// The gate's refusal, three times: this connection declared no panel at all,
	// so it is not the conductor's however it asks.
	for _, i := range []int{1, 2, 3} {
		if resps[i].Result["isError"] != true {
			t.Fatalf("response %d should be a tool error, got %+v", i+1, resps[i].Result)
		}
		if txt := contentText(t, resps[i].Result); !strings.Contains(txt, "only the conductor") {
			t.Fatalf("response %d = %q, want the conductor-only refusal", i+1, txt)
		}
	}

	// The handlers' own guards, which never reach the daemon at all.
	for _, i := range []int{4, 5} {
		if resps[i].Result["isError"] != true {
			t.Fatalf("response %d should be a tool error, got %+v", i+1, resps[i].Result)
		}
		if txt := contentText(t, resps[i].Result); !strings.Contains(txt, "required") {
			t.Fatalf("response %d = %q, want a missing-argument error", i+1, txt)
		}
	}
}

// conductorFleet starts a daemon with a live score store and a real conductor
// panel, seeds n entries, and returns the socket, the store, and the conductor's
// panel id. /bin/cat stands in for the agent CLI: it holds its pty open, so the
// panel does not exit out from under the test.
func conductorFleet(t *testing.T, n int) (sock string, st *score.Store, conductor string, ids []string) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // keep the workspace out of the real runtime dir

	sock, st = startScoredServer(t)
	for i := range n {
		e, _, serr := st.Submit(fmt.Sprintf("observation number %d", i), score.Provenance{Source: score.SourceUser})
		if serr != nil {
			t.Fatalf("submit: %v", serr)
		}
		ids = append(ids, e.Id)
	}

	c, err := control.DialSocket(sock, "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if conductor, err = c.Spawn(proto.Command{
		Action: "panel.create", Kind: "agent", Path: "/bin/cat", Conductor: true,
	}); err != nil {
		t.Fatalf("create conductor: %v", err)
	}
	return sock, st, conductor, ids
}

// runAs feeds the newline-joined requests through an MCP server wired to a live
// baton over sock, declaring self on every dial — which is what control.Dial does
// inside a conductor panel, reading BATON_PANEL_ID out of the injected
// environment. It reuses the production dial-per-call path, so each tool call
// below really does open and close its own connection.
//
// It is the one of these, and run() is it with no panel declared: the identity is
// the only thing the two ever differed in, and a second copy of the serve-and-
// decode loop is a second place for a decoding assumption to drift.
func runAs(t *testing.T, sock, self string, reqs ...string) []testResp {
	t.Helper()
	s := New("9.9.9")
	s.dial = func() (*control.Client, error) { return control.DialSocket(sock, "", self) }

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

// TestMCPRefineIsThrottled measures the cap on the path it actually broke on. Everything above tests the refine verbs through a socket a
// test holds open; THIS drives them the way the conductor does — through the MCP
// server, which dials a fresh connection for every tool call and closes it
// (callTool) — and that is the only shape in which the defect was visible.
//
// The cap used to keep its stamp on the clientConn, so the state it depended on
// was destroyed between two calls of a loop: measured at sixty merges in three
// seconds, all sixty admitted, a sixty-one entry store collapsed to one, while a
// persistent-connection test of the very same cap passed. It is keyed on the
// conductor's PANEL now, which is the identity the gate already used.
func TestMCPRefineIsThrottled(t *testing.T) {
	sock, st, conductor, ids := conductorFleet(t, 12)

	var calls []string
	for i, id := range ids[1:] {
		calls = append(calls, fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"score_merge","arguments":{"id":%q,"from":%q}}}`,
			i+1, ids[0], id))
	}
	start := time.Now()
	resps := runAs(t, sock, conductor, calls...)
	elapsed := time.Since(start)

	if len(resps) != len(calls) {
		t.Fatalf("want %d responses, got %d", len(calls), len(resps))
	}
	var merged, throttled int
	for i, r := range resps {
		txt := contentText(t, r.Result)
		switch {
		case r.Result["isError"] != true && strings.Contains(txt, "merged "):
			merged++
		case strings.Contains(txt, "too fast"):
			throttled++
		default:
			t.Fatalf("call %d answered %q, want a merge or the rate refusal", i+1, txt)
		}
	}
	// One per gap. The loop runs far faster than minRefineGap, so the honest
	// assertion is "at most what the elapsed time allows", not a bare 1 — a slow
	// machine may legitimately let a second through.
	if want := int(elapsed/(250*time.Millisecond)) + 1; merged > want {
		t.Fatalf("%d of %d merges were admitted in %v, want no more than %d", merged, len(calls), elapsed, want)
	}
	if merged+throttled != len(calls) || throttled == 0 {
		t.Fatalf("%d merged and %d throttled of %d: the cap is not holding on the MCP path",
			merged, throttled, len(calls))
	}
	// And the store is what that arithmetic says. Before the fix this was 1.
	if got, want := st.Len(), len(ids)-merged; got != want {
		t.Fatalf("store holds %d entries after %d admitted merges, want %d", got, merged, want)
	}
	t.Logf("MCP path: %d calls in %v, %d merged, %d throttled, store %d -> %d",
		len(calls), elapsed, merged, throttled, len(ids), st.Len())
}
