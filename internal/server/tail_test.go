package server

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// tailReply drains what a verb sent back and returns the first "tail" message,
// so a case can assert on the bytes rather than on the channel.
func tailReply(t *testing.T, cc *clientConn) proto.ServerMsg {
	t.Helper()
	for {
		select {
		case msg := <-cc.out:
			if msg.Type == "error" {
				t.Fatalf("panel.tail answered with an error: %s", msg.Error)
			}
			if msg.Type == "tail" {
				return msg
			}
		default:
			t.Fatal("panel.tail sent no reply")
			return proto.ServerMsg{}
		}
	}
}

// noisyPty returns a PTY manager holding one panel that has printed more than
// maxTailBytes, so the clamp has something to bite on.
func noisyPty(t *testing.T, id string, want int) *ptymgr.Manager {
	t.Helper()
	pm := ptymgr.New()
	t.Cleanup(func() { pm.Stop(id) })
	// A line of 79 characters plus the PTY's CRLF, repeated until the ring holds
	// more than the caller asked for. `yes` is the cheapest generator that keeps
	// going; head bounds it so the panel exits on its own.
	spec := ptymgr.Spec{Command: "/bin/sh", Args: []string{"-c",
		`yes ` + strings.Repeat("x", 79) + ` | head -n ` + strconv.Itoa(want/80+40)}}
	if err := pm.StartCmd(id, spec); err != nil {
		t.Fatalf("start the noisy panel: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(pm.Tail(id, 1<<20)) <= want {
		if time.Now().After(deadline) {
			t.Fatalf("the noisy panel only produced %d bytes, want > %d", len(pm.Tail(id, 1<<20)), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return pm
}

// TestTailIsTheWindowThatRaisedTheFlag is the whole contract of panel.tail: what
// the inbox shows a human is byte-for-byte what looksLikeAttention read when it
// decided the panel needed one. A tail that came from anywhere else — a fresh
// read of a different size, a second implementation, a snapshot field computed on
// another tick — would let the queue show one thing and act on another.
func TestTailIsTheWindowThatRaisedTheFlag(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Attention})
	s.pty = askingPty(t, "a1")
	cc := ctl("")

	s.onCommand(cc, proto.Command{Action: "panel.tail", ID: "a1"})
	got := tailReply(t, cc)

	if got.ID != "a1" {
		t.Errorf("the reply must name the panel it is about, got %q", got.ID)
	}
	if want := s.pty.Tail("a1", attnTailBytes); string(got.Data) != string(want) {
		t.Errorf("a Count-less tail must be the Monitor's own window\n got %q\nwant %q", got.Data, want)
	}
	if !strings.Contains(strings.ToLower(string(got.Data)), "[y/n]") {
		t.Errorf("the question that raised the flag should be in the reply, got %q", got.Data)
	}
}

// TestTailClampsWhatOneRowCanPull. The inbox opens this door once per cursor
// move, so a cockpit walking a thirty-row queue must not be able to drag thirty
// whole replay rings through it. A request over the cap is served short rather
// than refused: the pane wants the END of the output, and the end is exactly what
// it gets.
func TestTailClampsWhatOneRowCanPull(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "a1", Kind: panel.Agent, State: panel.Idle})
	s.pty = noisyPty(t, "a1", maxTailBytes*2)
	cc := ctl("")

	s.onCommand(cc, proto.Command{Action: "panel.tail", ID: "a1", Count: 1 << 20})
	if got := tailReply(t, cc); len(got.Data) != maxTailBytes {
		t.Errorf("an over-large Count should clamp to %d bytes, got %d", maxTailBytes, len(got.Data))
	}

	// A Count the caller can actually justify is honoured as asked.
	s.onCommand(cc, proto.Command{Action: "panel.tail", ID: "a1", Count: 256})
	if got := tailReply(t, cc); len(got.Data) != 256 {
		t.Errorf("Count=256 should return 256 bytes, got %d", len(got.Data))
	}
	// …and it is the TRAILING bytes, not the leading ones: the question is always
	// the last thing a panel printed.
	s.onCommand(cc, proto.Command{Action: "panel.tail", ID: "a1", Count: 64})
	short := tailReply(t, cc)
	full := s.pty.Tail("a1", maxTailBytes)
	if !strings.HasSuffix(string(full), string(short.Data)) {
		t.Errorf("a short tail must be the END of the ring, got %q", short.Data)
	}
}

// TestTailOnAnUnknownPanelIsQuiet. A row can be reaped between the keystroke that
// selected it and the read that serves it. That race is not the human's doing and
// an error popup for it is noise, so the pane comes back empty and the next
// snapshot takes the row away.
func TestTailOnAnUnknownPanelIsQuiet(t *testing.T) {
	s, _, _ := gateServer()
	cc := ctl("")

	s.onCommand(cc, proto.Command{Action: "panel.tail", ID: "gone"})

	got := tailReply(t, cc) // fails the test if an "error" arrived instead
	if len(got.Data) != 0 {
		t.Errorf("an unknown id has no output to show, got %q", got.Data)
	}
	if got.ID != "gone" {
		t.Errorf("the reply should still name what was asked for, got %q", got.ID)
	}
}

// TestConductorCannotPullATail pins DESIGN §12's "no conductor inbox" as an
// enforced boundary rather than an intention. The inbox is an operator surface;
// an agent triaging the fleet's attention queue is a design this round did not
// build, and the fence is where that shows up in code.
func TestConductorCannotPullATail(t *testing.T) {
	s, _, _ := gateServer(panel.Panel{ID: "c1", Kind: panel.Agent, State: panel.Running, Conductor: true})
	cc := ctl("c1")
	cc.role = roleConductor

	s.onCommand(cc, proto.Command{Action: "panel.tail", ID: "c1"})

	msg := <-cc.out
	if msg.Type != "error" || !strings.Contains(msg.Error, "operator surface") {
		t.Fatalf("a conductor must be refused the inbox verbs, got %+v", msg)
	}
}
