package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/i18n"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/signals"
)

// helpModel is a cockpit sized to render a whole key list, in the given language
// and opened from the given view.
func helpModel(lang i18n.Lang, from mode) model {
	return model{
		mode: modeHelp, helpFrom: from, width: 120, height: 400,
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t", lang: lang,
	}
}

// TestHelpIsFullyTranslated is the completeness check for the key lists: for
// every view help opens over, each rendered row must read differently in zh-TW
// than in English. A row someone forgot to key — or keyed with a typo — falls
// back to its English and shows up here as an identical line.
func TestHelpIsFullyTranslated(t *testing.T) {
	for _, from := range []mode{modeDashboard, modeZoom, modeGroupZoom} {
		enTitle, en := helpRows(helpModel(i18n.EN, from))
		zhTitle, zh := helpRows(helpModel(i18n.ZhTW, from))

		if len(en) != len(zh) {
			t.Fatalf("view %v: translating changed the row count, %d → %d", from, len(en), len(zh))
		}
		if zhTitle == enTitle {
			t.Errorf("view %v: the title is untranslated (%q)", from, enTitle)
		}
		for i := range en {
			if strings.TrimSpace(en[i]) == "" {
				continue // spacer rows are blank in both
			}
			if en[i] == zh[i] {
				t.Errorf("view %v row %d is untranslated: %q", from, i, en[i])
			}
		}
	}
}

// TestBindingsAreFullyTranslated checks every editable binding has a zh-TW
// description, since the key list and the key-bindings screen both render it.
func TestBindingsAreFullyTranslated(t *testing.T) {
	zh := model{lang: i18n.ZhTW}
	en := model{}
	for _, b := range bindings {
		if got := en.bindDesc(b); got != b.desc {
			t.Errorf("binding %q should render its English source, got %q", b.name, got)
		}
		if zh.bindDesc(b) == b.desc {
			t.Errorf("binding %q has no zh-TW description (key bind.%s)", b.name, b.name)
		}
	}
}

// TestSettingsAndCategoriesAreTranslated covers the rest of the key-bindings
// screen: the purpose sections it shares with the key list, and its settings
// rows.
func TestSettingsAndCategoriesAreTranslated(t *testing.T) {
	zh, en := model{lang: i18n.ZhTW}, model{}
	for _, cat := range []string{"Navigation", "Panels", "Work items", "View", "Session"} {
		if en.trCat(cat) != cat {
			t.Errorf("category %q should render its English source, got %q", cat, en.trCat(cat))
		}
		if zh.trCat(cat) == cat {
			t.Errorf("category %q is untranslated", cat)
		}
	}
	for i := 0; i < numSettings; i++ {
		if zh.settingLabel(i) == en.settingLabel(i) {
			t.Errorf("settings row %d is untranslated: %q", i, en.settingLabel(i))
		}
	}
}

// TestEnglishIsTheDefault checks an untouched cockpit — no config, no language —
// still renders the English written at the call sites, so localisation is opt-in
// and cannot change what an existing user sees.
func TestEnglishIsTheDefault(t *testing.T) {
	m := helpModel("", modeDashboard)
	if m.effLang() != i18n.EN {
		t.Fatalf("an unset language should default to English, got %q", m.effLang())
	}
	_, rows := helpRows(m)
	view := m.helpView() + "\n" + strings.Join(rows, "\n")
	for _, want := range []string{spaced("DASHBOARD KEYS"), "Navigation", "close the selected panel"} {
		if !strings.Contains(view, want) {
			t.Errorf("the default help should read in English, missing %q", want)
		}
	}
}

// TestLanguageDetectedFromConfig checks the config file's settings.language
// reaches the cockpit through the same prefs path every other setting uses.
func TestLanguageDetectedFromConfig(t *testing.T) {
	t.Setenv("BATON_LANG", "")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "")

	var cfg config.Config
	if got := prefsFromConfig(cfg).lang; got != i18n.EN {
		t.Errorf("an unset language should follow the environment, got %q", got)
	}
	cfg.Settings.Language = "zh-TW"
	if got := prefsFromConfig(cfg).lang; got != i18n.ZhTW {
		t.Errorf("settings.language should win over the environment, got %q", got)
	}

	m := model{}.applyPrefs(prefsFromConfig(cfg))
	if m.lang != i18n.ZhTW {
		t.Errorf("applyPrefs should carry the language onto the model, got %q", m.lang)
	}
}

// TestEditedLanguageAppliesOnReload is the promise the language setting makes:
// edit settings.language, press C-t R, and the help surfaces redraw in it — a
// reload rather than a restart.
//
// It used to be checked through the daemon's config push, and that is no longer
// how it travels. The daemon holds its config from startup until a reload of its
// own, so a long-lived one kept pinning the language of every cockpit that
// attached; and over --remote it is not merely stale but wrong, since that
// daemon is on another machine whose locale says nothing about this terminal.
// The reload re-reads the cockpit's OWN config instead, which is both the file
// the user just edited and the right machine to be asking.
func TestEditedLanguageAppliesOnReload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BATON_LANG", "")
	t.Setenv("LC_ALL", "en_US.UTF-8") // the environment says English...
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "")

	if err := os.MkdirAll(filepath.Join(home, ".baton"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// ...and the file the user just edited says otherwise, which wins.
	if err := os.WriteFile(filepath.Join(home, ".baton", "config"),
		[]byte("settings:\n    language: zh-TW\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	m := model{binds: append([]binding(nil), bindings...)}
	next, _ := m.runAction(actReload)
	if got := next.(model).lang; got != i18n.ZhTW {
		t.Fatalf("an edited settings.language should apply on reload, got %q", got)
	}
}

// TestLanguageRowCycles checks the key-bindings screen's language row: enter
// advances it through the supported languages, the badge shows the active tag,
// and the help redraws in it.
func TestLanguageRowCycles(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // cycling the language persists to $HOME/.baton/config

	m := model{mode: modeKeyMap, width: 120, height: 400,
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t",
		cursor: len(bindings) + 1 + settingLanguage}

	if kind, idx := m.keyMapRow(); kind != rowSetting || idx != settingLanguage {
		t.Fatalf("the cursor should rest on the language row, got kind %v idx %d", kind, idx)
	}
	if !strings.Contains(m.settingBadge(settingLanguage), string(i18n.EN)) {
		t.Error("the language badge should show the active language tag")
	}

	next, _ := m.activate()
	m = next.(model)
	if m.lang != i18n.Next(i18n.EN) {
		t.Fatalf("enter should advance the language, got %q", m.lang)
	}
	if !strings.Contains(m.keyMapView(), string(m.lang)) {
		t.Error("the language badge should follow the cycle")
	}

	// Cycling all the way round comes home to English.
	for range i18n.Supported() {
		next, _ = m.activate()
		m = next.(model)
	}
	if m.lang != i18n.Next(i18n.EN) {
		t.Errorf("a full cycle should return to where it started, got %q", m.lang)
	}
}

// TestLocalisedHelpKeepsOnePopupWidth guards the two features together: CJK rows
// are twice as wide per glyph, and the fixed-width box must clip them to the
// frame rather than let them push or wrap it.
func TestLocalisedHelpKeepsOnePopupWidth(t *testing.T) {
	for _, from := range []mode{modeDashboard, modeZoom, modeGroupZoom} {
		en := helpModel(i18n.EN, from)
		zh := helpModel(i18n.ZhTW, from)
		want := en.popupWidth() + 2*popupPadX + 2
		if got := lipgloss.Width(zh.helpView()); got != want {
			t.Errorf("view %v: zh-TW help is %d wide, want %d", from, got, want)
		}
		if got := lipgloss.Width(en.helpView()); got != want {
			t.Errorf("view %v: English help is %d wide, want %d", from, got, want)
		}
	}
}

// helpRows is every tab's rows in order — "the whole key list", which is what a
// test means when it asks whether the list carries something. The panel itself
// only ever shows one tab.
func helpRows(m model) (string, []string) {
	title, secs := m.helpSections()
	var all []string
	for _, sec := range secs {
		all = append(all, sec.rows...)
	}
	return title, all
}

// TestPanelConfigIsFullyTranslated is the completeness check for the prefix + P
// page, and it works the way the key list's does: every line the page draws must
// read differently in zh-TW than in English. A string someone forgot to key falls
// back to its English and shows up here as an identical line.
//
// It holds for EVERY line because this page has no data on it. The rows are the
// fleet's own settings, and the two things on it that stay English in both
// languages — the resource-limit keys (cpus, nofile) and the values beside them —
// share a line with a label or a value that does not, so no line is English in
// full. A page state that puts a machine's own words on a line of their own (the
// KNOWN, NOT INSTALLED roll, which lists backend names and homepages) is left out
// of the fixture for that reason, not because it is exempt.
func TestPanelConfigIsFullyTranslated(t *testing.T) {
	page := func(lang i18n.Lang, tab int) []string {
		m := baseModel()
		m.mode, m.lang, m.height = modePanelConfig, lang, 44
		m.panelTab, m.shellPath = tab, "/bin/zsh"
		var out []string
		for _, line := range strings.Split(ansi.Strip(m.panelConfigView()), "\n") {
			if line = strings.TrimSpace(strings.Trim(line, "│╭╮╰╯─ ")); line != "" {
				out = append(out, line)
			}
		}
		return out
	}

	// Every tab, since each draws its own rows and its own hints now — checking
	// the one that happens to be open would leave two thirds of the page unread.
	for tab := range panelCfgTabs {
		en, zh := page(i18n.EN, tab), page(i18n.ZhTW, tab)
		if len(en) != len(zh) {
			t.Fatalf("tab %d: translating changed the line count, %d → %d", tab, len(en), len(zh))
		}
		for i := range en {
			if en[i] == zh[i] {
				t.Errorf("tab %d line %d is untranslated: %q", tab, i, en[i])
			}
		}
	}
}

// TestInputOverlaysAreFullyTranslated: every text-input popup — its title, its
// prompt and the verb on enter — reads differently in zh-TW than in English.
//
// It walks the table rather than a list written here, so an overlay added to
// inputSpecs without a catalog entry fails on its first frame instead of
// appearing in English to the people who cannot read it. The five resource-limit
// overlays ride on limitFields and are walked with them.
func TestInputOverlaysAreFullyTranslated(t *testing.T) {
	// Line by line, not whole overlays: the title, the prompt and the hint line
	// are three separate strings, and comparing the popup as one blob passes as
	// soon as ANY of them is keyed — a title left in English hides behind a
	// translated "cancel" on the line below it.
	lines := func(lang i18n.Lang, in inputPurpose, limitRow int) []string {
		m := baseModel()
		m.lang, m.input, m.limitRow = lang, in, limitRow
		var out []string
		for _, line := range strings.Split(ansi.Strip(m.inputView()), "\n") {
			line = strings.TrimSpace(strings.Trim(line, "│╭╮╰╯─ "))
			// The field itself is the typed text and is the same in every language.
			if line == "" || strings.Contains(line, "›") {
				continue
			}
			out = append(out, line)
		}
		return out
	}
	check := func(what string, in inputPurpose, limitRow int) {
		en, zh := lines(i18n.EN, in, limitRow), lines(i18n.ZhTW, in, limitRow)
		if len(en) != len(zh) {
			t.Fatalf("%s: translating changed the line count, %d → %d", what, len(en), len(zh))
		}
		for i := range en {
			if en[i] == zh[i] {
				t.Errorf("%s: line %d is untranslated: %q", what, i, en[i])
			}
		}
	}

	for in := range inputSpecs {
		check(fmt.Sprintf("input %d", in), in, 0)
	}
	for i := range limitFields {
		check("the "+limitFields[i].label+" limit overlay", inputLimit, firstLimitRow+i)
	}
	// The generic overlay an input with no table row falls back to.
	check("the fallback overlay", inputNone, 0)
}

// TestPickersAreFullyTranslated: the two centred pickers — pick an agent, send a
// signal — read differently in zh-TW than in English on every line that is not a
// name the machine owns.
//
// Those names are the point of the exemption. A backend is called claude because
// that is the binary on the PATH, and a signal is called SIGINT because that is
// the word `kill` takes; a row that renamed either would be naming something that
// does not exist. So a line carrying one of those may read the same in both
// languages — and every other line, being prose, may not.
func TestPickersAreFullyTranslated(t *testing.T) {
	for _, tc := range []struct {
		name string
		view func(model) string
		mut  func(*model)
	}{
		{"agent picker", model.agentPickerView, func(m *model) {
			m.agentList = []proto.AgentBackend{{Name: "claude", Command: "claude"}}
		}},
		{"default-agent picker", model.agentPickerView, func(m *model) {
			m.agentPurpose = agentForDefault
			m.agentList = []proto.AgentBackend{{Name: "claude", Command: "claude"}}
		}},
		{"signal picker", model.signalPickerView, func(m *model) {
			m.signalScope, m.signalTargets = "shell #1", []string{"1"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := func(lang i18n.Lang) []string {
				m := baseModel()
				m.lang = lang
				tc.mut(&m)
				var out []string
				for _, line := range strings.Split(ansi.Strip(tc.view(m)), "\n") {
					if line = strings.TrimSpace(strings.Trim(line, "│╭╮╰╯─ ")); line != "" {
						out = append(out, line)
					}
				}
				return out
			}
			en, zh := lines(i18n.EN), lines(i18n.ZhTW)
			if len(en) != len(zh) {
				t.Fatalf("translating changed the line count, %d → %d", len(en), len(zh))
			}
			for i := range en {
				// Compare what is left of the line once the machine's own words are
				// taken out of it, not the line. A gloss left in English sits on the
				// same row as SIGHUP, and a whole-line exemption would let the name
				// cover for it.
				a, b := prose(en[i]), prose(zh[i])
				if a == b && hasLetters(a) {
					t.Errorf("line %d is untranslated: %q", i, en[i])
				}
			}
		})
	}
}

// prose strips the names the machine owns out of a picker line — the signal wire
// names and the agent backend in the fixture — leaving the text that a
// translation is answerable for.
func prose(line string) string {
	for _, s := range signals.Choices {
		line = strings.ReplaceAll(line, s.Name, "")
	}
	return strings.TrimSpace(strings.ReplaceAll(line, "claude", ""))
}

// hasLetters reports whether a string still says anything a reader would read —
// so a row that is nothing but a keycap and a machine name is not demanded of the
// catalog.
func hasLetters(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// TestDashboardChromeLeavesNoEnglishBehind sweeps the whole dashboard frame in
// zh-TW and fails on any English word still in it.
//
// It is a scanner rather than a line-by-line comparison against the English
// frame, because that comparison is too coarse to catch what actually goes wrong
// here. A chip strip reading "◆ 1 需要你 · ● 2 idle" differs from its English
// line in every way a diff can see, and the one word nobody keyed sits in the
// middle of it. Whole-line equality goes green on exactly the bug this test is
// for — it did, on both mutations, before it was written this way.
//
// The fleet is named in machine words on purpose. A real panel is called
// "claude · refactor auth", and those are the fleet's words and never the
// cockpit's, so a fixture full of them would need an allowlist longer than the
// thing it is checking. With the fleet's own text reduced to p1..p4, every Latin
// word left on screen belongs to baton, and the allowlist is the short list of
// the ones that are meant to be there.
func TestDashboardChromeLeavesNoEnglishBehind(t *testing.T) {
	// What English on this screen is load-bearing: the product, the wire protocol,
	// the two panel KINDS (baton's own words, the ones `ctl spawn` takes), and the
	// host readout's units.
	allowed := map[string]bool{
		"baton": true, "protocol": true, "dev": true,
		"agent": true, "shell": true, "command": true,
		"cpu": true, "mem": true,
	}

	m := baseModel()
	m.lang, m.height = i18n.ZhTW, 40
	m.fleet = []panel.Panel{
		{ID: "1", Title: "p1", Kind: panel.Agent, State: panel.Attention},
		{ID: "2", Title: "p2", Kind: panel.Shell, State: panel.Running},
		{ID: "3", Title: "p3", Kind: panel.Agent, State: panel.Idle},
		{ID: "4", Title: "p4", Kind: panel.Agent, State: panel.Exited},
	}

	word := regexp.MustCompile(`[A-Za-z]{3,}`)
	for _, line := range strings.Split(ansi.Strip(m.frame()), "\n") {
		for _, w := range word.FindAllString(line, -1) {
			if !allowed[strings.ToLower(w)] {
				t.Errorf("untranslated English on the dashboard: %q in %q", w, strings.TrimSpace(line))
			}
		}
	}
}

// TestOverlaysLeaveNoEnglishBehind sweeps every pop-up the cockpit draws and
// fails on any English word left in the zh-TW render.
//
// It is the completeness check for the surfaces that have no fixture worth
// comparing against their English selves: an overlay's chrome is a title, an
// empty state and a legend, and the line-by-line comparisons elsewhere in this
// file cannot see a single unkeyed word inside a line that is otherwise
// translated. The scanner can, which is the same reason the dashboard has one.
//
// The allowlist is per overlay and short on purpose. A word earns its place
// there by belonging to something other than the cockpit — git's own
// subcommands, the wire words of a protocol, a plugin language's name — and each
// entry is a claim that translating it would be wrong, not that nobody got to it
// yet.
func TestOverlaysLeaveNoEnglishBehind(t *testing.T) {
	// Big enough that every overlay draws its whole body: a pop-up squeezed by a
	// short terminal drops rows, and an empty state that is not on screen is one
	// this test would pass without reading.
	m := baseModel()
	m.lang, m.width, m.height = i18n.ZhTW, 160, 48

	// Everywhere: the KEY NAMES, which are never translated because a translated
	// key is a key nobody can press; the product; and baton's own words for the
	// things it spawns.
	common := []string{
		"esc", "tab", "enter", "ctrl", "alt", "shift", "space", "backspace",
		"home", "end", "pgup", "pgdn",
		"baton", "agent", "shell", "command",
	}

	for _, tc := range []struct {
		name    string
		view    func() string
		allowed []string
	}{
		{"inbox", m.inboxView, nil},
		{"queue", m.queueView, nil},
		{"proc tree", m.procTreeView, nil},
		{"fleet search", m.fleetSearchView, nil},
		{"remote", m.remoteView, []string{"passkey"}},
		{"dir picker", m.dirPickView, nil},
		{"commands", m.commandPickerView, []string{"lua"}},
		// The git menu runs git's own ops and names each one as git does.
		{"git menu", m.gitPickerView, []string{
			"git", "diff", "log", "status", "stage", "all", "commit", "push",
			"branch", "worktree", "worktrees", "rm", "editor", "add", "repo",
		}},
		{"git output", func() string {
			return m.openGitOutPopup("git status", "on branch main", false).gitOutView()
		}, []string{"git", "status", "on", "branch", "main"}}, // the op and its output
		{"diff", m.diffView, nil},
		{"usage", m.usageView, []string{"claude", "code"}}, // the backend that reports quota
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed := map[string]bool{}
			for _, w := range append(append([]string(nil), common...), tc.allowed...) {
				allowed[w] = true
			}
			word := regexp.MustCompile(`[A-Za-z]{3,}`)
			for _, line := range strings.Split(ansi.Strip(tc.view()), "\n") {
				for _, w := range word.FindAllString(line, -1) {
					if !allowed[strings.ToLower(w)] {
						t.Errorf("untranslated English: %q in %q", w, strings.TrimSpace(line))
					}
				}
			}
		})
	}
}

// msgPair matches a message key paired with its English source string, in any of
// the forms the cockpit writes one: m.tr("k", "en"), the tr shorthand inside a
// view, i18n.T(lang, "k", "en"), and the tables that carry the pair as two fields
// (the key map's bindings, the input overlays, the git menu, the limit rows).
var msgPair = regexp.MustCompile(`"([a-z][a-z0-9-]*(?:\.[a-z0-9-]+)+)",\s*"((?:[^"\\]|\\.)*)"`)

// TestEveryMessageKeyIsTranslated reads this package's own source, collects every
// message key with the English written beside it, and fails on any whose zh-TW
// rendering is the same string.
//
// This is the completeness check the rendering tests cannot be. They can only
// reach what a fixture puts on screen — a status line needs the keystroke that
// sets it, an error needs the failure that raises it — and the cockpit has more
// than five hundred messages, most of which no fixture will ever draw. Reading
// the source reaches all of them, including the ones behind a condition nobody
// has hit yet.
//
// Two keys are exempt, and both are exempt for the reason the catalog gives
// throughout: they are not the cockpit's words. `git add -A` is a command line,
// and passkey is what the thing is called in baton's own docs and CLI.
func TestEveryMessageKeyIsTranslated(t *testing.T) {
	exempt := map[string]bool{
		"git.desc.stage-all": true, // a git command line, quoted as it is typed
		"remote.passkey":     true, // baton's own word for it, in the docs and the CLI
		"rform.passkey":      true, // the same word, as the field asking for one
		"rform.address.hint": true, // the three address FORMS, which are typed as shown
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	en, zh := model{}, model{lang: i18n.ZhTW}
	seen := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, mm := range msgPair.FindAllStringSubmatch(string(data), -1) {
			key, eng := mm[1], strings.ReplaceAll(mm[2], `\"`, `"`)
			if seen[key] || eng == "" || exempt[key] {
				continue
			}
			seen[key] = true
			if en.tr(key, eng) == zh.tr(key, eng) {
				t.Errorf("%s: no zh-TW for %q (%s)", f, eng, key)
			}
		}
	}
	// A guard on the guard: a regex that stopped matching would report nothing and
	// pass, which is the one way this test could quietly stop being one.
	if len(seen) < 400 {
		t.Errorf("only %d message keys found in the source; the scan is not reaching them", len(seen))
	}
}
