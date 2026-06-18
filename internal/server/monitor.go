package server

import (
	"fmt"
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

// observed records n bytes of output: it resets the quiet timer and adds to the
// current sparkline bucket.
func (mo *monitor) observed(id string, n int) {
	if pm := mo.panels[id]; pm != nil {
		pm.lastOutput = mo.now()
		pm.bucket += n
	}
}

// entered restarts the activity duration when a panel changes state.
func (mo *monitor) entered(id string) {
	if pm := mo.panels[id]; pm != nil {
		pm.stateSince = mo.now()
	}
}

// quiet reports whether a panel has produced no output for at least idleAfter — an
// untracked panel reads as quiet so a stray id never animates.
func (mo *monitor) quiet(id string) bool {
	pm := mo.panels[id]
	return pm == nil || mo.now().Sub(pm.lastOutput) >= idleAfter
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

// nextState is the Monitor's pure transition: given the current lifecycle state,
// whether output has gone quiet, and whether a quiet tail looks like the panel
// needs you, it returns the next state and whether it changed. Waking back to
// running on resumed output is the server's job (it sees the bytes arrive); this
// covers the settle: a running or just-spawned panel that falls quiet drops to
// attention when its tail reads like a prompt, otherwise to idle. Exited is
// terminal; idle and attention hold until output resumes.
func nextState(cur panel.State, quiet, attention bool) (panel.State, bool) {
	switch cur {
	case panel.Running, panel.Spawning:
		if quiet {
			if attention {
				return panel.Attention, true
			}
			return panel.Idle, true
		}
	}
	return cur, false
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
