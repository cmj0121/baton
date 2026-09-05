package paths_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/paths"
)

// writeMode drops a file with an exact mode, defeating the process umask (which
// WriteFile's perm argument is masked by, so a 0666 request lands as 0644 under
// the usual 022 and the case under test would never be built).
func writeMode(t *testing.T, dir, name string, perm os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("-- lua\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	if err := os.Chmod(p, perm); err != nil {
		t.Fatalf("chmod %s: %v", p, err)
	}
	return p
}

// privateDir is a 0700 directory, the shape $HOME/.baton has.
func privateDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "priv")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return d
}

// mustOpen asserts OpenTrusted accepts a path, and closes what it hands back.
func mustOpen(t *testing.T, path string) {
	t.Helper()
	f, err := paths.OpenTrusted(path)
	if err != nil {
		t.Fatalf("OpenTrusted(%s) refused a file it should accept: %v", path, err)
	}
	_ = f.Close()
}

// mustRefuse asserts OpenTrusted refuses a path, and that the reason names want
// — a test that only checked "an error" would pass on the wrong error.
func mustRefuse(t *testing.T, path, want string) {
	t.Helper()
	f, err := paths.OpenTrusted(path)
	if err == nil {
		_ = f.Close()
		t.Fatalf("OpenTrusted(%s) accepted a file it must refuse", path)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("OpenTrusted(%s) refused for %q, want a reason mentioning %q", path, err, want)
	}
}

// TestOpenTrustedAcceptsAPrivateFile is the negative control: the ordinary
// $HOME/.baton/plug-in.lua shape must pass, or every other case here proves
// nothing but that the function refuses everything.
func TestOpenTrustedAcceptsAPrivateFile(t *testing.T) {
	mustOpen(t, writeMode(t, privateDir(t), "plug-in.lua", 0o600))
}

// TestOpenTrustedAcceptsAReadableFile pins the boundary the check deliberately
// does NOT police: a plugin the rest of the box can READ is not a plugin the
// rest of the box can CHANGE, and 0644 is what a file checked out of a dotfiles
// repository arrives as. Refusing it would break real setups for no gain.
func TestOpenTrustedAcceptsAReadableFile(t *testing.T) {
	mustOpen(t, writeMode(t, privateDir(t), "plug-in.lua", 0o644))
}

func TestOpenTrustedRefusesAWritableFile(t *testing.T) {
	dir := privateDir(t)
	mustRefuse(t, writeMode(t, dir, "world.lua", 0o666), "writable by group or other")
	mustRefuse(t, writeMode(t, dir, "group.lua", 0o660), "writable by group or other")
}

// TestOpenTrustedRefusesAWritableDirectory covers the case the file's own mode
// cannot defend: a 0600 file is still replaceable by anyone who may unlink it,
// so a 0777 parent hands the plugin to the whole box.
func TestOpenTrustedRefusesAWritableDirectory(t *testing.T) {
	open := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(open, 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	mustRefuse(t, writeMode(t, open, "plug-in.lua", 0o600), "not sticky")
}

// TestOpenTrustedAcceptsAStickyDirectory is why the directory rule is not just
// "any group/other write bit": /tmp is 1777 everywhere, and under the sticky bit
// only the owner may unlink, so the file cannot be swapped after all.
func TestOpenTrustedAcceptsAStickyDirectory(t *testing.T) {
	sticky := filepath.Join(t.TempDir(), "sticky")
	if err := os.Mkdir(sticky, 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(sticky, 0o777|os.ModeSticky); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if fi, err := os.Stat(sticky); err != nil || fi.Mode()&os.ModeSticky == 0 {
		t.Skipf("this filesystem will not hold the sticky bit (%v, %v)", fi, err)
	}
	mustOpen(t, writeMode(t, sticky, "plug-in.lua", 0o600))
}

// TestOpenTrustedFollowsASymlink is the dotfiles case: ~/.baton/plug-in.lua
// pointing into a repository is the ordinary way to keep a plugin, so the link
// is followed and judged by its target rather than refused for being a link.
func TestOpenTrustedFollowsASymlink(t *testing.T) {
	target := writeMode(t, privateDir(t), "real.lua", 0o600)
	link := filepath.Join(privateDir(t), "plug-in.lua")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	mustOpen(t, link)
}

// TestOpenTrustedRefusesASymlinkToAWritableFile is the other half of following
// links: a check that only lstat'ed the name would see a symlink's meaningless
// 0777 mode, or the link's own private directory, and wave the target through.
func TestOpenTrustedRefusesASymlinkToAWritableFile(t *testing.T) {
	target := writeMode(t, privateDir(t), "real.lua", 0o666)
	link := filepath.Join(privateDir(t), "plug-in.lua")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	mustRefuse(t, link, "writable by group or other")
}

// TestOpenTrustedRefusesASymlinkIntoAWritableDirectory catches the target's
// DIRECTORY rather than the target: a private link name pointing at a private
// file inside a 0777 directory, where the file is replaceable even though both
// ends look fine on their own.
func TestOpenTrustedRefusesASymlinkIntoAWritableDirectory(t *testing.T) {
	open := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(open, 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	target := writeMode(t, open, "real.lua", 0o600)
	link := filepath.Join(privateDir(t), "plug-in.lua")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	mustRefuse(t, link, "not sticky")
}

// TestOpenTrustedRefusesASymlinkInAWritableDirectory is the LINK's own
// directory, which the target tells you nothing about: both ends are private,
// but the link sits where anyone may unlink it, so another user re-points the
// name at code of their own and the target that passed is never read.
func TestOpenTrustedRefusesASymlinkInAWritableDirectory(t *testing.T) {
	target := writeMode(t, privateDir(t), "real.lua", 0o600)
	open := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(open, 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	link := filepath.Join(open, "plug-in.lua")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	mustRefuse(t, link, "not sticky")
}

// TestOpenTrustedRefusesADirectory keeps the caller from reaching Lua with a
// descriptor that cannot be read as a file.
func TestOpenTrustedRefusesADirectory(t *testing.T) {
	mustRefuse(t, privateDir(t), "not a regular file")
}

// TestOpenTrustedReportsAMissingFileAsNotExist pins the error the plugin loader
// reads to tell "no plugin here" (a clean no-op) from "a plugin baton will not
// run" (loud). Wrapping that would silently turn every fresh install into a
// refusal notice.
func TestOpenTrustedReportsAMissingFileAsNotExist(t *testing.T) {
	_, err := paths.OpenTrusted(filepath.Join(privateDir(t), "absent.lua"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file gave %v, want an fs.ErrNotExist", err)
	}
}

// TestOpenTrustedReadsTheDescriptorItVetted proves the returned handle is the
// one that passed, not a fresh open of the path — the caller must never re-open.
func TestOpenTrustedReadsTheDescriptorItVetted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unlink-while-open is a unix guarantee")
	}
	path := writeMode(t, privateDir(t), "plug-in.lua", 0o600)
	f, err := paths.OpenTrusted(path)
	if err != nil {
		t.Fatalf("OpenTrusted: %v", err)
	}
	defer func() { _ = f.Close() }()
	// Swap the file for a hostile one AFTER the verdict. A caller that re-opened
	// the path would run this; a caller reading the descriptor reads the original.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.WriteFile(path, []byte("SWAPPED"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, 16)
	n, _ := f.Read(got)
	if string(got[:n]) != "-- lua\n" {
		t.Fatalf("descriptor read %q, want the vetted original %q", got[:n], "-- lua\n")
	}
}
