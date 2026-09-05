package server_test

// The daemon's two untrusted inputs, driven with what a peer can actually say.
//
// Go does not confine a panic to the goroutine that raised it, and the daemon
// recovers nowhere: a panic in one connection's reader, or in one panel's output
// pump, ends the process and every panel in the fleet with it. Nothing in either
// package asserts otherwise, so these two tests are the assertion — they do not
// check a return value, they check that the daemon is still answering a fresh
// connection afterwards, which is the only property that matters here.
//
// A panic takes the whole test binary down with a stack, so a failure here is not
// a diff to read: it names the input and the line.

import (
	"bufio"
	"encoding/json"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// hostileActions is every action the wire accepts, from proto.Command's own list.
// The four that reach outside the daemon are left out and named rather than
// silently dropped: command.run and panel.create with a hostile Path execute a
// binary, worktree.sweep and server.reload touch the operator's repos and config,
// and remote.* opens a listening socket. Their argument handling is covered by
// their own tests; what this one is for is the dispatch arithmetic behind them.
// The order is load-bearing: panel.close and panel.purge REAP the live panel, and
// every id-targeted action after them would then stop at "no panel with that id"
// — which is the lookup, not the arithmetic this test is for. They go last.
var hostileActions = []string{
	"hello", "panel.list",
	"panel.attach", "panel.detach", "panel.input", "panel.dispatch", "panel.dispatch-group",
	"panel.resize", "panel.group", "panel.ungroup", "panel.rename", "panel.move", "panel.pin",
	"panel.unpin", "panel.favourite", "panel.unfavourite", "panel.attention",
	"panel.resolve", "panel.ack", "panel.tail", "panel.diff", "panel.git", "panel.log",
	"panel.logview", "fleet.search", "group.show", "group.layout", "group.favourite",
	"group.unfavourite", "task.enqueue", "task.list", "task.cancel", "task.promote",
	"task.demote", "task.drain", "config.get", "score.submit", "score.list", "score.status",
	"score.merge", "score.reword", "score.lower", "worktree.list",
	"panel.signal", "panel.respawn", "panel.close", "panel.purge",
}

// hostileNumbers are the values every int field on a Command is sent as. The two
// extremes are the ones that matter: MinInt64 is what a negative index or count
// looks like after a client's own arithmetic went wrong, and MaxInt64 is what any
// doubling downstream overflows from.
var hostileNumbers = []int{math.MinInt64, math.MinInt32, -1, 0, 1, math.MaxInt32, math.MaxInt64}

// hostileStrings are the values every string field is sent as: empty and blank,
// a NUL, an escape sequence, a traversal, a replacement rune, something past the
// caps, and five shapes that are not valid regexps (fleet.search compiles Query).
var hostileStrings = []string{
	"", " ", "\x00", "\x1b[2J", "\r\n", "../../../etc/passwd", "�",
	strings.Repeat("A", 4096), "(", "[", "a{2,1}", "*", "\\", strings.Repeat("(", 2048),
}

// hostileFrames are shapes proto.Command cannot be encoded into: a null action, an
// empty object, a lone surrogate, and the extreme numbers again as literals — so
// the decoder is exercised as well as the dispatch.
var hostileFrames = []string{
	`{"action":"panel.move","ids":null,"index":-9223372036854775808}`,
	`{"action":"panel.tail","count":-9223372036854775808}`,
	`{"action":"group.show","group":"g","count":-9223372036854775808}`,
	`{"action":"panel.resize","rows":-9223372036854775808,"cols":-9223372036854775808}`,
	`{"action":"panel.ack","until":"not-a-time"}`,
	`{"action":"fleet.search","query":"\ud800"}`,
	`{"action":null}`,
	`{}`,
	`{"action":"\ud800"}`,
}

// TestTheCommandLoopSurvivesWhatAPeerCanSay throws every action at the daemon with
// out-of-range numbers, hostile strings, and frames the typed encoder cannot make.
func TestTheCommandLoopSurvivesWhatAPeerCanSay(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, _ := listen(t)
	srv := server.New(ln)
	serve(t, srv)

	// One real panel, so the id-targeted half of every action reaches the panel
	// code behind it rather than stopping at "no panel with that id". Without it
	// this test is a test of the lookup, and the arithmetic it is for never runs.
	setup := dial(t, sock)
	if err := setup.Send(proto.Command{Action: "panel.create"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	live := recv(t, setup).Panels[0].ID

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	enc := json.NewEncoder(conn)
	if err := enc.Encode(proto.Command{Action: "hello"}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	// Drain in the background: the server's writer must never block on this test.
	go func() {
		br := bufio.NewReader(conn)
		for {
			if _, err := br.ReadString('\n'); err != nil {
				return
			}
		}
	}()

	for _, act := range hostileActions {
		for _, n := range hostileNumbers {
			// Once against the live panel and once against no panel at all: the
			// first reaches the arithmetic, the second the lookup that guards it.
			c := proto.Command{Action: act, ID: live, IDs: []string{live}, Group: "g", Rows: n, Cols: n, Index: n, Count: n}
			if err := enc.Encode(c); err != nil {
				t.Fatalf("send %s (number %d, live panel): %v", act, n, err)
			}
			c.ID, c.IDs = "", nil
			if err := enc.Encode(c); err != nil {
				t.Fatalf("send %s (number %d): %v", act, n, err)
			}
		}
		for _, s := range hostileStrings {
			// Self and Role are deliberately NOT set here. They are declared once, on
			// hello, and a non-empty Self puts the whole connection under the
			// conductor fence — which refuses a third of these actions before their
			// arguments are looked at, so setting them would quietly gut the flood.
			// The hostile hello frames below carry them, on their own connections.
			c := proto.Command{
				Action: act, ID: s, Group: s, Name: s, Query: s, Prompt: s, Signal: s,
				Until: s, Layout: s, Git: s, Dir: s, Kind: s, Submit: s, Reason: s,
				IDs: []string{s, ""}, Data: []byte(s),
			}
			if err := enc.Encode(c); err != nil {
				t.Fatalf("send %s (string %q): %v", act, s, err)
			}
		}
		if err := enc.Encode(proto.Command{Action: act}); err != nil {
			t.Fatalf("send %s (bare): %v", act, err)
		}
	}
	for _, raw := range hostileFrames {
		if _, err := conn.Write([]byte(raw + "\n")); err != nil {
			t.Fatalf("send raw %q: %v", raw, err)
		}
	}

	// The declared identity, on its own connection so the fence it raises does not
	// shadow the flood above. A conductor is refused a third of the actions, and
	// what is exercised here is the refusal path taking a hostile Self with it.
	for _, s := range hostileStrings {
		hostileHello(t, sock, s, live)
	}

	requireDaemonAnswers(t, sock)
}

// hostileHello opens a connection that declares itself a conductor with a hostile
// Self and Role, then drives every action through the fence that raises.
func hostileHello(t *testing.T, sock, decl, live string) {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	go func() {
		br := bufio.NewReader(conn)
		for {
			if _, err := br.ReadString('\n'); err != nil {
				return
			}
		}
	}()
	// A write error here is the daemon HANGING UP, which is a refusal and one of
	// the outcomes under test — an over-long Self trips the identity cap and the
	// connection goes. What must not happen is the process ending, and that is
	// what the caller's requireDaemonAnswers asks after every one of these.
	enc := json.NewEncoder(conn)
	if err := enc.Encode(proto.Command{Action: "hello", Role: "conductor", Self: decl}); err != nil {
		return
	}
	for _, act := range hostileActions {
		c := proto.Command{Action: act, ID: live, IDs: []string{live}, Group: decl, Count: -1, Index: -1}
		if err := enc.Encode(c); err != nil {
			return
		}
	}
}

// hostileOutput is what a panel's own process can print. This is the wider of the
// daemon's two untrusted surfaces: the bytes reach the OSC 7 cwd sniff, the
// attention tail, the signature fold, the panel log and the client fan-out, and
// they are written by the agent rather than by a cockpit anyone reviewed.
var hostileOutput = [][]byte{
	[]byte("\x1b"),    // a lone ESC, and nothing after it
	[]byte("\x1b["),   // a CSI that never arrives
	[]byte("\x1b]"),   // an OSC that never terminates
	[]byte("\x1b]7;"), // the cwd report, truncated
	[]byte("\x1b]7;file://host/not/a/dir\x07"),              // a cwd that does not exist
	[]byte("\x1b]7;" + strings.Repeat("x", 1<<16)),          // an unterminated OSC 7 past any carry
	[]byte("\x1b]0;" + strings.Repeat("t", 1<<16) + "\x07"), // a title far past any cap
	[]byte("\x1b[999999999999999999999m"),                   // a parameter past int64
	[]byte("\x1b[" + strings.Repeat("1;", 4096) + "m"),      // more parameters than any CSI has
	[]byte("\x1b[c\x1b[>c\x1b[=c"),                          // device-attribute queries, which are replied to
	[]byte("\xff\xfe\xfd invalid utf-8"),                    // bytes no decoder can turn into runes
	[]byte(strings.Repeat("\x00", 4096)),                    // NULs, which terminate a C string but not this
	[]byte(strings.Repeat("\r", 4096)),                      // a carriage-return flood: lineShape splits on these
	[]byte("\x1bP" + strings.Repeat("q", 4096)),             // a DCS with no ST
	[]byte("\x1b_" + strings.Repeat("a", 4096)),             // an APC with no ST
	[]byte("done? \x1b[0m"),                                 // reads as attention, so the sniff runs on it
}

// TestTheOutputPumpSurvivesWhatAPanelCanPrint runs one panel per payload, through
// the real PTY and the real pump, and then asks the daemon a question.
func TestTheOutputPumpSurvivesWhatAPanelCanPrint(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	ln, sock, _ := listen(t)
	srv := server.New(ln)
	serve(t, srv)

	c := dial(t, sock)
	for i, payload := range hostileOutput {
		f := filepath.Join(dir, "p"+string(rune('a'+i)))
		if err := os.WriteFile(f, payload, 0o600); err != nil {
			t.Fatalf("write payload %d: %v", i, err)
		}
		cmd := proto.Command{Action: "panel.create", Kind: "agent", Path: "/bin/cat", Args: []string{f}, Dir: dir}
		if err := c.Send(cmd); err != nil {
			t.Fatalf("create panel %d: %v", i, err)
		}
	}

	// Let every cat run, exit, and be reaped, so at least one monitor tick reads
	// each panel's tail — the attention sniff and the signature fold are on that
	// tick, not on the output path, and they are half of what is being exercised.
	time.Sleep(2 * time.Second)
	requireDaemonAnswers(t, sock)
}

// requireDaemonAnswers proves the daemon is alive on a connection it has not seen
// before: a panic would have ended the process, and a wedged one cannot reply.
func requireDaemonAnswers(t *testing.T, sock string) {
	t.Helper()
	c := dial(t, sock)
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("the daemon stopped answering: %v", err)
	}
	if got := recv(t, c); got.Type != "panels" {
		t.Fatalf("want a panels reply, got %+v", got)
	}
}
