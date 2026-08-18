package panellog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// at is a fixed instant, so the markers and the filename a test asserts on do
// not move with the clock.
var at = time.Date(2026, 8, 18, 15, 4, 5, 0, time.UTC)

// read returns a log file's whole contents, failing the test if it cannot.
func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestSlug covers the filename reduction: what survives, what collapses, and the
// two ways a name can end up with nothing usable in it.
func TestSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"claude", "claude"},
		{"claude · ~/work", "claude-work"},
		{"a/b/c", "a-b-c"},
		{"  spaced  name  ", "spaced-name"},
		{"tag_1.2", "tag_1.2"},
		{"…", "panel"},
		{"", "panel"},
		{"---", "panel"},
		{strings.Repeat("x", 200), strings.Repeat("x", maxSlug)},
	}
	for _, tt := range tests {
		if got := Slug(tt.in); got != tt.want {
			t.Errorf("Slug(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

// TestFileName checks the shape a log directory sorts by: date first, id last.
func TestFileName(t *testing.T) {
	if got, want := FileName("claude · ~/work", "7", at), "2026-08-18-claude-work-7.log"; got != want {
		t.Errorf("FileName = %q; want %q", got, want)
	}
}

// TestMaxBytes checks the mebibyte conversion and that an unset value takes the
// default rather than rolling on every write.
func TestMaxBytes(t *testing.T) {
	if got, want := MaxBytes(2), int64(2*mib); got != want {
		t.Errorf("MaxBytes(2) = %d; want %d", got, want)
	}
	for _, unset := range []int{0, -1} {
		if got, want := MaxBytes(unset), int64(DefaultMaxMB*mib); got != want {
			t.Errorf("MaxBytes(%d) = %d; want the default %d", unset, got, want)
		}
	}
}

// TestStartFlushesReplay is the behaviour the whole feature turns on: enabling
// logging writes what the panel had ALREADY produced, marked as un-timestamped,
// before the live stream begins.
func TestStartFlushesReplay(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "claude", "3", 0, at)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Start("claude", "/work", []byte("\x1b[32mearlier\x1b[0m\r\n"), at); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Write([]byte("live\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Close("logging stopped", at); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := read(t, s.Path())
	for _, want := range []string{
		"=== baton log · claude · /work ===",
		"=== logging started · 2026-08-18T15:04:05Z ===",
		"--- replay buffer:",
		"earlier\n",
		"=== live output follows · ",
		"live\n",
		"=== logging stopped · ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("escape sequences reached the file:\n%q", got)
	}
	if i, j := strings.Index(got, "earlier"), strings.Index(got, "live\n"); i < 0 || j < 0 || i > j {
		t.Errorf("the replay prefix must precede the live output:\n%s", got)
	}
}

// TestStartWithoutReplay covers the ordinary case of a panel that has produced
// nothing yet: no replay block is written at all, rather than an empty one.
func TestStartWithoutReplay(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "shell", "1", 0, at)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, replay := range [][]byte{nil, []byte("\x1b[2J \r\n")} {
		if err := s.Start("shell", "", replay, at); err != nil {
			t.Fatalf("Start: %v", err)
		}
	}
	if err := s.Close("logging stopped", at); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := read(t, s.Path()); strings.Contains(got, "replay buffer") {
		t.Errorf("an empty replay must not open a block:\n%s", got)
	}
}

// TestSuspendResumeAppends is the respawn contract: the previous run stays, and
// the new one opens under a marker of its own.
func TestSuspendResumeAppends(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "claude", "3", 0, at)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Start("claude", "/work", nil, at); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Write([]byte("first run\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Suspend("process exited (code 0)", at); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	// A suspended sink discards rather than failing: the daemon's output path has
	// no second check to make.
	if err := s.Write([]byte("into the void\n")); err != nil {
		t.Fatalf("Write while suspended: %v", err)
	}
	if err := s.Resume("session restarted", at.Add(time.Minute)); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := s.Resume("session restarted", at.Add(2*time.Minute)); err != nil {
		t.Fatalf("second Resume: %v", err)
	}
	if err := s.Write([]byte("second run\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Close("logging stopped", at); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := read(t, s.Path())
	if !strings.Contains(got, "first run\n") || !strings.Contains(got, "second run\n") {
		t.Errorf("a respawn must append, not truncate:\n%s", got)
	}
	if strings.Contains(got, "into the void") {
		t.Errorf("a suspended sink must discard:\n%s", got)
	}
	if n := strings.Count(got, "=== session restarted"); n != 1 {
		t.Errorf("session-restarted markers = %d; want exactly 1 (a resume of an open sink is a no-op)", n)
	}
	if n := strings.Count(got, "=== process exited (code 0)"); n != 1 {
		t.Errorf("exit markers = %d; want 1", n)
	}
}

// TestReopenAppends checks that logging a panel, stopping, and starting again
// keeps the earlier session — the file is opened in append mode, never truncated.
func TestReopenAppends(t *testing.T) {
	dir := t.TempDir()
	for _, line := range []string{"session one\n", "session two\n"} {
		s, err := Open(dir, "claude", "3", 0, at)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := s.Start("claude", "/work", nil, at); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := s.Write([]byte(line)); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := s.Close("logging stopped", at); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	got := read(t, filepath.Join(dir, FileName("claude", "3", at)))
	if !strings.Contains(got, "session one") || !strings.Contains(got, "session two") {
		t.Errorf("reopening must append:\n%s", got)
	}
}

// TestRollKeepsOnePreviousGeneration checks the disk bound: the log rolls to .1
// and no further, and the fresh file says where it came from.
func TestRollKeepsOnePreviousGeneration(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "build", "9", 64, at) // roll almost immediately
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; i < 12; i++ {
		if err := s.Write([]byte(strings.Repeat("x", 32) + "\n")); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if err := s.Close("logging stopped", at); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 2 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("generations kept = %v; want exactly the log and its .1", names)
	}
	if _, err := os.Stat(s.Path() + ".1"); err != nil {
		t.Errorf("the previous generation should be .1: %v", err)
	}
	if got := read(t, s.Path()); !strings.Contains(got, "=== continued from ") {
		t.Errorf("a rolled log should say where it came from:\n%s", got)
	}
}

// TestCloseIsIdempotent covers the paths that all end in a close — switching
// logging off, the panel exiting, the daemon shutting down — overlapping.
func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "shell", "1", 0, at)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close("logging stopped", at); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close("daemon shutting down", at); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := s.Suspend("process exited", at); err != nil {
		t.Fatalf("Suspend after Close: %v", err)
	}
	got := read(t, s.Path())
	if n := strings.Count(got, "=== logging stopped"); n != 1 {
		t.Errorf("logging-stopped markers = %d; want only the first close to have written one", n)
	}
	if strings.Contains(got, "daemon shutting down") || strings.Contains(got, "process exited") {
		t.Errorf("a closed sink must write nothing more:\n%s", got)
	}
}

// TestClosePreservesPartialSequence checks that bytes held as an incomplete
// escape when the stream ends are written as text rather than lost.
func TestClosePreservesPartialSequence(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "shell", "1", 0, at)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Write([]byte("tail\x1b[3")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Close("logging stopped", at); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := read(t, s.Path()); !strings.Contains(got, "\x1b[3") {
		t.Errorf("a partial sequence at the end should survive as text:\n%q", got)
	}
}

// TestOpenRejectsUnusableDir covers the two ways the destination can be no
// destination at all — the case an auto-logging profile has to survive.
func TestOpenRejectsUnusableDir(t *testing.T) {
	if _, err := Open("  ", "shell", "1", 0, at); err == nil {
		t.Errorf("an empty log-dir should refuse to open")
	}
	blocked := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := Open(filepath.Join(blocked, "logs"), "shell", "1", 0, at); err == nil {
		t.Errorf("a log-dir under a regular file should refuse to open")
	}
}

// TestWriteOfPureEscapes checks that a chunk carrying nothing but sequences adds
// nothing to the file, rather than a stream of empty lines.
func TestWriteOfPureEscapes(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "shell", "1", 0, at)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	before, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if err := s.Write([]byte("\x1b[1;1H\x1b[2K")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	after, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if after.Size() != before.Size() {
		t.Errorf("a chunk of pure escapes grew the file by %d bytes", after.Size()-before.Size())
	}
	if err := s.Close("logging stopped", at); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
