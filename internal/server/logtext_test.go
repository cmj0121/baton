package server

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// legitimateLogQuote is the longest wire string these tests claim a log line has
// to carry whole, written as a figure rather than as maxLogRunes-minus-something
// so a cap narrowed to where it bites is caught. A hundred runes is a search term
// or an opening clause, which is what a reader greps the log for.
const legitimateLogQuote = 100

// TestALogLineIsNotAnAgentsDiskBudget drives a vetoed brief as long as one
// command frame allows and checks the log line it leaves stays a log line.
//
// The dispatch veto is the site that still needs this. A brief has no rune cap of
// its own and should not have one — a task brief is genuinely prose — so what the
// frame cap leaves is a megabyte per WARN line, against a log that rotates at
// 8 MiB. Eight refused briefs would churn a rotation, and an agent that can get
// its own briefs refused chooses how often that happens.
func TestALogLineIsNotAnAgentsDiskBudget(t *testing.T) {
	st, _ := scoreStore(t)
	s, clk, delivered := busyScoreServer(st)
	s.onFilterTask = func(TaskBrief) (TaskBrief, bool) { return TaskBrief{}, false }

	brief := strings.Repeat("Z", maxCommandBytes-1024)
	dispatchTo(t, s, conn(""), brief)

	logged := captureLog(t)
	settle(s, clk) // the tick that binds the parked brief, and refuses it

	if len(*delivered) != 0 {
		t.Fatalf("a vetoed delivery wrote %d bytes", len(*delivered))
	}
	got := logged()
	if !strings.Contains(got, "task.pre hook refused") {
		t.Fatalf("the veto was not logged at all:\n%s", got[:min(len(got), 500)])
	}
	// Generous: the line carries its level, timestamp, message, task id and panel
	// too. The point is that it is a log line rather than a megabyte of one.
	if len(got) > 4096 {
		t.Fatalf("one veto wrote %d bytes of log for a %d-byte brief", len(got), len(brief))
	}
}

// TestAnOversizedSearchTermIsRefused: the term is compiled, and compilation is
// not linear in what it is given — a 1 MiB term of "(a)(a)…" takes 193 ms and
// allocates 350 MiB before a single panel's ring is touched. maxHitsPerPanel and
// maxHitsTotal bound the REPLY and none of that.
func TestAnOversizedSearchTermIsRefused(t *testing.T) {
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := New(ln)

	cc := &clientConn{out: make(chan proto.ServerMsg, 4)}
	err = s.sendSearch(cc, strings.Repeat("(a)", (maxCommandBytes-1024)/3))
	if err == nil {
		t.Fatal("a megabyte of regexp was compiled rather than refused")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("the refusal should say why, got %q", err)
	}
}

// TestARealSearchTermIsStillCompiled is the other half: an alternation of a
// hundred branches is a term somebody could paste, and it has to run.
func TestARealSearchTermIsStillCompiled(t *testing.T) {
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := New(ln)

	branches := make([]string, 100)
	for i := range branches {
		branches[i] = fmt.Sprintf("needl%03d", i)
	}
	cc := &clientConn{out: make(chan proto.ServerMsg, 4)}
	if err := s.sendSearch(cc, "("+strings.Join(branches, "|")+")"); err != nil {
		t.Fatalf("a hundred-branch alternation should search, got %v", err)
	}
	<-cc.out
}

// TestALogQuoteKeepsWhatAReaderCameFor is the other half: the term still has to
// be recognisable in the line, or the cap has traded a disk problem for a
// debugging one.
func TestALogQuoteKeepsWhatAReaderCameFor(t *testing.T) {
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := New(ln)

	logged := captureLog(t)
	term := strings.Repeat("q", legitimateLogQuote)
	cc := &clientConn{out: make(chan proto.ServerMsg, 4)}
	if err := s.sendSearch(cc, term); err != nil {
		t.Fatalf("sendSearch: %v", err)
	}
	<-cc.out

	if got := logged(); !strings.Contains(got, term) {
		t.Fatalf("a %d-rune term should appear whole in the log line:\n%s", legitimateLogQuote, got)
	}
}

// TestALogQuoteCarriesNoControlBytes: zerolog escapes a control byte to six
// characters, so an agent naming itself in ESC would be a sixfold multiplier on
// whatever the rune cap allowed. The scrub is what stops the cap being paid six
// times over.
func TestALogQuoteCarriesNoControlBytes(t *testing.T) {
	if got := logText("a\x1b[2Jb\x07c"); got != "a[2Jbc" {
		t.Fatalf("logText = %q, want the control bytes gone", got)
	}
	if got := logText(strings.Repeat("x", maxLogRunes+50)); len([]rune(got)) != maxLogRunes {
		t.Fatalf("logText kept %d runes, want %d", len([]rune(got)), maxLogRunes)
	}
}
