package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestConductorFile checks the operator-brief path resolves under the home dir.
func TestConductorFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".baton", "CONDUCTOR.md")
	if got := ConductorFile(); got != want {
		t.Fatalf("ConductorFile() = %q, want %q", got, want)
	}
}

// TestConductorWorkspaceIsFixedPerSocket checks the workspace is one stable
// directory per socket — the whole point of the change — and a different one for
// a different socket.
func TestConductorWorkspaceIsFixedPerSocket(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base) // conductorBase becomes <base>/baton

	ws, err := ConductorWorkspace("/run/baton.sock")
	if err != nil {
		t.Fatalf("ConductorWorkspace: %v", err)
	}
	fi, err := os.Stat(ws)
	if err != nil || !fi.IsDir() {
		t.Fatalf("workspace %q is not a directory (err %v)", ws, err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("workspace %q is reachable by group or other (%04o)", ws, perm)
	}
	if root := filepath.Join(base, "baton"); !strings.HasPrefix(ws, root) {
		t.Fatalf("workspace %q should sit under the temporary base %q", ws, root)
	}

	again, err := ConductorWorkspace("/run/baton.sock")
	if err != nil {
		t.Fatalf("second ConductorWorkspace: %v", err)
	}
	if again != ws {
		t.Fatalf("the same socket gave two workspaces: %q then %q", ws, again)
	}

	other, err := ConductorWorkspace("/run/other.sock")
	if err != nil {
		t.Fatalf("other ConductorWorkspace: %v", err)
	}
	if other == ws {
		t.Fatalf("two sockets share the workspace %q", ws)
	}
}

// TestConductorWorkspaceSurvivesSameBoot checks what a restart is supposed to
// keep: whatever the previous conductor left in the workspace is still there.
func TestConductorWorkspaceSurvivesSameBoot(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	stubBoot(t, time.Unix(1_700_000_000, 0))

	ws, err := ConductorWorkspace("/run/baton.sock")
	if err != nil {
		t.Fatalf("ConductorWorkspace: %v", err)
	}
	memory := filepath.Join(ws, "settings.local.json")
	if err := os.WriteFile(memory, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	if _, err := ConductorWorkspace("/run/baton.sock"); err != nil {
		t.Fatalf("second ConductorWorkspace: %v", err)
	}
	if _, err := os.Stat(memory); err != nil {
		t.Fatalf("the workspace should have survived within one boot: %v", err)
	}
}

// TestConductorWorkspaceClearedOnReboot checks the other half of the contract: a
// host that has rebooted since the stamp was written gets an empty workspace.
func TestConductorWorkspaceClearedOnReboot(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	stubBoot(t, time.Unix(1_700_000_000, 0))

	ws, err := ConductorWorkspace("/run/baton.sock")
	if err != nil {
		t.Fatalf("ConductorWorkspace: %v", err)
	}
	memory := filepath.Join(ws, "settings.local.json")
	if err := os.WriteFile(memory, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	stubBoot(t, time.Unix(1_700_090_000, 0)) // the machine rebooted
	again, err := ConductorWorkspace("/run/baton.sock")
	if err != nil {
		t.Fatalf("ConductorWorkspace after reboot: %v", err)
	}
	if again != ws {
		t.Fatalf("the path should not move across a reboot: %q then %q", ws, again)
	}
	if _, err := os.Stat(memory); !os.IsNotExist(err) {
		t.Fatalf("the workspace should have been cleared by the reboot, stat err = %v", err)
	}
	if _, err := os.Stat(ws); err != nil {
		t.Fatalf("the workspace should have been rebuilt empty: %v", err)
	}
}

// TestConductorWorkspaceToleratesBootSkew checks a boot time that drifted by a
// second — the clock being adjusted, not a reboot — keeps the workspace.
func TestConductorWorkspaceToleratesBootSkew(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	stubBoot(t, time.Unix(1_700_000_000, 0))

	ws, err := ConductorWorkspace("/run/baton.sock")
	if err != nil {
		t.Fatalf("ConductorWorkspace: %v", err)
	}
	memory := filepath.Join(ws, "kept")
	if err := os.WriteFile(memory, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	stubBoot(t, time.Unix(1_700_000_002, 0)) // within bootSkew
	if _, err := ConductorWorkspace("/run/baton.sock"); err != nil {
		t.Fatalf("ConductorWorkspace: %v", err)
	}
	if _, err := os.Stat(memory); err != nil {
		t.Fatalf("a two-second drift is not a reboot: %v", err)
	}
}

// TestConductorWorkspaceRefusesAnUnsafeBase checks the guard that replaced
// MkdirTemp's unguessable name: a base reachable by group or other is refused
// rather than used.
func TestConductorWorkspaceRefusesAnUnsafeBase(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", root)
	base := filepath.Join(root, "baton")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	if err := os.Chmod(base, 0o755); err != nil { // MkdirAll applies the umask
		t.Fatalf("chmod base: %v", err)
	}

	if _, err := ConductorWorkspace("/run/baton.sock"); err == nil {
		t.Fatal("a world-readable base should have been refused")
	}
}

// TestRemoveConductorWorkspace checks the reset escape hatch removes both the
// workspace and the stamp that decides whether it survives.
func TestRemoveConductorWorkspace(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	stubBoot(t, time.Unix(1_700_000_000, 0))

	ws, err := ConductorWorkspace("/run/baton.sock")
	if err != nil {
		t.Fatalf("ConductorWorkspace: %v", err)
	}
	if err := RemoveConductorWorkspace(ws); err != nil {
		t.Fatalf("RemoveConductorWorkspace: %v", err)
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Fatalf("workspace still there, stat err = %v", err)
	}
	if _, err := os.Stat(ConductorStampFile(ws)); !os.IsNotExist(err) {
		t.Fatalf("stamp still there, stat err = %v", err)
	}
}

// TestLegacyConductorWorkspaces checks the sweep finds the throwaway directories
// older versions leaked, and never the one in use.
func TestLegacyConductorWorkspaces(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", root)

	ws, err := ConductorWorkspace("/run/baton.sock")
	if err != nil {
		t.Fatalf("ConductorWorkspace: %v", err)
	}
	legacy := filepath.Join(root, "baton", "conductor-1241177684")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}

	found := LegacyConductorWorkspaces(ws)
	if len(found) != 1 || found[0] != legacy {
		t.Fatalf("LegacyConductorWorkspaces = %v, want [%s]", found, legacy)
	}
}

// stubBoot pins the host boot time for a test, restoring the real one after it.
func stubBoot(t *testing.T, at time.Time) {
	t.Helper()
	prev := hostBootTime
	hostBootTime = func() (time.Time, error) { return at, nil }
	t.Cleanup(func() { hostBootTime = prev })
}
