package queue

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/task"
)

func fixedClock() func() time.Time {
	t := time.Unix(1_700_000_000, 0).UTC()
	return func() time.Time { return t }
}

func newStore(t *testing.T) *Store {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "queue"), fixedClock())
}

// TestSaveLoadRoundTrip checks a task survives Save → LoadAll intact, one file per
// task, with no leftover temp file.
func TestSaveLoadRoundTrip(t *testing.T) {
	s := newStore(t)
	in := []task.Task{
		{ID: "t1", Prompt: "a", Status: task.Queued, Group: "api", Attempts: 1},
		{ID: "t2", Prompt: "b", Status: task.Running, Panel: "5", Attempts: 2, Result: "ok"},
	}
	for _, tk := range in {
		if err := s.Save(tk); err != nil {
			t.Fatalf("save %s: %v", tk.ID, err)
		}
	}

	got, bad, err := s.LoadAll()
	if err != nil || len(bad) != 0 {
		t.Fatalf("LoadAll err=%v bad=%v", err, bad)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 tasks, got %d", len(got))
	}
	byID := map[string]task.Task{}
	for _, tk := range got {
		byID[tk.ID] = tk
	}
	if byID["t1"].Prompt != "a" || byID["t2"].Attempts != 2 || byID["t2"].Result != "ok" {
		t.Fatalf("round-trip lost fields: %+v", byID)
	}
	// One file per task, no temp leftover.
	entries, _ := os.ReadDir(s.Dir())
	if len(entries) != 2 {
		t.Fatalf("want 2 files, got %d: %v", len(entries), entries)
	}
}

// TestRemoveAndVanish checks Remove deletes the file and is a no-op when it is
// already gone.
func TestRemoveAndVanish(t *testing.T) {
	s := newStore(t)
	if err := s.Save(task.Task{ID: "t1", Status: task.Queued}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.Remove("t1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := s.Remove("t1"); err != nil {
		t.Fatalf("remove of an absent task should be nil, got %v", err)
	}
	if got, _, _ := s.LoadAll(); len(got) != 0 {
		t.Fatalf("backlog should be empty, got %d", len(got))
	}
}

// TestLoadAllSkipsAndQuarantinesBad checks a malformed file is left out of the
// load and reported, an over-schema file likewise, valid files still load, and a
// quarantined file is then ignored entirely.
func TestLoadAllSkipsAndQuarantinesBad(t *testing.T) {
	s := newStore(t)
	if err := s.Save(task.Task{ID: "t1", Prompt: "good", Status: task.Queued}); err != nil {
		t.Fatalf("save good: %v", err)
	}
	_ = os.MkdirAll(s.Dir(), 0o700)
	// A truncated JSON file and a future-schema file.
	if err := os.WriteFile(filepath.Join(s.Dir(), "t2.json"), []byte(`{"schema":1,"task":{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir(), "t3.json"), []byte(`{"schema":999,"task":{"id":"t3"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, bad, err := s.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 1 || got[0].ID != "t1" {
		t.Fatalf("only the good task should load, got %+v", got)
	}
	if len(bad) != 2 {
		t.Fatalf("both malformed files should be reported bad, got %v", bad)
	}

	for _, id := range bad {
		if err := s.Quarantine(id); err != nil {
			t.Fatalf("quarantine %s: %v", id, err)
		}
	}
	// After quarantine the bad files are .bad-* and no longer seen by LoadAll.
	got, bad, _ = s.LoadAll()
	if len(got) != 1 || len(bad) != 0 {
		t.Fatalf("after quarantine: got=%d bad=%v", len(got), bad)
	}
}

// TestMissingDirIsEmpty checks LoadAll on a never-created backlog is empty, not an
// error (first run).
func TestMissingDirIsEmpty(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "never"), fixedClock())
	got, bad, err := s.LoadAll()
	if err != nil || len(got) != 0 || len(bad) != 0 {
		t.Fatalf("missing dir should load empty, got=%d bad=%v err=%v", len(got), bad, err)
	}
}

// TestLoadAllIgnoresNoise checks the loader skips entries that are not task files:
// a subdirectory, a non-json file, an already-quarantined .bad- file, and a json
// file whose name is not a valid task id.
func TestLoadAllIgnoresNoise(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "queue"), nil) // nil clock → defaults to time.Now
	if err := s.Save(task.Task{ID: "t1", Status: task.Queued}); err != nil {
		t.Fatalf("save: %v", err)
	}
	_ = os.MkdirAll(filepath.Join(s.Dir(), "subdir"), 0o700)
	_ = os.WriteFile(filepath.Join(s.Dir(), "notes.txt"), []byte("hi"), 0o600)
	_ = os.WriteFile(filepath.Join(s.Dir(), "t9.json.bad-20200101T000000Z"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(s.Dir(), "garbage.json"), []byte(`{"schema":1,"task":{"id":"garbage"}}`), 0o600)

	got, bad, err := s.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 1 || got[0].ID != "t1" {
		t.Fatalf("only the valid task file should load, got %+v", got)
	}
	if len(bad) != 0 {
		t.Fatalf("noise must not be reported as bad task files, got %v", bad)
	}
}

// TestInvalidIDRefused checks an id that could escape the directory is refused by
// every path-taking method, so a hand-crafted task file cannot traverse out.
func TestInvalidIDRefused(t *testing.T) {
	s := newStore(t)
	bad := task.Task{ID: "../escape", Status: task.Queued}
	if err := s.Save(bad); err == nil {
		t.Fatal("Save should refuse a traversal id")
	}
	if err := s.Remove("../escape"); err == nil {
		t.Fatal("Remove should refuse a traversal id")
	}
	if err := s.Quarantine("a/b"); err == nil {
		t.Fatal("Quarantine should refuse a traversal id")
	}
}

// TestSaveUnwritableDir checks a Save into an unwritable backlog surfaces the
// error rather than panicking (a full or read-only disk).
func TestSaveUnwritableDir(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("chmod-based denial does not hold for root or on windows")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(dir, 0o500); err != nil { // read+execute, no write
		t.Fatal(err)
	}
	s := New(filepath.Join(dir, "queue"), fixedClock())
	// The parent is unwritable, so creating the queue subdir fails.
	if err := s.Save(task.Task{ID: "t1", Status: task.Queued}); err == nil {
		t.Fatal("Save into an unwritable parent should error")
	}
}
