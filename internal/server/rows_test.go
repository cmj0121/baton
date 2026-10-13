package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/proto"
)

// rowsHome points $HOME at a fresh directory so the two row files are the
// test's own.
func rowsHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// fileLines is the file's lines, or nil when it does not exist.
func fileLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// recordingServer is a fleet with recording ON and one agent panel whose
// screen is the given text — the tail every event below reads.
func recordingServer(t *testing.T, state panel.State, screen, needle string) (*Server, *fakeClock) {
	t.Helper()
	rowsHome(t)
	s, clk, _ := gateServer(panel.Panel{ID: "p1", Kind: panel.Agent, State: state})
	s.pty = scriptPty(t, "p1", screen, needle)
	s.classifierRecord = true
	return s, clk
}

// notice drains a verb's reply for its notice text, "" when there was none.
func notice(cc *clientConn) string { return replyOf(cc, "notice") }

// seedRows writes body as the whole of the row file at path, making its
// directory first — a file the daemon finds already there.
func seedRows(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDeclarationWritesAPositiveRow is source 1 of D6: the agent's own
// declaration is the one label with no regex in play, so its tail at that
// instant lands in the rows file — content lines only, as one mold row.
func TestDeclarationWritesAPositiveRow(t *testing.T) {
	s, _ := recordingServer(t, panel.Running, "Your call, and then I continue", "I continue")

	s.declareAttention(ctl("p1"), proto.Command{Action: "panel.attention", Reason: "need a decision"})

	got := fileLines(t, paths.AttentionRows())
	want := `{"text":"Your call, and then I continue","keys":["attention"]}`
	if len(got) != 1 || got[0] != want {
		t.Fatalf("rows file = %q, want exactly [%s]", got, want)
	}
	if h := fileLines(t, paths.AttentionHoldout()); h != nil {
		t.Fatalf("a declaration is never a negative, got holdout %q", h)
	}
	if st, err := os.Stat(paths.AttentionRows()); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the rows file is scrollback on disk and must be private, got %v %v", st.Mode(), err)
	}
}

// TestDeclarationFromAShellWritesAPositive: the rows are the screens the
// classifier is asked about, and it is asked about shells too — so a shell's
// declaration labels its tail like an agent's does.
func TestDeclarationFromAShellWritesAPositive(t *testing.T) {
	rowsHome(t)
	s, _, _ := gateServer(panel.Panel{ID: "sh1", Kind: panel.Shell, State: panel.Running})
	s.pty = scriptPty(t, "sh1", "Password:", "Password")
	s.classifierRecord = true

	s.declareAttention(ctl(""), proto.Command{Action: "panel.attention", ID: "sh1", Reason: "typed by hand"})
	if got := stateOf(s, "sh1"); got != panel.Attention {
		t.Fatalf("the declaration itself still stands, got %v", got)
	}
	want := `{"text":"Password:","keys":["attention"]}`
	if got := fileLines(t, paths.AttentionRows()); len(got) != 1 || got[0] != want {
		t.Fatalf("rows file = %q, want exactly [%s]", got, want)
	}
}

// TestMarkWritesAPositiveRowAndLeavesTheStateAlone is source 2 of D6: an
// operator saying "this is a wait" from the inbox, a zoom or the CLI. The tail
// is the row; the panel's state is still the ladder's to decide; the reply
// says which row it became.
func TestMarkWritesAPositiveRowAndLeavesTheStateAlone(t *testing.T) {
	s, _ := recordingServer(t, panel.Idle, "Pick a base branch: ", "branch")
	cc := ctl("")

	s.onCommand(cc, proto.Command{Action: "attention.mark", ID: "p1"})
	if got := notice(cc); got != "marked as a wait (row 1)" {
		t.Fatalf("the reply should name the row, got %q", got)
	}
	if err := replyErr(cc); err != "" {
		t.Fatalf("mark: %v", err)
	}
	if got := stateOf(s, "p1"); got != panel.Idle {
		t.Fatalf("a mark is a label, not a verdict: state = %v, want idle", got)
	}
	want := `{"text":"Pick a base branch:","keys":["attention"]}`
	if got := fileLines(t, paths.AttentionRows()); len(got) != 1 || got[0] != want {
		t.Fatalf("rows file = %q, want exactly [%s]", got, want)
	}

	// The same screen marked again is one row, not two — and the reply says so.
	s.onCommand(cc, proto.Command{Action: "attention.mark", ID: "p1"})
	if got := notice(cc); got != "marked as a wait (not recorded)" {
		t.Fatalf("a duplicate should say it was not recorded, got %q", got)
	}
	if got := fileLines(t, paths.AttentionRows()); len(got) != 1 {
		t.Fatalf("a duplicate text is written once, got %q", got)
	}

	// An unknown id is a silent no-op, as panel.ack's is.
	s.onCommand(cc, proto.Command{Action: "attention.mark", ID: "nope"})
	if err := replyErr(cc); err != "" {
		t.Fatalf("an unknown id should be silent, got %q", err)
	}
	if got := notice(cc); got != "" {
		t.Fatalf("an unknown id should say nothing, got %q", got)
	}
}

// TestMarkRefusesAnExitedPanelOutLoud: a dead screen is not a wait, and the
// cockpit's row may be stale, so the daemon writes nothing and says why rather
// than leaving the status line on "marked".
func TestMarkRefusesAnExitedPanelOutLoud(t *testing.T) {
	s, _ := recordingServer(t, panel.Exited, "Password:", "Password")
	cc := ctl("")

	s.onCommand(cc, proto.Command{Action: "attention.mark", ID: "p1"})
	if got := notice(cc); got != "not marked: the panel has exited" {
		t.Fatalf("an exited panel should be refused with a notice, got %q", got)
	}
	if got := fileLines(t, paths.AttentionRows()); got != nil {
		t.Fatalf("an exited panel's screen is not a row, got %q", got)
	}
}

// TestMarkIsAnOperatorVerb: the inbox is an operator surface, and a conductor
// labelling tails across the fleet would be an agent teaching the classifier
// what an agent thinks a wait looks like.
func TestMarkIsAnOperatorVerb(t *testing.T) {
	s, _ := recordingServer(t, panel.Idle, "Password:", "Password")
	cc := ctl("c1")
	cc.role = roleConductor

	if reason := s.guardConductor(cc, proto.Command{Action: "attention.mark", ID: "p1"}); reason == "" {
		t.Fatal("a conductor must not mark attention")
	}
	s.onCommand(cc, proto.Command{Action: "attention.mark", ID: "p1"})
	if err := replyErr(cc); err == "" {
		t.Fatal("the fence should answer a conductor with an error")
	}
	if got := fileLines(t, paths.AttentionRows()); got != nil {
		t.Fatalf("a refused mark writes nothing, got %q", got)
	}
}

// TestDismissOfAGuessWritesANegative is source 3 of D6: the regex raised the
// hand, the human dismissed it, and that disagreement is the one honest
// negative — a `keys: []` row in the holdout, never in the rows file.
func TestDismissOfAGuessWritesANegative(t *testing.T) {
	s, clk := recordingServer(t, panel.Running, "Apply this refactor?", "refactor?")

	settle(s, clk)
	if stateOf(s, "p1") != panel.Attention || sourceOf(s, "p1") != "tail" {
		t.Fatalf("setup: the regex should raise it, got %v %q", stateOf(s, "p1"), sourceOf(s, "p1"))
	}

	cc := ctl("")
	s.onCommand(cc, proto.Command{Action: "panel.ack", ID: "p1"})
	if err := replyErr(cc); err != "" {
		t.Fatalf("ack: %v", err)
	}
	want := `{"text":"Apply this refactor?","keys":[]}`
	if got := fileLines(t, paths.AttentionHoldout()); len(got) != 1 || got[0] != want {
		t.Fatalf("holdout = %q, want exactly [%s]", got, want)
	}
	if got := fileLines(t, paths.AttentionRows()); got != nil {
		t.Fatalf("a dismiss is never a positive, got rows %q", got)
	}
}

// TestSnoozeOfAGuessWritesNothing (R5): a snooze is "later", not "wrong".
func TestSnoozeOfAGuessWritesNothing(t *testing.T) {
	s, clk := recordingServer(t, panel.Running, "Apply this refactor?", "refactor?")
	settle(s, clk)
	if sourceOf(s, "p1") != "tail" {
		t.Fatalf("setup: the regex should raise it, got %q", sourceOf(s, "p1"))
	}

	until := clk.now().Add(10 * time.Minute).Format(time.RFC3339Nano)
	s.onCommand(ctl(""), proto.Command{Action: "panel.ack", ID: "p1", Until: until})
	if got := fileLines(t, paths.AttentionHoldout()); got != nil {
		t.Fatalf("a snooze is not a label, got holdout %q", got)
	}
}

// TestDismissOfADeclarationWritesNothing: the agent said it was waiting, and a
// human clearing the row is not a vote against the agent's own word.
func TestDismissOfADeclarationWritesNothing(t *testing.T) {
	s, clk := recordingServer(t, panel.Running, "Apply this refactor?", "refactor?")
	settle(s, clk)
	s.declareAttention(ctl("p1"), proto.Command{Action: "panel.attention", Reason: "really waiting"})
	if sourceOf(s, "p1") != "declared" {
		t.Fatalf("setup: the declaration should stand, got %q", sourceOf(s, "p1"))
	}
	rowsBefore := fileLines(t, paths.AttentionRows())

	s.onCommand(ctl(""), proto.Command{Action: "panel.ack", ID: "p1"})
	if got := fileLines(t, paths.AttentionHoldout()); got != nil {
		t.Fatalf("dismissing a declared row is not a negative, got %q", got)
	}
	if got := fileLines(t, paths.AttentionRows()); len(got) != len(rowsBefore) {
		t.Fatalf("a dismiss must not add a positive either, got %q", got)
	}
}

// TestAnsweredAckWritesNothing: the inbox clears an answered row with the
// same panel.ack as a dismiss, marked Answered. A wait the human met is the
// opposite of a negative, and it is not a positive here either — that is
// panel.attention's and attention.mark's to certify.
func TestAnsweredAckWritesNothing(t *testing.T) {
	s, clk := recordingServer(t, panel.Running, "Apply this refactor?", "refactor?")
	settle(s, clk)
	if sourceOf(s, "p1") != "tail" {
		t.Fatalf("setup: the regex should have raised it, got %q", sourceOf(s, "p1"))
	}

	s.onCommand(ctl(""), proto.Command{Action: "panel.ack", ID: "p1", Answered: true})
	if got := fileLines(t, paths.AttentionHoldout()); got != nil {
		t.Fatalf("an answered wait is not a negative, got %q", got)
	}
	if got := fileLines(t, paths.AttentionRows()); got != nil {
		t.Fatalf("an answered ack certifies nothing on its own, got %q", got)
	}
}

// TestDismissOfATimerRowWritesNothing: done and stuck are the clock's doing,
// and there is no guess about the text to be wrong about.
func TestDismissOfATimerRowWritesNothing(t *testing.T) {
	s, clk := recordingServer(t, panel.Idle, "all tests pass", "pass")
	clk.add(90 * time.Second)
	s.monitorTick()
	if got := stateOf(s, "p1"); got != panel.Done {
		t.Fatalf("setup: the quiet ladder should reach done, got %v", got)
	}

	s.onCommand(ctl(""), proto.Command{Action: "panel.ack", ID: "p1"})
	if got := fileLines(t, paths.AttentionHoldout()); got != nil {
		t.Fatalf("a timer-raised row has no guess to be wrong, got %q", got)
	}
}

// TestRecordOffWritesNothingAnywhere is D8: writing scrollback to disk is its
// own decision, and none of the three events makes it on the operator's behalf.
func TestRecordOffWritesNothingAnywhere(t *testing.T) {
	s, clk := recordingServer(t, panel.Running, "Apply this refactor?", "refactor?")
	s.classifierRecord = false

	cc := ctl("")
	s.onCommand(cc, proto.Command{Action: "attention.mark", ID: "p1"})
	if got := notice(cc); got != "marked as a wait (not recorded)" {
		t.Fatalf("with record off the reply says so, got %q", got)
	}
	settle(s, clk)
	s.onCommand(cc, proto.Command{Action: "panel.ack", ID: "p1"})
	s.declareAttention(ctl("p1"), proto.Command{Action: "panel.attention", Reason: "waiting"})

	for _, path := range []string{paths.AttentionRows(), paths.AttentionHoldout()} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s should not exist with record off, stat: %v", filepath.Base(path), err)
		}
	}
}

// TestRowFileDedupsAcrossALoad: the set is filled from the file on first use,
// so the same overlay is one row across daemon restarts too.
func TestRowFileDedupsAcrossALoad(t *testing.T) {
	rowsHome(t)
	path := paths.AttentionRows()
	seedRows(t, path, `{"text":"Password:","keys":["attention"]}`+"\n")
	f := &rowFile{path: path}
	if f.append("Password:", true) != 0 {
		t.Fatal("a text already on disk must not be written again")
	}
	if n := f.append("Continue? [y/N]", true); n != 2 {
		t.Fatalf("a new text should be written as line 2, got %d", n)
	}
	if f.append("", true) != 0 {
		t.Fatal("an empty text is no row")
	}
	if got := fileLines(t, path); len(got) != 2 || got[1] != `{"text":"Continue? [y/N]","keys":["attention"]}` {
		t.Fatalf("file = %q", got)
	}
	if f.lines != 2 {
		t.Fatalf("lines = %d, want 2", f.lines)
	}
}

// TestRowFileStopsAtTheCap is D13: at the cap the write is skipped, said once,
// and the file is not grown — never truncated.
func TestRowFileStopsAtTheCap(t *testing.T) {
	rowsHome(t)
	path := paths.AttentionHoldout()
	line := `{"text":"x","keys":[]}` + "\n"
	full := strings.Repeat(line, rowCap/len(line)+1)
	seedRows(t, path, full)
	logs := captureLog(t)

	f := &rowFile{path: path}
	for _, text := range []string{"one", "two"} {
		if f.append(text, false) != 0 {
			t.Fatalf("%q: a file at its cap must not grow", text)
		}
	}
	if st, _ := os.Stat(path); st.Size() != int64(len(full)) {
		t.Fatalf("size changed: %d, want %d", st.Size(), len(full))
	}
	if n := strings.Count(logs(), "at its cap"); n != 1 {
		t.Fatalf("the cap is said once, got %d times in %q", n, logs())
	}
	if !f.capped {
		t.Fatal("the latch should be set")
	}
}

// TestRowCountsReadTheFiles is what `classifier status` shows: lines per file,
// zero when absent, and the cap flag — read from disk, under no lock.
func TestRowCountsReadTheFiles(t *testing.T) {
	rowsHome(t)

	if r, h, c := rowCounts(); r != 0 || h != 0 || c {
		t.Fatalf("absent files count as empty, got %d %d %v", r, h, c)
	}

	seedRows(t, paths.AttentionRows(), strings.Repeat(`{"text":"a","keys":["attention"]}`+"\n", 3))
	seedRows(t, paths.AttentionHoldout(), `{"text":"b","keys":[]}`+"\n")
	if r, h, c := rowCounts(); r != 3 || h != 1 || c {
		t.Fatalf("counts = %d %d %v, want 3 1 false", r, h, c)
	}

	line := `{"text":"x","keys":[]}` + "\n"
	seedRows(t, paths.AttentionHoldout(), strings.Repeat(line, rowCap/len(line)+1))
	if _, _, c := rowCounts(); !c {
		t.Fatal("a file at its cap should be reported")
	}
}
