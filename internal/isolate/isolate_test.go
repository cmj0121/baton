package isolate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/limits"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// argv joins a rendered command back into one string, so a test can ask whether a
// flag PAIR is present rather than whether two adjacent elements happen to exist.
func argv(spec ptymgr.Spec) string {
	return strings.Join(append([]string{spec.Command}, spec.Args...), " ")
}

// dockerPolicy is a minimal working policy: the least a spawn needs.
func dockerPolicy() Policy {
	return Policy{Mode: ModeDocker, Image: "example/agent:1"}
}

func TestEnabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Policy
		want bool
	}{
		{"zero value isolates nothing", Policy{}, false},
		{"an explicit none isolates nothing", Policy{Mode: ModeNone}, false},
		{"docker isolates", Policy{Mode: ModeDocker}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Enabled(); got != tc.want {
				t.Fatalf("Enabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	if err := (Policy{}).Validate(); err != nil {
		t.Fatalf("a disabled policy must validate: %v", err)
	}
	if err := (Policy{Mode: ModeDocker}).Validate(); err == nil {
		t.Fatal("docker with no image must be rejected: baton ships none")
	}
	if err := dockerPolicy().Validate(); err != nil {
		t.Fatalf("a complete policy must validate: %v", err)
	}
}

func TestRuntime(t *testing.T) {
	if got := dockerPolicy().Runtime(); got != "docker" {
		t.Fatalf("Runtime() = %q, want docker", got)
	}
	if got := (Policy{}).Runtime(); got != "" {
		t.Fatalf("a disabled policy has no runtime, got %q", got)
	}
}

func TestParsers(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Mode
		ok   bool
	}{
		{"docker", ModeDocker, true},
		{" DOCKER ", ModeDocker, true},
		{"none", ModeNone, true},
		{"podman", ModeNone, false},
		{"", ModeNone, false},
	} {
		if got, ok := ParseMode(tc.in); got != tc.want || ok != tc.ok {
			t.Errorf("ParseMode(%q) = %q,%v; want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	for _, tc := range []struct {
		in   string
		want Mount
		ok   bool
	}{
		{"workspace", MountWorkspace, true},
		{"workspace+home", MountHome, true},
		{"everything", MountWorkspace, false},
	} {
		if got, ok := ParseMount(tc.in); got != tc.want || ok != tc.ok {
			t.Errorf("ParseMount(%q) = %q,%v; want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	for _, tc := range []struct {
		in   string
		want Network
		ok   bool
	}{
		{"host", NetworkHost, true},
		{"bridge", NetworkBridge, true},
		{"none", NetworkNone, true},
		{"vpn", NetworkHost, false},
	} {
		if got, ok := ParseNetwork(tc.in); got != tc.want || ok != tc.ok {
			t.Errorf("ParseNetwork(%q) = %q,%v; want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestWrapDisabledIsUntouched(t *testing.T) {
	in := ptymgr.Spec{Command: "claude", Args: []string{"--foo"}, Dir: t.TempDir(), Env: []string{"K=v"}}
	out, err := Policy{}.Wrap("baton-1", in, limits.Limits{})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if out.Command != in.Command || len(out.Args) != 1 || out.Args[0] != "--foo" || out.Dir != in.Dir {
		t.Fatalf("a disabled policy must return the spec byte-for-byte, got %+v", out)
	}
	if len(out.Env) != 1 || out.Env[0] != "K=v" {
		t.Fatalf("a disabled policy must not touch Env, got %v", out.Env)
	}
}

func TestWrapRendersTheRun(t *testing.T) {
	dir := t.TempDir()
	in := ptymgr.Spec{Command: "claude", Args: []string{"--dangerously", "-p"}, Dir: dir}
	out, err := dockerPolicy().Wrap("baton-7-abcd", in, limits.Limits{})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if out.Command != "docker" {
		t.Fatalf("Command = %q, want docker", out.Command)
	}
	if out.Dir != dir {
		t.Fatalf("Dir = %q, want the host workdir %q — host-side git tooling resolves through it", out.Dir, dir)
	}
	if len(out.Env) != 0 {
		t.Fatalf("Env must be dropped; only --env crosses. got %v", out.Env)
	}

	got := argv(out)
	for _, want := range []string{
		"docker run --rm --init --interactive --tty --name baton-7-abcd",
		"--user " + fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"--volume " + dir + ":" + dir,
		"--workdir " + dir,
		"--network host",
		"example/agent:1 claude --dangerously -p",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered command missing %q\ngot: %s", want, got)
		}
	}
	// The image must come last before the program: anything after it is the
	// container's argv, not the runtime's.
	if i, j := strings.Index(got, "example/agent:1"), strings.Index(got, "--network"); i < j {
		t.Errorf("the image must follow every runtime flag\ngot: %s", got)
	}
}

func TestWrapEmptyCommandRunsTheShell(t *testing.T) {
	out, err := dockerPolicy().Wrap("baton-1", ptymgr.Spec{Dir: t.TempDir()}, limits.Limits{})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if !strings.Contains(argv(out), "example/agent:1 "+ptymgr.DefaultShell()) {
		t.Fatalf("an empty command must fall back to the shell, as it does unisolated\ngot: %s", argv(out))
	}
}

func TestWrapNeedsSomethingToMount(t *testing.T) {
	t.Setenv("HOME", "") // PanelDir falls back to home; with none there is no tree at all
	if _, err := dockerPolicy().Wrap("baton-1", ptymgr.Spec{Command: "claude"}, limits.Limits{}); err == nil {
		t.Fatal("a panel with no resolvable working directory must fail the spawn")
	}
}

func TestWrapRejectsAnUnrunnablePolicy(t *testing.T) {
	p := Policy{Mode: ModeDocker} // no image
	if _, err := p.Wrap("baton-1", ptymgr.Spec{Command: "claude", Dir: t.TempDir()}, limits.Limits{}); err == nil {
		t.Fatal("Wrap must refuse a policy Validate rejects, rather than render a broken run")
	}
}

func TestWrapMountHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A workspace outside $HOME needs both mounts.
	outside := t.TempDir()
	p := dockerPolicy()
	p.Mount = MountHome
	got := argv(mustWrap(t, p, ptymgr.Spec{Command: "claude", Dir: outside}))
	if !strings.Contains(got, "--volume "+home+":"+home) || !strings.Contains(got, "--volume "+outside+":"+outside) {
		t.Errorf("a workspace outside $HOME needs both mounts\ngot: %s", got)
	}
	if !strings.Contains(got, "--env HOME="+home) {
		t.Errorf("a mounted home must be the container's HOME\ngot: %s", got)
	}

	// A workspace INSIDE $HOME is already covered by the home mount.
	inside := filepath.Join(home, "proj")
	got = argv(mustWrap(t, p, ptymgr.Spec{Command: "claude", Dir: inside}))
	if strings.Contains(got, "--volume "+inside+":"+inside) {
		t.Errorf("a workspace under a mounted $HOME must not be bound twice\ngot: %s", got)
	}
	if !strings.Contains(got, "--workdir "+inside) {
		t.Errorf("the workdir is still the workspace, mounted or not\ngot: %s", got)
	}
}

func TestWrapWorkspaceOnlyKeepsHomeOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()

	got := argv(mustWrap(t, dockerPolicy(), ptymgr.Spec{Command: "claude", Dir: dir}))
	if strings.Contains(got, home+":"+home) {
		t.Errorf("mount: workspace must not bind $HOME\ngot: %s", got)
	}
	if !strings.Contains(got, "--env HOME="+containerHome) {
		t.Errorf("without a mounted home the container still needs a writable one\ngot: %s", got)
	}
}

func TestWrapNetworkPolicies(t *testing.T) {
	for _, tc := range []struct {
		set  Network
		want string
	}{
		{"", "--network host"},
		{NetworkHost, "--network host"},
		{NetworkBridge, "--network bridge"},
		{NetworkNone, "--network none"},
	} {
		p := dockerPolicy()
		p.Network = tc.set
		got := argv(mustWrap(t, p, ptymgr.Spec{Command: "claude", Dir: t.TempDir()}))
		if !strings.Contains(got, tc.want) {
			t.Errorf("network %q rendered %s, want %q", tc.set, got, tc.want)
		}
	}
}

func TestWrapUser(t *testing.T) {
	p := dockerPolicy()
	p.User = "root"
	if got := argv(mustWrap(t, p, ptymgr.Spec{Command: "claude", Dir: t.TempDir()})); !strings.Contains(got, "--user root") {
		t.Errorf("an explicit user must win\ngot: %s", got)
	}
	p.User = "  " // whitespace is not a user
	want := fmt.Sprintf("--user %d:%d", os.Getuid(), os.Getgid())
	if got := argv(mustWrap(t, p, ptymgr.Spec{Command: "claude", Dir: t.TempDir()})); !strings.Contains(got, want) {
		t.Errorf("the default must be the host's own uid:gid, so nothing lands root-owned\ngot: %s", got)
	}
}

// TestWrapEnvIsAnAllowlist is the acceptance criterion in test form: nothing from
// the environment reaches the container unless env-allow names it.
func TestWrapEnvIsAnAllowlist(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-allowed")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "never-crosses")

	p := dockerPolicy()
	p.EnvAllow = []string{"ANTHROPIC_API_KEY", " ", "NOT_SET_ANYWHERE"}
	spec := ptymgr.Spec{Command: "claude", Dir: t.TempDir(), Env: []string{"BATON_PANEL_ID=7", "malformed"}}
	got := argv(mustWrap(t, p, spec))

	if !strings.Contains(got, "--env ANTHROPIC_API_KEY=sk-allowed") {
		t.Errorf("an allowed name must cross with its value\ngot: %s", got)
	}
	if strings.Contains(got, "AWS_SECRET_ACCESS_KEY") {
		t.Errorf("a name env-allow does not list must not cross at all\ngot: %s", got)
	}
	if strings.Contains(got, "NOT_SET_ANYWHERE") {
		t.Errorf("an unset name must pass nothing, not an empty string\ngot: %s", got)
	}
	if !strings.Contains(got, "--env BATON_PANEL_ID=7") {
		t.Errorf("what baton injected for this panel must cross\ngot: %s", got)
	}
	if strings.Contains(got, "--env malformed") {
		t.Errorf("an entry with no = is not an assignment\ngot: %s", got)
	}
	if !strings.Contains(got, "--env TERM=xterm-256color") {
		t.Errorf("the container needs its own TERM: the host's belongs to the client\ngot: %s", got)
	}
	// Every --env is a full assignment; a bare name would inherit the host's value
	// and quietly undo the whole promise.
	args := mustWrap(t, p, spec).Args
	for i, a := range args {
		if a == "--env" && !strings.Contains(args[i+1], "=") {
			t.Errorf("--env %q is a bare name and would inherit from the host", args[i+1])
		}
	}
}

func TestWrapResourceLimits(t *testing.T) {
	caps := limits.Limits{CPUs: "1.5", Memory: "4Gi", MemoryHigh: "3Gi", Pids: "512", NOFile: "4096"}
	got := argv(mustWrap(t, dockerPolicy(), ptymgr.Spec{Command: "claude", Dir: t.TempDir()}, caps))
	for _, want := range []string{
		"--cpus=1.5",
		"--memory=" + fmt.Sprint(4*1024*1024*1024),
		"--memory-reservation=" + fmt.Sprint(3*1024*1024*1024),
		"--pids-limit=512",
		"--ulimit=nofile=4096:4096",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("caps must reach the runtime, missing %q\ngot: %s", want, got)
		}
	}
}

func TestWrapUncappedEmitsNothing(t *testing.T) {
	// Unset and explicitly lifted are both "no cap", and the runtime's default is
	// already no cap — so the correct rendering of both is silence.
	for _, caps := range []limits.Limits{{}, {CPUs: limits.Unlimited, Memory: limits.Unlimited, Pids: limits.Unlimited, NOFile: limits.Unlimited}} {
		got := argv(mustWrap(t, dockerPolicy(), ptymgr.Spec{Command: "claude", Dir: t.TempDir()}, caps))
		for _, flag := range []string{"--cpus", "--memory", "--pids-limit", "--ulimit"} {
			if strings.Contains(got, flag) {
				t.Errorf("an uncapped policy emitted %s\ngot: %s", flag, got)
			}
		}
	}
}

func TestWrapRejectsUnreadableCaps(t *testing.T) {
	for field, caps := range map[string]limits.Limits{
		"cpus":        {CPUs: "two"},
		"memory":      {Memory: "4 gigs"},
		"memory-high": {MemoryHigh: "lots"},
		"pids":        {Pids: "many"},
		"nofile":      {NOFile: "plenty"},
	} {
		_, err := dockerPolicy().Wrap("baton-1", ptymgr.Spec{Command: "claude", Dir: t.TempDir()}, caps)
		if err == nil {
			t.Errorf("an unreadable %s must fail the spawn rather than run uncapped", field)
			continue
		}
		if !strings.Contains(err.Error(), field) {
			t.Errorf("the error must name the field the user wrote: %v", err)
		}
	}
}

func TestUnenforced(t *testing.T) {
	// nofile is what the cgroup backend has to report as unenforced; a runtime
	// takes it, so under isolation the answer is nothing.
	if got := dockerPolicy().Unenforced(limits.Limits{NOFile: "4096"}); len(got) != 0 {
		t.Fatalf("Unenforced() = %v, want none", got)
	}
}

func TestContainerName(t *testing.T) {
	a, b := ContainerName("7"), ContainerName("7")
	if !strings.HasPrefix(a, "baton-7-") {
		t.Fatalf("ContainerName(%q) = %q, want a traceable baton- prefix", "7", a)
	}
	if a == b {
		t.Fatal("two runs of the same panel must not collide on a name")
	}
}

func TestRemoveIsANoOpWhenDisabled(t *testing.T) {
	// Neither call may reach a runtime: the first has no mode, the second no name.
	Remove(ModeNone, "baton-1")
	Remove(ModeDocker, "")
}

func TestUnder(t *testing.T) {
	for _, tc := range []struct {
		path, base string
		want       bool
	}{
		{"/home/u/proj", "/home/u", true},
		{"/home/u", "/home/u", true},
		{"/home/u/", "/home/u", true},
		{"/home/u/proj/../proj", "/home/u", true},
		{"/home/other", "/home/u", false},
		{"/home", "/home/u", false},
		{"/home/username", "/home/user", false},
	} {
		if got := under(tc.path, tc.base); got != tc.want {
			t.Errorf("under(%q, %q) = %v, want %v", tc.path, tc.base, got, tc.want)
		}
	}
}

// mustWrap wraps with an optional cap policy, failing the test on error.
func mustWrap(t *testing.T, p Policy, spec ptymgr.Spec, caps ...limits.Limits) ptymgr.Spec {
	t.Helper()
	var l limits.Limits
	if len(caps) > 0 {
		l = caps[0]
	}
	out, err := p.Wrap("baton-test", spec, l)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	return out
}

// TestInvalidStaysEnabled guards the one asymmetry in this package: a policy the
// config could not read must keep failing spawns, because falling back means an
// unconfined panel — the outcome the setting existed to prevent.
func TestInvalidStaysEnabled(t *testing.T) {
	p := Policy{Invalid: `isolate "dockerr" is not a runtime baton offers`}
	if !p.Enabled() {
		t.Fatal("a broken policy must stay enabled")
	}
	err := p.Validate()
	if err == nil || !strings.Contains(err.Error(), "dockerr") {
		t.Fatalf("Validate must report the config's own reason, got %v", err)
	}
	if _, err := p.Wrap("baton-1", ptymgr.Spec{Command: "claude", Dir: t.TempDir()}, limits.Limits{}); err == nil {
		t.Fatal("Wrap must refuse a poisoned policy rather than fall through to the host")
	}
}
