package tui

import (
	"os"
	"testing"
)

// TestMain pins the cockpit's message language to English for the whole package.
//
// Without it the suite reads the developer's locale. i18n.Detect falls through to
// $LANG, so every test that builds a model the way the real cockpit does — through
// prefsFromConfig — renders in whatever the machine is set to, and an assertion
// written against the English source string passes in Berlin and fails in Taipei.
// CI is a C locale, so the failure would arrive on one person's machine only,
// which is the worst place for it to live.
//
// A test that is ABOUT the language says so: it sets m.lang, or it sets the
// environment with t.Setenv, and either beats this default for that test alone.
func TestMain(m *testing.M) {
	_ = os.Setenv("BATON_LANG", "en")
	os.Exit(m.Run())
}
