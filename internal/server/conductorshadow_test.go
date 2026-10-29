package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConductorWarnsAboutAShadowingPersonalSkill: a ~/.claude/skills/conduct
// outranks the workspace copy writeConductorFiles writes, so the daemon names it
// at Warn. A personal skill the conductor set does not include (drive) is no
// shadow and is not named.
func TestConductorWarnsAboutAShadowingPersonalSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"conduct", "drive"} {
		if err := os.MkdirAll(filepath.Join(home, ".claude", "skills", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{}
	logged := captureLog(t)
	s.warnShadowedSkills(t.TempDir())
	got := logged()

	shadow := filepath.Join(home, ".claude", "skills", "conduct")
	if !strings.Contains(got, shadow) || !strings.Contains(got, `"level":"warn"`) {
		t.Errorf("no Warn naming %s:\n%s", shadow, got)
	}
	if strings.Contains(got, filepath.Join(home, ".claude", "skills", "drive")) {
		t.Errorf("drive is not a conductor skill and shadows nothing:\n%s", got)
	}
}

// TestConductorShadowWarningIsOncePerRun: the workspace is rewritten on every
// spawn, respawn and reload; the same path is named the first time only.
func TestConductorShadowWarningIsOncePerRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills", "conduct"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	s.warnShadowedSkills(t.TempDir())
	logged := captureLog(t)
	s.warnShadowedSkills(t.TempDir())
	if got := logged(); strings.Contains(got, "shadows") {
		t.Errorf("a second write warned again:\n%s", got)
	}
}

// TestOpeningTheConductorWarnsOnceAboutAShadow runs the real workspace path, so
// it is the call in makeConductorWorkspace that is pinned, not just the helper:
// the first open names the personal copy, a second open in the same run does not.
func TestOpeningTheConductorWarnsOnceAboutAShadow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	shadow := filepath.Join(home, ".claude", "skills", "conduct")
	if err := os.MkdirAll(shadow, 0o700); err != nil {
		t.Fatal(err)
	}
	s := &Server{}

	logged := captureLog(t)
	if _, err := s.makeConductorWorkspace("c1"); err != nil {
		t.Fatal(err)
	}
	first := logged()
	if !strings.Contains(first, shadow) || !strings.Contains(first, "shadows") {
		t.Fatalf("opening the conductor did not name %s:\n%s", shadow, first)
	}
	if _, err := s.makeConductorWorkspace("c1"); err != nil {
		t.Fatal(err)
	}
	if again := strings.TrimPrefix(logged(), first); strings.Contains(again, "shadows") {
		t.Errorf("a second open warned again:\n%s", again)
	}
}

// TestConductorIsQuietWithNoPersonalSkills: the warning is not a fixture of
// every spawn.
func TestConductorIsQuietWithNoPersonalSkills(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logged := captureLog(t)
	(&Server{}).warnShadowedSkills(t.TempDir())
	if got := logged(); strings.Contains(got, "shadows") {
		t.Errorf("a shadow warning with no personal skills:\n%s", got)
	}
}
