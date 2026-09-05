package usage

import (
	"path/filepath"
	"strings"
	"testing"
)

// legitimateSettings is the largest settings file these tests claim anyone has,
// as a figure rather than as maxSettingsFile-minus-something so a cap narrowed to
// where it bites is caught. Sixty-four kibibytes is a long permissions allowlist
// with room over.
const legitimateSettings = 64 << 10

// paddedSettings is a settings file carrying a real status line, padded with a
// key nothing reads. Valid JSON all the way down and only its size wrong, so the
// only thing that can refuse it is the cap.
func paddedSettings(pad int) string {
	return `{"statusLine":{"type":"command","command":"echo hi"},"pad":"` +
		strings.Repeat("p", pad) + `"}`
}

// TestOversizedSettingsAreNotRead covers the read in this package whose PATH a
// peer chooses: StatusLine runs on every panel spawn against the panel's working
// directory, and that directory arrived on the socket as panel.create's Dir. So a
// peer that can put a file anywhere names the file the daemon reads, unbounded,
// once per spawn.
func TestOversizedSettingsAreNotRead(t *testing.T) {
	cfg := claudeHome(t)
	dir := t.TempDir()
	writeSettings(t, filepath.Join(dir, ".claude", "settings.json"), paddedSettings(maxSettingsFile))
	// A real user file behind it, so what is asserted is that the oversized one
	// was SKIPPED rather than that nothing was found at all.
	writeSettings(t, filepath.Join(cfg, "settings.json"),
		`{"statusLine":{"type":"command","command":"user-line"}}`)

	cmd, configured := StatusLine(dir)
	if !configured || cmd != "user-line" {
		t.Fatalf("StatusLine = (%q, %v), want the oversized project file skipped and the user's used", cmd, configured)
	}
}

// TestALargeSettingsFileIsStillRead is the other half: a long permissions
// allowlist is an ordinary file, and the status line in it still has to win.
func TestALargeSettingsFileIsStillRead(t *testing.T) {
	claudeHome(t)
	dir := t.TempDir()
	writeSettings(t, filepath.Join(dir, ".claude", "settings.json"), paddedSettings(legitimateSettings))

	cmd, configured := StatusLine(dir)
	if !configured || cmd != "echo hi" {
		t.Fatalf("StatusLine = (%q, %v), want a %d-byte settings file read", cmd, configured, legitimateSettings)
	}
}
