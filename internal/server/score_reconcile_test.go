package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
)

// This file covers the read path's half of #38's invariant I2: every render,
// list, and status reconciles score.md first, so an operator editing the file
// in their own editor sees it take effect on the next dispatch rather than on
// the next restart — and a restart returns every panel as Exited.

// editScoreMD rewrites score.md the way an operator's editor would, and pushes
// its mtime forward so the change is visible to the store's fingerprint gate on
// a filesystem with coarse timestamps.
func editScoreMD(t *testing.T, dir, content string) {
	t.Helper()
	path := filepath.Join(dir, "score.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write score.md: %v", err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes score.md: %v", err)
	}
}

// TestLiveEditReachesTheNextDispatch is #38's third verification check, end to
// end: one editor save carrying all three edit kinds, reflected in the bytes
// the very next dispatch delivers.
func TestLiveEditReachesTheNextDispatch(t *testing.T) {
	st, dir := scoreStore(t)
	reworded, _, err := st.Submit("prefer table-driven tests", score.Provenance{Source: "user"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, _, err := st.Submit("drop this one", score.Provenance{Source: "user"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, _, delivered := scoreServer(st)

	// One save: reword one line, delete another, add a third with no id.
	editScoreMD(t, dir, "- ["+reworded.Id+"] prefer table-driven tests, always\n- ask before force-pushing\n")

	cc := conn("")
	s.onCommand(cc, proto.Command{Action: "panel.dispatch", ID: "p1", Prompt: "fix the login flow"})
	noError(t, cc)

	got := string(*delivered)
	if !strings.Contains(got, "prefer table-driven tests, always") {
		t.Errorf("the reworded entry did not reach the brief:\n%s", got)
	}
	if strings.Contains(got, "drop this one") {
		t.Errorf("the deleted entry still reaches the brief:\n%s", got)
	}
	if !strings.Contains(got, "ask before force-pushing") {
		t.Errorf("the added entry did not reach the brief:\n%s", got)
	}
}

// TestLiveEditReachesScoreListAndStatus checks the other two read paths: the
// cockpit's list and status must not answer from a view the operator has
// already moved past either.
func TestLiveEditReachesScoreListAndStatus(t *testing.T) {
	st, dir := scoreStore(t)
	if _, _, err := st.Submit("one real entry", score.Provenance{Source: "user"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s, _, _ := scoreServer(st)

	editScoreMD(t, dir, "- a line the operator typed\n- and another\n")

	entries := listed(t, s, "").Entries
	// By membership, not by position: score.list is RANKED, so which of the two
	// leads it is the ranking's business and not this test's.
	texts := map[string]bool{}
	for _, e := range entries {
		texts[e.Text] = true
	}
	if len(entries) != 2 || !texts["a line the operator typed"] || !texts["and another"] {
		t.Fatalf("score.list = %+v, want the operator's two lines", entries)
	}

	if got := status(t, s); got.Entries != 2 || got.Rendered != 2 {
		t.Fatalf("score.status = %+v, want both counts from the edited file", got)
	}
}

// TestReconcileOnADisabledStoreIsInert keeps the disabled contract on the new
// read hook: a server holding no store must not error or block on it.
func TestReconcileOnADisabledStoreIsInert(t *testing.T) {
	s, _, _ := scoreServer(nil)
	s.scoreView(score.Context{})
	cc := conn("")
	s.scoreList(cc, proto.Command{Action: "score.list"})
	if got := string(reply(t, cc).Score); got != `{"context":{},"entries":[]}` {
		t.Fatalf("score.list on a disabled store = %s, want an empty list under an empty context", got)
	}
}
