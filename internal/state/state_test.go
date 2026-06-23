package state

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/paths"
)

// sample builds a non-trivial snapshot: multiple panels across groups, with
// per-group visible-count view settings.
func sample() State {
	return State{
		Schema:   Schema,
		SavedAt:  time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC),
		LastBoot: time.Date(2026, 6, 21, 9, 0, 0, 0, time.UTC),
		Seq:      42,
		Panels: []PanelState{
			{
				ID: "p1", Kind: "shell", Title: "build", Group: "g1", Pinned: true,
				Spec: Spec{Command: "/bin/bash", Args: []string{"-l"}, Dir: "/tmp"},
			},
			{
				ID: "p2", Kind: "agent", Title: "claude", Group: "g1", Pinned: false,
				Spec: Spec{Command: "claude", Args: []string{"--print"}, Dir: "/work"},
			},
			{
				ID: "p3", Kind: "shell", Title: "logs", Group: "",
				Spec: Spec{Command: "/bin/sh"},
			},
		},
		Groups: []GroupLayout{
			{Group: "g1", Shown: 3},
			{Group: "g2"},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.state.json")
	want := sample()
	if err := want.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestSaveSetsSchemaAndSavedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.state.json")
	var s State // zero Schema, zero SavedAt
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Schema != Schema {
		t.Errorf("Schema = %d, want %d", got.Schema, Schema)
	}
	if got.SavedAt.IsZero() {
		t.Error("SavedAt is zero, want it set by Save")
	}
}

func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.state.json")
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, State{Schema: Schema}) {
		t.Fatalf("missing file: got %+v, want empty State", got)
	}
}

func TestLoadCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error, want nil: %v", err)
	}
	if !reflect.DeepEqual(got, State{Schema: Schema}) {
		t.Fatalf("corrupt: got %+v, want empty State", got)
	}
	assertCorruptedAside(t, dir, path)
}

func TestLoadNewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.state.json")
	s := sample()
	s.Schema = Schema + 1
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, State{Schema: Schema}) {
		t.Fatalf("newer schema: got %+v, want empty State", got)
	}
	assertCorruptedAside(t, dir, path)
}

func TestLoadInvalidLayout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.state.json")
	s := State{Schema: Schema, Groups: []GroupLayout{
		{Group: "g", Shown: -1}, // a negative visible count is nonsensical
	}}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, State{Schema: Schema}) {
		t.Fatalf("invalid layout: got %+v, want empty State", got)
	}
	assertCorruptedAside(t, dir, path)
}

func TestLoadIgnoresUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.state.json")
	if err := os.WriteFile(path, []byte(`{"schema":1,"seq":7,"surprise":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Seq != 7 {
		t.Errorf("Seq = %d, want 7 (unknown field should be ignored)", got.Seq)
	}
}

func TestSaveNoLeftoverTmp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.state.json")
	if err := sample().Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestStateFileDerivation(t *testing.T) {
	socket := "/run/baton/baton-123.sock"
	want := "/run/baton/baton-123.state.json"
	if got := paths.StateFile(socket); got != want {
		t.Errorf("StateFile(%q) = %q, want %q", socket, got, want)
	}
	// Mirrors the PidFile convention (same stem, different suffix).
	pid := paths.PidFile(socket)
	if strings.TrimSuffix(pid, ".pid") != strings.TrimSuffix(want, ".state.json") {
		t.Errorf("StateFile stem %q != PidFile stem %q", want, pid)
	}
}

// TestLoadReadError covers the non-missing read failure: a path that is a
// directory is not os.IsNotExist, so Load must surface the error rather than
// pretend it was a clean first run.
func TestLoadReadError(t *testing.T) {
	dir := t.TempDir() // a directory, not a file
	got, err := Load(dir)
	if err == nil {
		t.Fatal("Load(dir) returned nil error, want a read error")
	}
	if got.Schema != Schema {
		t.Errorf("Schema = %d, want %d even on error", got.Schema, Schema)
	}
}

// TestSaveTempCreateFails covers the temp-file open failure: a path whose parent
// directory does not exist cannot be written, and Save must report it.
func TestSaveTempCreateFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "x.state.json")
	if err := sample().Save(path); err == nil {
		t.Fatal("Save into a missing directory returned nil error")
	}
}

// assertCorruptedAside checks that exactly the bad file was renamed to a
// .corrupt-* sibling and the original path no longer exists.
func assertCorruptedAside(t *testing.T, dir, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("original %s still exists, want it renamed aside", path)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(path)
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), base+".corrupt-") {
			found = true
		}
	}
	if !found {
		t.Errorf("no %s.corrupt-* file found in %s", base, dir)
	}
}
