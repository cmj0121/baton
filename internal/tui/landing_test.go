package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/i18n"
	"github.com/cmj0121/baton/internal/proto"
)

// The shipped key map, driven the way a hand drives it. keypending_test.go
// proves the machinery on a made-up map; this file proves the map itself — that
// the landings really are landings, that the keys they freed are free, and that
// the families behind them still reach their actions.

// A landing does nothing on its own, in the map as shipped.
func TestShippedLandingsAreInert(t *testing.T) {
	for _, land := range []string{"n", "v", "g", "x"} {
		m := press(model{fleet: sampleFleet(), mode: modeDashboard, cursor: 1}, land)
		if len(m.pending) != 1 || m.pending[0] != land {
			t.Errorf("%q should open a run and do nothing else, pending = %v", land, m.pending)
		}
		if m.input != inputNone || m.status != "" {
			t.Errorf("%q acted on its own: input %v, status %q", land, m.input, m.status)
		}
	}
}

// The work-item family: g g marks, g c creates, g u dissolves.
func TestWorkItemFamily(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard, cursor: 1}

	m = press(m, "g", "g")
	if len(m.marked) != 1 {
		t.Fatalf("g g should mark the selection, marked = %v", m.marked)
	}
	m = press(m, "g", "c")
	if m.input != inputGroupName {
		t.Fatalf("g c should open the group-name overlay, got %v", m.input)
	}
}

// The view family draws and never touches the fleet, so each of its keys is
// safe to assert by its own toggle.
func TestViewFamily(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}

	if m = press(m, "v", "k"); !m.keycast {
		t.Error("v k should turn the keycast readout on")
	}
	if m = press(m, "v", "p"); !m.preview {
		t.Error("v p should turn the detail pane on")
	}
	before := m.usageMode
	if m = press(m, "v", "u"); m.usageMode == before {
		t.Error("v u should cycle the usage footer")
	}
}

// Purging the fleet's dead is a double tap, and the first tap alone must not do
// it — that is the whole point of spelling it x x.
func TestPurgeNeedsBothTaps(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}

	m = press(m, "x")
	if m.status != "" {
		t.Errorf("one x should be silent, got %q", m.status)
	}
	m = press(m, "x")
	if m.status == "" {
		t.Error("x x should have acted on the exited panels")
	}
}

// The letters the landings freed are no longer bound to anything, which is what
// let the escapes under the leader stop shadowing them.
func TestFreedKeysAreUnbound(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}
	for _, k := range []string{"c", ".", "C", "H", "G", "a", "u", "U", "K", "V", "z"} {
		if b, ok := m.lookupCmd(k); ok {
			t.Errorf("%q should be free, still bound to %q", k, b.name)
		}
	}
}

// C-t a and C-t c were unreachable from a zoom while `a` and `c` were commands,
// because an escape shadows a command under the leader. Nothing starts with
// either letter now.
func TestEscapesNoLongerShadowCommands(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}
	for _, b := range m.keymap() {
		if !isEscape(b.act) {
			continue
		}
		esc := b.seq()
		if len(esc) != 1 {
			t.Errorf("escape %q is %q — escapes are reached on one key after the leader", b.name, b.key)
			continue
		}
		for _, c := range m.keymap() {
			if isEscape(c.act) {
				continue
			}
			if c.seq()[0] == esc[0] {
				t.Errorf("escape %q (%s) shadows command %q (%s) under the leader", b.name, b.key, c.name, c.key)
			}
		}
	}
}

// Restart ends the fleet, and bare S signals a whole work item in the split, so
// the two must not be the same keystroke in different views.
func TestRestartIsPrefixOnly(t *testing.T) {
	m := model{mode: modeDashboard, fleet: sampleFleet()}

	if m := press(m, keyRestart); m.pendingRestart {
		t.Error("bare S must not arm a force-restart")
	}
	if m := press(m, "ctrl+t", keyRestart); !m.pendingRestart {
		t.Error("C-t S should arm the force-restart confirmation")
	}
}

// The key map and its help surfaces have to print a sequence as the run you
// press, not as one impossible key.
func TestSequencesRenderAsSeparateCaps(t *testing.T) {
	cases := []struct {
		prefix, key string
		want        []string
	}{
		{"", keyNewPanel, []string{"p"}},
		{"", keyMark, []string{"g", "g"}},
		{"", keyGroup, []string{"g", "c"}},
		{"", keyNewForm, []string{"n", "c"}},
		{"C-t", keyDashboard, []string{"C-t", "d"}},
		{"", keyExpand, []string{"space"}},
	}
	for _, c := range cases {
		got := strings.Fields(stripANSI(keycaps(c.prefix, c.key, false)))
		if len(got) != len(c.want) {
			t.Errorf("keycaps(%q, %q) rendered %v, want %v", c.prefix, c.key, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("keycaps(%q, %q) rendered %v, want %v", c.prefix, c.key, got, c.want)
				break
			}
		}
	}
}

// A key the landing pass freed answers with its new home for one release. A key
// that did something yesterday and does nothing at all today reads as a broken
// cockpit, not as a rebind.
func TestFreedKeysPointAtTheirNewHome(t *testing.T) {
	cases := map[string]string{
		"C": seqLabel(keyConductor),
		"a": seqLabel(keyAdd),
		"U": seqLabel(keyUsage),
		"V": seqLabel(keyDashLayout),
		"z": seqLabel(keyLens),
	}
	for old, want := range cases {
		m := press(model{fleet: sampleFleet(), mode: modeDashboard}, old)
		if !strings.Contains(m.status, want) {
			t.Errorf("%q should point at %q, got %q", old, want, m.status)
		}
	}

	// Restart is an escape now, so its hint has to carry the leader — otherwise
	// it names a key that still does nothing.
	m := press(model{fleet: sampleFleet(), mode: modeDashboard}, keyRestart)
	if !strings.Contains(m.status, "C-t "+seqLabel(keyRestart)) {
		t.Errorf("the restart hint should name the leader, got %q", m.status)
	}
}

// The hint must never fire for a key that actually works, so a user who binds
// something onto a freed letter is not told it moved.
func TestRebindingAFreedKeySilencesItsHint(t *testing.T) {
	m := model{fleet: sampleFleet(), mode: modeDashboard}
	m = rebind(m, "preview", "z")

	m = press(m, "z")
	if !m.preview {
		t.Fatal("a rebound freed key should run its binding")
	}
	if strings.Contains(m.status, "moved") {
		t.Errorf("a key that works must not claim to have moved, got %q", m.status)
	}
}

// The editor collects a run, not a key, so a two-key binding can be typed in
// the cockpit rather than only in the config file.
func TestKeyMapBindsARun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := model{mode: modeKeyMap, fleet: sampleFleet(), binds: append([]binding(nil), bindings...), cursor: 1}

	m = press(m, "e", "n", "z")
	if !m.editing {
		t.Fatal("the capture should stay open while a run is being typed")
	}
	if !strings.Contains(m.status, "n z") {
		t.Errorf("the status should echo the run so far, got %q", m.status)
	}
	m = press(m, "enter")
	if m.editing {
		t.Fatal("enter should end the capture")
	}
	if got := m.binds[0].key; got != "n z" {
		t.Fatalf("expected the run bound, got %q", got)
	}
	if m = press(m, "n", "z"); m.input != inputNewPanelCmd && m.status == "" {
		t.Error("the run should reach its action once bound")
	}
}

// Binding a run whose start is already a binding is allowed — vim's d and dd
// relate that way — but it costs the shorter one the timeout, and that is not
// something to discover by feel a week later.
func TestKeyMapWarnsOnAnAmbiguousRebind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := model{mode: modeKeyMap, fleet: sampleFleet(), binds: append([]binding(nil), bindings...), cursor: 1}

	// new-panel is p; bind close to "p x" so p becomes the start of a longer run.
	for i, b := range m.binds {
		if b.name == "close" {
			m.cursor = i + 1
		}
	}
	m = press(m, "e", "p", "x", "enter")
	if !strings.Contains(m.status, "waits") {
		t.Errorf("an ambiguous rebind should say what it costs, got %q", m.status)
	}
}

// An unambiguous rebind says what it did and nothing more.
func TestKeyMapQuietOnAPlainRebind(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := model{mode: modeKeyMap, fleet: sampleFleet(), binds: append([]binding(nil), bindings...), cursor: 1}

	m = press(m, "e", "y", "enter")
	if strings.Contains(m.status, "waits") {
		t.Errorf("a plain rebind should not warn, got %q", m.status)
	}
	if !strings.Contains(m.status, "rebound") {
		t.Errorf("a rebind should report itself, got %q", m.status)
	}
}

// Every description in the key map starts in the same column, whatever its keys
// spell. The panel used to line up by accident, because every command was one
// keycap; landings made half the rows two caps wide and the accident ran out.
// A translated cockpit shows it up worst — a description that begins on a wide
// glyph gives the eye a hard left edge to notice the raggedness against.
func TestKeyMapDescriptionsShareAColumn(t *testing.T) {
	m := model{width: 120, height: 60, fleet: sampleFleet(), prefixKey: keyPrefix,
		binds: append([]binding(nil), bindings...), mode: modeKeyMap}
	view := stripANSI(m.View())

	// One row per key width: a single key, a landing run, and a prefixed escape.
	probes := map[string]string{
		"new-panel": "spawn a new shell panel",
		"mark":      "mark a panel for grouping",
		"log":       "start / stop logging",
	}
	col := -1
	for name, desc := range probes {
		var line string
		for _, l := range strings.Split(view, "\n") {
			if strings.Contains(l, desc) {
				line = l
				break
			}
		}
		if line == "" {
			t.Fatalf("no key-map row for %q", name)
		}
		at := strings.Index(line, desc)
		if col == -1 {
			col = at
			continue
		}
		if at != col {
			t.Errorf("%q starts its description at column %d, the others at %d", name, at, col)
		}
	}
}

// An unrelated save must not stamp the language into the config. Writing it on
// every save ended environment detection for good: the first bell toggle or
// rebind froze whatever had resolved at that instant, and since an explicit
// setting beats $LANG, a zh_TW.UTF-8 machine could end up pinned to English
// with no way to tell why.
func TestSaveDoesNotPinTheLanguage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BATON_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "zh_TW.UTF-8")

	m := model{fleet: sampleFleet(), binds: append([]binding(nil), bindings...)}
	if err := m.saveConfig(); err != nil {
		t.Fatalf("save: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.Settings.Language != "" {
		t.Fatalf("an unrelated save wrote language=%q; the environment can never win again", cfg.Settings.Language)
	}
	// And the environment still decides, which is the whole point.
	if got := prefsFromConfig(cfg).lang; got != i18n.ZhTW {
		t.Errorf("with LANG=zh_TW.UTF-8 and nothing pinned, expected zh-TW, got %q", got)
	}
}

// Picking a language from the key map is a choice, and choices are persisted.
func TestChoosingTheLanguagePersistsIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	m := model{fleet: sampleFleet(), binds: append([]binding(nil), bindings...), mode: modeKeyMap}
	m.cursor = len(m.keymap()) + 1 + settingLanguage
	m = press(m, "enter")

	if !m.langChosen {
		t.Fatal("cycling the language row should count as a choice")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cfg.Settings.Language == "" {
		t.Fatal("a chosen language should be written to the config")
	}
	if cfg.Settings.Language != string(m.effLang()) {
		t.Errorf("persisted %q, cockpit shows %q", cfg.Settings.Language, m.effLang())
	}
}

// A daemon's config must not decide the cockpit's language. The daemon reads
// its config once at startup and holds it until a reload, so a long-lived one
// was pinning the language of every cockpit that attached — editing the file
// looked like it did nothing, because the stale copy won on the next push.
func TestDaemonConfigDoesNotDecideTheLanguage(t *testing.T) {
	t.Setenv("BATON_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "zh_TW.UTF-8")

	m := model{fleet: sampleFleet(), lang: i18n.ZhTW}

	// The daemon is still serving the language it read before the file was fixed.
	stale := config.Config{}
	stale.Settings.Language = "en"
	blob, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	m.applyEvent(proto.ServerMsg{Type: "config", Config: blob})

	if m.effLang() != i18n.ZhTW {
		t.Fatalf("a stale daemon config pinned the cockpit to %q; the terminal's own language should win", m.effLang())
	}
}

// The rest of a pushed config still applies — only the language is held back.
func TestDaemonConfigStillAppliesEverythingElse(t *testing.T) {
	m := model{fleet: sampleFleet(), lang: i18n.ZhTW}

	pushed := config.Config{}
	on := true
	pushed.Settings.Mouse = &on
	pushed.Panel.Shell = "/bin/fish"
	blob, err := json.Marshal(pushed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	m.applyEvent(proto.ServerMsg{Type: "config", Config: blob})

	if !m.mouseEnabled {
		t.Error("a pushed setting should still apply")
	}
	if m.shellPath != "/bin/fish" {
		t.Errorf("a pushed panel default should still apply, got %q", m.shellPath)
	}
	if m.effLang() != i18n.ZhTW {
		t.Errorf("the language should be untouched, got %q", m.effLang())
	}
}

// The dashboard's key list groups a landing's family under the landing, showing
// each member by the key that completes it. A flat list of rows that happen to
// start with the same cap does not say "these four live under n" — which is the
// one thing a reader needs in order to use a landing at all.
func TestDashboardHelpGroupsLandings(t *testing.T) {
	m := model{width: 120, height: 400, fleet: sampleFleet(), prefixKey: keyPrefix,
		binds: append([]binding(nil), bindings...), mode: modeHelp, helpFrom: modeDashboard}
	_, body := helpRows(m)

	var text []string
	for _, l := range body {
		text = append(text, stripANSI(l))
	}
	joined := strings.Join(text, "\n")

	// Every landing in the shipped map gets a header naming it.
	for _, land := range []string{"n", "v", "g", "x"} {
		if !strings.Contains(joined, land+"  …") {
			t.Errorf("the key list should head the %q family with its landing", land)
		}
	}

	// A member shows the key that completes it, not the whole run: under g, mark
	// is "g", not "g g".
	markRow := ""
	for _, l := range text {
		if strings.Contains(l, "mark a panel for grouping") {
			markRow = l
			break
		}
	}
	if markRow == "" {
		t.Fatal("no row for mark")
	}
	if strings.Contains(markRow, "g   g") {
		t.Errorf("a family member should show only the key that completes it, got %q", markRow)
	}
	if !strings.HasPrefix(markRow, "   g") {
		t.Errorf("a family member should be indented under its landing, got %q", markRow)
	}
}

// The key list is tabbed by purpose, and the arrows walk the tabs. One long
// scroll made "how do I group these" a hunt past every panel verb.
func TestHelpTabsWalkByPurpose(t *testing.T) {
	m := model{width: 96, height: 24, fleet: sampleFleet(), prefixKey: keyPrefix,
		binds: append([]binding(nil), bindings...), mode: modeHelp, helpFrom: modeDashboard}

	_, secs := m.helpSections()
	if len(secs) < 4 {
		t.Fatalf("the dashboard list should have a tab per purpose, got %d", len(secs))
	}

	// Each tab shows only its own rows.
	m.helpTab = 0
	_, first := m.helpContent()
	m.helpTab = 1
	_, second := m.helpContent()
	if len(first) == 0 || len(second) == 0 {
		t.Fatal("both tabs should hold rows")
	}
	if strings.Join(first, "\n") == strings.Join(second, "\n") {
		t.Error("two tabs rendered the same rows")
	}

	// The arrows walk them, and wrap at each end.
	m.helpTab = 0
	m = press(m, "right")
	if m.helpTab != 1 {
		t.Errorf("→ should open the next tab, got %d", m.helpTab)
	}
	m = press(m, "left", "left")
	if m.helpTab != len(secs)-1 {
		t.Errorf("← past the first tab should wrap to the last, got %d", m.helpTab)
	}
	m = press(m, "right")
	if m.helpTab != 0 {
		t.Errorf("→ past the last tab should wrap to the first, got %d", m.helpTab)
	}

	// Switching tabs starts at the top, so a scrolled tab does not strand the
	// next one mid-list.
	m.helpScroll = 5
	m = press(m, "right")
	if m.helpScroll != 0 {
		t.Errorf("a new tab should open at the top, got offset %d", m.helpScroll)
	}
}

// The bar names the tab either side, so the arrows have somewhere legible to go.
func TestHelpTabBarNamesTheTabs(t *testing.T) {
	m := model{width: 96, height: 24, fleet: sampleFleet(), prefixKey: keyPrefix,
		binds: append([]binding(nil), bindings...), mode: modeHelp, helpFrom: modeDashboard}

	_, secs := m.helpSections()
	bar := stripANSI(strings.Join(m.helpTabBar(secs, m.width-8), " "))
	for _, want := range []string{"Panels", "Work items", "View"} {
		if !strings.Contains(bar, want) {
			t.Errorf("the tab bar should name %q, got %q", want, bar)
		}
	}

	// Too narrow for every tab: it narrows to the neighbours rather than
	// spilling, and says there is more either side.
	narrow := stripANSI(strings.Join(m.helpTabBar(secs, 20), " "))
	if !strings.Contains(narrow, "▸") {
		t.Errorf("a clipped bar should point at the tabs it dropped, got %q", narrow)
	}
}
