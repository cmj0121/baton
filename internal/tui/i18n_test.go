package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/i18n"
	"github.com/cmj0121/baton/internal/proto"
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
		enTitle, en := helpModel(i18n.EN, from).helpContent()
		zhTitle, zh := helpModel(i18n.ZhTW, from).helpContent()

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
	view := m.helpView()
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

// TestLanguageSurvivesTheDaemonPush walks the reload path end to end: the daemon
// re-reads the config, marshals it, and pushes it to every open cockpit, which
// projects it back onto its prefs. This is what makes an edited settings.language
// a reload rather than a restart, and the JSON hop in the middle is the link that
// would break silently — the yaml tags do not travel with it.
func TestLanguageSurvivesTheDaemonPush(t *testing.T) {
	t.Setenv("BATON_LANG", "")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "")

	var cfg config.Config
	cfg.Settings.Language = "zh-TW"
	data, err := json.Marshal(cfg) // exactly what the daemon's applyConfig sends
	if err != nil {
		t.Fatalf("marshalling the effective config: %v", err)
	}

	m := model{binds: append([]binding(nil), bindings...)}
	m.applyEvent(proto.ServerMsg{Type: "config", Config: data})
	if m.lang != i18n.ZhTW {
		t.Fatalf("a pushed config should carry the language, got %q", m.lang)
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
