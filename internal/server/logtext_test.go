package server

import (
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

// TestALogLineIsNotAnAgentsDiskBudget drives a fleet search whose term is as long
// as one command frame allows and checks the log line it leaves stays a log line.
//
// fleet.search logs at INFO, which is the DEFAULT level, and it quoted the term
// verbatim. So the size of a line in the operator's log file was a number an agent
// chose — and rotation at 8 MiB means it also chose how often the file churns. The
// frame cap took that from unbounded to a megabyte a line, which is the difference
// between losing a disk and losing a log; this takes it the rest of the way,
// because a megabyte in a log file is not evidence of anything.
func TestALogLineIsNotAnAgentsDiskBudget(t *testing.T) {
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
	cc := &clientConn{out: make(chan proto.ServerMsg, 4)}
	// Just under maxCommandBytes: the largest term the socket will carry at all.
	if err := s.sendSearch(cc, strings.Repeat("Z", maxCommandBytes-1024)); err != nil {
		t.Fatalf("sendSearch: %v", err)
	}
	<-cc.out

	got := logged()
	if !strings.Contains(got, "fleet search") {
		t.Fatalf("the search was not logged at all:\n%s", got[:min(len(got), 500)])
	}
	// Generous: the line carries its level, timestamp, message and hit count too.
	// The point is that it is a log line rather than a megabyte of one.
	if len(got) > 4096 {
		t.Fatalf("one search wrote %d bytes of log for a %d-byte term", len(got), maxCommandBytes-1024)
	}
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
