package server

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/state"
)

// peerEvent is one plugin event a test saw, by name and fields.
type peerEvent struct {
	name   string
	fields map[string]any
}

// peerFleet builds a server holding one of every panel a peer verb has to tell
// apart, with peers ON for "work" alone. The writer and the event sink record,
// so a case asserts on the exact bytes a target was typed and on what a plugin
// was told. The clock is the fake one, which is what the rate cap reads.
//
//	a1, a2  agents in "work" (the ordinary pair)
//	sh      a shell in "work"
//	ask     an agent in "work" waiting on a human
//	dead    an exited agent in "work"
//	boss    the conductor, filed in "work"
//	gs      the global shell, filed in "work"
//	o1      an agent in "other" (peers off)
//	sub     an agent in "work/sub" (peers off; the flag on "work" is not its flag)
//	lone    an agent in no group
func peerFleet() (*Server, *fakeClock, *[]string, *[]peerEvent) {
	s, clk, written := gateServer(
		panel.Panel{ID: "a1", Kind: panel.Agent, Title: "alice", Group: "work", State: panel.Idle},
		panel.Panel{ID: "a2", Kind: panel.Agent, Title: "bob", Group: "work", State: panel.Running},
		panel.Panel{ID: "sh", Kind: panel.Shell, Title: "term", Group: "work", State: panel.Idle},
		panel.Panel{ID: "ask", Kind: panel.Agent, Title: "asker", Group: "work", State: panel.Attention},
		panel.Panel{ID: "dead", Kind: panel.Agent, Title: "gone", Group: "work", State: panel.Exited},
		panel.Panel{ID: "boss", Kind: panel.Agent, Title: "conductor", Group: "work", State: panel.Idle, Conductor: true},
		panel.Panel{ID: "gs", Kind: panel.Shell, Title: "global", Group: "work", State: panel.Idle, GlobalShell: true},
		panel.Panel{ID: "o1", Kind: panel.Agent, Title: "outsider", Group: "other", State: panel.Idle},
		panel.Panel{ID: "sub", Kind: panel.Agent, Title: "nested", Group: "work/sub", State: panel.Idle},
		panel.Panel{ID: "lone", Kind: panel.Agent, Title: "alone", State: panel.Idle},
	)
	s.groupPeers = map[string]bool{"work": true}
	events := &[]peerEvent{}
	s.eventSink = func(name string, fields map[string]any) { *events = append(*events, peerEvent{name, fields}) }
	return s, clk, written, events
}

// peerReply returns the one message a peer verb answered with. Every peer verb
// answers exactly once, to its caller alone, so none and two are both failures.
func peerReply(t *testing.T, cc *clientConn) proto.ServerMsg {
	t.Helper()
	select {
	case msg := <-cc.out:
		select {
		case extra := <-cc.out:
			t.Fatalf("a peer verb answered twice: %+v then %+v", msg, extra)
		default:
		}
		return msg
	default:
		t.Fatal("a peer verb sent no reply")
		return proto.ServerMsg{}
	}
}

// peerDo runs one command as the panel self declared and returns its reply.
func peerDo(t *testing.T, s *Server, self string, cmd proto.Command) proto.ServerMsg {
	t.Helper()
	cc := ctl(self)
	s.onCommand(cc, cmd)
	return peerReply(t, cc)
}

// wantPeerRefusal asserts msg is a peer refusal naming the cause.
func wantPeerRefusal(t *testing.T, what string, msg proto.ServerMsg, want string) {
	t.Helper()
	if msg.Type != "error" {
		t.Fatalf("%s: want a refusal naming %q, got %+v", what, want, msg)
	}
	if !strings.HasPrefix(msg.Error, "peer:") {
		t.Fatalf("%s: a peer refusal must start `peer:`, got %q", what, msg.Error)
	}
	if !strings.Contains(msg.Error, want) {
		t.Fatalf("%s: refusal %q does not name %q", what, msg.Error, want)
	}
}

// peerVerbs are the three verbs as a caller reaching target would send them.
func peerVerbs(target string) []proto.Command {
	return []proto.Command{
		{Action: "peer.list"},
		{Action: "peer.tail", ID: target},
		{Action: "peer.send", ID: target, Data: []byte("hi")},
	}
}

// TestPeerVerbsAllowedInAPeersOnGroup is the control every refusal below is read
// against: the same three commands, from a panel in a peers-on group to a live
// group-mate, are answered rather than refused.
func TestPeerVerbsAllowedInAPeersOnGroup(t *testing.T) {
	s, _, written, _ := peerFleet()
	want := []string{"peers", "tail", "notice"}
	for i, cmd := range peerVerbs("a2") {
		if msg := peerDo(t, s, "a1", cmd); msg.Type != want[i] {
			t.Fatalf("%s should answer %q, got %+v", cmd.Action, want[i], msg)
		}
	}
	if len(*written) != 1 {
		t.Fatalf("the one send should have typed once, got %q", *written)
	}
}

// TestPeerCallerRefusals walks every reason the CALLER is not a peer. Each one
// refuses all three verbs, types nothing and tells a plugin nothing.
func TestPeerCallerRefusals(t *testing.T) {
	cases := []struct {
		name string
		self string
		role string
		want string
	}{
		{"no declared panel", "", "", "declared no panel"},
		{"unknown panel", "ghost", "", `"ghost" is not a live panel`},
		{"exited panel", "dead", "", `"dead" is not a live panel`},
		{"conductor role", "a1", roleConductor, "conductor role"},
		{"the conductor panel without the role", "boss", "", "the conductor and the global shell are not peers"},
		{"the global shell", "gs", "", "the conductor and the global shell are not peers"},
		{"no group", "lone", "", `"lone" is in no group`},
		{"peers off", "o1", "", `off for group "other"`},
		{"sub-group of a peers-on group", "sub", "", `off for group "work/sub"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, written, events := peerFleet()
			for _, cmd := range peerVerbs("a2") {
				cc := ctl(tc.self)
				cc.role = tc.role
				s.onCommand(cc, cmd)
				wantPeerRefusal(t, cmd.Action, peerReply(t, cc), tc.want)
			}
			if len(*written) != 0 || len(*events) != 0 {
				t.Fatalf("a refused caller must type and emit nothing, got %q / %+v", *written, *events)
			}
		})
	}
}

// TestPeerTargetRefusals walks every reason a TARGET is out of reach, for the
// two verbs that name one.
func TestPeerTargetRefusals(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   string
	}{
		{"no target", "", "a target panel id is required"},
		{"unknown", "ghost", `no panel with id "ghost"`},
		{"self", "a1", `"a1" is your own panel`},
		{"another group", "o1", `"o1" is not in your group "work"`},
		{"a sub-group", "sub", `"sub" is not in your group "work"`},
		{"the conductor", "boss", `"boss" is the conductor`},
		{"the global shell", "gs", `"gs" is the global shell`},
		{"exited", "dead", `"dead" has exited`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, written, events := peerFleet()
			// Peers on for the sub-group too, so that case is refused for the exact
			// path and not because the other group happens to be off.
			s.groupPeers["work/sub"] = true
			for _, cmd := range peerVerbs(tc.target)[1:] {
				wantPeerRefusal(t, cmd.Action, peerDo(t, s, "a1", cmd), tc.want)
			}
			if len(*written) != 0 || len(*events) != 0 {
				t.Fatalf("a refused target must be typed nothing, got %q / %+v", *written, *events)
			}
		})
	}
}

// TestPeerSendRefusals covers what refuses a send that a read is still allowed:
// a target waiting on a human, nothing to send, and too much to send.
func TestPeerSendRefusals(t *testing.T) {
	s, _, written, events := peerFleet()

	wantPeerRefusal(t, "to a panel in attention",
		peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: "ask", Data: []byte("yes")}), "waiting for a human")
	// The read is not refused with it: seeing what a group-mate is asking is not
	// answering it.
	if msg := peerDo(t, s, "a1", proto.Command{Action: "peer.tail", ID: "ask"}); msg.Type != "tail" || msg.ID != "ask" {
		t.Fatalf("peer.tail of a panel in attention should be answered, got %+v", msg)
	}

	wantPeerRefusal(t, "empty",
		peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: "a2"}), "needs something to send")

	over := []byte(strings.Repeat("x", maxPeerSendBytes+1))
	wantPeerRefusal(t, "over the cap",
		peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: "a2", Data: over}), "4097 bytes, over the 4096-byte cap")
	if len(*written) != 0 || len(*events) != 0 {
		t.Fatalf("a refused send must type and emit nothing, got %d writes / %+v", len(*written), *events)
	}

	// Exactly at the cap is admitted, and the prefix is not charged against it.
	if msg := peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: "a2", Data: over[1:]}); msg.Type != "notice" {
		t.Fatalf("a send of exactly the cap should be admitted, got %+v", msg)
	}
}

// TestPeerListIsOnlyEligibleTargets pins that the list is the same rule the
// other two verbs refuse on: live group-mates, and never the caller, the two
// singletons, an exited slot, another group or a sub-group.
func TestPeerListIsOnlyEligibleTargets(t *testing.T) {
	s, _, _, _ := peerFleet()
	s.groupPeers["work/sub"] = true

	msg := peerDo(t, s, "a1", proto.Command{Action: "peer.list"})
	var got []string
	for _, p := range msg.Panels {
		got = append(got, p.ID)
		if p.Group != "work" {
			t.Fatalf("a listed peer must carry the caller's group, got %+v", p)
		}
	}
	if want := []string{"a2", "sh", "ask"}; msg.Type != "peers" || !slices.Equal(got, want) {
		t.Fatalf("peer.list = %q %v, want \"peers\" %v", msg.Type, got, want)
	}
	// And every one it lists can actually be read by the caller.
	for _, id := range got {
		if r := peerDo(t, s, "a1", proto.Command{Action: "peer.tail", ID: id}); r.Type != "tail" {
			t.Fatalf("listed peer %q should be readable, got %+v", id, r)
		}
	}
}

// TestPeerSendPrefixesAnAgentAndNotAShell reads the bytes the target was typed.
// An agent is told who is typing; a shell gets the bytes as sent. Both end in
// the Enter a dispatched brief would, and neither gets a task: a peer's
// message is not an assignment.
func TestPeerSendPrefixesAnAgentAndNotAShell(t *testing.T) {
	s, _, written, _ := peerFleet()
	s.panels[0].Title = "al\x1b[2Jice\nthe first" // a title is whatever a rename set

	msg := peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: "a2", Data: []byte("are you done?")})
	// The notice carries the target's id: a notice is also what the daemon
	// broadcasts for a plugin and for the fleet memory, and the id is how a client
	// knows this one is the answer to its send.
	if msg.ID != "a2" {
		t.Fatalf("the peer.send notice should carry the target's id, got %+v", msg)
	}
	if msg.Type != "notice" || !strings.Contains(msg.Notice, "bob (a2)") || !strings.Contains(msg.Notice, "13 bytes") {
		t.Fatalf("the reply should be a notice naming the target and the size, got %+v", msg)
	}
	peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: "sh", Data: []byte("make test")})

	want := []string{
		"a2:[from al[2Jice the first (a1)] are you done?\n",
		"sh:make test\n",
	}
	if !slices.Equal(*written, want) {
		t.Fatalf("typed into the targets:\n got %q\nwant %q", *written, want)
	}

	// a2 was mid-output and the send still went straight through, with nothing
	// recorded behind it.
	if len(s.tasks) != 0 || len(s.pendingDispatch) != 0 || len(s.panelTask) != 0 {
		t.Fatalf("a peer send must record no task: tasks=%v pending=%v", s.tasks, s.pendingDispatch)
	}
	for _, p := range s.panels {
		if p.Task != "" {
			t.Fatalf("a peer send must leave no brief on a card, panel %s has %q", p.ID, p.Task)
		}
	}
}

// TestPeerSendEmitsOneEventPerAdmittedSend pins the plugin event: it names
// sender, target, group and the size the sender gave (not the prefixed size).
func TestPeerSendEmitsOneEventPerAdmittedSend(t *testing.T) {
	s, _, _, events := peerFleet()
	peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: "a2", Data: []byte("hello")})
	peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: "a1", Data: []byte("hello")}) // refused

	if len(*events) != 1 || (*events)[0].name != "peer.send" {
		t.Fatalf("want exactly one peer.send event, got %+v", *events)
	}
	f := (*events)[0].fields
	if f["from"] != "a1" || f["to"] != "a2" || f["group"] != "work" || f["bytes"] != 5 || len(f) != 4 {
		t.Fatalf("peer.send event fields = %+v", f)
	}
}

// peerSendOK reports whether one send from self to target was admitted, and
// fails on a refusal that is not the rate cap's.
func peerSendOK(t *testing.T, s *Server, self, target string) bool {
	t.Helper()
	msg := peerDo(t, s, self, proto.Command{Action: "peer.send", ID: target, Data: []byte("ping")})
	if msg.Type == "notice" {
		return true
	}
	wantPeerRefusal(t, "a capped send", msg, "sending too fast; retry in")
	return false
}

// TestPeerSendRateCapRefusesAFlood drives the cap on the fake clock: a sender
// gets its burst, then one more per gap, and is refused rather than queued in
// between. The cap is per SENDER, so one panel flooding does not silence another.
func TestPeerSendRateCapRefusesAFlood(t *testing.T) {
	s, clk, written, events := peerFleet()

	for i := range peerSendBurst {
		if !peerSendOK(t, s, "a1", "a2") {
			t.Fatalf("send %d of the burst of %d was refused", i+1, peerSendBurst)
		}
	}
	for range 20 {
		if peerSendOK(t, s, "a1", "a2") {
			t.Fatal("a send past the burst was admitted")
		}
	}
	if len(*written) != peerSendBurst || len(*events) != peerSendBurst {
		t.Fatalf("a refused send must not be queued or announced: %d writes, %d events", len(*written), len(*events))
	}

	// Another sender has its own allowance, to the same target.
	if !peerSendOK(t, s, "sh", "a2") {
		t.Fatal("one sender's flood must not cap another sender")
	}

	// Just short of the gap is still refused; the gap buys exactly one.
	clk.add(minPeerSendGap - time.Millisecond)
	if peerSendOK(t, s, "a1", "a2") {
		t.Fatal("a send inside the gap was admitted")
	}
	clk.add(time.Millisecond)
	if !peerSendOK(t, s, "a1", "a2") {
		t.Fatal("a send after the gap should be admitted: refusals must not push it away")
	}
	if peerSendOK(t, s, "a1", "a2") {
		t.Fatal("one gap must buy one send, not a new burst")
	}
}

// TestRefusedPeerSendSpendsNoBudget pins the order of the checks: the rate cap
// is charged last, so a send refused for any other reason costs nothing.
func TestRefusedPeerSendSpendsNoBudget(t *testing.T) {
	s, _, _, _ := peerFleet()

	refused := []proto.Command{
		{Action: "peer.send", ID: "a1", Data: []byte("x")},                                     // self
		{Action: "peer.send", ID: "o1", Data: []byte("x")},                                     // out of group
		{Action: "peer.send", ID: "ask", Data: []byte("x")},                                    // attention
		{Action: "peer.send", ID: "a2"},                                                        // empty
		{Action: "peer.send", ID: "a2", Data: []byte("x"), Submit: "\r"},                       // a submit sequence
		{Action: "peer.send", ID: "a2", Data: []byte("x\x1b[Z")},                               // control bytes
		{Action: "peer.send", ID: "a2", Data: []byte(" \n ")},                                  // nothing but whitespace
		{Action: "peer.send", ID: "a2", Data: []byte(strings.Repeat("x", maxPeerSendBytes+1))}, // over the cap
	}
	for range 4 { // far more refusals than the burst holds
		for _, cmd := range refused {
			if msg := peerDo(t, s, "a1", cmd); msg.Type != "error" || strings.Contains(msg.Error, "too fast") {
				t.Fatalf("setup: %+v should be refused for its own reason, got %+v", cmd, msg)
			}
		}
	}
	for i := range peerSendBurst {
		if !peerSendOK(t, s, "a1", "a2") {
			t.Fatalf("send %d of the burst was refused: the refused sends spent the budget", i+1)
		}
	}
	if peerSendOK(t, s, "a1", "a2") {
		t.Fatal("the burst itself must still be capped")
	}
}

// TestPeerAuthorisedPerCallFromLiveState is R6: nothing is granted at the first
// call. The flag turned off, or either panel moved out of the group, refuses
// the very next call with the reason — and turning it back restores it.
func TestPeerAuthorisedPerCallFromLiveState(t *testing.T) {
	s, _, written, _ := peerFleet()
	all := func(want string) {
		t.Helper()
		for _, cmd := range peerVerbs("a2") {
			wantPeerRefusal(t, cmd.Action, peerDo(t, s, "a1", cmd), want)
		}
	}
	if !peerSendOK(t, s, "a1", "a2") {
		t.Fatal("setup: the conversation should start allowed")
	}

	if _, err := s.setGroupPeers("", "work", false); err != nil {
		t.Fatalf("turn peers off: %v", err)
	}
	all(`off for group "work"`)

	if _, err := s.setGroupPeers("", "work", true); err != nil {
		t.Fatalf("turn peers back on: %v", err)
	}
	if !peerSendOK(t, s, "a1", "a2") {
		t.Fatal("turning the flag back on should allow the next call")
	}

	// The TARGET moved out: still listed nowhere, readable nowhere.
	if err := s.groupPanels([]string{"a2"}, "other"); err != nil {
		t.Fatalf("move the target out: %v", err)
	}
	for _, cmd := range peerVerbs("a2")[1:] {
		wantPeerRefusal(t, cmd.Action, peerDo(t, s, "a1", cmd), `"a2" is not in your group "work"`)
	}
	// The CALLER moved out, into a group with peers off.
	if err := s.groupPanels([]string{"a1"}, "other"); err != nil {
		t.Fatalf("move the caller out: %v", err)
	}
	all(`off for group "other"`)

	if len(*written) != 2 {
		t.Fatalf("only the two allowed sends should have typed, got %q", *written)
	}
}

// TestPeerCapsAreTheDesignedOnes pins the three numbers themselves. Every other
// test here is written in terms of the constants so that it keeps testing the
// mechanism when one is retuned; this is the one that makes retuning a decision.
func TestPeerCapsAreTheDesignedOnes(t *testing.T) {
	if maxPeerSendBytes != 4096 || peerSendBurst != 3 || minPeerSendGap != 2*time.Second {
		t.Fatalf("peer caps = %d bytes, burst %d, gap %s; the design says 4096, 3, 2s",
			maxPeerSendBytes, peerSendBurst, minPeerSendGap)
	}
	// And the server New builds carries them, rather than a zero cap that admits
	// everything.
	if got := New(nil).peerSends; got.gap != minPeerSendGap || got.burst != peerSendBurst {
		t.Fatalf("New built the peer cap as gap %s burst %d", got.gap, got.burst)
	}
}

// TestPeerPrefixBoundsTheSendersTitle checks a long title cannot turn the
// prefix into the payload: it is cut to maxPeerTitleRunes before it is typed.
func TestPeerPrefixBoundsTheSendersTitle(t *testing.T) {
	s, _, written, _ := peerFleet()
	s.panels[0].Title = strings.Repeat("t", maxPeerTitleRunes+50)

	peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: "a2", Data: []byte("hi")})
	want := "a2:[from " + strings.Repeat("t", maxPeerTitleRunes) + " (a1)] hi\n"
	if len(*written) != 1 || (*written)[0] != want {
		t.Fatalf("typed %q, want %q", *written, want)
	}
}

// TestPeerSendTakesNoSubmitSequence pins that the sender does not choose how a
// send ends. A submit sequence would be typed raw and outside the size cap, so
// any non-empty one is refused — to an agent and to a shell alike — and nothing
// reaches the target.
func TestPeerSendTakesNoSubmitSequence(t *testing.T) {
	s, _, written, events := peerFleet()
	for _, target := range []string{"a2", "sh"} {
		for _, submit := range []string{"\r", "\n", strings.Repeat("x", 900_000)} {
			msg := peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: target, Data: []byte("x"), Submit: submit})
			wantPeerRefusal(t, "a submit sequence to "+target, msg, "takes no submit sequence")
		}
	}
	if len(*written) != 0 || len(*events) != 0 {
		t.Fatalf("a refused send must type and emit nothing, got %d writes / %+v", len(*written), *events)
	}
}

// TestPeerSendRefusesControlBytesToAnAgent walks the byte classes an agent
// target is never typed: a raw key drives the agent's interface rather than
// saying something to it. They are refused, not stripped — and a shell target,
// where typing keys is the point, gets the very same bytes verbatim.
func TestPeerSendRefusesControlBytesToAnAgent(t *testing.T) {
	const control = "control bytes are not sent to an agent"
	cases := []struct {
		name string
		data string
		want string
	}{
		{"ESC", "a\x1bb", control},
		{"a CSI key (Shift-Tab)", "\x1b[Z", control},
		{"Ctrl-C", "a\x03", control},
		{"NUL", "a\x00b", control},
		{"the C0 just under space", "a\x1fb", control},
		{"DEL", "a\x7fb", control},
		{"a C1 control (the 8-bit CSI)", "a\u009b31mb", control},
		{"a bidi override", "safe\u202eevil", control},
		{"a zero-width space", "a\u200bb", control},
		{"a byte-order mark", "\ufeffa", control},
		{"the replacement character", "a\ufffdb", control},
		{"invalid UTF-8", "a\xffb", "must be valid UTF-8"},
		{"a truncated rune", "caf\xc3", "must be valid UTF-8"},
		{"whitespace only", " \t\r\n\u0085\u2028\u2029 ", "needs something to send"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, written, events := peerFleet()
			msg := peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: "a2", Data: []byte(tc.data)})
			wantPeerRefusal(t, "to an agent", msg, tc.want)
			if len(*written) != 0 || len(*events) != 0 {
				t.Fatalf("a refused send must type and emit nothing, got %q / %+v", *written, *events)
			}

			if msg := peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: "sh", Data: []byte(tc.data)}); msg.Type != "notice" {
				t.Fatalf("the same bytes to a shell should be sent, got %+v", msg)
			}
			if want := []string{"sh:" + tc.data + "\n"}; !slices.Equal(*written, want) {
				t.Fatalf("a shell target must get the bytes verbatim:\n got %q\nwant %q", *written, want)
			}
		})
	}
}

// TestPeerSendFoldsLineBreaksForAnAgent pins that everything the prefix labels
// arrives as ONE submission: a line break inside the message would submit the
// labelled first line and type the rest unlabelled. A shell gets the same bytes
// untouched, and the size reported is the size given, not the folded one.
func TestPeerSendFoldsLineBreaksForAnAgent(t *testing.T) {
	s, _, written, events := peerFleet()
	const multi = "\n a\r\nb\n\n\rc\td \r"

	msg := peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: "a2", Data: []byte(multi)})
	if msg.Type != "notice" || !strings.Contains(msg.Notice, "sent 14 bytes") {
		t.Fatalf("the reply should report the 14 bytes as given, got %+v", msg)
	}
	peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: "sh", Data: []byte(multi)})

	want := []string{
		"a2:[from alice (a1)] a b c d\n",
		"sh:" + multi + "\n",
	}
	if !slices.Equal(*written, want) {
		t.Fatalf("typed into the targets:\n got %q\nwant %q", *written, want)
	}
	if len(*events) != 2 || (*events)[0].fields["bytes"] != len(multi) {
		t.Fatalf("the event should report the size as given (%d), got %+v", len(multi), *events)
	}

	// Every kind of line break folds, a mixed run of them and tabs to ONE space,
	// and none of them is refused for being a control or a separator.
	*written = nil
	const breaks = "a\u0085b\u2028c\u2029d\t\r\u0085\n\u2028\u2029\te"
	peerDo(t, s, "sh", proto.Command{Action: "peer.send", ID: "a2", Data: []byte(breaks)})
	peerDo(t, s, "a2", proto.Command{Action: "peer.send", ID: "sh", Data: []byte(breaks)})
	want = []string{"a2:[from term (sh)] a b c d e\n", "sh:" + breaks + "\n"}
	if !slices.Equal(*written, want) {
		t.Fatalf("typed into the targets:\n got %q\nwant %q", *written, want)
	}

	// The cap is on the bytes as given too: line breaks that would fold away
	// still count, so a message cannot be padded past it.
	over := []byte(strings.Repeat("\n", maxPeerSendBytes) + "x")
	wantPeerRefusal(t, "over the cap before folding",
		peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: "a2", Data: over}), "over the 4096-byte cap")
}

// peerWidenRefusal is the fence's one refusal, for the group it names.
func peerWidenRefusal(group string) string {
	return `group "` + group + `" has peer interaction on; the operator adds panels to it`
}

// peerGroupOf returns the group a panel sits in.
func peerGroupOf(t *testing.T, s *Server, id string) string {
	t.Helper()
	i := s.indexLocked(id)
	if i < 0 {
		t.Fatalf("no panel %q", id)
	}
	return s.panels[i].Group
}

// TestPeersOnGroupIsWidenedByTheOperatorOnly is the hole beside the flag's own
// fence: group.peers is refused to an agent and to the conductor, so neither
// may reach the same end by pulling an existing panel into a group that is
// already on. Every re-filing verb is walked, as each kind of caller.
func TestPeersOnGroupIsWidenedByTheOperatorOnly(t *testing.T) {
	type caller struct {
		name, self, role string
		fenced           bool
	}
	callers := []caller{
		{"an agent panel", "a1", "", true},
		{"an agent outside the group", "lone", "", true},
		{"the conductor role", "boss", roleConductor, true},
		{"the conductor role with no self", "", roleConductor, true},
		{"the cockpit", "", "", false},
		{"a shell panel", "sh", "", false},
		{"a self naming no panel", "ghost", "", false},
	}
	moves := []struct {
		name  string
		cmd   proto.Command
		moved string // a panel the command re-files…
		from  string // …out of this group…
		to    string // …into this peers-on one
	}{
		{"panel.group", proto.Command{Action: "panel.group", IDs: []string{"o1"}, Group: "work"}, "o1", "other", "work"},
		{"panel.group, one already inside and one not", proto.Command{Action: "panel.group", IDs: []string{"a2", "o1"}, Group: "work"}, "o1", "other", "work"},
		{"panel.group of an ungrouped panel", proto.Command{Action: "panel.group", IDs: []string{"lone"}, Group: "work"}, "lone", "", "work"},
		{"panel.rename onto the group", proto.Command{Action: "panel.rename", Group: "other", Name: "work"}, "o1", "other", "work"},
		{"panel.ungroup dissolving a sub-group into it", proto.Command{Action: "panel.ungroup", Group: "work/sub"}, "sub", "work/sub", "work"},
	}
	for _, mv := range moves {
		for _, who := range callers {
			t.Run(mv.name+"/"+who.name, func(t *testing.T) {
				s, _, _, _ := peerFleet()
				s.allowNameConflict = true // or a rename onto a taken name stops at the name check
				cc := ctl(who.self)
				cc.role = who.role
				s.onCommand(cc, mv.cmd)
				err := replyErr(cc)

				if !who.fenced {
					if err != "" || peerGroupOf(t, s, mv.moved) != mv.to {
						t.Fatalf("the operator's %s should go through: error %q, %s sits in %q",
							mv.cmd.Action, err, mv.moved, peerGroupOf(t, s, mv.moved))
					}
					if !s.groupPeers[mv.to] {
						t.Fatalf("group %q should still be peers-on after the operator added to it", mv.to)
					}
					return
				}
				if err != peerWidenRefusal(mv.to) {
					t.Fatalf("want the refusal %q, got %q", peerWidenRefusal(mv.to), err)
				}
				// Refused whole: nothing moved, not even the panel that was allowed to.
				want := map[string]string{"a1": "work", "a2": "work", "o1": "other", "sub": "work/sub", "lone": ""}
				for id, group := range want {
					if got := peerGroupOf(t, s, id); got != group {
						t.Fatalf("a refused %s must move nothing: %s went from %q to %q", mv.cmd.Action, id, group, got)
					}
				}
			})
		}
	}
}

// TestPeersFenceLeavesOrdinaryRegroupsAlone is the other side: the fence is
// about ADDING to a peers-on group and nothing else. An agent may still re-file
// a panel where it already sits, move panels between peers-off groups, take
// one out of a peers-on group, and dissolve a group whose parent is off.
func TestPeersFenceLeavesOrdinaryRegroupsAlone(t *testing.T) {
	cases := []struct {
		name  string
		cmd   proto.Command
		panel string
		want  string
	}{
		{"re-filing a panel already in the peers-on group", proto.Command{Action: "panel.group", IDs: []string{"a2"}, Group: "work"}, "a2", "work"},
		{"into a peers-off group", proto.Command{Action: "panel.group", IDs: []string{"lone"}, Group: "other"}, "lone", "other"},
		{"out of the peers-on group into a peers-off one", proto.Command{Action: "panel.group", IDs: []string{"a2"}, Group: "other"}, "a2", "other"},
		{"out of the peers-on group by id", proto.Command{Action: "panel.ungroup", IDs: []string{"a2"}}, "a2", ""},
		{"renaming the peers-on group itself", proto.Command{Action: "panel.rename", Group: "work", Name: "team"}, "a2", "team"},
		{"dissolving a group with a peers-off parent", proto.Command{Action: "panel.ungroup", Group: "other"}, "o1", ""},
		{"reordering", proto.Command{Action: "panel.move", IDs: []string{"o1"}, Index: 0}, "o1", "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _ := peerFleet()
			cc := ctl("a1")
			s.onCommand(cc, tc.cmd)
			if err := replyErr(cc); err != "" {
				t.Fatalf("an agent's %s should not be fenced, got %q", tc.cmd.Action, err)
			}
			if got := peerGroupOf(t, s, tc.panel); got != tc.want {
				t.Fatalf("%s should sit in %q, sits in %q", tc.panel, tc.want, got)
			}
		})
	}
}

// TestPeersFenceIsNotOnASubGroupsOwnRekey pins the one destination the fence
// must not count. Dissolving "a/b" moves its sub-group "a/b/b" up onto "a/b",
// and "a/b" is peers-on — but its own panels and its flag are leaving for "a"
// in the same step, so nobody is staying there to be added to.
func TestPeersFenceIsNotOnASubGroupsOwnRekey(t *testing.T) {
	s, _, _ := gateServer(
		panel.Panel{ID: "w", Kind: panel.Agent, State: panel.Idle},
		panel.Panel{ID: "own", Kind: panel.Agent, Group: "a/b", State: panel.Idle},
		panel.Panel{ID: "kid", Kind: panel.Agent, Group: "a/b/b", State: panel.Idle},
	)
	s.groupPeers = map[string]bool{"a/b": true}

	cc := ctl("w")
	s.onCommand(cc, proto.Command{Action: "panel.ungroup", Group: "a/b"})
	if err := replyErr(cc); err != "" {
		t.Fatalf("dissolving a group is not adding to it, got %q", err)
	}
	if peerGroupOf(t, s, "own") != "a" || peerGroupOf(t, s, "kid") != "a/b" {
		t.Fatalf("the dissolve should have promoted both: own in %q, kid in %q", peerGroupOf(t, s, "own"), peerGroupOf(t, s, "kid"))
	}
	// The flag went with the panels it was set for, and the sub-group that took
	// the old path did not inherit it.
	if !s.groupPeers["a"] || s.groupPeers["a/b"] {
		t.Fatalf("the flag should have followed its panels to \"a\", got %v", s.groupPeers)
	}
}

// TestServerOwnSpawnIntoAPeersOnGroupIsNotFenced: a NEW panel filed straight
// into a peers-on group by the server — a worktree spawn under its branch, a
// plugin's spawn — is not the fence's business. Those go through groupPanels,
// which has no connection to ask.
func TestServerOwnSpawnIntoAPeersOnGroupIsNotFenced(t *testing.T) {
	s, _, _, _ := peerFleet()
	if err := s.groupPanels([]string{"lone"}, "work"); err != nil {
		t.Fatalf("the server's own filing into a peers-on group should go through: %v", err)
	}
	if peerGroupOf(t, s, "lone") != "work" || !s.groupPeers["work"] {
		t.Fatalf("lone should sit in a still peers-on work, sits in %q", peerGroupOf(t, s, "lone"))
	}
}

// TestGroupPeersValidatesTheNameBothWays: turning the flag OFF used to skip the
// name check, so a name of any size and any bytes reached an event, a log line
// and a broadcast. Both directions refuse what panel.group refuses.
func TestGroupPeersValidatesTheNameBothWays(t *testing.T) {
	bad := map[string]string{
		"an escape sequence":              "work\x1b[2J\x07",
		"too long":                        strings.Repeat("g", maxNameRunes+1),
		"a hundred kilobytes":             strings.Repeat("g\x1b", 50_000),
		"an empty segment":                "work//sub",
		"an invalid path a megabyte long": strings.Repeat("g//", 300_000),
	}
	for name, group := range bad {
		for _, on := range []bool{true, false} {
			s, _, _, events := peerFleet()
			changed, err := s.setGroupPeers("", group, on)
			if err == nil || changed {
				t.Fatalf("%s (enable=%v): want a refusal, got changed=%v err=%v", name, on, changed, err)
			}
			if len(err.Error()) > 1024 {
				t.Fatalf("%s (enable=%v): the refusal carries %d bytes of the name back", name, on, len(err.Error()))
			}
			if len(*events) != 0 || len(s.groupPeers) != 1 {
				t.Fatalf("%s (enable=%v): a refused name must change and emit nothing: %+v", name, on, *events)
			}
		}
	}
}

// TestGroupPeersNoOpSaysNothing: asking for the state a group is already in is
// a success that did nothing, so it emits nothing and broadcasts nothing — the
// caller alone gets the snapshot it reads as its answer.
func TestGroupPeersNoOpSaysNothing(t *testing.T) {
	s, _, _, events := peerFleet()
	watcher := ctl("")
	watcher.greeted = true
	s.clients[watcher] = struct{}{}

	for _, cmd := range []proto.Command{
		{Action: "group.peers", Group: "nobody"},             // off, never on
		{Action: "group.peers", Group: "other"},              // off, already off
		{Action: "group.peers", Group: "work", Enable: true}, // on, already on
	} {
		cc := ctl("")
		s.onCommand(cc, cmd)
		if msg := peerReply(t, cc); msg.Type != "panels" {
			t.Fatalf("a no-op %+v should still answer its caller with the snapshot, got %+v", cmd, msg)
		}
		if len(*events) != 0 || len(watcher.out) != 0 {
			t.Fatalf("a no-op %+v must emit and broadcast nothing: %d events, %d broadcasts", cmd, len(*events), len(watcher.out))
		}
	}
	if len(s.groupPeers) != 1 || !s.groupPeers["work"] {
		t.Fatalf("the no-ops must have changed nothing, got %v", s.groupPeers)
	}

	// The control: a real flip does emit, and does reach the other client.
	cc := ctl("")
	s.onCommand(cc, proto.Command{Action: "group.peers", Group: "work"})
	if len(*events) != 1 || len(watcher.out) != 1 || s.groupPeers["work"] {
		t.Fatalf("a real flip should emit once and broadcast once: %d events, %d broadcasts, flags %v", len(*events), len(watcher.out), s.groupPeers)
	}
}

// TestPeerPrefixScrubsTheSendersID: ids are minted by the server, but a restore
// takes them from the state file as written. Whatever one holds, the prefix is
// one line with nothing in it a terminal acts on.
func TestPeerPrefixScrubsTheSendersID(t *testing.T) {
	s, _, written := gateServer(
		panel.Panel{ID: "p\x1b[2J\n9" + strings.Repeat("9", 200), Kind: panel.Agent, Title: "alice", Group: "work", State: panel.Idle},
		panel.Panel{ID: "a2", Kind: panel.Agent, Title: "bob", Group: "work", State: panel.Idle},
	)
	s.groupPeers = map[string]bool{"work": true}

	if msg := peerDo(t, s, s.panels[0].ID, proto.Command{Action: "peer.send", ID: "a2", Data: []byte("hi")}); msg.Type != "notice" {
		t.Fatalf("the send should be admitted, got %+v", msg)
	}
	want := "a2:[from alice (p[2J 9" + strings.Repeat("9", maxPeerTitleRunes-6) + ")] hi\n"
	if len(*written) != 1 || (*written)[0] != want {
		t.Fatalf("typed %q, want %q", *written, want)
	}
}

// TestRefusalsDoNotEchoALongName: a refusal that quotes a group name back must
// not be a way to have 120 KB returned. Every verb that takes a group name off
// the wire is sent one, as a valid path and as an invalid one.
func TestRefusalsDoNotEchoALongName(t *testing.T) {
	names := map[string]string{
		"one long segment": strings.Repeat("g", 120_000),
		"an invalid path":  strings.Repeat("g//", 40_000),
		"a deep path":      strings.Repeat("g/", 60_000) + "g",
	}
	for what, long := range names {
		for _, cmd := range []proto.Command{
			{Action: "panel.group", IDs: []string{"a1"}, Group: long},
			{Action: "panel.ungroup", Group: long},
			{Action: "panel.rename", Group: long, Name: "team"},
			{Action: "panel.rename", Group: "work", Name: long},
			{Action: "panel.dispatch-group", Group: long, Prompt: "go"},
			{Action: "group.peers", Group: long, Enable: true},
			{Action: "group.peers", Group: long},
		} {
			s, _, _, _ := peerFleet()
			cc := ctl("")
			s.onCommand(cc, cmd)
			err := replyErr(cc)
			if err == "" {
				t.Fatalf("%s with %s (%d bytes) should be refused", cmd.Action, what, len(long))
			}
			if len(err) > 1024 {
				t.Fatalf("%s with %s: the refusal is %d bytes long: %.80s…", cmd.Action, what, len(err), err)
			}
		}
	}
}

// TestRestoreSkipsAPanelWithAnInvalidID: a snapshot is a file. An id is typed
// into terminals and quoted in replies as it stands, so a record whose id is
// not a clean name is left out rather than carried into the fleet.
func TestRestoreSkipsAPanelWithAnInvalidID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st := state.State{Seq: 3, Panels: []state.PanelState{
		{ID: "1", Kind: "agent", Title: "good", Group: "work"},
		{ID: "p\x1b[2J", Kind: "agent", Title: "escape", Group: "work"},
		{ID: "7\n8", Kind: "agent", Title: "newline", Group: "work"},
		{ID: "a\u202eb", Kind: "agent", Title: "bidi", Group: "work"},
		{ID: "", Kind: "agent", Title: "empty", Group: "work"},
		{ID: strings.Repeat("9", maxNameRunes+1), Kind: "agent", Title: "long", Group: "work"},
		{ID: "2", Kind: "shell", Title: "also good", Group: "work"},
	}}
	if err := st.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	s := New(nil, WithStateFile(path))
	s.Restore()

	var got []string
	for _, p := range s.panels {
		got = append(got, p.ID)
	}
	if want := []string{"1", "2"}; !slices.Equal(got, want) {
		t.Fatalf("restored panels %q, want only the clean ids %q", got, want)
	}
	if len(s.specs) != 2 {
		t.Fatalf("a skipped panel must leave no spawn spec behind, got %d specs", len(s.specs))
	}
}

// TestPeerSendNoticeScrubsTheTargetID is the defence behind that one: whatever
// id a target carries, the sender's notice is one clean line.
func TestPeerSendNoticeScrubsTheTargetID(t *testing.T) {
	bad := "t\x1b[2J\n9" + strings.Repeat("9", 200)
	s, _, _ := gateServer(
		panel.Panel{ID: "a1", Kind: panel.Agent, Title: "alice", Group: "work", State: panel.Idle},
		panel.Panel{ID: bad, Kind: panel.Shell, Title: "bob", Group: "work", State: panel.Idle},
	)
	s.groupPeers = map[string]bool{"work": true}

	msg := peerDo(t, s, "a1", proto.Command{Action: "peer.send", ID: bad, Data: []byte("hi")})
	want := "sent 2 bytes to bob (t[2J 9" + strings.Repeat("9", maxPeerTitleRunes-6) + ")"
	if msg.Type != "notice" || msg.Notice != want {
		t.Fatalf("notice %q, want %q", msg.Notice, want)
	}
}
