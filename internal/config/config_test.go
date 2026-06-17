package config

import "testing"

func TestLoadMissingIsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatalf("loading a missing config should not error: %v", err)
	}
	if len(c.Keys) != 0 {
		t.Fatalf("missing config should be empty, got %+v", c.Keys)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	off := false
	want := Config{
		Keys:     map[string]string{"new-panel": "x", "close": "W"},
		Settings: Settings{ConfirmClose: &off},
	}
	if err := want.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Keys["new-panel"] != "x" || got.Keys["close"] != "W" {
		t.Fatalf("keys round-trip mismatch: %+v", got.Keys)
	}
	if got.Settings.ConfirmClose == nil || *got.Settings.ConfirmClose {
		t.Fatalf("confirm-close should round-trip as false, got %+v", got.Settings.ConfirmClose)
	}
}

func TestLoadMissingSettingIsNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := (Config{Keys: map[string]string{"close": "w"}}).Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Settings.ConfirmClose != nil {
		t.Fatal("an unset confirm-close should load as nil so the default applies")
	}
}
