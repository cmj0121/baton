package server

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// This file is the WRITE side of the detection precedence's top rung: an agent
// saying, in its own words, that it needs a human, and later saying the need has
// passed. Everything below it — the quiet timers, the tail heuristic — is baton
// guessing about a panel from the outside. This is the one signal that came from
// the thing being described, which is why it outranks both.
//
// The read side (declaredLocked, the declared flag on stateSignals, rung 1 of
// nextState) lives with the Monitor. Nothing here decides a state itself: both
// verbs go back through nextState, so the precedence stays in exactly one switch.

// maxReasonRunes caps how much of an agent's reason the server keeps. It is a
// sentence, not a payload: the inbox has one row for it and the card one line,
// and the value rides every fleet snapshot to every client, so an agent that
// sends a hundred thousand characters must not make the whole fleet carry them.
const maxReasonRunes = 200

// declareAttention handles panel.attention: an agent raises its own hand, with a
// reason, and the panel enters attention immediately.
//
// Immediately is the operative word. The scheduler's free pool and the dispatch
// ready-gate both read nothing but panel.State, so a declaration that only took
// effect on the next monitor tick would leave a window of up to a second in
// which baton hands backlog work to a panel that has already said it is waiting
// on a person — the exact opposite of what the declaration asked for. Re-deriving
// here closes it: by the time this returns, the panel is out of freeForWork.
//
// An empty reason is refused rather than accepted as a bare flag. A declaration
// displaces the timer and the heuristic precisely because it can say WHY, and one
// that cannot say why is worth no more than the guess it would displace — while
// still being immune to the output that withdraws a guessed attention.
//
// The reply is the server's error, or the fleet snapshot the change produced. It
// is a plain broadcast rather than broadcastFleet: a declaration is a live
// process's statement about itself and is deliberately not persisted, so there is
// nothing here to mark dirty.
func (s *Server) declareAttention(cc *clientConn, cmd proto.Command) {
	id, err := s.attentionTarget(cc, cmd)
	if err != nil {
		send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
		return
	}
	reason := sanitizeReason(cmd.Reason)
	if reason == "" {
		send(cc, proto.ServerMsg{Type: "error", Error: "panel.attention: a reason is required"})
		return
	}

	s.mu.Lock()
	i := s.indexLocked(id)
	switch {
	case i < 0:
		s.mu.Unlock()
		send(cc, proto.ServerMsg{Type: "error", Error: fmt.Sprintf("no panel with id %q", id)})
		return
	case s.panels[i].State == panel.Exited:
		s.mu.Unlock()
		send(cc, proto.ServerMsg{Type: "error", Error: fmt.Sprintf("panel %q has exited and cannot ask for anything", id)})
		return
	}
	// A fresh entry, so Cleared is zeroed with it: a new declaration ends whatever
	// heuristic suppression an earlier resolve left behind.
	s.declared[id] = &declaration{Reason: reason}
	s.panels[i].Reason = reason
	s.rederiveLocked(i, s.panels[i].State)
	s.mu.Unlock()

	s.broadcast(s.panelsMsg())
}

// resolveAttention handles panel.resolve: the agent withdraws its declaration
// and the panel's state is derived again rather than guessed back to a named one.
//
// It re-enters the ladder at running, and that is a decision worth its lines. No
// rung of the precedence walks a panel DOWN out of attention — every resting
// state holds until output arrives — so re-deriving from attention would resolve
// to attention and the verb would do nothing. Re-entering at running instead asks
// the ladder the question it was built to answer, from the same quiet clock the
// panel already carries: silent, it settles to idle (and climbs to done or stuck
// on the ticks that follow); still producing output, it stays running. What it
// never does is jump to a state this function picked.
//
// Withdrawing a declaration that does not stand is a no-op and not an error. A
// resolve is an agent tidying up after itself, and an agent should be free to
// send one without first having to know whether its hand is still up — after a
// restart, after its panel died, after a human already dealt with it.
func (s *Server) resolveAttention(cc *clientConn, cmd proto.Command) {
	id, err := s.attentionTarget(cc, cmd)
	if err != nil {
		send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
		return
	}

	s.mu.Lock()
	i := s.indexLocked(id)
	if i < 0 || s.panels[i].State == panel.Exited || !s.declaredLocked(id) {
		// Nothing stands, so the agent asked for a state it is already in. An exited
		// panel is refused for the same reason declareAttention refuses one: rung 0
		// of the ladder says exited is terminal, and re-deriving one would resurrect
		// a dead panel as running. onPanelExit drops the entry so this cannot happen
		// today; the invariant belongs to the function that would break it, not to
		// something else remembering to tidy up first.
		s.mu.Unlock()
		return
	}
	// The entry survives the resolve, carrying Cleared instead of a reason. That
	// is the whole of the suppression mechanism — see suppressedLocked.
	s.declared[id] = &declaration{Cleared: s.mon.now()}
	s.panels[i].Reason = ""
	s.rederiveLocked(i, panel.Running)
	s.mu.Unlock()

	s.broadcast(s.panelsMsg())
}

// attentionTarget resolves which panel a declaration verb is about: the id the
// command names, or — when it names none — the panel the connection declared as
// its own on hello.
//
// The empty-id form is the one an agent is meant to use. `baton ctl attention`
// inside a panel baton identified needs no argument and no way to learn its own
// id: the identity is already on the connection, so an agent can only ever raise
// its own hand by accident of addressing.
//
// Every agent panel can use it: createPanel injects the identity env
// (paths.EnvPanelID) into each one, so a worker agent's connection carries a
// self exactly as the conductor's does. A shell panel deliberately does not get
// one — a shell is a launcher, and every child it starts would inherit a panel
// id that is not its own — so `baton ctl attention` from a shell must still name
// a panel with --id.
func (s *Server) attentionTarget(cc *clientConn, cmd proto.Command) (string, error) {
	if cmd.ID != "" {
		return cmd.ID, nil
	}
	if cc.self == "" {
		return "", fmt.Errorf("%s: no panel id, and this connection declared no self", cmd.Action)
	}
	return cc.self, nil
}

// rederiveLocked runs one panel through the detection ladder now, with base
// standing in for the state the ladder reads, and applies the result. It reports
// whether the panel moved. Caller holds s.mu.
//
// It exists so a declaration and a resolve take effect through the SAME switch
// the monitor tick uses, rather than assigning a state of their own. DESIGN §2.2
// makes that a rule and not a preference: nothing outside nextState may decide a
// panel's state from a declaration, a timer, or a heuristic, or the precedence
// stops being a precedence and becomes three of them.
//
// taskDone is passed as false rather than read from s.taskSettled because that
// set is an EDGE the monitor tick consumes: only the tick that saw a task go
// terminal-done may act on it. Reading it here would either swallow the edge
// (the tick that follows would not see it) or double-count it.
func (s *Server) rederiveLocked(i int, base panel.State) bool {
	p := &s.panels[i]
	sig := s.signalsLocked(*p, false)
	sig.cur = base
	ns, _ := nextState(sig)
	if ns == p.State {
		return false
	}
	from := p.State
	p.State = ns
	s.mon.entered(p.ID)
	p.Activity = activityText(ns, s.mon.since(p.ID))
	f := panelFields(*p)
	f["from"], f["to"] = from.String(), ns.String()
	s.emit("panel.state", f)
	if ns == panel.Attention {
		s.emit("panel.attention", panelFields(*p))
	}
	return true
}

// suppressedLocked reports whether the tail heuristic is muted for a panel: true
// while the instant a resolve recorded is at or after the panel's last byte of
// output. Caller holds s.mu.
//
// Without it, resolve would be a verb that undoes itself. The tail that made
// looksLikeAttention say yes is still sitting in the ring buffer after the
// resolve, unchanged, because the panel has not spoken since — so the very next
// tick would read the same bytes, reach the same conclusion, and put the panel
// straight back into attention. An agent would have no way to say "I dealt with
// it" that survived a second.
//
// A byte of new output ends the suppression, and that is the right edge to end it
// on: a new tail is a new claim. The panel has said something since the human
// was let off the hook, and whatever it says now deserves to be read on its own
// terms rather than dismissed by an answer to the last thing it said.
func (s *Server) suppressedLocked(id string) bool {
	d := s.declared[id]
	if d == nil || d.Cleared.IsZero() {
		return false
	}
	return !d.Cleared.Before(s.mon.lastByte(id))
}

// sanitizeReason scrubs an agent-supplied reason at the boundary it enters the
// server on, and it is deliberate that the scrubbing happens HERE rather than in
// each renderer.
//
// A reason is written by an agent, stored by the daemon, and then fanned out to
// every consumer there is: the cockpit's inbox (which draws it into a real
// terminal), `baton ctl`, an MCP tool result, a plugin's event handler. Left raw,
// each of those would have to remember to neutralise it, and the first one to
// forget hands an agent a cursor-control or OSC 52 clipboard write on the
// operator's terminal. Scrubbing once, where the untrusted text crosses into
// baton, means every reader downstream is holding text that is already safe —
// frontends MUST NOT escape it a second time. (This is the opposite of the rule
// for panel OUTPUT, which the server passes through byte-exact because it is a
// terminal stream and the emulator is what interprets it. A reason is a field.)
//
// It keeps printable runes, folds every run of whitespace into one space so a
// reason is one line by construction (a card and an inbox row both have exactly
// one to give it), and drops three classes of rune:
//
//   - Control characters (Cc, which is both C0 and C1). An escape sequence loses
//     its ESC and leaves its parameters behind as plain text — "[1;31m" rather
//     than a colour — which is not tidied up on purpose: a reason that tried to
//     carry an escape should look wrong to whoever reads it, not quietly become
//     clean prose.
//   - FORMAT characters (Cf): U+202E RIGHT-TO-LEFT OVERRIDE and the bidi
//     isolates render a line backwards, U+200B is invisible. None of them are
//     control characters, so IsControl does not see them, and since the contract
//     above tells every frontend the text is already safe, this is the last place
//     they can be stopped.
//   - The replacement character, which is what an invalid UTF-8 byte decodes to.
//
// The result is capped at maxReasonRunes. A reason is a sentence for a person to
// read, and the value is broadcast to every client on every fleet snapshot — an
// unbounded agent-controlled string on that path is a cost the fleet pays over
// and over for text no inbox row could show.
func sanitizeReason(reason string) string {
	var b strings.Builder
	b.Grow(len(reason))
	pendingSpace := false
	for _, r := range reason {
		switch {
		case unicode.IsSpace(r):
			pendingSpace = true
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar:
			continue
		default:
			if pendingSpace && b.Len() > 0 {
				b.WriteRune(' ')
			}
			pendingSpace = false
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > maxReasonRunes { // bytes >= runes, so this skips the common case
		if rs := []rune(out); len(rs) > maxReasonRunes {
			out = strings.TrimRight(string(rs[:maxReasonRunes]), " ")
		}
	}
	return out
}
