package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// indexPage is the site's landing page, which is also the only index a reader
// arriving from the web has.
const indexPage = "index.html"

// docLink is how the page spells a link to a page in this directory.
const docLink = "blob/main/docs/"

// englishPages are the pages the site is expected to list: every .md here that
// is not a translation and not a README, since the site links the English set
// and each page carries its own language switcher.
func englishPages(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob("*.md")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var out []string
	for _, p := range all {
		name := filepath.Base(p)
		if strings.Contains(name, ".zh-TW.") || strings.HasPrefix(name, "README.") {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		t.Fatal("no English pages found; this test would pass vacuously")
	}
	return out
}

// The site lists every page, and every page it lists exists.
//
// Both directions, because they fail differently and a reader meets both: a
// page that is written and never linked is invisible from the web however good
// it is, and a link left behind by a rename is a 404 with the project's name on
// it. Neither is visible to `make ci` otherwise -- it does not read HTML -- so
// this is the only thing standing between a new page and silence.
func TestTheSiteListsEveryPageAndEveryPageItListsExists(t *testing.T) {
	raw, err := os.ReadFile(indexPage)
	if err != nil {
		t.Fatalf("read %s: %v", indexPage, err)
	}
	page := string(raw)

	for _, name := range englishPages(t) {
		if !strings.Contains(page, docLink+name) {
			t.Errorf("%s is not linked from %s; a page nobody can reach from the site", name, indexPage)
		}
	}

	// The other direction: every docs/ link the page carries must resolve.
	for rest := page; ; {
		i := strings.Index(rest, docLink)
		if i < 0 {
			break
		}
		rest = rest[i+len(docLink):]
		end := strings.IndexAny(rest, `"'< `)
		if end < 0 {
			end = len(rest)
		}
		target := rest[:end]
		if target == "" {
			continue
		}
		if _, err := os.Stat(target); err != nil {
			t.Errorf("%s links docs/%s, which does not exist: %v", indexPage, target, err)
		}
	}
}
