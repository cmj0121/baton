package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/cmj0121/baton/internal/i18n"
	"github.com/cmj0121/baton/internal/proto"
)

// The overlay's columns are laid out by lipgloss, so they have to be measured by
// lipgloss. fmt's %-12s counts runes: 代理 is two of them and four columns, and a
// translated cockpit drew every header two cells right of the rows beneath it.
func TestPadMeasuresDisplayColumnsNotRunes(t *testing.T) {
	for _, s := range []string{"代理", "Agent", "剩餘 5h", "已用", "面板用量", ""} {
		if got := lipgloss.Width(pad(s, 12)); got != 12 {
			t.Errorf("pad(%q, 12) lays out %d columns, want 12", s, got)
		}
		if got := lipgloss.Width(padLeft(s, 12)); got != 12 {
			t.Errorf("padLeft(%q, 12) lays out %d columns, want 12", s, got)
		}
	}
	// A cell wider than its column is cut to fit rather than pushing the next one.
	if got := lipgloss.Width(pad("本窗口的消耗來源", 6)); got != 6 {
		t.Errorf("an oversized cell laid out %d columns, want 6", got)
	}
	// And the right-hand form puts the padding on the left, which is what makes a
	// column of figures read down.
	if !strings.HasPrefix(padLeft("71%", 8), "     ") {
		t.Errorf("padLeft put its padding on the wrong side: %q", padLeft("71%", 8))
	}
}

// columnOf is where text starts, in display columns.
func columnOf(t *testing.T, row, text string) int {
	t.Helper()
	i := strings.Index(row, text)
	if i < 0 {
		t.Fatalf("%q is not in %q", text, row)
	}
	return lipgloss.Width(row[:i])
}

// The header and the rows under it have to agree about where a column starts, in
// every language baton ships. This is the assertion the fixture cannot fake: the
// figure and the word above it are measured independently and compared.
func TestTheRollLinesUpWhenTranslated(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.EN, i18n.ZhTW} {
		m := quotaModel(
			[]proto.VendorUsage{{Vendor: "claude", State: "reading", Tokens: 1_200_000}},
			&proto.LimitsInfo{
				FiveHour: &proto.LimitWindow{UsedPercent: 38},
				SevenDay: &proto.LimitWindow{UsedPercent: 29},
			}, nil, nil)
		m.lang = lang

		rows := m.usageVendorSection()
		header, row := rows[0], rowFor(t, rows, "claude")
		for _, c := range []struct {
			what   string
			key    string
			value  string
			header string
		}{
			{"the quota column", "usage.view.session-left", "62%", "5h left"},
			{"the weekly column", "usage.view.week-left", "71%", "7d left"},
			{"the spent column", "usage.view.spent", "1.2M tok", "spent"},
		} {
			want := columnOf(t, header, i18n.T(lang, c.key, c.header))
			if got := columnOf(t, row, c.value); got != want {
				t.Errorf("%s: %s starts at column %d under a header at column %d (lang %q)\n%s\n%s",
					c.what, c.value, got, want, lang, header, row)
			}
		}
	}
}

// The mark for a reading baton does not have is an ASCII hyphen, and that is a
// layout decision rather than a typographic one. An em dash is East Asian
// ambiguous: runewidth with the condition a CJK terminal sets calls it two cells,
// lipgloss calls it one, and the column below it steps sideways on exactly the
// machines this cockpit is most read on.
func TestTheUnknownCellIsOneColumnUnderEveryRuler(t *testing.T) {
	ea := runewidth.NewCondition()
	ea.EastAsianWidth = true
	if got, want := ea.StringWidth(unknownCell), lipgloss.Width(unknownCell); got != want {
		t.Errorf("the two rulers disagree about %q: runewidth says %d, lipgloss says %d",
			unknownCell, got, want)
	}
	if ea.StringWidth("—") == lipgloss.Width("—") {
		t.Skip("this build's runewidth does not treat the em dash as ambiguous; the guard above still holds")
	}
}
