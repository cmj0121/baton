package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/isolate"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// TestMain answers the --plugin-dir probe "no" for the whole package, so no test
// ever execs a real claude --help — nor a test's own claude shim, which records
// its argv and parks, and would hand the probe a two-second timeout and the
// launch-argv file a "--help" it never launched with. A test that wants a yes
// says so with stubProbe.
func TestMain(m *testing.M) {
	realProbePluginDir = probePluginDir
	probePluginDir = func(string) (bool, error) { return false, nil }
	os.Exit(m.Run())
}

// realProbePluginDir is the probe TestMain replaced, kept for the tests that
// run it — against scripts of their own, never a real claude.
var realProbePluginDir func(string) (bool, error)

// script writes an executable sh script with body into dir and returns it.
func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil { //nolint:gosec // a test binary must be executable
		t.Fatal(err)
	}
	return bin
}

// TestProbePluginDirReadsTheHelp: the probe is a yes only when --help runs
// cleanly and names the flag, and a clean no is told apart from no answer.
func TestProbePluginDirReadsTheHelp(t *testing.T) {
	// Every case here answers, so the deadline only has to outlast a loaded
	// -race box: the production two seconds is a timeout this test does not
	// test, and under a full parallel suite a fresh script can miss it.
	prev := pluginProbeTimeout
	pluginProbeTimeout = 15 * time.Second
	t.Cleanup(func() { pluginProbeTimeout = prev })

	dir := t.TempDir()
	for _, tc := range []struct {
		name, body string
		want, err  bool
	}{
		{"names-the-flag", "echo '  --plugin-dir <paths...>  Load plugins'", true, false},
		{"an-older-cli", "echo '  --mcp-config <configs...>'", false, false},
		{"help-fails", "echo '--plugin-dir'; exit 1", false, true},
	} {
		got, err := realProbePluginDir(script(t, dir, tc.name, tc.body))
		if got != tc.want || (err != nil) != tc.err {
			t.Errorf("%s: probe = (%v, %v), want (%v, error %v)", tc.name, got, err, tc.want, tc.err)
		}
	}
	if ok, err := realProbePluginDir(filepath.Join(dir, "absent")); ok || err == nil {
		t.Errorf("a binary that will not run is a no with an error, got (%v, %v)", ok, err)
	}
}

// TestProbePluginDirKillsTheWholeWrapper: a claude that is a shell wrapper and
// hangs is answered by the deadline — as a DeadlineExceeded, so it is not
// cached — and the kill takes the wrapper's child with it. The child here is
// backgrounded and waited on, the shape that survives killing sh alone: it
// would keep the output pipe open until WaitDelay gave up on it, a full second
// later, and live on after that.
func TestProbePluginDirKillsTheWholeWrapper(t *testing.T) {
	if !alive(t, os.Getpid()) {
		t.Skip("this box's ps does not see the test itself, so its silence about the child would prove nothing")
	}
	prev := pluginProbeTimeout
	// Long enough for the wrapper to have forked its child: a fresh script's first
	// exec can take a few hundred milliseconds on macOS, more on a loaded -race
	// box, and a deadline that kills sh before it forks proves nothing.
	pluginProbeTimeout = 3 * time.Second
	t.Cleanup(func() { pluginProbeTimeout = prev })

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	bin := script(t, dir, "claude", "sleep 30 &\necho $! > "+pidFile+"\nwait")
	ok, err := realProbePluginDir(bin)
	if ok || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a hung --help should be a no for want of an answer, got (%v, %v)", ok, err)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Skipf("the deadline killed the wrapper before it recorded its child, so this run proves nothing: %v", err)
	}
	recorded := strings.TrimSpace(string(data))
	if recorded == "" {
		t.Skip("the deadline killed the wrapper between opening child.pid and writing it, so this run proves nothing")
	}
	pid, err := strconv.Atoi(recorded)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	// Generous because it is a ceiling, not a wait: a dead child ends the loop at
	// once, and only a loaded box's slow ps calls ever spend it.
	deadline := time.Now().Add(10 * time.Second)
	for alive(t, pid) {
		if time.Now().After(deadline) {
			t.Fatalf("the wrapper's child (pid %d) outlived the probe's kill", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// alive reports whether pid is a running process. A zombie counts as dead: the
// killed child is orphaned, and a PID 1 that never reaps (docker without
// --init) would leave it answering signal 0 forever. A ps that cannot answer
// skips the test rather than let it pass on no evidence.
func alive(t *testing.T, pid int) bool {
	t.Helper()
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	stat := strings.TrimSpace(string(out))
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && stat == "":
		return false
	case err != nil:
		t.Skipf("this box's ps is unusable, so the child's fate is unknown: %v", err)
	case stat == "" || stat[0] < 'A' || stat[0] > 'Z':
		t.Skipf("this box's ps is unusable, it answered %q for a process state", stat)
	}
	return stat[0] != 'Z'
}

// stubProbe answers the probe with ok for one test and counts how often it ran.
func stubProbe(t *testing.T, ok bool) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	prev := probePluginDir
	probePluginDir = func(string) (bool, error) { calls.Add(1); return ok, nil }
	t.Cleanup(func() { probePluginDir = prev })
	return &calls
}

// yes and no stand in for the server's answer when withAgentMCP is driven alone.
func yes(ptymgr.Spec) bool { return true }
func no(ptymgr.Spec) bool  { return false }

// pluginDirOf is the value after the LAST --plugin-dir in args, or "".
func pluginDirOf(args []string) string {
	dir := ""
	for i, a := range args {
		if a == "--plugin-dir" && i+1 < len(args) {
			dir = args[i+1]
		}
	}
	return dir
}

// TestWithAgentMCPHandsAWiredWorkerItsSkills: a worker agent whose memory tool
// is wired also gets the worker skill set, as a plugin built under baton's own
// home — and that set, not the conductor's.
func TestWithAgentMCPHandsAWiredWorkerItsSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, why := withAgentMCP(true, ptymgr.Spec{Command: "claude"}, yes)
	if why != memoryToolWired || countFlag(got.Args, "--mcp-config") != 1 {
		t.Fatalf("the memory tool should be wired, got %v (%q)", got.Args, why)
	}
	if n := countFlag(got.Args, "--plugin-dir"); n != 1 {
		t.Fatalf("want exactly one --plugin-dir, got %d in %v", n, got.Args)
	}
	dir := pluginDirOf(got.Args)
	if base := filepath.Join(home, ".baton", "agent-plugin"); filepath.Dir(dir) != base {
		t.Fatalf("the plugin should be a hashed dir under %s, got %s", base, dir)
	}
	for _, name := range []string{"baton", "peers", "score"} {
		if _, err := os.Stat(filepath.Join(dir, "skills", name, "SKILL.md")); err != nil {
			t.Errorf("the worker skill %q is missing: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "conduct")); !os.IsNotExist(err) {
		t.Errorf("a worker must not be taught the conductor's skill (err %v)", err)
	}

	// The flag is repeatable, so a user's own plugin dir is no reason to leave
	// baton's off — both load.
	mine, _ := withAgentMCP(true, ptymgr.Spec{Command: "claude", Args: []string{"--plugin-dir", "/mine"}}, yes)
	if n := countFlag(mine.Args, "--plugin-dir"); n != 2 || !slices.Contains(mine.Args, "/mine") {
		t.Errorf("a user's own --plugin-dir should sit beside baton's, got %v", mine.Args)
	}
}

// TestWithAgentMCPGivesNoSkillsWithoutTheTool: every panel the memory tool is
// not wired on gets no skills either — they describe tools it would not have —
// and neither does one whose CLI the probe says cannot take the flag, because
// an unknown flag is a panel that does not start.
func TestWithAgentMCPGivesNoSkillsWithoutTheTool(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name string
		wire bool
		spec ptymgr.Spec
		ok   func(ptymgr.Spec) bool
	}{
		{"the probe says no", true, ptymgr.Spec{Command: "claude"}, no},
		{"no probe at all (isolated)", true, ptymgr.Spec{Command: "claude"}, nil},
		{"startPanel said no (setting off, conductor, not an agent)", false, ptymgr.Spec{Command: "claude"}, yes},
		{"a grok panel", true, ptymgr.Spec{Command: "grok"}, yes},
		{"a user-supplied --mcp-config", true,
			ptymgr.Spec{Command: "claude", Args: []string{"--mcp-config", "/mine.json"}}, yes},
	} {
		got, _ := withAgentMCP(tc.wire, tc.spec, tc.ok)
		if slices.Contains(got.Args, "--plugin-dir") {
			t.Errorf("%s: should carry no --plugin-dir, got %v", tc.name, got.Args)
		}
	}
}

// TestWithAgentMCPLaunchesWhenTheSkillsCannotBeWritten: a plugin tree baton
// cannot build is a Warn, not a refused spawn — the panel still starts, wired
// to its memory tool, just without the prose about it.
func TestWithAgentMCPLaunchesWhenTheSkillsCannotBeWritten(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".baton"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A file where the plugin base directory should be.
	if err := os.WriteFile(filepath.Join(home, ".baton", "agent-plugin"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, why := withAgentMCP(true, ptymgr.Spec{Command: "claude"}, yes)
	if why != memoryToolWired || countFlag(got.Args, "--mcp-config") != 1 {
		t.Fatalf("the memory tool should still be wired, got %v (%q)", got.Args, why)
	}
	if slices.Contains(got.Args, "--plugin-dir") {
		t.Errorf("no plugin was built, so none should be named, got %v", got.Args)
	}
}

// TestIsolatedPanelIsNeverProbed: the host's claude says nothing about the one
// in a container image, so an isolated launch gets no probe and no skills.
func TestIsolatedPanelIsNeverProbed(t *testing.T) {
	calls := stubProbe(t, true)
	s := &Server{}
	if ok := s.pluginsOK(isolate.Policy{Mode: isolate.ModeDocker}); ok != nil {
		t.Fatal("an isolated panel should get no probe at all")
	}
	if calls.Load() != 0 {
		t.Errorf("the probe ran %d times for an isolated panel", calls.Load())
	}
	if s.pluginsOK(isolate.Policy{}) == nil {
		t.Error("an unisolated panel should be probed")
	}
}

// fakeBin writes an executable named name in a fresh dir and returns that dir.
func fakeBin(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // a test binary must be executable
		t.Fatal(err)
	}
	return dir
}

// TestSupportsPluginDirProbesOncePerBinary: the probe runs once per binary and
// is remembered, asked again when the binary is replaced in place, found on
// the DAEMON's PATH — the one exec.Command resolves the launch on, so a PATH in
// the spec's env must not steer it — and a binary that is nowhere is a no.
func TestSupportsPluginDirProbesOncePerBinary(t *testing.T) {
	var asked []string
	prev := probePluginDir
	probePluginDir = func(bin string) (bool, error) { asked = append(asked, bin); return true, nil }
	t.Cleanup(func() { probePluginDir = prev })

	dir := fakeBin(t, "claude")
	decoy := fakeBin(t, "claude")
	t.Setenv("PATH", dir)
	spec := ptymgr.Spec{Command: "claude", Env: []string{"PATH=" + decoy}}
	s := &Server{}

	for range 2 {
		if !s.supportsPluginDir(spec) {
			t.Fatal("the probe said yes; so should the server")
		}
	}
	want := filepath.Join(dir, "claude")
	if len(asked) != 1 || asked[0] != want {
		t.Fatalf("want one probe of %s (the daemon's PATH, not the spec env's), got %v", want, asked)
	}

	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(want, later, later); err != nil {
		t.Fatal(err)
	}
	s.supportsPluginDir(spec)
	if len(asked) != 2 {
		t.Errorf("an upgraded binary should be asked again, got %v", asked)
	}

	t.Setenv("PATH", t.TempDir())
	if s.supportsPluginDir(spec) {
		t.Error("a claude on no PATH should be a no")
	}
	if len(asked) != 2 {
		t.Errorf("a binary that is nowhere should not be probed, got %v", asked)
	}
}

// TestSupportsPluginDirDoesNotCacheATimeout: a probe that got no answer in time
// says nothing about the binary, so the next spawn asks again — while a binary
// whose --help failed outright is a fact about the file, and is remembered.
func TestSupportsPluginDirDoesNotCacheATimeout(t *testing.T) {
	var calls int
	answer := fmt.Errorf("no answer: %w", context.DeadlineExceeded)
	prev := probePluginDir
	probePluginDir = func(string) (bool, error) { calls++; return false, answer }
	t.Cleanup(func() { probePluginDir = prev })

	t.Setenv("PATH", fakeBin(t, "claude"))
	spec := ptymgr.Spec{Command: "claude"}
	s := &Server{}

	for range 2 {
		if s.supportsPluginDir(spec) {
			t.Fatal("a timed-out probe is a no")
		}
	}
	if calls != 2 {
		t.Fatalf("a timeout must not be cached; want 2 probes, got %d", calls)
	}

	answer = errors.New("exit status 1")
	for range 2 {
		s.supportsPluginDir(spec)
	}
	if calls != 3 {
		t.Errorf("a failed --help is a fact about the binary; want it probed once more, got %d probes", calls)
	}
}

// TestWorkerPanelLaunchesWithItsSkills drives the real fork point: a claude
// agent panel with agent-mcp on and a probe answering yes is launched with
// --plugin-dir, and the spec the server retains for respawn never carries it.
func TestWorkerPanelLaunchesWithItsSkills(t *testing.T) {
	stubProbe(t, true)
	command, argv := claudeShim(t)
	s, dir := identityServer(t, WithAgentMCP(true))

	id, err := s.createPanel(originOperator, proto.KindAgent, command, nil, dir, "", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	got := launchedArgs(t, argv)
	if n := countFlag(got, "--plugin-dir"); n != 1 {
		t.Fatalf("want one --plugin-dir on the launch, got %d in %v", n, got)
	}
	if p := pluginDirOf(got); !strings.Contains(p, filepath.Join(".baton", "agent-plugin")) {
		t.Errorf("the plugin should be baton's own, got %s", p)
	}
	if spec := s.specs[id].Spec; slices.Contains(spec.Args, "--plugin-dir") {
		t.Errorf("the retained spec gained baton's skills, got %v", spec.Args)
	}
}
