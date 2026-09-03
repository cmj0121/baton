package control_test

import (
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/score"
	"github.com/cmj0121/baton/internal/server"
)

// startScoredServer is startServer with a live score store attached, so the
// score.* verbs answer instead of refusing. The store lives in its own temp
// dir; the socket keeps a short name to stay under the unix path cap.
func startScoredServer(t *testing.T) string {
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
	t.Cleanup(func() { _ = ln.Close(); st.Close() })
	// The store and the config knob are separate states on the wire (score.status
	// reports both), so a server standing in for a healthy daemon declares both.
	state := server.ScoreState{Store: st, Enabled: true}
	srv := server.New(ln, server.WithScore(state))
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { srv.Shutdown() })
	return sock
}

// TestScoreRoundtrip drives the fleet-memory wrappers over a real socket:
// submit returns the created entry's id, list shows it, and status reports the
// running subsystem. The first call lands right after Dial, while the seed
// "stats" push is still queued on the connection — proving the score read loop
// tolerates interleaved pushes of other types until its "score" reply arrives.
func TestScoreRoundtrip(t *testing.T) {
	sock := startScoredServer(t)

	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	id, folded, err := c.ScoreSubmit("agents in this fleet forget to run the linter")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if id == "" {
		t.Fatal("submit returned an empty id")
	}
	if folded {
		t.Fatal("a first submission came back as a fold")
	}
	// The same observation again comes back as the same entry, said to have
	// folded — #38's "new or folded into id", which is what lets the CLI and the
	// MCP tool tell an agent the fleet already knew this.
	// From a SECOND panel, because the daemon caps submissions per actor and
	// these two calls are back to back: a fold is a property of the store's
	// normalisation rather than of one connection, and two agents saying the same
	// thing is exactly what it is for.
	c2, err := control.DialSocket(sock, "", "p2", "")
	if err != nil {
		t.Fatalf("dial the second panel: %v", err)
	}
	defer func() { _ = c2.Close() }()
	again, folded, err := c2.ScoreSubmit("Agents in this fleet forget to run the linter.")
	if err != nil {
		t.Fatalf("submit repeat: %v", err)
	}
	if again != id || !folded {
		t.Fatalf("repeat = (%q, folded=%v), want (%q, folded=true)", again, folded, id)
	}

	list, err := c.ScoreList("")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listed struct {
		Context score.Context  `json:"context"`
		Entries []score.Ranked `json:"entries"`
	}
	if err := json.Unmarshal([]byte(list), &listed); err != nil {
		t.Fatalf("ScoreList should be a JSON object, got %q: %v", list, err)
	}
	entries := listed.Entries
	// Ranked against nothing, because this client named no panel — and the echo
	// is what says so, rather than leaving the operator to infer it from the
	// column of 1.0s in every breakdown.
	if listed.Context != (score.Context{}) {
		t.Fatalf("ScoreList(\"\") echoed %+v, want the empty context", listed.Context)
	}
	if !strings.Contains(list, id) {
		t.Fatalf("the submitted entry %q should be in the list, got:\n%s", id, list)
	}

	st, err := c.ScoreStatus()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var got struct {
		Enabled   bool   `json:"enabled"`
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
		Entries   int    `json:"entries"`
		Rendered  int    `json:"rendered"`
		Dir       string `json:"dir"`
	}
	if err := json.Unmarshal([]byte(st), &got); err != nil {
		t.Fatalf("ScoreStatus should be a JSON object, got %q: %v", st, err)
	}
	// entries counts what is on disk and rendered what one brief would be told;
	// score.list answers with the ENTRIES, uncapped, since #42 (the working set
	// is the `active` flag on each of them). The two counts are equal here only
	// because one entry is under any budget. A running store is on AND
	// available, and has no reason to give.
	if !got.Enabled || !got.Available || got.Reason != "" || got.Entries != len(entries) || got.Rendered != 1 || got.Dir == "" {
		t.Fatalf("status should report the running store, got %+v (list has %d)", got, len(entries))
	}
}

// TestScoreDisabled covers the nil-store server: a submission is refused
// plainly, while the read verbs still answer (an empty list, a disabled
// status) — the client surfaces each as the server sent it.
func TestScoreDisabled(t *testing.T) {
	sock := startServer(t)

	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	if _, _, err := c.ScoreSubmit("remember me"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("submit on a disabled store should be refused plainly, got %v", err)
	}
	list, err := c.ScoreList("")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.TrimSpace(list) != `{"context":{},"entries":[]}` {
		t.Fatalf("a disabled store should list no entries under an empty context, got %q", list)
	}
	st, err := c.ScoreStatus()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(st, `"enabled":false`) {
		t.Fatalf("a disabled store should say so, got %q", st)
	}

	// Once the connection is closed, every wrapper fails fast on the send.
	_ = c.Close()
	if _, _, err := c.ScoreSubmit("after close"); err == nil {
		t.Fatal("submit on a closed client should error")
	}
	if _, err := c.ScoreList(""); err == nil {
		t.Fatal("list on a closed client should error")
	}
	if _, err := c.ScoreStatus(); err == nil {
		t.Fatal("status on a closed client should error")
	}
}
