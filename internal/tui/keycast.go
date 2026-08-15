package tui

import (
	"strings"
	"time"
)

// keycastFor is how long a key stays on the footer after it is pressed. Long
// enough to read back on a screen recording, short enough that an idle cockpit
// is not still advertising the last thing you did.
const keycastFor = 3 * time.Second

// navLabels name the keys that move you around the cockpit but are not
// rebindable commands, so the readout says "right" instead of leaving a bare l
// for the viewer to decode.
var navLabels = map[string]string{
	"h": "left", "j": "down", "k": "up", "l": "right",
	"left": "left", "down": "down", "up": "up", "right": "right",
	"enter": "open", "esc": "back", "tab": "next", "shift+tab": "prev",
}

// actionLabel turns a binding's stable id into something a footer can show:
// "global-shell" → "global shell". The id is used rather than the description
// because it is already short, and it stays English on purpose — same reason
// key names do.
func actionLabel(b binding) string { return strings.ReplaceAll(b.name, "-", " ") }

// noteKey records a key press for the footer readout.
//
// It deliberately sees only baton's own actions. In a zoom, an interact tile,
// the scratch shell or a text field the keystrokes belong to the program you
// are driving, and echoing those to the footer would turn a demo aid into a
// keylogger — so they are dropped. The leader is the one exception: it is
// baton's key wherever it is pressed, and so is whatever completes it.
func (m model) noteKey(key string) model {
	if !m.keycast {
		return m
	}
	pfx := m.effPrefix()
	// Read the armed flags before the mode handlers consume them: if the leader
	// was pressed last, this key is the one that completes the chord.
	armed := m.prefix || m.zoomArmed || m.groupArmed || m.scratchArmed || m.scrollArmed
	ours := m.input == inputNone && !m.scratchOpen && m.mode != modeZoom && m.mode != modeGroupZoom

	if !ours && !armed && key != pfx {
		return m
	}

	switch {
	case armed:
		m.keycastKey = keyLabel(pfx) + " " + keyLabel(key)
		m.keycastAct = ""
		if b, ok := m.lookupEscape(key); ok {
			m.keycastAct = actionLabel(b)
		} else if b, ok := m.lookupCmd(key); ok {
			m.keycastAct = actionLabel(b)
		}
	case key == pfx:
		m.keycastKey, m.keycastAct = keyLabel(key), "…" // the leader, waiting for its second half
	default:
		m.keycastKey, m.keycastAct = keyLabel(key), ""
		if b, ok := m.lookupCmd(key); ok {
			m.keycastAct = actionLabel(b)
		} else if nav, ok := navLabels[key]; ok {
			m.keycastAct = nav
		}
	}
	m.keycastAt = m.now
	return m
}

// ageKeycast clears the readout once the key has been on screen long enough.
// Driven by the footer's own one-second tick, so it costs no extra timer.
func (m model) ageKeycast() model {
	if m.keycastKey != "" && m.now.Sub(m.keycastAt) > keycastFor {
		m.keycastKey, m.keycastAct = "", ""
	}
	return m
}

// keycastSeg renders the readout in the footer's hint style — the key in the
// strong colour, what it did in the plain one, matching "? keys" beside it.
func (m model) keycastSeg() string {
	if !m.keycast || m.keycastKey == "" {
		return ""
	}
	out := m.barStrong().Render(" " + m.keycastKey)
	if m.keycastAct != "" {
		out += m.bar().Render(" " + m.keycastAct)
	}
	return out + m.bar().Render(" ")
}
