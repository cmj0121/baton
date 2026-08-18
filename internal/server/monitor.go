package server

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"time"

	"github.com/cmj0121/baton/internal/panel"
)

// The Monitor's timing and shape. monitorInterval is how often the Monitor
// re-evaluates every panel and rolls its sparkline forward one bucket; idleAfter
// is how long output must stay quiet before a running panel settles to idle (or
// attention); sparkWidth is how many buckets the sparkline shows; attnTailBytes is
// how much trailing output the attention sniff inspects.
const (
	monitorInterval = time.Second
	idleAfter       = 10 * time.Second
	sparkWidth      = 8
	attnTailBytes   = 1024
)

// sparkRunes are the eight bar heights a sparkline bucket can render as, lowest to
// highest.
var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// ansiSeq matches a CSI escape sequence, stripped before the attention sniff so a
// coloured prompt is read by its text, not its escape codes.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// panelMon is the Monitor's per-panel bookkeeping: when output last arrived, when
// the panel entered its current state (for the activity duration), the bytes seen
// since the last tick, and the rolling window of per-bucket byte counts behind the
// sparkline. It lives beside the panel rather than on it because none of it
// belongs on the wire.
type panelMon struct {
	lastOutput time.Time
	stateSince time.Time
	bucket     int
	spark      [sparkWidth]int

	// The similarity signature (see the bottom of this file) and the two inputs
	// it was computed from. Caching it here is what keeps the fold affordable: it
	// is recomputed only when the panel has spoken since the last one or has
	// changed state, so a quiet fleet reads no tails at all.
	sig      string
	sigState panel.State
	sigDirty bool
}

// declaration is what an agent has explicitly said about ITSELF, and it sits at
// the top of the detection precedence for that reason: a timer is a guess about
// silence and the tail heuristic is a guess about text, while this is the only
// signal that came from the thing being described.
//
// Reason is why. There is deliberately no "when": a queue sorts on the state
// clock the wire already carries (proto.Panel.Since), so that a declared
// attention and a heuristic one order by the same rule, and a second instant
// kept here would be one nothing reads.
//
// Cleared is the inverse, with teeth: when a resolve withdraws the reason the
// entry stays behind carrying the instant it was withdrawn, and while that
// instant is at or after the panel's last byte of output the tail heuristic is
// suppressed for it (see Server.suppressedLocked). Without that, the unchanged
// tail that raised the flag would simply raise it again on the next tick and a
// resolve would be a verb that undoes itself.
//
// It is deliberately not persisted. A declaration is a live process's statement
// about itself, and restore brings every panel back as an exited slot.
type declaration struct {
	Reason  string
	Cleared time.Time
}

// monitor is the MONITOR core block: it watches each panel's output stream and
// decides, on a fixed tick, how its lifecycle state should move. The server owns
// panel state; the monitor owns only this bookkeeping and the decisions, so it
// stays small and unit-testable. Its maps are guarded by the owning Server.mu —
// every method runs with that lock held.
type monitor struct {
	now    func() time.Time // injectable clock, so tests need not sleep
	panels map[string]*panelMon
}

// newMonitor returns a monitor on the real clock.
func newMonitor() *monitor {
	return &monitor{now: time.Now, panels: make(map[string]*panelMon)}
}

// spawned begins tracking a freshly created panel, clock running from now.
func (mo *monitor) spawned(id string) {
	t := mo.now()
	mo.panels[id] = &panelMon{lastOutput: t, stateSince: t}
}

// forget drops a panel's bookkeeping when it exits or is closed.
func (mo *monitor) forget(id string) { delete(mo.panels, id) }

// observed records n bytes of output: it resets the quiet timer, adds to the
// current sparkline bucket, and marks the similarity signature stale.
func (mo *monitor) observed(id string, n int) {
	if pm := mo.panels[id]; pm != nil {
		pm.lastOutput = mo.now()
		pm.bucket += n
		pm.sigDirty = true // it has something new to look like; the next tick re-hashes it
	}
}

// entered restarts the activity duration when a panel changes state.
func (mo *monitor) entered(id string) {
	if pm := mo.panels[id]; pm != nil {
		pm.stateSince = mo.now()
	}
}

// quiet reports whether a panel has produced no output for at least idleAfter,
// the first rung of the ladder.
func (mo *monitor) quiet(id string) bool { return mo.quietFor(id, idleAfter) }

// quietFor reports whether a panel has produced no output for at least d — an
// untracked panel reads as quiet so a stray id never animates.
//
// This is THE clock every threshold in the lifecycle reads, and it measures from
// the last byte of output, never from how long a state has held. That is what
// lets one monotonically growing number carry the whole ladder — idle at 10s,
// done at done-after, stuck at stuck-after — and, in particular, what lets a
// panel that already settled to done keep climbing to stuck. Entering a state
// moves stateSince (see entered); it does not touch lastOutput, so nothing but
// the panel actually speaking resets the ladder.
func (mo *monitor) quietFor(id string, d time.Duration) bool {
	pm := mo.panels[id]
	return pm == nil || mo.now().Sub(pm.lastOutput) >= d
}

// lastByte is when a panel last produced output, or the zero time when it is not
// tracked. It is the raw reading behind quietFor, exposed because the heuristic
// suppression a resolve installs is not a duration but a comparison against this
// instant: it holds until the panel says something NEW.
func (mo *monitor) lastByte(id string) time.Time {
	pm := mo.panels[id]
	if pm == nil {
		return time.Time{}
	}
	return pm.lastOutput
}

// enteredAt is when a panel entered its current state, or the zero time when it
// is not tracked. The wire carries it as an instant (proto.Panel.Since) rather
// than as the rendered activity line, because a queue has to sort on it.
func (mo *monitor) enteredAt(id string) time.Time {
	pm := mo.panels[id]
	if pm == nil {
		return time.Time{}
	}
	return pm.stateSince
}

// since reports how long a panel has held its current state, for the activity line.
func (mo *monitor) since(id string) time.Duration {
	pm := mo.panels[id]
	if pm == nil {
		return 0
	}
	return mo.now().Sub(pm.stateSince)
}

// roll advances the sparkline window by one bucket — pushing the bytes seen this
// tick onto the right and dropping the oldest — and returns the rendered bars.
func (mo *monitor) roll(id string) string {
	pm := mo.panels[id]
	if pm == nil {
		return ""
	}
	copy(pm.spark[:], pm.spark[1:])
	pm.spark[sparkWidth-1] = pm.bucket
	pm.bucket = 0
	return renderSpark(pm.spark[:])
}

// stateSignals is everything the Monitor knows about one panel on one tick, in
// the form the transition consumes. It is assembled by Server.signalsLocked (the
// only place that touches server state) and read by nextState (the only place
// that decides), so the detection precedence lives in exactly one switch.
//
// It is a struct rather than the three booleans this used to take because the
// ladder now has seven rungs, and seven positional booleans at a call site is a
// bug waiting for its first typo.
type stateSignals struct {
	cur      panel.State // the state the panel is in now
	agent    bool        // Kind == panel.Agent; the ladder above idle is agent-only
	declared bool        // an agent's own panel.attention declaration stands
	quiet    bool        // no output for at least idleAfter
	doneDue  bool        // agent, done-on-quiet is on, and quiet for at least done-after
	stuckDue bool        // agent, stuck-after is armed, and quiet for at least stuck-after
	taskDone bool        // the panel's in-flight task just went terminal-done (an event, not a timer)
	looksAtt bool        // the quiet tail reads like a question, and nothing suppresses the heuristic
}

// nextState is the Monitor's pure transition: it returns the state the panel
// should be in and whether that is a move. Waking back to running on resumed
// output is the server's job (it sees the bytes arrive); this covers the settle
// and everything the ladder climbs to afterwards.
//
// The order of the switch IS the detection precedence, highest first, and two of
// the orderings are load-bearing rather than incidental:
//
//   - The stuck TIMER outranks the tail HEURISTIC. An agent silent for ten
//     minutes whose tail happens to end in "?" is better described as stuck than
//     as attention: the timer is certain and the tail is a guess.
//   - The tail heuristic outranks the done timer. A tail reading "Apply this
//     refactor? [y/N]" at twenty seconds is a question NOW, and making it wait
//     out done-after to be called done would bury an answerable item under a
//     reviewable one.
//   - The task EVENT does not fire on a panel already in attention, for the same
//     reason. A panel waiting on a human can still have its task settle — a
//     dispatch is delivered to an attention panel, so the task moves on the very
//     tick the heuristic raised the flag — and letting the event win would demote
//     "answer me" to "review me" one tick after it was raised. Only output (which
//     withdraws an undeclared attention) or an explicit resolve leaves that state.
//     This exclusion is a project-lead ruling on top of DESIGN §2.1, which lists
//     rung 2b as firing from any live state.
//
// Exited is terminal. Idle, done, stuck and attention all hold until output
// resumes or a higher rung fires — resting states never move on their own except
// upwards, along the quiet clock.
func nextState(sig stateSignals) (panel.State, bool) {
	ns := sig.cur
	switch {
	case sig.cur == panel.Exited: // rung 0: the process is gone; nothing else applies
		ns = panel.Exited
	case sig.declared: // rung 1: the agent said so itself, and outranks every guess
		ns = panel.Attention
	case sig.stuckDue && stuckable(sig.cur): // rung 2a: the silence has outlasted the agent's budget
		ns = panel.Stuck
	case sig.taskDone && sig.agent && sig.cur != panel.Attention: // rung 2b: the server SAW the work finish, rather than inferring it
		ns = panel.Done
	case sig.looksAtt: // rung 3: the tail reads like a question
		ns = panel.Attention
	case sig.doneDue && sig.cur == panel.Idle: // rung 2c: quiet long enough that the turn reads as over
		ns = panel.Done
	case sig.quiet && settling(sig.cur): // rung 4: output stopped, and nothing above claimed it
		ns = panel.Idle
	}
	return ns, ns != sig.cur
}

// stuckable are the states the stuck timer may escalate from.
//
// attention is excluded on purpose: a panel explicitly waiting on a human is not
// stuck, the human is, and relabelling it would replace a correct state with a
// worse one. exited and stuck itself have nowhere left to go. The remaining
// exclusion — shells — is not a state at all but a kind, and lives in the agent
// flag on stuckDue: a shell nobody has touched since Tuesday is idle on purpose,
// and escalating forty-five of them every ten minutes is precisely the noise the
// queue exists to prevent.
func stuckable(st panel.State) bool {
	switch st {
	case panel.Running, panel.Spawning, panel.Idle, panel.Done:
		return true
	}
	return false
}

// settling are the states a quiet panel drops out of — the ones where output was
// expected. Everything else is already at rest.
func settling(st panel.State) bool { return st == panel.Running || st == panel.Spawning }

// wantsTail reports whether rung 3 can still decide this panel's state, which is
// the only condition under which the tail is worth reading. The heuristic is the
// one expensive signal in the set — it copies and scans a kilobyte of output —
// so it is computed only when every cheaper rung above it has left the decision
// open. On a fleet of fifty quiet panels that is the difference between fifty
// tail reads a second and none.
func (sig stateSignals) wantsTail() bool {
	if !settling(sig.cur) {
		return false // rung 3's own precondition, and it excludes exited with it
	}
	return sig.quiet && !sig.declared && !sig.stuckDue && (!sig.taskDone || !sig.agent)
}

// signalsLocked reads everything nextState needs about one panel: the quiet
// clock at up to three thresholds, the standing declaration, the task event,
// and — only when the cheaper signals leave the decision open — the tail
// heuristic. It is the only place that touches server state on behalf of the
// transition, which is what keeps nextState pure and the precedence in one
// switch. Caller holds s.mu.
//
// taskDone is passed in rather than derived here because it is an EDGE: the tick
// that saw the panel's task go terminal-done is the only one entitled to act on
// it. Derived from the task table instead, it would stay true, and a panel that
// woke back to running would be dragged to done by work that finished minutes
// ago.
func (s *Server) signalsLocked(p panel.Panel, taskDone bool) stateSignals {
	d := s.declared[p.ID]
	sig := stateSignals{
		cur:      p.State,
		agent:    p.Kind == panel.Agent,
		declared: d != nil && d.Reason != "",
		quiet:    s.mon.quiet(p.ID),
		taskDone: taskDone,
	}
	// The two upper rungs are agent-only, and their thresholds are resolved from
	// the panel's profile every tick so a SIGHUP takes hold without a respawn.
	if sig.agent {
		pol := s.effectiveAttentionLocked(s.specs[p.ID].Profile)
		if w := pol.Done(); w > 0 && pol.DoneQuiet() {
			sig.doneDue = s.mon.quietFor(p.ID, w)
		}
		if w := pol.Stuck(); w > 0 {
			sig.stuckDue = s.mon.quietFor(p.ID, w)
		}
	}
	if sig.wantsTail() && !s.suppressedLocked(p.ID) {
		sig.looksAtt = looksLikeAttention(s.pty.Tail(p.ID, attnTailBytes))
	}
	return sig
}

// renderSpark turns a window of per-bucket byte counts into a bar sparkline,
// scaled to the busiest bucket so the shape shows relative output rate. An
// all-quiet window renders as flat baseline bars.
func renderSpark(buckets []int) string {
	max := 0
	for _, b := range buckets {
		if b > max {
			max = b
		}
	}
	var sb strings.Builder
	for _, b := range buckets {
		idx := 0
		if max > 0 {
			idx = b * (len(sparkRunes) - 1) / max
		}
		sb.WriteRune(sparkRunes[idx])
	}
	return sb.String()
}

// looksLikeAttention reports whether a quiet panel's trailing output reads like it
// is waiting on you — the last line is a question or a yes/no-style confirmation.
// It is deliberately conservative: the safe default is idle, since over-flagging
// attention cries wolf. Process completion is handled separately, as the exited
// state.
func looksLikeAttention(tail []byte) bool {
	text := strings.TrimRight(ansiSeq.ReplaceAllString(string(tail), ""), " \t\r\n")
	if text == "" {
		return false
	}
	line := text
	if nl := strings.LastIndexByte(text, '\n'); nl >= 0 {
		line = text[nl+1:]
	}
	line = strings.TrimSpace(line)
	lower := strings.ToLower(line)
	switch {
	case strings.HasSuffix(line, "?"):
		return true
	case strings.Contains(lower, "(y/n)"), strings.Contains(lower, "[y/n]"),
		strings.Contains(lower, "[yes/no]"), strings.Contains(lower, "yes/no"):
		return true
	case strings.Contains(lower, "do you want"), strings.Contains(lower, "would you like"):
		return true
	case strings.Contains(lower, "press") && strings.Contains(lower, "continue"):
		return true
	}
	return false
}

// activityText is the live status line for a state and how long it has held —
// "running · 12s", "needs you · 1m". Exited keeps its own terminal note.
func activityText(state panel.State, since time.Duration) string {
	switch state {
	case panel.Spawning:
		return "spawning · " + compactDur(since)
	case panel.Running:
		return "running · " + compactDur(since)
	case panel.Idle:
		return "idle · " + compactDur(since)
	case panel.Attention:
		return "needs you · " + compactDur(since)
	case panel.Done:
		return "done · " + compactDur(since)
	case panel.Stuck:
		return "stuck · " + compactDur(since)
	default:
		return "exited"
	}
}

// compactDur renders a short, single-unit age: seconds under a minute, then
// minutes, then hours.
func compactDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// --- the similarity signature -------------------------------------------------
//
// At fifty panels the interesting members are the ones that do NOT look like
// everyone else, so a group's summary tile folds the lookalikes and spends its
// live tiles on the outliers. Deciding who looks alike needs each panel's last
// line, and a cockpit holds no output for a panel it never attached to — so the
// daemon answers the only question the fold actually asks (same, or different?)
// with eight hex characters per panel per snapshot, rather than shipping fifty
// tails through a frame that exists to stay small.

// sigTailBytes is how much trailing output the signature reads. A quarter of
// attnTailBytes, because the signature keeps only the LAST non-blank line: a
// kilobyte would be 900 bytes copied in order to be thrown away, fifty times a
// second.
const sigTailBytes = 256

// oscSeq matches an OSC sequence — ESC ] … BEL, or ESC ] … ST — stripped before
// the signature reads a line. This is not cosmetic: an interactive shell writes an
// OSC 0/2 title before virtually every prompt, and those titles routinely carry a
// per-panel value (the cwd, the last command). Left in, every member's shape would
// differ, no majority would ever form, and the whole group would fall silently
// back to the positional fold — a failure that looks exactly like the feature
// being off. It is deliberately separate from ansiSeq, which the attention sniff
// also reads and which this must not change the meaning of.
var oscSeq = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

// digitRun matches a run of decimal digits. Collapsing every run to a single "#"
// is what makes "48 identical" true after a broadcast — "[3/12] building" and
// "[7/12] building" are the same thing happening, and two shells whose prompts
// differ only in a clock are not two different things.
var digitRun = regexp.MustCompile(`[0-9]+`)

// panelSig is what a panel looks like right now, as eight hex characters: its
// lifecycle state joined to the shape of its last output line. The state is in
// the hash because two panels showing the same prompt are still different events
// when one of them is stuck and the other is running.
//
// FNV-1a rather than a cryptographic hash: nothing here is a secret or an
// identity, the only operation ever performed on the value is equality against
// another panel's, and a collision costs one tile folded that need not have been.
func panelSig(st panel.State, tail []byte) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(st.String()))
	_, _ = h.Write([]byte{0}) // separator, so a state name can never bleed into the line
	_, _ = h.Write([]byte(lineShape(tail)))
	return fmt.Sprintf("%08x", h.Sum32())
}

// lineShape reduces trailing output to the SHAPE of its last non-blank line: CSI
// and OSC stripped, trailing whitespace trimmed, digit runs collapsed. Empty when
// the panel has printed nothing but blanks.
//
// Carriage returns split lines alongside newlines because a progress bar redraws
// in place: the tail of a downloading shell is one physical line holding every
// frame it ever drew, and only the last frame describes it now.
func lineShape(tail []byte) string {
	// OSC first: its payload is free text that can itself contain a "[", which a
	// CSI pass run first would happily chew a hole through.
	s := ansiSeq.ReplaceAllString(oscSeq.ReplaceAllString(string(tail), ""), "")
	for {
		i := strings.LastIndexAny(s, "\r\n")
		if seg := strings.TrimRight(s[i+1:], " \t"); seg != "" {
			return digitRun.ReplaceAllString(seg, "#")
		}
		if i < 0 {
			return ""
		}
		s = s[:i]
	}
}

// sig is a panel's last computed signature, or empty for one the Monitor does not
// track — an exited panel, whose bookkeeping is forgotten the moment it dies.
func (mo *monitor) sig(id string) string {
	pm := mo.panels[id]
	if pm == nil {
		return ""
	}
	return pm.sig
}

// refreshSigLocked recomputes one panel's signature, but only when it can have
// changed: the panel produced bytes since the last one (sigDirty, set by
// observed), or its state moved, or it has never had one. It reports whether the
// value moved, so the tick can broadcast a panel whose ONLY change this second was
// what it looks like — rare (new bytes usually move the sparkline too) but real,
// and a fold working off a signature the cockpit was never sent is a bug nobody
// would find by looking.
//
// Caller holds s.mu.
//
// The recompute guard is the whole cost story, and it is why this scales. The two
// tail reads on this path are complementary rather than additive, which is the
// part worth knowing: the attention sniff reads a kilobyte only when a panel is
// QUIET (see wantsTail), and this reads 256 bytes only when it has just SPOKEN.
// So a resting fleet of fifty pays the sniff and nothing here; a fully busy fifty
// pays fifty 256-byte reads and fifty FNV passes a second — about 13 KiB of
// memcpy, comfortably sub-millisecond — and pays no sniff at all. Neither fleet
// pays both.
func (s *Server) refreshSigLocked(id string, st panel.State) bool {
	pm := s.mon.panels[id]
	if pm == nil {
		return false
	}
	if pm.sig != "" && !pm.sigDirty && pm.sigState == st {
		return false
	}
	pm.sigDirty, pm.sigState = false, st
	was := pm.sig
	pm.sig = panelSig(st, s.pty.Tail(id, sigTailBytes))
	return pm.sig != was
}
