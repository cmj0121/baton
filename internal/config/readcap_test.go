package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legitimateConfig is the largest config file these tests claim anyone writes,
// as a figure rather than as maxConfigBytes-minus-something so a cap narrowed to
// where it bites is caught. Sixty-four kibibytes is already an agents map with a
// hundred profiles in it; Load's own comment calls the real file "a few
// kilobytes".
const legitimateConfig = 64 << 10

// writeConfig puts body at path and points the loader at it through $HOME.
func writeConfig(t *testing.T, name, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".baton")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAnOversizedConfigIsRefusedNotRead: the SIGHUP that re-reads this file
// arrives as a server.reload over the control socket, so how much the daemon
// allocates here is a thing a peer can ask for again and again — and Load
// unmarshals the same bytes twice on top of the read.
//
// The refusal degrades exactly as a syntax error already does: every caller falls
// back to the built-in defaults, so an unreadable file costs the operator their
// settings rather than the fleet its daemon.
func TestAnOversizedConfigIsRefusedNotRead(t *testing.T) {
	// The overflow is a COMMENT, and that is the whole design of this input. A
	// comment truncated anywhere is still a valid comment, so a reader that
	// bounded the read and then simply parsed what it got would parse this
	// happily — and apply a config it never finished reading, from a file it
	// cannot know the rest of. That is the silent truncation the explicit length
	// test exists to refuse, and it is why the LimitReader alone is not the cap.
	writeConfig(t, "config", "panel:\n  shell: /bin/zsh\n#"+strings.Repeat("p", maxConfigBytes)+"\n")
	if c, err := Load(); err == nil {
		t.Fatalf("a config past the cap was parsed and applied: shell = %q", c.Panel.Shell)
	}
}

func TestAnOversizedTUIConfigIsRefusedNotRead(t *testing.T) {
	writeConfig(t, "TUI.yaml", "#"+strings.Repeat("p", maxConfigBytes)+"\n")
	if _, err := LoadTUI(); err == nil {
		t.Fatal("a TUI config past the cap was parsed")
	}
}

// TestAnElaborateConfigStillLoads is the other half: a file far larger than
// anybody writes must still load, or the cap has taken the operator's settings to
// protect them from nothing.
func TestAnElaborateConfigStillLoads(t *testing.T) {
	writeConfig(t, "config", "panel:\n  shell: /bin/zsh\ncomment: \""+strings.Repeat("p", legitimateConfig)+"\"\n")
	c, err := Load()
	if err != nil {
		t.Fatalf("a %d-byte config should load, got %v", legitimateConfig, err)
	}
	if c.Panel.Shell != "/bin/zsh" {
		t.Fatalf("shell = %q, want the configured one", c.Panel.Shell)
	}
}
