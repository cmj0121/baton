package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/i18n"
	"github.com/cmj0121/baton/internal/panel"
)

// zoomStatusFor opens a zoom on p in the given language and returns the status
// line it lands on.
func zoomStatusFor(t *testing.T, lang i18n.Lang, p panel.Panel) string {
	t.Helper()
	c, _ := recordingServer(t)
	m := model{client: c, width: 80, height: 24, lang: lang,
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
	return m.zoomInto(p).status
}

// TestExitedZoomStatusIsFullyTranslated pins the whole result line, not its
// prefix. The line used to be a translated "result · " with a hardcoded
// " (exited)" concatenated on, so a zh-TW cockpit read "結果 · sh #7 (exited)" —
// the one word the status exists to say stayed in English.
//
// The assertion is on the ENGLISH word surviving into the zh-TW line rather than
// on the zh-TW wording itself, because that is what the defect was and what a
// re-introduction would look like: put the suffix back outside m.tr and this
// fails, reword 已結束 and it does not.
func TestExitedZoomStatusIsFullyTranslated(t *testing.T) {
	// Replay: the panel died under this daemon and its last screen is still
	// there, which is the exited zoom that opens at all — a dead slot with
	// nothing behind it is refused before it can set a status (see deadSlot).
	dead := panel.Panel{ID: "7", Title: "sh #7", State: panel.Exited, Replay: true}

	zh := zoomStatusFor(t, i18n.ZhTW, dead)
	if strings.Contains(strings.ToLower(zh), "exited") {
		t.Errorf("the zh-TW result line still carries the English word: %q", zh)
	}
	if !strings.Contains(zh, "sh #7") {
		t.Errorf("the result line dropped the panel's title: %q", zh)
	}

	en := zoomStatusFor(t, i18n.EN, dead)
	if en != "result · sh #7 (exited)" {
		t.Errorf("English result line = %q, want %q", en, "result · sh #7 (exited)")
	}
	if zh == en {
		t.Errorf("the result line is untranslated: %q", zh)
	}
}

// TestLiveZoomStatusStillNamesThePanel guards the other arm of the same branch:
// turning the exited line into a format string must not have cost the live one
// its title.
func TestLiveZoomStatusStillNamesThePanel(t *testing.T) {
	live := panel.Panel{ID: "7", Title: "sh #7", State: panel.Running}

	en := zoomStatusFor(t, i18n.EN, live)
	if en != "zoomed · sh #7" {
		t.Errorf("English zoom line = %q, want %q", en, "zoomed · sh #7")
	}
	if zh := zoomStatusFor(t, i18n.ZhTW, live); zh == en {
		t.Errorf("the live zoom line is untranslated: %q", zh)
	}
}
