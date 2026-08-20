package usage

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSettings drops a Claude Code settings file at path, creating its
// directory.
func writeSettings(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// claudeHome points CLAUDE_CONFIG_DIR at a fresh directory and returns it, so a
// test never reads the developer's own settings.
func claudeHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	return dir
}

func TestStatusLineFromUserSettings(t *testing.T) {
	cfg := claudeHome(t)
	writeSettings(t, filepath.Join(cfg, "settings.json"),
		`{"theme":"dark","statusLine":{"type":"command","command":"bash ~/.claude/statusline.sh"}}`)

	cmd, configured := StatusLine(t.TempDir())
	if !configured || cmd != "bash ~/.claude/statusline.sh" {
		t.Errorf("StatusLine = (%q, %v), want the user's command", cmd, configured)
	}
}

// A cockpit that ran the wrong status line would be running one the user had
// already overridden, so the narrowest file in force wins.
func TestStatusLinePrecedence(t *testing.T) {
	cfg := claudeHome(t)
	dir := t.TempDir()
	writeSettings(t, filepath.Join(cfg, "settings.json"), `{"statusLine":{"type":"command","command":"user"}}`)

	if cmd, _ := StatusLine(dir); cmd != "user" {
		t.Errorf("with only user settings: %q, want %q", cmd, "user")
	}
	writeSettings(t, filepath.Join(dir, ".claude", "settings.json"), `{"statusLine":{"type":"command","command":"project"}}`)
	if cmd, _ := StatusLine(dir); cmd != "project" {
		t.Errorf("project settings should outrank the user's: %q", cmd)
	}
	writeSettings(t, filepath.Join(dir, ".claude", "settings.local.json"), `{"statusLine":{"type":"command","command":"local"}}`)
	if cmd, _ := StatusLine(dir); cmd != "local" {
		t.Errorf("local settings should outrank the project's: %q", cmd)
	}
}

// A file that names a status line baton cannot reproduce still counts as one
// being configured. Falling through to a broader file would run something the
// user had already overridden.
func TestStatusLineUnreproducibleForm(t *testing.T) {
	cfg := claudeHome(t)
	dir := t.TempDir()
	writeSettings(t, filepath.Join(cfg, "settings.json"), `{"statusLine":{"type":"command","command":"user"}}`)
	writeSettings(t, filepath.Join(dir, ".claude", "settings.json"), `{"statusLine":{"type":"something-else"}}`)

	cmd, configured := StatusLine(dir)
	if cmd != "" || !configured {
		t.Errorf("StatusLine = (%q, %v), want (\"\", true) — configured but not reproducible", cmd, configured)
	}
}

func TestStatusLineAbsent(t *testing.T) {
	cfg := claudeHome(t)
	dir := t.TempDir()

	// No settings files at all.
	if cmd, configured := StatusLine(dir); cmd != "" || configured {
		t.Errorf("StatusLine with no settings = (%q, %v), want (\"\", false)", cmd, configured)
	}
	// Settings that simply do not mention a status line.
	writeSettings(t, filepath.Join(cfg, "settings.json"), `{"theme":"dark"}`)
	if _, configured := StatusLine(dir); configured {
		t.Error("settings without a statusLine key reported one as configured")
	}
	// A cockpit is in no position to adjudicate somebody's JSON: malformed reads as
	// absent rather than as a reason to guess.
	writeSettings(t, filepath.Join(dir, ".claude", "settings.json"), `{{{ not json`)
	if _, configured := StatusLine(dir); configured {
		t.Error("malformed settings reported a status line")
	}
	// A panel with no working directory resolves against the user's settings alone.
	if _, configured := StatusLine(""); configured {
		t.Error("an empty dir should not pick up the project's settings")
	}
}
