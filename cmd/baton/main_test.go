package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/limits"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/server"
)

// testDaemonChildEnv marks a daemon child that THIS TEST BINARY fork-exec'd, as
// distinct from the production daemonEnv (BATON_DAEMON). Gating TestMain on a
// dedicated, test-only var keeps the suite from hijacking itself into the daemon
// branch when it merely runs inside a real baton session (where BATON_DAEMON=1 is
// legitimately set). TestStartStopDaemon sets it before startDaemon so the child
// inherits it.
const testDaemonChildEnv = "BATON_TEST_DAEMON_CHILD"

// TestMain lets the test binary stand in for the real daemon: when a daemon test
// re-execs it with testDaemonChildEnv=1 (riding alongside the production
// BATON_DAEMON the way startDaemon launches a daemon child), it runs the server
// loop and exits instead of running the test suite. This makes the fork-exec
// orchestration testable against a real, working child process.
func TestMain(m *testing.M) {
	if os.Getenv(testDaemonChildEnv) == "1" {
		// The child logs to a file beside its socket; the global logger must be
		// initialised before runServer uses it.
		logPath := filepath.Join(filepath.Dir(os.Getenv("BATON_SOCK")), "daemon.log")
		_ = setupLogger(0, logPath)
		if err := runServer(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func boolPtr(b bool) *bool { return &b }

func TestResolveLogPath(t *testing.T) {
	if got := resolveLogPath("/tmp/explicit.log"); got != "/tmp/explicit.log" {
		t.Fatalf("explicit flag: got %q, want /tmp/explicit.log", got)
	}
	if got := resolveLogPath(""); got != paths.LogFile() {
		t.Fatalf("default: got %q, want %q", got, paths.LogFile())
	}
}

func TestClaimDaemonRole(t *testing.T) {
	t.Setenv(daemonEnv, "")
	if claimDaemonRole() {
		t.Fatal("empty env should not be a daemon child")
	}
	t.Setenv(daemonEnv, "0")
	if claimDaemonRole() {
		t.Fatal("env=0 should not be a daemon child")
	}
	t.Setenv(daemonEnv, "1")
	if !claimDaemonRole() {
		t.Fatal("env=1 should be a daemon child")
	}
}

// TestClaimDaemonRoleTakesTheMarker: the marker must not survive being read. The
// daemon's environment is inherited by every panel it spawns, and by everything
// run inside one — so a marker left in place would make the next `baton` typed
// in a panel try to become a second daemon instead of attaching a cockpit.
func TestClaimDaemonRoleTakesTheMarker(t *testing.T) {
	t.Setenv(daemonEnv, "1")

	if !claimDaemonRole() {
		t.Fatal("the daemon child should claim the role")
	}
	if v, set := os.LookupEnv(daemonEnv); set {
		t.Fatalf("the marker outlived the claim as %q; panels would inherit it", v)
	}
	if claimDaemonRole() {
		t.Fatal("the role is claimed once; a second read must not see it")
	}
}

// TestDaemonEnvironIsNotInheritedTwice: the marker baton sets on the daemon child
// is the same one the child clears, so a panel's environment — built from the
// daemon's — carries the socket but not the role.
func TestDaemonEnvironIsNotInheritedTwice(t *testing.T) {
	t.Setenv(daemonEnv, "1")
	claimDaemonRole()

	env := daemonEnviron(os.Environ(), "/run/baton.sock", "")
	var markers int
	for _, kv := range env {
		if strings.HasPrefix(kv, daemonEnv+"=") {
			markers++
		}
	}
	if markers != 1 {
		t.Fatalf("the child env should carry exactly one %s, got %d: %v", daemonEnv, markers, env)
	}
}

func TestParsePid(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{"valid", "1234", 1234, false},
		{"valid with surrounding whitespace", "  4321\n", 4321, false},
		{"valid with trailing newline", "7\n", 7, false},
		{"zero rejected", "0", 0, true},
		{"negative rejected", "-1", 0, true},
		{"non-numeric rejected", "not-a-pid", 0, true},
		{"empty rejected", "", 0, true},
		{"blank rejected", "   \n\t ", 0, true},
		{"float rejected", "12.5", 0, true},
		{"trailing garbage rejected", "123abc", 0, true},
		{"huge in-range accepted", strconv.Itoa(1 << 30), 1 << 30, false},
		{"overflow rejected", "999999999999999999999999", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePid(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parsePid(%q): expected error, got pid %d", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePid(%q): unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("parsePid(%q): got %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestReadPidFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baton.pid")

	// Missing file: error.
	if _, err := readPidFile(path); err == nil {
		t.Fatal("readPidFile on a missing file should error")
	}

	// writePidFile -> readPidFile round-trips a valid PID.
	if err := writePidFile(path, 4242); err != nil {
		t.Fatalf("writePidFile: %v", err)
	}
	got, err := readPidFile(path)
	if err != nil {
		t.Fatalf("readPidFile: %v", err)
	}
	if got != 4242 {
		t.Fatalf("round-trip pid: got %d, want 4242", got)
	}

	// File holding garbage is rejected by the validation in parsePid.
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPidFile(path); err == nil {
		t.Fatal("readPidFile should reject garbage contents")
	}

	// A zero PID on disk is rejected too.
	if err := os.WriteFile(path, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPidFile(path); err == nil {
		t.Fatal("readPidFile should reject a zero pid")
	}
}

func TestWritePidFilePerms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baton.pid")
	if err := writePidFile(path, 99); err != nil {
		t.Fatalf("writePidFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("pid file perms: got %o, want 600", perm)
	}
}

func TestDaemonArgs(t *testing.T) {
	tests := []struct {
		verbose int
		want    []string
	}{
		{0, []string{"--log", "/x.log"}},
		{1, []string{"--log", "/x.log", "-v"}},
		{2, []string{"--log", "/x.log", "-v", "-v"}},
	}
	for _, tt := range tests {
		got := daemonArgs("/x.log", tt.verbose)
		if len(got) != len(tt.want) {
			t.Fatalf("daemonArgs(v=%d): got %v, want %v", tt.verbose, got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Fatalf("daemonArgs(v=%d)[%d]: got %q, want %q", tt.verbose, i, got[i], tt.want[i])
			}
		}
	}
}

func TestDaemonEnviron(t *testing.T) {
	base := []string{"PATH=/bin", "HOME=/home/me"}

	// Without a plugin path: base + daemon marker + socket, no BATON_PLUGIN.
	env := daemonEnviron(base, "/run/baton.sock", "")
	if !hasEnv(env, "PATH=/bin") || !hasEnv(env, "HOME=/home/me") {
		t.Fatalf("daemonEnviron dropped the base environment: %v", env)
	}
	if !hasEnv(env, daemonEnv+"=1") {
		t.Fatalf("daemonEnviron missing %s=1: %v", daemonEnv, env)
	}
	if !hasEnv(env, "BATON_SOCK=/run/baton.sock") {
		t.Fatalf("daemonEnviron missing BATON_SOCK: %v", env)
	}
	if envValue(env, "BATON_PLUGIN") != "" {
		t.Fatalf("daemonEnviron should not set BATON_PLUGIN when plugin path is empty: %v", env)
	}

	// With a plugin path: BATON_PLUGIN is carried across.
	env = daemonEnviron(base, "/run/baton.sock", "/cfg/plug.lua")
	if envValue(env, "BATON_PLUGIN") != "/cfg/plug.lua" {
		t.Fatalf("daemonEnviron should carry BATON_PLUGIN: %v", env)
	}

	// The base slice must not be mutated (append-aliasing guard).
	if len(base) != 2 {
		t.Fatalf("daemonEnviron mutated the base environment: %v", base)
	}
}

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			return e[len(prefix):]
		}
	}
	return ""
}

// restartModel is a minimal tea.Model that reports a fixed RestartRequested.
type restartModel struct{ restart bool }

func (m restartModel) Init() tea.Cmd                       { return nil }
func (m restartModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m restartModel) View() string                        { return "" }
func (m restartModel) RestartRequested() bool              { return m.restart }

// plainModel is a tea.Model without a RestartRequested method.
type plainModel struct{}

func (plainModel) Init() tea.Cmd                       { return nil }
func (plainModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return plainModel{}, nil }
func (plainModel) View() string                        { return "" }

func TestRestartRequested(t *testing.T) {
	if restartRequested(plainModel{}) {
		t.Fatal("a model without RestartRequested should mean a normal exit")
	}
	if restartRequested(restartModel{restart: false}) {
		t.Fatal("RestartRequested()=false should mean a normal exit")
	}
	if !restartRequested(restartModel{restart: true}) {
		t.Fatal("RestartRequested()=true should request a restart")
	}
	if restartRequested(nil) {
		t.Fatal("a nil model should mean a normal exit")
	}
}

func TestReloadableSettings(t *testing.T) {
	t.Run("empty config uses defaults", func(t *testing.T) {
		rc := reloadableSettings(config.Config{})
		if rc.settings.AllowNameConflict {
			t.Error("unset allow-name-conflict should default to false (strict names)")
		}
		if rc.settings.DefaultDir != "" {
			t.Errorf("defaultDir: got %q, want empty", rc.settings.DefaultDir)
		}
		if rc.settings.ReplayBytes != 0 {
			t.Errorf("replayBytes: got %d, want 0 (server default)", rc.settings.ReplayBytes)
		}
		if rc.settings.DiffCommand != "" {
			t.Errorf("diffCommand should be empty: %+v", rc)
		}
		if rc.settings.Editor != "" {
			t.Errorf("editor should be empty: %+v", rc)
		}
		if rc.settings.WorktreeDir != "" {
			t.Errorf("worktreeDir should be empty: %+v", rc)
		}
	})

	t.Run("full config maps every field", func(t *testing.T) {
		cfg := config.Config{
			Settings: config.Settings{AllowNameConflict: boolPtr(true)},
			Panel: config.PanelDefaults{
				Workdir:     "/work",
				ReplayKB:    8,
				DiffCommand: "delta",
				Editor:      "vim",
				WorktreeDir: "/wt",
			},
		}
		rc := reloadableSettings(cfg)
		if !rc.settings.AllowNameConflict {
			t.Error("allow-name-conflict=true should map through")
		}
		if rc.settings.DefaultDir != "/work" {
			t.Errorf("defaultDir: got %q, want /work", rc.settings.DefaultDir)
		}
		if rc.settings.ReplayBytes != 8*1024 {
			t.Errorf("replayBytes: got %d, want %d", rc.settings.ReplayBytes, 8*1024)
		}
		if rc.settings.DiffCommand != "delta" {
			t.Errorf("diffCommand: got %q, want delta", rc.settings.DiffCommand)
		}
		if rc.settings.Editor != "vim" {
			t.Errorf("editor: got %q, want vim", rc.settings.Editor)
		}
		if rc.settings.WorktreeDir != "/wt" {
			t.Errorf("worktreeDir: got %q, want /wt", rc.settings.WorktreeDir)
		}
	})

	t.Run("explicit allow-name-conflict false is honoured", func(t *testing.T) {
		rc := reloadableSettings(config.Config{
			Settings: config.Settings{AllowNameConflict: boolPtr(false)},
		})
		if rc.settings.AllowNameConflict {
			t.Error("explicit false should stay false")
		}
	})

	t.Run("zero replay-kb keeps the server default", func(t *testing.T) {
		rc := reloadableSettings(config.Config{Panel: config.PanelDefaults{ReplayKB: 0}})
		if rc.settings.ReplayBytes != 0 {
			t.Errorf("replayBytes: got %d, want 0", rc.settings.ReplayBytes)
		}
	})

	t.Run("negative replay-kb is treated as unset", func(t *testing.T) {
		rc := reloadableSettings(config.Config{Panel: config.PanelDefaults{ReplayKB: -5}})
		if rc.settings.ReplayBytes != 0 {
			t.Errorf("replayBytes: got %d, want 0 for a non-positive replay-kb", rc.settings.ReplayBytes)
		}
	})
}

func TestBuildServerOptions(t *testing.T) {
	// Without a replay size, the replay option is omitted (server keeps its default).
	base := buildServerOptions(reloadable{settings: server.Settings{DefaultDir: "/work"}}, "/state.json")
	withReplay := buildServerOptions(reloadable{settings: server.Settings{DefaultDir: "/work", ReplayBytes: 4096}}, "/state.json")
	if len(withReplay) != len(base)+1 {
		t.Fatalf("a positive replayBytes should add exactly one option: base=%d withReplay=%d", len(base), len(withReplay))
	}
	if len(base) == 0 {
		t.Fatal("buildServerOptions should always include the baseline options")
	}
}

// TestRunServerOn drives the real server loop in-process on a temp socket, then
// closes the listener to make Serve return on its own — exercising runServerOn
// without forking a daemon or hitting the os.Exit signal path.
func TestRunServerOn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)

	sock := filepath.Join(t.TempDir(), "baton.sock")
	t.Setenv("BATON_SOCK", sock)

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- runServerOn(ln, sock, loadServerBoot(sock)) }()

	// Wait for the loop to be serving before signalling it. This used to wait on
	// the PID file, which runServerOn wrote first; the pid is published from above
	// the bind now, by loadServerBoot, so by the time this test has a boot to hand
	// runServerOn the file is already there and says nothing about the loop. An
	// answered hello does: it comes from Serve, which starts after the signal
	// handlers below are installed, so the SIGHUP cannot arrive while its default
	// disposition is still "kill this process".
	waitServing(t, sock)

	// A SIGHUP exercises the reload goroutine (config + plugin re-read).
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Closing the listener makes Serve return, so runServerOn returns nil.
	_ = ln.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServerOn returned %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runServerOn did not return after the listener closed")
	}
}

// TestStartStopDaemon forks a real daemon child (the test binary re-exec'd with
// BATON_DAEMON=1, which lands in runServer) and then force-stops it, covering the
// startDaemon fork-exec path and stopDaemon's signalling path end-to-end.
func TestStartStopDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping daemon fork-exec in -short")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	// Mark the fork-exec'd child so its TestMain runs the server loop. startDaemon
	// builds the child env from os.Environ(), so this set var is inherited.
	t.Setenv(testDaemonChildEnv, "1")

	sock := filepath.Join(t.TempDir(), "baton.sock")
	t.Setenv("BATON_SOCK", sock)
	logPath := filepath.Join(home, "baton.log")

	if err := startDaemon(0, logPath, ""); err != nil {
		t.Fatalf("startDaemon: %v", err)
	}
	if !alive(sock) {
		t.Fatal("daemon should be alive after startDaemon")
	}

	// A second startDaemon is a no-op while one is already alive.
	if err := startDaemon(0, logPath, ""); err != nil {
		t.Fatalf("startDaemon (already running): %v", err)
	}

	if err := stopDaemon(sock); err != nil {
		t.Fatalf("stopDaemon: %v", err)
	}
	if alive(sock) {
		t.Fatal("daemon should be stopped after stopDaemon")
	}
}

// TestRunClientDialError covers runClient's fail-fast path: with no daemon on
// the socket, the first Dial fails and runClient returns the wrapped error
// without ever launching a TUI.
func TestRunClientDialError(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "absent.sock")
	t.Setenv("BATON_SOCK", sock)
	if err := runClient(0, "/tmp/baton.log", ""); err == nil {
		t.Fatal("runClient should fail to attach when no daemon is listening")
	}
}

// TestAttachStartFailure covers attach's force + startDaemon paths: with no
// daemon alive, the force branch's stopDaemon is a no-op, then startDaemon fails
// because the socket's directory cannot be created, so attach returns that error
// before it would ever reach runClient (no TUI is launched).
func TestAttachStartFailure(t *testing.T) {
	// A regular file standing where the socket's parent directory should be makes
	// paths.EnsureDir(sock) fail inside startDaemon.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(blocker, "baton.sock")
	t.Setenv("BATON_SOCK", sock)

	if err := attach(0, "/tmp/baton.log", "", true); err == nil {
		t.Fatal("attach should fail when the daemon cannot be started")
	}
}

func TestSetupLogger(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "logs", "baton.log")
	for _, v := range []int{0, 1, 2} {
		if err := setupLogger(v, logPath); err != nil {
			t.Fatalf("setupLogger(%d): %v", v, err)
		}
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("log file not created: %v", err)
	}

	// A log path under a regular file cannot have its directory created.
	bad := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setupLogger(0, filepath.Join(bad, "nested.log")); err == nil {
		t.Fatal("setupLogger should fail when the log dir cannot be created")
	}
}

func TestAlive(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "baton.sock")
	if alive(sock) {
		t.Fatal("a missing socket should not be alive")
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	if !alive(sock) {
		t.Fatal("a listening socket should be alive")
	}
}

func TestClearStaleSocket(t *testing.T) {
	dir := t.TempDir()

	// Missing socket: nothing to do.
	if err := clearStaleSocket(filepath.Join(dir, "missing.sock")); err != nil {
		t.Fatalf("missing socket: %v", err)
	}

	// A leftover (dead) socket file is removed, along with its orphaned PID file
	// (a SIGKILLed daemon leaves both behind).
	stale := filepath.Join(dir, "stale.sock")
	stalePid := paths.PidFile(stale)
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stalePid, []byte("99999"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearStaleSocket(stale); err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale socket should have been removed")
	}
	if _, err := os.Stat(stalePid); !os.IsNotExist(err) {
		t.Fatal("orphaned PID file should have been removed")
	}

	// A live socket is refused.
	live := filepath.Join(dir, "live.sock")
	ln, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if err := clearStaleSocket(live); err == nil {
		t.Fatal("clearStaleSocket should refuse a live socket")
	}
}

func TestWaitFor(t *testing.T) {
	if !waitFor(func() bool { return true }, 3, time.Millisecond) {
		t.Fatal("waitFor should succeed when the condition holds")
	}
	calls := 0
	if waitFor(func() bool { calls++; return false }, 3, time.Millisecond) {
		t.Fatal("waitFor should fail when the condition never holds")
	}
	if calls < 3 {
		t.Fatalf("expected at least 3 polls, got %d", calls)
	}
}

func TestStopDaemon(t *testing.T) {
	dir := t.TempDir()

	// No server alive: a no-op (and tidies a stale socket).
	if err := stopDaemon(filepath.Join(dir, "none.sock")); err != nil {
		t.Fatalf("stopDaemon with no server: %v", err)
	}

	// Alive but no PID file: cannot locate the daemon.
	sock := filepath.Join(dir, "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if err := stopDaemon(sock); err == nil {
		t.Fatal("stopDaemon should fail without a PID file")
	}

	// Alive with a non-numeric PID file: parse error.
	if err := os.WriteFile(paths.PidFile(sock), []byte("not-a-pid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stopDaemon(sock); err == nil {
		t.Fatal("stopDaemon should fail on an unparseable PID")
	}

	// Alive with a PID that cannot be signalled (already-reaped): signal error.
	if err := os.WriteFile(paths.PidFile(sock), []byte(strconv.Itoa(1<<30)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stopDaemon(sock); err == nil {
		t.Fatal("stopDaemon should fail signalling a non-existent PID")
	}
}

// TestAgentLimitsProjection checks what reaches the server: the caps keyed by
// profile name and nothing else. A profile that sets no caps of its own is left
// out entirely, so the server's lookup misses and falls through to the
// fleet-wide limits instead of hitting an empty override.
func TestAgentLimitsProjection(t *testing.T) {
	profiles := map[string]config.AgentProfile{
		"heavy": {Command: "claude", Limits: limits.Limits{Memory: "16Gi"}},
		"plain": {Command: "other"},
	}
	got := agentLimits(profiles)
	if len(got) != 1 {
		t.Fatalf("only the capped profile should travel, got %v", got)
	}
	if got["heavy"].Memory != "16Gi" {
		t.Errorf(`agentLimits["heavy"] = %+v`, got["heavy"])
	}
	if agentLimits(nil) != nil {
		t.Error("no profiles should project to a nil table, not an empty map")
	}
	if agentLimits(map[string]config.AgentProfile{"plain": {Command: "other"}}) != nil {
		t.Error("profiles with no caps should project to a nil table")
	}
}

// TestReloadableSettingsCarriesLimits is the config→server hand-off for the
// resource policy: both layers must land on the Settings the reload path swaps in.
func TestReloadableSettingsCarriesLimits(t *testing.T) {
	cfg := config.Config{Panel: config.PanelDefaults{
		Limits: limits.Limits{CPUs: "2", Memory: "4Gi"},
		Agents: map[string]config.AgentProfile{"heavy": {Command: "claude", Limits: limits.Limits{Memory: "16Gi"}}},
	}}
	rc := reloadableSettings(cfg)

	if rc.settings.Limits != (limits.Limits{CPUs: "2", Memory: "4Gi"}) {
		t.Errorf("the fleet-wide limits should map through, got %+v", rc.settings.Limits)
	}
	if rc.settings.AgentLimits["heavy"].Memory != "16Gi" {
		t.Errorf("the per-profile limits should map through, got %+v", rc.settings.AgentLimits)
	}
	if rc := reloadableSettings(config.Config{}); !rc.settings.Limits.IsZero() || rc.settings.AgentLimits != nil {
		t.Errorf("an empty config should cap nothing, got %+v", rc.settings)
	}
}

// waitServing blocks until the server on sock has answered a hello, and fails
// the test if it does not. It is the readiness gate for a test that drives
// runServerOn on a listener it bound itself: the answer comes out of Serve, so
// it stands for everything runServerOn does before Serve, the signal handlers
// included.
func waitServing(t *testing.T, sock string) {
	t.Helper()
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial %s: %v", sock, err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Wait(5 * time.Second); err != nil {
		t.Fatalf("the server on %s did not answer a hello: %v", sock, err)
	}
}
