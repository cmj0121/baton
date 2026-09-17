package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/cmj0121/baton/internal/i18n"
)

// mustModel unwraps a runAction result.
func mustModel(t *testing.T, out tea.Model) model {
	t.Helper()
	m, ok := out.(model)
	if !ok {
		t.Fatalf("expected a model, got %T", out)
	}
	return m
}

// TestTheGroupStatusFormatsInZhTW catches a class rather than a line.
//
// The zh-TW catalogue reorders a format string's placeholders to read naturally
// — "%s 裡的 %d 個面板" for "%d panel(s) in %s" — and Go does not reorder the
// ARGUMENTS to match. status.re-running-d-panel did exactly that, so every group
// re-run in a zh-TW cockpit rendered as
//
//	正在重跑 %!s(int=3) 裡的 %!d(string=api) 個面板
//
// Asserting on "%!" rather than on the wording is what makes this a guard for the
// next reordered entry rather than a pin on this one. The catalogue's other three
// reordered pairs were checked against their call sites when this was found; they
// agree, which is why only one line moved.
//
// Both re-run verbs are driven, because the second one's entry was written from
// the first one's and would have copied the defect with the phrasing.
func TestTheGroupStatusFormatsInZhTW(t *testing.T) {
	for _, act := range []action{actRespawn, actRelaunch} {
		c, _ := recordingServer(t)
		m := model{client: c, width: 80, height: 24, mode: modeDashboard, lang: i18n.ZhTW,
			fleet: deadGroup(), binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}
		m.cursor = 0

		out, _ := m.runAction(act)
		got := mustModel(t, out)
		if strings.Contains(got.status, "%!") {
			t.Errorf("the zh-TW status mis-renders its placeholders: %q", got.status)
		}
		if !strings.Contains(got.status, "catnip") || !strings.Contains(got.status, "2") {
			t.Errorf("the zh-TW status should carry the group and the count, got %q", got.status)
		}
	}
}
