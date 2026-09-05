package paths_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/paths"
)

// mkfifo drops a named pipe with no writer — the shape whose open never returns.
// A platform that cannot make one skips the test rather than passing it silently.
func mkfifo(t *testing.T, path string) string {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo %s: %v", path, err)
	}
	return path
}

// withinASecond runs fn and fails if it has not returned by then. The bound is
// what the test is about: a FIFO with no writer parks a plain open FOREVER, so
// an assertion on the returned error alone would hang instead of failing, and a
// hung test reports as a timeout panic in whatever ran last.
func withinASecond(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s did not return within a second — the open is blocking on the FIFO", what)
	}
}

// TestOpenRegularRefusesAFifoInsteadOfBlocking is the case the helper exists for.
// The path is one a peer names (a panel's working directory) or one an agent
// writes into (its own work tree), so a FIFO there is one command to arrange, and
// a plain open of it parks the daemon's handler for good.
func TestOpenRegularRefusesAFifoInsteadOfBlocking(t *testing.T) {
	p := mkfifo(t, filepath.Join(t.TempDir(), "settings.json"))

	var err error
	withinASecond(t, "OpenRegular on a FIFO", func() {
		var f *os.File
		f, err = paths.OpenRegular(p)
		if f != nil {
			_ = f.Close()
		}
	})
	if err == nil {
		t.Fatal("OpenRegular on a FIFO = nil error, want a refusal")
	}
}

// TestOpenRegularFollowsALinkToAFifo is the same case one hop away, and it is the
// one that actually reaches baton: `git status` never lists an untracked FIFO,
// but it does list an untracked SYMLINK, so a link is how a FIFO gets a name the
// daemon will open.
func TestOpenRegularFollowsALinkToAFifo(t *testing.T) {
	dir := t.TempDir()
	fifo := mkfifo(t, filepath.Join(dir, "pipe"))
	link := filepath.Join(dir, "notes.txt")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	var err error
	withinASecond(t, "OpenRegular on a link to a FIFO", func() {
		var f *os.File
		f, err = paths.OpenRegular(link)
		if f != nil {
			_ = f.Close()
		}
	})
	if err == nil {
		t.Fatal("OpenRegular on a symlink to a FIFO = nil error, want a refusal")
	}
}

// TestOpenRegularReadsAPlainFile is the other half: the guard must not refuse the
// ordinary file every caller is actually after, and must hand back its contents.
func TestOpenRegularReadsAPlainFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	f, err := paths.OpenRegular(p)
	if err != nil {
		t.Fatalf("OpenRegular(%s) = %v, want a readable file", p, err)
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	if err != nil || string(got) != `{"ok":true}` {
		t.Fatalf("read back %q (%v), want the file's contents", got, err)
	}
}

// TestOpenRegularFollowsALinkToAPlainFile keeps the deliberate permissiveness
// honest: a settings file or an untracked file kept as a symlink is ordinary, and
// only the target's KIND is what this refuses on.
func TestOpenRegularFollowsALinkToAPlainFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	f, err := paths.OpenRegular(link)
	if err != nil {
		t.Fatalf("OpenRegular(link) = %v, want the target read through it", err)
	}
	_ = f.Close()
}

// TestOpenRegularRefusesADirectory covers the other non-file a joined path lands
// on — `<dir>/.claude/settings.json` created as a directory reads as a settings
// file baton cannot parse, not as a file it should try to.
func TestOpenRegularRefusesADirectory(t *testing.T) {
	d := filepath.Join(t.TempDir(), "settings.json")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := paths.OpenRegular(d)
	if err == nil {
		_ = f.Close()
		t.Fatal("OpenRegular on a directory = nil error, want a refusal")
	}
}

// TestOpenRegularReportsAMissingFileAsNotExist pins the error every caller reads
// as "there is no settings file here" rather than as a failure worth reporting.
func TestOpenRegularReportsAMissingFileAsNotExist(t *testing.T) {
	_, err := paths.OpenRegular(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("OpenRegular on a missing file = %v, want fs.ErrNotExist", err)
	}
}
