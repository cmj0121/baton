package tui

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmj0121/baton/internal/panel"
)

// The attention notifications: everything the cockpit does to TELL a human a
// panel wants them, as opposed to the queue (inbox.go) they work through once
// they are already looking.
//
// The surfaces are deliberately independent, because each reaches a different
// moment. The status pop is what you catch if you happen to be watching the
// footer. The bell is what reaches you when you are not, but are still at this
// desk. The badge is what is still there when you come back. None of them
// suppresses another: dropping one because a louder one fired would silently
// disable the escalation exactly when it was needed.
//
// The fourth surface, OSC 9, reaches the human who is at no terminal of ours at
// all. It is the only one that is coalesced and the only one that is off by
// default; both of those are explained where it is implemented, below.
//
// The rising edge they all hang off is computed once, in refreshAttention — a
// panel is announced when it ENTERS the state, not every tick it sits in it.

// refreshAttention fires a footer notification on the rising edge of a panel
// entering the attention state — when the Monitor decides it needs you. It tracks
// the set of panels currently flagged (attnSeen) so the pop fires once per entry,
// not every tick a panel sits waiting; a panel that resolves and later needs you
// again notifies afresh. The persistent count lives in the footer badge; this is
// the one-shot nudge that names the panel the moment it calls for you. An error
// status is left in place — it is not noise to bury under a notification.
//
// It computes TWO edge sets in the one pass, because the two audiences are not
// the same. attnSeen drives the pop and the bell and stays exactly as narrow as
// it has always been: a panel in `attention`, which is a panel asking a
// question. notifySeen drives the desktop toast and is the wider wantsHuman set,
// because someone who is away from the terminal wants to hear that an agent
// wedged or died too, and will not be back to read the badge that says so.
//
// A move WITHIN the wider set raises no second toast — a stuck panel that starts
// asking a question is already in notifySeen — while the bell and the pop do fire,
// because for them it is a genuine entry into `attention`. That asymmetry is the
// point rather than an oversight: the toast's job is to fetch somebody who is not
// here, and once it has done that, telling them again that the panel they were
// already called about has changed its mind is noise arriving on the one channel
// that must not become noise.
//
// The second set is built only when notifications are on, which is not the
// default. The comment below about not allocating per event applies to it too.
func (m *model) refreshAttention() {
	cur := make(map[string]bool)
	var fresh []string
	var need map[string]bool
	var freshNeed []notifyEdge
	if m.notifyEnabled {
		need = make(map[string]bool, len(m.notifySeen))
	}
	// Inline singleton skip (not m.visibleFleet()): this fires on every snapshot and
	// telemetry tick, so it avoids allocating a filtered slice per event.
	for _, p := range m.fleet {
		if p.Conductor || p.GlobalShell {
			continue
		}
		if m.notifyEnabled && wantsHuman(p) {
			need[p.ID] = true
			if !m.notifySeen[p.ID] {
				freshNeed = append(freshNeed, notifyEdge{id: p.ID, title: p.Title})
			}
		}
		if p.State != panel.Attention {
			continue
		}
		cur[p.ID] = true
		if !m.attnSeen[p.ID] {
			fresh = append(fresh, p.Title)
		}
	}
	m.attnSeen = cur
	if m.notifyEnabled {
		m.notifySeen = need
		m.queueNotify(freshNeed)
	}
	if len(fresh) == 0 {
		return
	}
	if m.bellEnabled {
		m.bellPending = true // audible nudge on the rising edge, even when an error status hides the text
	}
	if strings.HasPrefix(m.status, "error") {
		return
	}
	if len(fresh) == 1 {
		m.status = "◆ " + fresh[0] + " needs you"
	} else {
		m.status = fmt.Sprintf("◆ %d panels need your attention", len(fresh))
	}
}

// bell rings the terminal once by writing the BEL control byte to the tty. It is
// emitted as a command so it rides bubbletea's own output cycle; BEL prints no
// glyph and moves no cursor, so it never disturbs the alt-screen the cockpit
// draws. Sent to stderr to stay off the renderer's stdout stream.
func bell() tea.Msg {
	_, _ = os.Stderr.WriteString("\a")
	return nil
}

// takeBell returns the bell command once when a panel has just entered attention,
// clearing the pending flag so the nudge sounds a single time per rising edge.
func (m *model) takeBell() tea.Cmd {
	if !m.bellPending {
		return nil
	}
	m.bellPending = false
	return bell
}

// attentionBadge is the footer notification that some panel needs you: a red cap
// carried by every view's status bar, so a panel asking for input is visible
// whether you are on the dashboard, in a zoom, or in a group split. It names the
// panel when exactly one waits, and counts them when several do. Empty when the
// fleet is calm.
func (m model) attentionBadge() string {
	var names []string
	// Range m.fleet with an inline singleton skip rather than m.visibleFleet(): this
	// runs in every view's footer on every frame, so it must not allocate a slice.
	for _, p := range m.fleet {
		if !p.Conductor && !p.GlobalShell && p.State == panel.Attention {
			names = append(names, p.Title)
		}
	}
	if len(names) == 0 {
		return ""
	}
	label := fmt.Sprintf("◆ %d need you", len(names))
	if len(names) == 1 {
		label = "◆ " + truncate(names[0], 16) + " needs you"
	}
	return seg(label, colDark, states[panel.Attention].color)
}

// --- OSC 9 desktop notifications ----------------------------------------------

// The escalation path used to stop at the bell. A bell reaches you at this desk
// and nowhere else, and since --remote landed the human is routinely at another
// machine entirely, where the daemon's terminal is nobody's. OSC 9 is the answer
// for the same reason OSC 52 was the answer for the clipboard: it is bytes to the
// terminal, so it needs no helper binary, no notify-send, no per-platform
// launcher — and it crosses the ssh hop for free, because the terminal that
// renders it is the one sitting in front of the person.
//
// Two properties are load-bearing and neither is optional:
//
//   - It is COALESCED. One notification per window, never one per panel. A fleet
//     moves in waves — a broadcast lands, twelve agents finish a tool call, six
//     ask a question in the same second — and twelve toasts is not twelve times
//     the information, it is the last straw before someone turns the channel off.
//     The first edge therefore does NOT fire: it opens the window, and one
//     notification goes out when the window closes. A delay on a queue you are by
//     definition not watching costs nothing.
//
//   - Its text is SANITISED. A panel title is whatever the agent renamed itself
//     to, and here it is pasted into the payload of an escape sequence. See
//     sanitizeNotify.

// maxNotifyRunes caps the title inside a notification. A desktop toast will elide
// it long before this anyway; the cap is here so that whatever an agent titles
// itself, the number of bytes the cockpit writes to the real terminal in one go
// stays something a human chose the shape of.
const maxNotifyRunes = 96

// wantsHuman reports whether a panel is in a state worth interrupting someone who
// is somewhere else for: it is asking a question, it has gone quiet far past what
// its work should take, or it died badly.
//
// `done` is deliberately absent, and stays absent even when the inbox is
// configured to show it. A finished turn is a review queue, not an interruption;
// waking someone at 2am to say an agent succeeded is precisely how a notification
// channel gets turned off, and it takes the wedges and the failures down with it.
// A clean exit is not news either — it is the thing that was supposed to happen.
func wantsHuman(p panel.Panel) bool {
	switch p.State {
	case panel.Attention, panel.Stuck:
		return true
	case panel.Exited:
		return p.ExitCode != 0
	default:
		return false
	}
}

// notifyEdge is one panel that has just started needing a human: the id the
// coalescing window dedupes on, and the title the sentence names it by.
type notifyEdge struct {
	id    string
	title string
}

// queueNotify folds this refresh's fresh edges into the coalescing window, opening
// one if none is open. Titles are sanitised on the way IN rather than on the way
// out, so no unscrubbed agent text is ever held in cockpit state.
//
// The dedupe is on panel ID, never on the title. Deduping on the title would be a
// counting bug — two panels sharing a name under allow-name-conflict, two that
// both scrubbed down to the placeholder, or two that differ only past the 96th
// rune would collapse into one, and "2 agents need you" would quietly become
// "<title> needs you". Worse, the last two are AGENT-CONTROLLED: an agent could
// swallow a peer's escalation by naming itself in control bytes or by sharing a
// long enough prefix, which would reopen at this layer exactly the hole
// sanitizeNotify's placeholder closes below it. The id also does the original job
// strictly better: one panel flapping in and out inside a window is one entry
// however it renames itself while flapping.
func (m *model) queueNotify(fresh []notifyEdge) {
	if !m.notifyEnabled {
		return
	}
	for _, e := range fresh {
		if m.notifyIDs[e.id] {
			continue
		}
		if m.notifyIDs == nil {
			m.notifyIDs = make(map[string]bool)
		}
		m.notifyIDs[e.id] = true
		m.notifyPending = append(m.notifyPending, sanitizeNotify(e.title))
		if m.notifyAt.IsZero() {
			m.notifyAt = m.now // the first edge opens the window; it does not fire
		}
	}
}

// takeNotify closes the coalescing window once it has been open for
// notify-coalesce, returning the single notification that stands for everything
// which raised a hand inside it — and nil the rest of the time.
//
// It is driven from the cockpit's one-second tick, because that tick is the only
// thing that moves m.now and so the only thing that can make an open window
// expire. Hanging it off the event handlers instead would be strictly worse. Most
// of the time it would look identical — a live panel keeps producing telemetry as
// its activity line's age rolls over, so frames do keep arriving over a quiet
// fleet — but a fleet that has entirely EXITED moves nothing and broadcasts
// nothing, and a fleet of non-zero exits is one of the three things this
// notification exists to report. The tick does not depend on the fleet being
// alive to notice that it is not.
func (m *model) takeNotify() tea.Cmd {
	if m.notifyAt.IsZero() || m.now.Sub(m.notifyAt) < m.notifyCoalesce {
		return nil
	}
	pending := m.notifyPending
	m.clearNotify()
	return notify(notifyText(pending))
}

// clearNotify closes the coalescing window and drops everything it was holding.
// Two callers: takeNotify, once the window has been spent, and applyPrefs, when a
// config reload turns notifications off — an open window outlives the setting that
// opened it otherwise, and "off" would mean one last toast up to a whole
// notify-coalesce later instead of not one byte.
func (m *model) clearNotify() {
	m.notifyPending, m.notifyIDs, m.notifyAt = nil, nil, time.Time{}
}

// notifyText is what the window says. One panel is named, because with one there
// is a useful thing to say; several are counted, because there is not.
func notifyText(titles []string) string {
	if len(titles) == 1 {
		return "baton · " + titles[0] + " needs you"
	}
	return fmt.Sprintf("baton · %d agents need you", len(titles))
}

// notify writes one OSC 9 notification to the terminal. Like the bell and the OSC
// 52 clipboard write it goes to stderr — the same tty, but off the stream
// bubbletea renders the alt-screen on, so it can never land mid-frame.
func notify(text string) tea.Cmd {
	seq := "\x1b]9;" + text + "\a"
	return func() tea.Msg {
		_, _ = os.Stderr.WriteString(seq)
		return nil
	}
}

// sanitizeNotify scrubs a panel title before it becomes the payload of an escape
// sequence. This is the one step in the notification path that is a security
// control rather than a nicety.
//
// A title is agent-controlled text: an agent renames its own panel, and the
// cockpit takes that string and writes it to the operator's real terminal wrapped
// in ESC ] 9 ; … BEL. A title carrying a BEL closes the sequence early, and
// everything after it is executed by the terminal as its own bytes — an OSC 52
// that rewrites the operator's clipboard, a cursor escape that scribbles over the
// frame. So every control rune goes, not only the two that terminate this
// particular sequence: C0 and C1 alike (which covers ESC, BEL, and the single-byte
// CSI/OSC introducers some terminals still honour), format runes, and the
// replacement rune invalid UTF-8 decodes to.
//
// It takes sanitizeReason's shape rather than sanitizeText's on purpose.
// sanitizeText protects a RENDERED line, so it may keep a tab and may drop an
// ESC-introduced sequence whole; the payload of an escape can afford neither
// guess. Whitespace runs fold to one space and the result is capped, for the same
// reason the server caps a declared reason: this is a sentence, and its length is
// not the agent's to choose.
//
// A title that scrubs away to nothing becomes a placeholder rather than being
// dropped. Losing the alert entirely would hand an agent a way to silence its own
// escalation just by naming itself in control bytes.
func sanitizeNotify(title string) string {
	var b strings.Builder
	b.Grow(len(title))
	pendingSpace := false
	for _, r := range title {
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
	if len(out) > maxNotifyRunes { // bytes >= runes, so this skips the common case
		if rs := []rune(out); len(rs) > maxNotifyRunes {
			out = strings.TrimRight(string(rs[:maxNotifyRunes]), " ")
		}
	}
	if out == "" {
		return "a panel"
	}
	return out
}

// defaultNotifyCoalesce is the window settings.notify-coalesce leaves unset.
// Thirty seconds is long enough that a wave of agents finishing a step together
// arrives as one sentence, and short enough that a question asked while you are
// making coffee reaches you before the coffee does.
const defaultNotifyCoalesce = 30 * time.Second

// parseCoalesce reads settings.notify-coalesce. An unparseable or negative value
// falls back to the default rather than to zero: a typo in a duration must not
// quietly turn the coalescer into the per-panel toast storm it exists to prevent.
// Zero itself is honoured — it means "send on the next tick".
func parseCoalesce(s string) time.Duration {
	if s == "" {
		return defaultNotifyCoalesce
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return defaultNotifyCoalesce
	}
	return d
}
