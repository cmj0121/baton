package server

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/scrub"
)

// The peer verbs: what a panel may do to the other panels of a group the
// operator switched on (group.peers) — list them, read one's trailing output,
// type into one.
//
// Nothing here is granted at spawn. Group membership and the flag both change
// while a panel runs, so every call is decided again from the fleet as it
// stands, under s.mu, by the panel the connection declared as its own on hello.
// A panel moved out of the group, or a flag turned off, mid-conversation is
// refused on its next call with the reason.
//
// Like the conductor fence this is a guardrail and not a boundary: a self is
// declared, not proven, and a worker's plain `baton ctl send` is as unfenced as
// it always was. peer.* is the easy road, which is why it is the one that says
// who is typing and refuses a flood.

const (
	// maxPeerSendBytes caps one peer.send, counted on what the sender gave — the
	// prefix an agent target gets is the server's own and is not charged.
	maxPeerSendBytes = 4096

	// minPeerSendGap and peerSendBurst are the per-sender cap on peer.send: three
	// at once, then one every two seconds. Two agents answering each other is the
	// obvious way this feature goes wrong, and a capped send is refused rather
	// than queued so the model reads the refusal instead of the loop continuing
	// behind it.
	minPeerSendGap = 2 * time.Second
	peerSendBurst  = 3

	// maxPeerTitleRunes caps the sender's title inside the prefix. A title is
	// whatever a rename set, so it is scrubbed to one line and bounded before it
	// is typed into somebody else's terminal.
	maxPeerTitleRunes = 64
)

// peerCallerLocked resolves the panel a peer.* call is made by, or says why
// there is none: the connection declared no panel, the panel is gone or exited,
// it is one of the two operator-owned singletons, or its group is empty or has
// peers off. The group is the EXACT path — the flag on "a" says nothing about
// "a/b". Caller holds s.mu.
func (s *Server) peerCallerLocked(self string) (panel.Panel, string) {
	if self == "" {
		return panel.Panel{}, "peer: this connection declared no panel of its own"
	}
	i := s.indexLocked(self)
	if i < 0 || s.panels[i].State == panel.Exited {
		return panel.Panel{}, fmt.Sprintf("peer: %q is not a live panel", self)
	}
	me := s.panels[i]
	switch {
	case me.Conductor || me.GlobalShell:
		return panel.Panel{}, "peer: the conductor and the global shell are not peers"
	case me.Group == "":
		return panel.Panel{}, fmt.Sprintf("peer: panel %q is in no group", self)
	case !s.groupPeers[me.Group]:
		return panel.Panel{}, fmt.Sprintf("peer: peer interaction is off for group %q; the operator switches it on", me.Group)
	}
	return me, ""
}

// peerTargetReason says why p is not a panel me may reach, or "" when it is.
// peer.list filters on it and peer.tail / peer.send refuse on it, so what a
// caller is shown and what it is then allowed cannot drift apart.
func peerTargetReason(me, p panel.Panel) string {
	switch {
	case p.ID == me.ID:
		return fmt.Sprintf("peer: %q is your own panel", p.ID)
	case p.Group != me.Group:
		return fmt.Sprintf("peer: panel %q is not in your group %q", p.ID, me.Group)
	case p.Conductor:
		return fmt.Sprintf("peer: panel %q is the conductor, which is never a peer", p.ID)
	case p.GlobalShell:
		return fmt.Sprintf("peer: panel %q is the global shell, which is never a peer", p.ID)
	case p.State == panel.Exited:
		return fmt.Sprintf("peer: panel %q has exited", p.ID)
	}
	return ""
}

// peerReasonLocked is the whole authorisation of one caller reaching one
// target: "" when self may read and type into target, the refusal otherwise.
// It returns both panels because every caller needs them next. Caller holds
// s.mu.
func (s *Server) peerReasonLocked(self, target string) (me, to panel.Panel, reason string) {
	me, reason = s.peerCallerLocked(self)
	if reason != "" {
		return me, to, reason
	}
	if target == "" {
		return me, to, "peer: a target panel id is required"
	}
	i := s.indexLocked(target)
	if i < 0 {
		return me, to, fmt.Sprintf("peer: no panel with id %q", target)
	}
	to = s.panels[i]
	return me, to, peerTargetReason(me, to)
}

// peerList handles peer.list: the group-mates the caller may reach, and only
// those — never itself, a singleton or an exited slot.
func (s *Server) peerList(cc *clientConn) {
	s.mu.Lock()
	me, reason := s.peerCallerLocked(cc.self)
	if reason != "" {
		s.mu.Unlock()
		send(cc, proto.ServerMsg{Type: "error", Error: reason})
		return
	}
	pids, held := s.pty.Pids(), s.pty.Held()
	var peers []proto.Panel
	for _, p := range s.panels {
		if peerTargetReason(me, p) == "" {
			peers = append(peers, s.wirePanel(p, pids, held))
		}
	}
	s.mu.Unlock()
	send(cc, proto.ServerMsg{Type: "peers", Panels: peers})
}

// peerTail handles peer.tail: a group-mate's trailing output, through the same
// clamp and the same read the inbox uses (sendTail).
//
// What comes back is the RAW tail of the target's replay ring — escape
// sequences included, capped in bytes and not in lines — which is exactly what
// panel.tail gives a cockpit. A cockpit hands it to a terminal emulator; a
// client that hands it to a model instead scrubs it first.
func (s *Server) peerTail(cc *clientConn, cmd proto.Command) {
	s.mu.Lock()
	_, _, reason := s.peerReasonLocked(cc.self, cmd.ID)
	s.mu.Unlock()
	if reason != "" {
		send(cc, proto.ServerMsg{Type: "error", Error: reason})
		return
	}
	s.sendTail(cc, cmd.ID, cmd.Count)
}

// peerSendLocked decides one peer.send and, when it is admitted, charges the
// sender's rate allowance and returns the text to type. The rate check is LAST
// on purpose: it is the only one that spends something, so a send refused for
// any other reason costs the sender nothing. Caller holds s.mu.
//
// The size cap is checked on data AS GIVEN, before an agent target's line
// breaks are folded, and that same len(data) is the size the reply, the log
// line and the event report: it is what the sender asked to send.
//
// A submit sequence is refused rather than honoured. It would be typed raw and
// outside the cap, and a sender that could choose it could choose none — a
// message left unsent in a group-mate's input line for the next one to land on.
// Every admitted send ends in the default Enter.
func (s *Server) peerSendLocked(self, target string, data []byte, submit string, now time.Time) (me, to panel.Panel, text, reason string) {
	me, to, reason = s.peerReasonLocked(self, target)
	switch {
	case reason != "":
		return me, to, "", reason
	case submit != "":
		return me, to, "", "peer: peer.send takes no submit sequence; every send ends in Enter"
	case len(data) == 0:
		return me, to, "", peerEmptyReason
	case len(data) > maxPeerSendBytes:
		return me, to, "", fmt.Sprintf("peer: peer.send carries %d bytes, over the %d-byte cap", len(data), maxPeerSendBytes)
	}
	text = string(data)
	if to.IsAgent() {
		if text, reason = peerAgentText(data); reason != "" {
			return me, to, "", reason
		}
	}
	if to.State == panel.Attention {
		// An agent must not answer another agent's permission prompt: a panel
		// waiting on a human stays the operator's until they have dealt with it.
		//
		// This is best-effort. The monitor sets attention on a tick, after it has
		// sniffed the tail, so a send can still land between a prompt being drawn
		// and the tick that sees it.
		return me, to, "", fmt.Sprintf("peer: panel %q is waiting for a human; it takes no peer input until the operator answers", to.ID)
	}
	if wait, refuse := s.peerSends.tooSoon(self, now); refuse {
		return me, to, "", fmt.Sprintf("peer: sending too fast; retry in %s", wait.Round(100*time.Millisecond))
	}
	return me, to, text, ""
}

// peerEmptyReason refuses a send with nothing in it — no bytes at all, or, to
// an agent, nothing but whitespace.
const peerEmptyReason = "peer: peer.send needs something to send"

// peerAgentText turns what a peer sent into the ONE line an agent target is
// typed, or says why it is not sent at all.
//
// An agent and a shell are treated differently, and deliberately. An agent
// reads the `[from …]` prefix to tell a peer from its human, so everything the
// prefix labels has to arrive as a single submission: a line break inside the
// message would submit the labelled first line and then type the rest as
// unlabelled input, and a raw key — ESC, Ctrl-C, a tab, a CSI sequence such as
// Shift-Tab — would drive the agent's own interface rather than say something
// to it. A shell has no reader to mislead, and typing into a shell, control
// keys and all, is the point (see peerPayload).
//
// ONE RULE COVERS BOTH HALVES OF THE LINE. The sender's title in the prefix
// and the body after it are held to the same definition of what a terminal
// executes or is lied to by — scrub.Drop: C0 and C1 controls, DEL, the format
// characters (bidi overrides, zero-width), U+FFFD. Nothing that predicate
// names is ever typed into an agent by this verb. The halves differ only in
// what happens to it: the title is the server's own label, so it is scrubbed
// in place (scrub.Capped, in peerPayload); the body is the sender's message,
// so it is REFUSED, because a message quietly altered on the way is worse than
// one the sender is told to send again as plain text. Invalid UTF-8 is refused
// with it.
//
// What is folded rather than refused is the whitespace a sender writes without
// meaning a key by it: a tab and every line break — CR, LF, NEL (U+0085), the
// line and paragraph separators (U+2028, U+2029). Each run of them, mixed in
// any order, becomes one space, and the ends are trimmed. The fold is asked
// FIRST and that order is load-bearing: tab, CR, LF and NEL are controls, so
// scrub.Drop names them too, and asking it first would refuse every multi-line
// message.
func peerAgentText(data []byte) (text, reason string) {
	if !utf8.Valid(data) {
		return "", "peer: text sent to an agent must be valid UTF-8"
	}
	var b strings.Builder
	b.Grow(len(data))
	inBreak := false
	for _, r := range string(data) {
		switch {
		case peerFolds(r): // before scrub.Drop: four of these are controls too
			if !inBreak {
				b.WriteByte(' ')
			}
			inBreak = true
			continue
		case scrub.Drop(r):
			return "", "peer: control bytes are not sent to an agent; send plain text"
		}
		inBreak = false
		b.WriteRune(r)
	}
	if text = strings.TrimSpace(b.String()); text == "" {
		return "", peerEmptyReason
	}
	return text, ""
}

// peerFolds reports whether r is whitespace an agent target gets as a single
// space: a tab or any line break. See peerAgentText.
func peerFolds(r rune) bool {
	switch r {
	case '\t', '\n', '\r', '\u0085', '\u2028', '\u2029':
		return true
	}
	return false
}

// peerPayload is the text a peer.send types into its target. An agent is told
// who is typing, because it has to tell a peer from its human; a shell (or a
// command panel) gets the bytes as they were sent, because a prefix would
// corrupt the command. Neither carries the Enter: peerSend has typeLine add the
// target's submit, as a dispatched brief's is added.
func peerPayload(me, to panel.Panel, text string) []byte {
	if to.IsAgent() {
		// The id is scrubbed with the title. The server mints ids, but a restore
		// takes them from the state file as written, and this line is typed into
		// somebody else's terminal.
		text = fmt.Sprintf("[from %s (%s)] %s",
			scrub.Capped(me.Title, maxPeerTitleRunes), scrub.Capped(me.ID, maxPeerTitleRunes), text)
	}
	return []byte(text)
}

// peerSend handles peer.send: type into a group-mate. It is delivered with the
// primitive a dispatched brief ends in (submitFor + typeLine) and nothing
// else of a dispatch — no task is recorded on the target, no brief reaches its
// card and no task hook runs, because a peer's message is not an assignment.
//
// The reply is a notice to the sender alone, carrying the target's id. The id
// is what lets a client tell this reply from the notices the daemon broadcasts
// to every connection for its own reasons. There is no fleet broadcast: nothing
// a snapshot carries changed.
func (s *Server) peerSend(cc *clientConn, cmd proto.Command) {
	s.mu.Lock()
	me, to, text, reason := s.peerSendLocked(cc.self, cmd.ID, cmd.Data, cmd.Submit, s.mon.now())
	s.mu.Unlock()
	if reason != "" {
		send(cc, proto.ServerMsg{Type: "error", Error: reason})
		return
	}

	// The submit is the target's, never the sender's (peerSendLocked refuses
	// one): "" lets submitFor take the target profile's sequence. typeLine blocks
	// for the gap on this connection goroutine, which is only this sender's wait.
	submit, agent := s.submitFor(to.ID, "")
	submitted := s.typeLine(to.ID, agent, peerPayload(me, to, text), submit)
	// A feature that lets agents type into terminals has to be visible while it
	// does it: one log line and one plugin event per admitted send. The size in
	// all three places is len(cmd.Data) as the sender gave it — not the folded
	// text, and not the prefix the server added.
	log.Info().Str("from", me.ID).Str("to", to.ID).Str("group", me.Group).Int("bytes", len(cmd.Data)).Bool("submitted", submitted).Msg("peer send")
	s.emit("peer.send", map[string]any{"from": me.ID, "to": to.ID, "group": me.Group, "bytes": len(cmd.Data)})
	send(cc, proto.ServerMsg{Type: "notice", ID: to.ID, Notice: fmt.Sprintf("sent %d bytes to %s (%s)", len(cmd.Data), scrub.Capped(to.Title, maxPeerTitleRunes), scrub.Capped(to.ID, maxPeerTitleRunes))})
}

// peersFencedLocked reports whether cc is held to the peers fence: a conductor
// connection, or one whose declared panel is an agent. It is the identity rule
// setGroupPeers refuses on, for the same reason — the cockpit declares no self,
// and a shell inside baton is the operator's own hands. nil is the server
// itself or a plugin, which is not a connection at all. Caller holds s.mu.
func (s *Server) peersFencedLocked(cc *clientConn) bool {
	switch {
	case cc == nil:
		return false
	case cc.role == roleConductor:
		return true
	case cc.self == "":
		return false
	}
	i := s.indexLocked(cc.self)
	return i >= 0 && s.panels[i].IsAgent()
}

// peersWidenLocked refuses a re-filing that would put an EXISTING panel into a
// peers-on group it is not already in, when an agent or the conductor asks for
// it. dest names where the command would file a panel, and false for a panel
// it does not touch. Caller holds s.mu, and holds it across the move, so the
// answer cannot go stale between the check and the write.
//
// Without this the flag's own fence is decoration. group.peers is refused to
// an agent and to the conductor because opening a channel between terminals is
// the operator's decision (D9) — and then panel.group, open to both, lets
// either pull any panel on the fleet into a group that is already on, which is
// the same decision made one panel at a time. So who is IN a peers-on group is
// the operator's to widen, as the flag is the operator's to set.
//
// The whole command is refused, never the offending part of it: a regroup that
// half happened is harder to read than one that did not.
//
// What is deliberately NOT fenced:
//
//   - a panel already in that exact group, re-filed where it sits;
//   - any move into a group that is peers-off, and any move out of one;
//   - a NEW panel spawned straight into a peers-on group — a worktree spawn
//     filed under its branch, a plugin's spawn. Those arrive through the
//     server's own groupPanels and not through a connection's panel.group, and
//     a conductor building out a group the operator opened is the ordinary
//     flow: the operator chose the group, not a list of ids.
//
// It is the declared-identity guardrail setGroupPeers is, no more: a self is
// declared, not proven, and this stops an agent's accident, not an agent
// that lies about who it is.
func (s *Server) peersWidenLocked(cc *clientConn, dest func(panel.Panel) (string, bool)) error {
	if !s.peersFencedLocked(cc) {
		return nil
	}
	for _, p := range s.panels {
		if to, ok := dest(p); ok && to != p.Group && s.groupPeers[to] {
			return fmt.Errorf("group %q has peer interaction on; the operator adds panels to it", to)
		}
	}
	return nil
}

// subtreeDest is the dest of a subtree move — a group rename, or a dissolve
// into the parent — for peersWidenLocked: where each panel under oldPrefix
// lands.
//
// A destination that is itself under oldPrefix is not one. That happens when a
// group dissolves and its sub-groups move up a level: "a/b/b" lands on "a/b",
// whose own panels and whose own flag are leaving in the same step. Nobody is
// staying there to be reached, so it is a re-key and not a merge (see
// remapGroupPeersLocked, which draws the same line).
func subtreeDest(oldPrefix, newPrefix string) func(panel.Panel) (string, bool) {
	return func(p panel.Panel) (string, bool) {
		if !panel.GroupIsUnder(oldPrefix, p.Group) {
			return "", false
		}
		to := swapGroupPrefix(p.Group, oldPrefix, newPrefix)
		return to, !panel.GroupIsUnder(oldPrefix, to)
	}
}
