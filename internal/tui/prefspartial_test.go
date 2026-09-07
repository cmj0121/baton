package tui

import (
	"os"
	"testing"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/paths"
)

// TestLoadPrefsOnAConfigThatWillNotParseComesUpOnTheDefaults is #77 at the
// cockpit's bootstrap.
//
// loadPrefs discards the error on purpose — the cockpit has to come up — so this
// call site had no way at all to notice a half-decoded config. Its comment said
// a failure yields a zero cfg, which was true of a read error and false of the
// case that matters: a file that decoded its prefix, its keys and its agent list
// and then hit a type error handed all of them back, so the cockpit booted on a
// configuration that was the operator's file with an arbitrary suffix missing.
//
// The file below is exactly that shape, and the precondition is asserted through
// config.LoadPartial: without it the assertion is vacuous, because a file the
// decoder abandoned whole would also yield the defaults.
func TestLoadPrefsOnAConfigThatWillNotParseComesUpOnTheDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := paths.EnsureDir(paths.ConfigFile()); err != nil {
		t.Fatal(err)
	}
	broken := "prefix: ctrl+a\n" +
		"settings:\n" +
		"  confirm-close: false\n" +
		"score:\n" +
		"  rank:\n" +
		"    cwd: fast\n"
	if err := os.WriteFile(paths.ConfigFile(), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	residue, err := config.LoadPartial()
	if err == nil {
		t.Fatal("the config parsed; this test needs one that does not")
	}
	if residue.Prefix != "ctrl+a" || residue.Settings.ConfirmClose == nil {
		t.Fatalf("the decoder kept nothing worth losing (%+v); this test is about a residue that exists", residue)
	}

	got := loadPrefs()
	if got.prefix != keyPrefix {
		t.Errorf("prefix = %q from a file nobody could read, want the built-in %q", got.prefix, keyPrefix)
	}
	if !got.confirmClose {
		t.Error("confirm-on-close was switched off by a file that never parsed")
	}
}
