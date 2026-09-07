package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// tableToken finds the backtick-quoted tokens in a markdown table row: the keys,
// the flags and the commands a feature table documents.
var tableToken = regexp.MustCompile("`[^`]+`")

// tokensInTables is every backtick-quoted token that appears in a table row of
// the file, deduplicated. Rows only, because prose legitimately differs between
// languages and a table is a list of the same facts in a different tongue.
func tokensInTables(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		for _, tok := range tableToken.FindAllString(line, -1) {
			seen[tok] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Every translated README documents the same set of keys and commands as the
// English one.
//
// Wording is the translator's and this says nothing about it. What it holds is
// the SET: a row added to the English table and to no other leaves five readers
// unable to learn a feature exists, and that is not a translation decision, it
// is an omission. It has happened twice in one day -- `g a` was missing from all
// six for weeks, and `baton serial` from five within hours of that being fixed --
// which is why it is a test and not a habit.
func TestEveryTranslatedReadmeDocumentsTheSameKeys(t *testing.T) {
	want := tokensInTables(t, filepath.Join("..", "README.md"))
	if len(want) == 0 {
		t.Fatal("no table tokens in README.md; this test would pass vacuously")
	}

	pages, err := filepath.Glob("README.*.md")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(pages) == 0 {
		t.Fatal("no README translations found; this test would pass vacuously")
	}

	for _, page := range pages {
		got := map[string]bool{}
		for _, tok := range tokensInTables(t, page) {
			got[tok] = true
		}
		for _, tok := range want {
			if !got[tok] {
				t.Errorf("%s documents no table row for %s, which README.md documents", page, tok)
			}
		}
	}
}

// Every English page has a Chinese one.
//
// Cheaper than comparing their contents and it catches the failure that actually
// happens: a page written in English and never translated at all. A page whose
// translation has gone stale is a judgement call; a page with no translation is
// not, and the switcher at the top of each page promises one.
func TestEveryPageHasATranslation(t *testing.T) {
	for _, name := range englishPages(t) {
		twin := strings.TrimSuffix(name, ".md") + ".zh-TW.md"
		if _, err := os.Stat(twin); err != nil {
			t.Errorf("%s has no %s; the language switcher on that page points at nothing", name, twin)
		}
	}
}
