package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The run of keys typed so far towards a binding, and the clock that bounds it.
//
// A landing key opens a family and then waits. Waiting forever is what the old
// leader did, and it had a quiet failure mode: the next key you pressed — a
// minute later, meaning something else — was swallowed to finish a sequence you
// had stopped thinking about. So a run expires, and while it is open the status
// bar says so and names what the run can still take. Discovery and safety turn
// out to be the same feature.

const (
	// defaultKeyTimeout is how long a landing waits for the key after it.
	// Long enough to be a pause rather than a race, short enough that a hanging
	// run clears before it can eat an unrelated keystroke.
	defaultKeyTimeout = 1200 * time.Millisecond

	// The range a configured timeout is taken seriously in. Below the floor a
	// landing is unreachable by a human hand; above the ceiling the cockpit
	// looks wedged rather than waiting. 0 is handled before these apply and
	// means "never expire".
	minKeyTimeout = 200 * time.Millisecond
	maxKeyTimeout = 10 * time.Second

	// neverKeyTimeout is what a configured 0 becomes on the model. The config
	// spells "never expire" as 0, but a model built by a test is zero-valued and
	// means "unset", so the two need different values; seqTick already declines
	// to schedule anything non-positive.
	neverKeyTimeout = -1
)

// parseKeyTimeout reads settings.key-timeout. Unset or unparseable falls back to
// the default, as does anything outside the sane range; a literal 0 is kept, and
// means a run waits indefinitely.
func parseKeyTimeout(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultKeyTimeout
	}
	d, err := time.ParseDuration(s)
	switch {
	case err != nil:
		return defaultKeyTimeout
	case d == 0:
		return neverKeyTimeout // asked for explicitly: never expire
	case d < minKeyTimeout || d > maxKeyTimeout:
		return defaultKeyTimeout
	}
	return d
}

// seqTimeoutMsg fires when a pending run has waited long enough. gen identifies
// the run that asked for it, so a tick left over from a run that has already
// been completed or cancelled is dropped instead of clearing its successor.
type seqTimeoutMsg struct{ gen int }

// seqTick schedules the expiry of the current run. A zero timeout schedules
// nothing, which is how "never expire" is spelled.
func seqTick(gen int, d time.Duration) tea.Cmd {
	if d <= 0 {
		return nil
	}
	return tea.Tick(d, func(time.Time) tea.Msg { return seqTimeoutMsg{gen: gen} })
}

// armPending starts (or extends) a pending run: it records the tokens, notes the
// binding that would fire if the run lapsed here (the zero binding when none
// would), and returns the tick that will end it.
func (m model) armPending(tokens []string, hit binding) (model, tea.Cmd) {
	m.pending, m.pendingHit = tokens, hit
	m.pendingGen++
	return m, seqTick(m.pendingGen, m.effKeyTimeout())
}

// leaderArmed reports whether any view is holding the leader down. The five
// flags are one concept wearing five names — each view arms its own — so every
// question about "is the leader down" has to ask all of them.
func (m model) leaderArmed() bool {
	return m.prefix || m.zoomArmed || m.groupArmed || m.scratchArmed || m.scrollArmed
}

// disarmLeader drops the leader wherever it is held.
func (m model) disarmLeader() model {
	m.prefix, m.zoomArmed, m.groupArmed = false, false, false
	m.scratchArmed, m.scrollArmed = false, false
	return m
}

// leaderTick schedules the expiry of a leader that was just armed. Every view
// that arms one calls it, so a hanging leader lapses wherever it was pressed
// rather than only on the dashboard — which matters most in a zoom, where a
// leader stuck down routes the program's next keystroke to baton instead.
func (m model) leaderTick() (tea.Model, tea.Cmd) {
	m.pendingGen++
	return m, seqTick(m.pendingGen, m.effKeyTimeout())
}

// clearPending drops the run without acting on it. Callers that leave a view, or
// that hand the keyboard to a program, use it so a half-typed run never survives
// into a place where its keys mean something else.
func (m model) clearPending() model {
	m.pending, m.pendingHit = nil, binding{}
	m.pendingGen++ // any tick already in flight is now stale
	return m
}

// effKeyTimeout is the configured landing timeout. A zero-valued model — the
// shape a test builds directly — takes the default; a configured "never" is
// carried as neverKeyTimeout so the two cannot be confused.
func (m model) effKeyTimeout() time.Duration {
	if m.keyTimeout == 0 {
		return defaultKeyTimeout
	}
	return m.keyTimeout
}

// expirePending handles the tick. A run that ends on a binding fires it — that
// is the vim d-versus-dd case, where waiting was the only way to know the longer
// sequence was not coming. A run that ends on a landing simply clears.
func (m model) expirePending(gen int) (tea.Model, tea.Cmd) {
	if gen != m.pendingGen {
		return m, nil // a tick from a run that has already been resolved
	}
	if len(m.pending) == 0 && !m.leaderArmed() {
		return m, nil
	}
	hit := m.pendingHit
	m = m.disarmLeader() // a leader left hanging lapses too, rather than eating the next key
	m = m.clearPending()
	if hit.name != "" {
		return m.runAction(hit.act)
	}
	return m, nil
}

// advanceSeq feeds one key to the pending run and acts on what it amounts to.
// want selects the half of the key map in play: the commands in a command-mode
// view, the escapes after the leader.
//
// esc and the interrupt keys always cancel rather than being matched, so a run
// opened by accident is closed by the key everyone already reaches for.
func (m model) advanceSeq(key string, want func(binding) bool, run func(model, binding) (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd) {
	if key == "esc" || key == keyCtrlC {
		if len(m.pending) == 0 {
			return m, nil
		}
		return m.clearPending(), nil
	}

	tokens := append(append([]string(nil), m.pending...), tok(key))
	b, res := matchSeq(m.keymap(), tokens, want)
	switch res {
	case seqExact:
		m = m.clearPending()
		return run(m, b)
	case seqPartial:
		return m.armPending(tokens, binding{})
	case seqExactPartial:
		return m.armPending(tokens, b)
	}

	// Nothing starts with this run.
	if len(m.pending) > 0 {
		// A landing was open, so this is a dead end and worth saying: the run is
		// gone and the user needs to know why nothing happened.
		m = m.clearPending()
		m.status = strings.Join(labelTokens(tokens), " ") + " — no binding"
		return m, nil
	}
	if where, ok := m.movedFrom(key); ok {
		m.status = where
	}
	// Otherwise silent. A stray key on an idle dashboard is not a mistake worth
	// a status line.
	return m, nil
}

// movedKeys are the keys the landing pass took away, and the action each one
// used to run. A key that did something yesterday and does nothing at all today
// reads as a broken cockpit, so for one release it answers with its new home
// instead of with silence.
//
// It is keyed on the OLD key and resolved through the CURRENT key map, so the
// hint names where the action actually lives — including after a rebind — and a
// user who binds something onto a freed key never sees it, because the matcher
// resolves that key before this is reached.
var movedKeys = map[string]action{
	"c": actNewForm,
	".": actNewHere,
	"C": actConductor,
	"H": actGlobalShell,
	"G": actGroup,
	"a": actAdd,
	"u": actUngroup,
	"U": actUsageToggle,
	"K": actKeycastToggle,
	"V": actDashLayout,
	"z": actLens,
	"S": actRestart,
}

// movedFrom is the "it lives here now" line for a key the landing pass freed.
func (m model) movedFrom(key string) (string, bool) {
	act, ok := movedKeys[key]
	if !ok {
		return "", false
	}
	b, ok := m.bindingFor(act)
	if !ok {
		return "", false
	}
	where := seqLabel(b.key)
	if isEscape(b.act) {
		where = keyLabel(m.effPrefix()) + " " + where
	}
	return m.bindDesc(b) + " moved → " + where, true
}

// labelTokens renders a run for display, each token through keyLabel.
func labelTokens(tokens []string) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = keyLabel(t)
	}
	return out
}

// --- the status bar's account of the run --------------------------------------

// pendingCap is the badge naming the run so far: "g …", "C-t …", "C-t v …". It
// replaces the older PREFIX chip, which said only that the leader was down and
// said it on the dashboard alone.
func (m model) pendingCap() string {
	toks := m.pendingTokens()
	if len(toks) == 0 {
		return ""
	}
	return seg(strings.Join(labelTokens(toks), " ")+" …", colDark, colBrandHi)
}

// pendingTokens is the run as the footer sees it: the leader, when it is armed
// in any view, followed by whatever has been typed after it.
func (m model) pendingTokens() []string {
	var out []string
	if m.leaderArmed() {
		out = append(out, m.effPrefix())
	}
	return append(out, m.pending...)
}

// pendingHint is the which-key line: what the open run can still take, each key
// beside the action it completes. A key that opens yet another family shows an
// ellipsis instead of an action, because it has none to name.
//
// It is what makes a landing self-teaching — the family is found by pressing the
// landing rather than by reading the manual — so it is built from the same key
// map the matcher uses and cannot advertise a key that would not work.
func (m model) pendingHint(want func(binding) bool) string {
	next := m.pendingNext(want)
	if len(next) == 0 {
		return ""
	}
	return m.hintLabelled(next)
}

// pendingHintWithin is the which-key line in the widest form that fits in cols:
// the labelled line, else the keys alone, else as many keys as fit with a "+N"
// for the rest.
//
// The status bar used to drop the hint whole when it would not fit, and it never
// fit: the labels are the sentences the key map shows in two columns, so the four
// landing families needed 206, 336, 138 and 25 columns against the ~55 a
// 128-column terminal can spare. Only the one-member family ever appeared, which
// meant the self-teaching half of the landing keys did not ship at all.
//
// Keys alone are a real answer rather than a consolation. The run so far is
// already on the bar ("g …"), so "g · c · a · u" says how many there are and
// which ones they are — enough to press one and find out, which is the loop the
// feature is for. The labels come back the moment there is room for them.
func (m model) pendingHintWithin(want func(binding) bool, cols int) string {
	next := m.pendingNext(want)
	if len(next) == 0 || cols <= 0 {
		return ""
	}
	if full := m.hintLabelled(next); lipgloss.Width(full) <= cols {
		return full
	}
	// Keys only, dropping members from the tail until what is left fits. The count
	// of what was dropped rides along, so a truncated family still says it is one.
	for n := len(next); n > 0; n-- {
		if line := m.hintKeys(next, n); lipgloss.Width(line) <= cols {
			return line
		}
	}
	return ""
}

// pendingNext is the set of keys the open run can still take, or nothing when no
// run is open.
func (m model) pendingNext(want func(binding) bool) []contin {
	if len(m.pending) == 0 {
		return nil
	}
	return seqNext(m.keymap(), m.pending, want)
}

// hintLabelled is the full form: every key beside what it does.
func (m model) hintLabelled(next []contin) string {
	parts := make([]string, 0, len(next))
	for _, c := range next {
		label := "…"
		if c.b.name != "" {
			label = m.bindDesc(c.b)
		}
		parts = append(parts, m.barStrong().Render(keyLabel(c.key))+m.bar().Render(" "+label))
	}
	return strings.Join(parts, " · ")
}

// hintKeys is the narrow form: the first n keys, with a "+N" when that is not all
// of them.
func (m model) hintKeys(next []contin, n int) string {
	parts := make([]string, 0, n+1)
	for _, c := range next[:n] {
		parts = append(parts, m.barStrong().Render(keyLabel(c.key)))
	}
	line := strings.Join(parts, m.bar().Render(" · "))
	if rest := len(next) - n; rest > 0 {
		line += m.bar().Render(fmt.Sprintf(" +%d", rest))
	}
	return line
}

// --- which half of the key map is in play -------------------------------------

// cmdBinding and escBinding select the two halves of the key map: the commands,
// which are bare in a command-mode view and prefix-reached in a zoom, and the
// escapes, which are prefix-reached everywhere. Every matcher call names the
// half in play, so a run can never resolve to a binding the current mode would
// not have fired.
func cmdBinding(b binding) bool { return !isEscape(b.act) }
func escBinding(b binding) bool { return isEscape(b.act) }

// runCmdBinding runs a resolved command. The fold row's refusal lives here
// rather than in the matcher: it is about what the cursor is sitting on, not
// about what the keys spelled.
func runCmdBinding(m model, b binding) (tea.Model, tea.Cmd) {
	if m.refusedOnFoldRow(b.act) {
		m.status = "expand the quiet group first"
		return m, nil
	}
	return m.runAction(b.act)
}
