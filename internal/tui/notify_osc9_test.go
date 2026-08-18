package tui

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/panel"
)

// notifyEpoch is the fixed instant every coalescing test starts from. The window
// is driven entirely by m.now, so these tests move the clock by assignment and
// never sleep — a coalescer tested by waiting for it would take half a minute and
// still be flaky.
var notifyEpoch = time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)

// notifyModel is a cockpit with notifications on, the clock parked at the epoch,
// and a fleet of quiet agents ready to be moved into whatever state the test is
// about.
func notifyModel(titles ...string) model {
	m := model{notifyEnabled: true, notifyCoalesce: defaultNotifyCoalesce, now: notifyEpoch}
	for i, title := range titles {
		m.fleet = append(m.fleet, panel.Panel{ID: string(rune('a' + i)), Title: title, State: panel.Running})
	}
	return m
}

// runCmd runs a tea.Cmd with os.Stderr redirected, returning the bytes it wrote —
// the actual escape the operator's terminal would receive.
func runCmd(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a notification command, got nil")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stderr
	os.Stderr = w
	cmd()
	os.Stderr = saved
	_ = w.Close()
	out, _ := io.ReadAll(r)
	_ = r.Close()
	return string(out)
}

// TestNotifyCoalescesTheWholeWindow is the promise the feature is named for:
// three agents raising their hands inside one window produce ONE notification
// that counts them, not three that interrupt three times.
func TestNotifyCoalescesTheWholeWindow(t *testing.T) {
	m := notifyModel("alpha", "beta", "gamma")

	// The first edge opens the window; it must not fire on its own.
	m.fleet[0].State = panel.Attention
	m.refreshAttention()
	if m.notifyAt.IsZero() {
		t.Fatal("the first edge should open the coalescing window")
	}
	if cmd := m.takeNotify(); cmd != nil {
		t.Fatal("the first edge must not fire immediately — that is the whole point")
	}

	// Two more join it while the window is still open, at different clock ticks.
	m.now = notifyEpoch.Add(5 * time.Second)
	m.fleet[1].State = panel.Attention
	m.refreshAttention()
	m.now = notifyEpoch.Add(20 * time.Second)
	m.fleet[2].State = panel.Attention
	m.refreshAttention()
	if cmd := m.takeNotify(); cmd != nil {
		t.Fatal("an open window must stay shut until notify-coalesce has elapsed")
	}

	// The window closes: one notification, naming the count.
	m.now = notifyEpoch.Add(defaultNotifyCoalesce)
	got := runCmd(t, m.takeNotify())
	if got != "\x1b]9;baton · 3 agents need you\a" {
		t.Fatalf("coalesced notification = %q", got)
	}
	if !m.notifyAt.IsZero() || len(m.notifyPending) != 0 {
		t.Fatal("draining the window should close it and clear what it held")
	}
	if cmd := m.takeNotify(); cmd != nil {
		t.Fatal("a drained window must not fire again")
	}
}

// TestNotifyNamesASinglePanel keeps the useful half of the message when there is
// exactly one agent to name: with one, "who" is worth saying.
func TestNotifyNamesASinglePanel(t *testing.T) {
	m := notifyModel("claude")
	m.fleet[0].State = panel.Attention
	m.refreshAttention()

	m.now = notifyEpoch.Add(defaultNotifyCoalesce)
	if got := runCmd(t, m.takeNotify()); got != "\x1b]9;baton · claude needs you\a" {
		t.Fatalf("single-panel notification = %q", got)
	}
}

// TestNotifyDedupesAFlickeringPanel proves one panel that leaves and re-enters
// attention inside a single window is still one agent in the count — including
// when it renames itself between the two edges, since the dedupe keys on the id.
// Without it the message would read "2 agents need you" about one agent.
func TestNotifyDedupesAFlickeringPanel(t *testing.T) {
	m := notifyModel("claude")
	m.fleet[0].State = panel.Attention
	m.refreshAttention()
	m.fleet[0].State = panel.Running
	m.refreshAttention()
	m.fleet[0].Title = "claude (renamed)"
	m.fleet[0].State = panel.Attention
	m.refreshAttention()

	m.now = notifyEpoch.Add(defaultNotifyCoalesce)
	if got := runCmd(t, m.takeNotify()); got != "\x1b]9;baton · claude needs you\a" {
		t.Fatalf("a flickering panel should still be one agent, got %q", got)
	}
}

// TestNotifyDisabledWritesNothing is the default cockpit: settings.notify off
// means no window ever opens and not one byte reaches the terminal, however many
// agents are shouting.
func TestNotifyDisabledWritesNothing(t *testing.T) {
	m := notifyModel("alpha", "beta")
	m.notifyEnabled = false
	m.fleet[0].State = panel.Attention
	m.fleet[1].State = panel.Stuck
	m.refreshAttention()

	if !m.notifyAt.IsZero() || len(m.notifyPending) != 0 {
		t.Fatal("a disabled notifier must not accumulate anything")
	}
	m.now = notifyEpoch.Add(time.Hour)
	if cmd := m.takeNotify(); cmd != nil {
		t.Fatal("a disabled notifier must never produce a command")
	}
}

// TestNotifyNeverWakesYouForDone holds the line the design draws around the
// notification channel: it carries questions, wedges and failures — the things a
// human has to act on — and never a finished turn or a clean exit, which are the
// things that would train someone to mute it.
func TestNotifyNeverWakesYouForDone(t *testing.T) {
	quiet := []panel.Panel{
		{ID: "1", Title: "finished", State: panel.Done},
		{ID: "2", Title: "clean", State: panel.Exited, ExitCode: 0},
		{ID: "3", Title: "busy", State: panel.Running},
		{ID: "4", Title: "waiting", State: panel.Idle},
		{ID: "5", Title: "starting", State: panel.Spawning},
	}
	for _, p := range quiet {
		if wantsHuman(p) {
			t.Errorf("%s (%s) must not raise a desktop notification", p.Title, p.State)
		}
	}

	loud := []panel.Panel{
		{ID: "6", Title: "asking", State: panel.Attention},
		{ID: "7", Title: "wedged", State: panel.Stuck},
		{ID: "8", Title: "crashed", State: panel.Exited, ExitCode: 1},
	}
	for _, p := range loud {
		if !wantsHuman(p) {
			t.Errorf("%s (%s) should raise a desktop notification", p.Title, p.State)
		}
	}

	// End to end: a fleet whose only news is a finished agent stays silent.
	m := model{notifyEnabled: true, notifyCoalesce: defaultNotifyCoalesce, now: notifyEpoch, fleet: quiet}
	m.refreshAttention()
	m.now = notifyEpoch.Add(time.Hour)
	if cmd := m.takeNotify(); cmd != nil {
		t.Fatal("a fleet of finished and idle panels must not notify")
	}
}

// TestNotifyIgnoresTheSingletons keeps the conductor and the global shell out of
// the count, exactly as the badge and the bell already do — they are cockpit
// furniture, not work anyone is waiting on.
func TestNotifyIgnoresTheSingletons(t *testing.T) {
	m := model{notifyEnabled: true, notifyCoalesce: defaultNotifyCoalesce, now: notifyEpoch, fleet: []panel.Panel{
		{ID: "1", Title: "conductor", State: panel.Attention, Conductor: true},
		{ID: "2", Title: "shell", State: panel.Stuck, GlobalShell: true},
	}}
	m.refreshAttention()
	m.now = notifyEpoch.Add(time.Hour)
	if cmd := m.takeNotify(); cmd != nil {
		t.Fatal("the conductor and the global shell must not raise a notification")
	}
}

// TestNotifyTitleCannotEscapeTheSequence is the security contract. A panel title
// is agent-controlled and lands inside ESC ] 9 ; … BEL: a title carrying a BEL
// would close the sequence early and hand the rest of its bytes to the operator's
// terminal as commands — an OSC 52 clipboard rewrite, a cursor escape over the
// frame. Whatever is written must therefore hold exactly one ESC and one BEL, and
// both must be baton's own.
func TestNotifyTitleCannotEscapeTheSequence(t *testing.T) {
	hostile := []struct {
		name  string
		title string
	}{
		{"bare BEL", "evil\aBEL"},
		{"bare ESC", "evil\x1b[31mred"},
		{"OSC 52 clipboard write", "x\x1b]52;c;cm0gLXJmIC8K\a"},
		{"closing then a new OSC 9", "x\a\x1b]9;spoofed"},
		{"C1 OSC introducer", "x\u009d52;c;AAAA\a"},
		{"raw C1 byte", "x\x9d52;c;AAAA\a"},
		{"newlines and tabs", "line\none\ttwo"},
		{"zero-width joiner", "a\u200db"},
	}
	for _, tc := range hostile {
		m := notifyModel(tc.title)
		m.fleet[0].State = panel.Attention
		m.refreshAttention()
		m.now = notifyEpoch.Add(defaultNotifyCoalesce)
		got := runCmd(t, m.takeNotify())

		if !strings.HasPrefix(got, "\x1b]9;") || !strings.HasSuffix(got, "\a") {
			t.Errorf("%s: the write is not one OSC 9 sequence: %q", tc.name, got)
			continue
		}
		if n := strings.Count(got, "\x1b"); n != 1 {
			t.Errorf("%s: wrote %d ESC bytes, want exactly baton's own: %q", tc.name, n, got)
		}
		if n := strings.Count(got, "\a"); n != 1 {
			t.Errorf("%s: wrote %d BEL bytes, want exactly the terminator: %q", tc.name, n, got)
		}
		// Nothing left in the payload can start or end a sequence of its own. What
		// survives is inert text: "]52;c;…" with no ESC in front of it is seven
		// characters on a toast, not a clipboard write.
		for _, r := range strings.TrimSuffix(strings.TrimPrefix(got, "\x1b]9;"), "\a") {
			if unicode.IsControl(r) || r == unicode.ReplacementChar {
				t.Errorf("%s: control rune %q survived into the payload: %q", tc.name, r, got)
			}
		}
	}
}

// TestSanitizeNotifyCapsAndFolds covers the rest of the scrub: whitespace runs
// collapse, the title is capped at maxNotifyRunes counted in RUNES not bytes, and
// a title that scrubs away to nothing becomes a placeholder rather than silently
// costing its panel the alert.
func TestSanitizeNotifyCapsAndFolds(t *testing.T) {
	if got := sanitizeNotify("  spaced   out \n"); got != "spaced out" {
		t.Errorf("whitespace should fold to single spaces, got %q", got)
	}
	long := strings.Repeat("な", 200)
	got := sanitizeNotify(long)
	if n := len([]rune(got)); n != maxNotifyRunes {
		t.Errorf("a long title should cap at %d runes, got %d", maxNotifyRunes, n)
	}
	if got := sanitizeNotify("\x1b\a\x00"); got != "a panel" {
		t.Errorf("a title of pure control bytes should fall back, got %q", got)
	}
	if got := sanitizeNotify("claude"); got != "claude" {
		t.Errorf("an ordinary title should pass through untouched, got %q", got)
	}
}

// TestNotifyAndBellDoNotSuppressEachOther: the two reach different people — the
// bell whoever is at this terminal, the toast whoever is not — so a rising edge
// arms both, and draining one leaves the other alone.
func TestNotifyAndBellDoNotSuppressEachOther(t *testing.T) {
	m := notifyModel("claude")
	m.bellEnabled = true
	m.fleet[0].State = panel.Attention
	m.refreshAttention()

	if !m.bellPending {
		t.Fatal("the bell should still arm with notifications on")
	}
	if m.notifyAt.IsZero() {
		t.Fatal("the notification window should still open with the bell on")
	}
	if m.takeBell() == nil {
		t.Fatal("the bell should ring immediately, without waiting for the window")
	}
	m.now = notifyEpoch.Add(defaultNotifyCoalesce)
	if got := runCmd(t, m.takeNotify()); got != "\x1b]9;baton · claude needs you\a" {
		t.Fatalf("the toast should still go out after the bell rang, got %q", got)
	}
	if m.status != "◆ claude needs you" {
		t.Fatalf("the footer pop should be untouched by either, got %q", m.status)
	}
}

// TestTickDrainsTheNotifyWindow proves the drain is wired to the one-second tick.
// It has to be: only the tick moves m.now, and telemetry is broadcast only when a
// panel MOVED — so a fleet that raises its hands and then goes still would sit on
// an open window forever if the drain hung off the event handlers instead.
func TestTickDrainsTheNotifyWindow(t *testing.T) {
	m := notifyModel("claude")
	m.fleet[0].State = panel.Attention
	m.refreshAttention()

	// A tick inside the window changes nothing.
	next, _ := m.Update(tickMsg(notifyEpoch.Add(time.Second)))
	m = next.(model)
	if m.notifyAt.IsZero() {
		t.Fatal("a tick inside the window must not close it")
	}

	// A tick past it closes the window and clears what it held.
	next, cmd := m.Update(tickMsg(notifyEpoch.Add(defaultNotifyCoalesce)))
	m = next.(model)
	if !m.notifyAt.IsZero() || len(m.notifyPending) != 0 {
		t.Fatal("a tick past notify-coalesce should drain the window")
	}
	if cmd == nil {
		t.Fatal("the draining tick should still return the clock's own re-arm")
	}
}

// TestNotifyPrefsFromConfig maps the two config keys onto the cockpit, defaults
// included: notify is OFF until asked for — a desktop toast goes somewhere baton
// was not invited — and an unusable coalesce window falls back rather than
// collapsing to the per-panel storm it exists to prevent.
func TestNotifyPrefsFromConfig(t *testing.T) {
	if p := prefsFromConfig(config.Config{}); p.notify || p.notifyCoalesce != defaultNotifyCoalesce {
		t.Fatalf("defaults should be off and %v, got %v / %v", defaultNotifyCoalesce, p.notify, p.notifyCoalesce)
	}

	on := true
	cfg := config.Config{}
	cfg.Settings.Notify = &on
	cfg.Settings.NotifyCoalesce = "2m"
	p := prefsFromConfig(cfg)
	if !p.notify || p.notifyCoalesce != 2*time.Minute {
		t.Fatalf("config should carry through, got %v / %v", p.notify, p.notifyCoalesce)
	}

	for _, bad := range []string{"not-a-duration", "-5s"} {
		if got := parseCoalesce(bad); got != defaultNotifyCoalesce {
			t.Errorf("%q should fall back to the default, got %v", bad, got)
		}
	}
	if got := parseCoalesce("0s"); got != 0 {
		t.Errorf("zero is a real setting — send on the next tick — got %v", got)
	}
}

// TestNotifyReachesTheModelThroughApplyPrefs closes the loop from the config file
// to the running cockpit, so a key that parses but is never plumbed cannot pass.
func TestNotifyReachesTheModelThroughApplyPrefs(t *testing.T) {
	on := true
	cfg := config.Config{}
	cfg.Settings.Notify = &on
	cfg.Settings.NotifyCoalesce = "45s"

	m := model{}.applyPrefs(prefsFromConfig(cfg))
	if !m.notifyEnabled || m.notifyCoalesce != 45*time.Second {
		t.Fatalf("applyPrefs should carry the notify settings, got %v / %v", m.notifyEnabled, m.notifyCoalesce)
	}
}

// TestNotifyCountsPanelsNotTitles is why the window dedupes on panel ID. Two
// DIFFERENT panels can end up with the same string in the sentence — sharing a
// name under allow-name-conflict, both scrubbing down to the placeholder, or
// differing only past the rune cap — and a title-keyed dedupe would collapse them
// and under-report the count. The last two cases are agent-controlled, so it would
// also hand one agent a way to swallow a peer's escalation.
func TestNotifyCountsPanelsNotTitles(t *testing.T) {
	long := strings.Repeat("z", maxNotifyRunes)
	collisions := [][2]string{
		{"claude", "claude"},           // allow-name-conflict
		{"\x1b\a", "\x00\x00"},         // both scrub to the placeholder
		{long + " one", long + " two"}, // differ only past the cap
	}
	for _, pair := range collisions {
		m := notifyModel(pair[0], pair[1])
		m.fleet[0].State = panel.Attention
		m.fleet[1].State = panel.Attention
		m.refreshAttention()
		m.now = notifyEpoch.Add(defaultNotifyCoalesce)

		got := runCmd(t, m.takeNotify())
		if got != "\x1b]9;baton · 2 agents need you\a" {
			t.Errorf("two panels titled %q / %q should count as two, got %q", pair[0], pair[1], got)
		}
	}
}

// TestNotifyOffMidWindowSendsNothing: switching settings.notify off — a config
// broadcast from the daemon, or C-t R — must take an already-open window with it.
// Otherwise "off" still means one last toast, up to a whole coalesce window after
// the setting changed.
func TestNotifyOffMidWindowSendsNothing(t *testing.T) {
	m := notifyModel("claude")
	m.fleet[0].State = panel.Attention
	m.refreshAttention()
	if m.notifyAt.IsZero() {
		t.Fatal("the window should be open before the reload")
	}

	m = m.applyPrefs(prefsFromConfig(config.Config{})) // the default config: notify off
	if !m.notifyAt.IsZero() || len(m.notifyPending) != 0 || len(m.notifyIDs) != 0 {
		t.Fatal("turning notifications off should close the open window")
	}
	m.now = notifyEpoch.Add(time.Hour)
	if cmd := m.takeNotify(); cmd != nil {
		t.Fatal("a window opened before the switch must not fire after it")
	}
}

// TestNotifyOffKeepsNoEdgeState is the allocation contract that goes with it: on
// the default cockpit refreshAttention must not build or keep the wider edge set
// at all, and switching notifications back on must then see whatever is currently
// outstanding rather than a stale set that hides it.
func TestNotifyOffKeepsNoEdgeState(t *testing.T) {
	m := notifyModel("alpha")
	m.notifyEnabled = false
	m.fleet[0].State = panel.Attention
	m.refreshAttention()
	if m.notifySeen != nil {
		t.Fatalf("a disabled notifier should keep no edge set, got %v", m.notifySeen)
	}

	// Switched on while the panel is still waiting: it is news to this notifier.
	m.notifyEnabled = true
	m.refreshAttention()
	m.now = notifyEpoch.Add(defaultNotifyCoalesce)
	if got := runCmd(t, m.takeNotify()); got != "\x1b]9;baton · alpha needs you\a" {
		t.Fatalf("enabling should report what is already outstanding, got %q", got)
	}
}

// TestNotifyStaysQuietWithinTheWiderSet documents the asymmetry between the
// surfaces: a stuck panel that starts asking a question is a genuine entry into
// `attention`, so the bell and the footer pop fire — but it never left the
// wantsHuman set, so the toast does not call the same person a second time about
// a panel they have already been called about.
func TestNotifyStaysQuietWithinTheWiderSet(t *testing.T) {
	m := notifyModel("claude")
	m.bellEnabled = true
	m.fleet[0].State = panel.Stuck
	m.refreshAttention()

	m.now = notifyEpoch.Add(defaultNotifyCoalesce)
	if got := runCmd(t, m.takeNotify()); got != "\x1b]9;baton · claude needs you\a" {
		t.Fatalf("stuck should raise the first toast, got %q", got)
	}

	// Now it asks a question. The bell and the pop fire; the toast does not.
	m.bellPending, m.status = false, "dashboard"
	m.fleet[0].State = panel.Attention
	m.refreshAttention()
	if !m.bellPending || m.status != "◆ claude needs you" {
		t.Fatalf("stuck → attention should still ring and pop, bell = %v status = %q", m.bellPending, m.status)
	}
	if !m.notifyAt.IsZero() {
		t.Fatal("a move within the wantsHuman set must not open a second window")
	}
}
