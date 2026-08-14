// Package i18n is baton's message localisation: a small lookup that maps a
// message key to the active language's text.
//
// English is the source language and stays at the call sites rather than living
// in a catalog of its own. Every lookup carries the English string as its
// fallback, so an untranslated key — or a language with no catalog at all —
// renders the English the code already reads, and a half-finished translation
// degrades to a mixed screen instead of a blank one. A catalog therefore holds
// only the overrides for one other language, and adding a language is one new
// file rather than a restatement of every English string.
package i18n

import (
	"os"
	"strings"
)

// Lang is a supported message language, named by its BCP-47 tag.
type Lang string

const (
	// EN is the source language: no catalog, every fallback is already English.
	EN Lang = "en"
	// ZhTW is Traditional Chinese as written in Taiwan.
	ZhTW Lang = "zh-TW"

	// Default is the language used when nothing selects one.
	Default = EN
)

// catalogs maps a language to its message overrides. English is absent by
// design — it is the fallback every lookup already carries.
var catalogs = map[Lang]map[string]string{
	ZhTW: zhTW,
}

// Supported lists the languages a user may select, in the order the cockpit's
// language switch cycles through them.
func Supported() []Lang {
	return []Lang{EN, ZhTW}
}

// Next returns the language after l in Supported order, wrapping at the end —
// what the cockpit's language row advances by. An unknown language cycles to
// the first supported one.
func Next(l Lang) Lang {
	all := Supported()
	for i, s := range all {
		if s == l {
			return all[(i+1)%len(all)]
		}
	}
	return all[0]
}

// T returns key's text in lang, falling back to the English source string when
// the language has no catalog or the catalog has no entry for the key. An empty
// fallback yields the key itself, so a message someone forgot to write shows up
// as a visible key rather than a blank gap in the layout.
func T(lang Lang, key, english string) string {
	if s, ok := catalogs[lang][key]; ok && s != "" {
		return s
	}
	if english == "" {
		return key
	}
	return english
}

// Normalize maps a locale string onto a supported language: "zh_TW.UTF-8",
// "zh-Hant", and "zh-tw" all give ZhTW, "en_US.UTF-8" gives EN. It reports false
// when the string names no supported language, so a caller can fall through to
// the next source rather than treating an unreadable locale as a choice.
func Normalize(s string) (Lang, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	// Drop the codeset and the modifier: "zh_TW.UTF-8@pinyin" → "zh_TW".
	if i := strings.IndexAny(s, ".@"); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(strings.ReplaceAll(s, "_", "-"))

	switch {
	case s == "c" || s == "posix":
		// The POSIX locale names no language; it means "unlocalised" — English.
		return EN, true
	case strings.HasPrefix(s, "en"):
		return EN, true
	case strings.HasPrefix(s, "zh"):
		// Traditional Chinese, by script tag or by the regions that write it. Every
		// other zh (zh-CN, zh-Hans) has no catalog yet and falls through, so it is
		// answered by the next source rather than by the wrong Chinese.
		for _, tag := range []string{"hant", "tw", "hk", "mo"} {
			if strings.Contains(s, tag) {
				return ZhTW, true
			}
		}
	}
	return "", false
}

// Detect resolves the language to render in: an explicit setting (the config
// file's settings.language) wins, then $BATON_LANG, then the POSIX locale chain
// ($LC_ALL, $LC_MESSAGES, $LANG), and finally English. A setting naming a
// language baton does not ship falls through to the environment rather than
// pinning the cockpit to English, so a typo is recoverable without an edit.
func Detect(setting string) Lang {
	if l, ok := Normalize(setting); ok {
		return l
	}
	for _, env := range []string{"BATON_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if l, ok := Normalize(os.Getenv(env)); ok {
			return l
		}
	}
	return Default
}
