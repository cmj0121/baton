package i18n

import "testing"

// TestNormalize covers the locale strings a user or a shell can hand us: tags,
// POSIX locales with a codeset and a modifier, and the ones that name no
// language baton ships.
func TestNormalize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Lang
		ok   bool
	}{
		{"en", EN, true},
		{"en_US.UTF-8", EN, true},
		{"en-GB", EN, true},
		{"C", EN, true},
		{"POSIX", EN, true},
		{"zh-TW", ZhTW, true},
		{"zh_TW.UTF-8", ZhTW, true},
		{"zh_TW.UTF-8@pinyin", ZhTW, true},
		{"zh-Hant", ZhTW, true},
		{"zh-hk", ZhTW, true},
		{"  zh-TW  ", ZhTW, true},
		{"zh-CN", "", false}, // Simplified Chinese has no catalog yet
		{"zh-Hans", "", false},
		{"ja_JP.UTF-8", "", false},
		{"", "", false},
	} {
		got, ok := Normalize(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Normalize(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// TestDetectPrecedence checks the resolution order: the explicit setting wins,
// then $BATON_LANG, then the POSIX chain, and English backs the lot. A setting
// naming no supported language falls through instead of pinning English.
func TestDetectPrecedence(t *testing.T) {
	t.Setenv("BATON_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "")

	if got := Detect(""); got != EN {
		t.Errorf("a bare environment should detect %q, got %q", EN, got)
	}
	if got := Detect("zh_TW.UTF-8"); got != ZhTW {
		t.Errorf("an explicit setting should win, got %q", got)
	}

	t.Setenv("LANG", "zh_TW.UTF-8")
	if got := Detect(""); got != ZhTW {
		t.Errorf("$LANG should be consulted when nothing is set, got %q", got)
	}
	if got := Detect("en"); got != EN {
		t.Errorf("an explicit setting should beat $LANG, got %q", got)
	}
	if got := Detect("kl_GL"); got != ZhTW {
		t.Errorf("an unsupported setting should fall through to $LANG, got %q", got)
	}

	t.Setenv("LC_ALL", "en_US.UTF-8")
	if got := Detect(""); got != EN {
		t.Errorf("$LC_ALL should beat $LANG, got %q", got)
	}
	t.Setenv("BATON_LANG", "zh-TW")
	if got := Detect(""); got != ZhTW {
		t.Errorf("$BATON_LANG should beat the POSIX chain, got %q", got)
	}
}

// TestTFallsBackToEnglish checks a lookup resolves to the catalog when it has the
// key, and to the English the call site carries when it does not — including for
// a language with no catalog at all.
func TestTFallsBackToEnglish(t *testing.T) {
	const key, english = "bind.close", "close the selected panel"

	zh := T(ZhTW, key, english)
	if zh == english || zh == "" {
		t.Fatalf("zh-TW should translate %q, got %q", key, zh)
	}
	if got := T(EN, key, english); got != english {
		t.Errorf("English has no catalog and should pass the source through, got %q", got)
	}
	if got := T("de", key, english); got != english {
		t.Errorf("a language with no catalog should fall back, got %q", got)
	}
	if got := T(ZhTW, "no.such.key", english); got != english {
		t.Errorf("a missing key should fall back to the English, got %q", got)
	}
	if got := T(ZhTW, "no.such.key", ""); got != "no.such.key" {
		t.Errorf("a missing key with no English should show the key, got %q", got)
	}
}

// TestNextCycles checks the language switch walks every supported language and
// wraps, and that an unknown language re-enters at the first.
func TestNextCycles(t *testing.T) {
	all := Supported()
	if len(all) < 2 || all[0] != EN {
		t.Fatalf("Supported() should start at English and offer a choice, got %v", all)
	}
	l := all[0]
	for range all {
		l = Next(l)
	}
	if l != all[0] {
		t.Errorf("cycling %d languages should return to %q, got %q", len(all), all[0], l)
	}
	if got := Next("de"); got != all[0] {
		t.Errorf("an unknown language should cycle to %q, got %q", all[0], got)
	}
}

// TestCatalogsAreForNonSourceLanguages guards the package's central rule: English
// is the source language and must never grow a catalog, or the fallback at every
// call site would quietly stop being the thing that renders.
func TestCatalogsAreForNonSourceLanguages(t *testing.T) {
	if _, ok := catalogs[EN]; ok {
		t.Fatal("English is the source language and must not have a catalog")
	}
	for _, l := range Supported() {
		if l == EN {
			continue
		}
		if len(catalogs[l]) == 0 {
			t.Errorf("supported language %q has no catalog", l)
		}
	}
}
