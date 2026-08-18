package server

import (
	"os"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/isolate"
	"github.com/cmj0121/baton/internal/limits"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// isolatedServer is a server whose "walled" profile isolates and whose "plain"
// one does not, plus the temp dir the tests use as a workdir.
//
// No test here starts a container. The spawn path is exercised up to the point
// where the runtime would be invoked — which is where every decision this feature
// makes actually lives — so the suite runs identically on a machine with docker
// and one without.
func isolatedServer(t *testing.T, pol isolate.Policy) (*Server, string) {
	t.Helper()
	s := newHostServer(t)
	s.Reload(Settings{AgentIsolate: map[string]isolate.Policy{"walled": pol}})
	t.Cleanup(func() { s.Shutdown() })
	return s, os.Getenv("BATON_TEST_DIR")
}

func dockerPolicy() isolate.Policy {
	return isolate.Policy{Mode: isolate.ModeDocker, Image: "example/agent:1"}
}

func TestIsolateSpecRewritesAndRecords(t *testing.T) {
	s, dir := isolatedServer(t, dockerPolicy())

	spec := ptymgr.Spec{Command: "claude", Args: []string{"-p"}, Dir: dir}
	if err := s.isolateSpec("7", dockerPolicy(), &spec, limits.Limits{Memory: "4Gi"}); err != nil {
		t.Fatalf("isolateSpec: %v", err)
	}
	if spec.Command != "docker" {
		t.Fatalf("Command = %q, want the runtime client", spec.Command)
	}
	joined := strings.Join(spec.Args, " ")
	if !strings.Contains(joined, "example/agent:1 claude -p") {
		t.Errorf("the panel's own program must follow the image\ngot: %s", joined)
	}
	if !strings.Contains(joined, "--memory=") {
		t.Errorf("caps must reach the runtime, not a cgroup around its client\ngot: %s", joined)
	}

	s.mu.Lock()
	name := s.containers["7"]
	s.mu.Unlock()
	if !strings.HasPrefix(name, "baton-7-") {
		t.Fatalf("the container name must be recorded for teardown, got %q", name)
	}
	if !strings.Contains(joined, "--name "+name) {
		t.Errorf("the recorded name must be the one the runtime was given\ngot: %s", joined)
	}
}

// TestIsolateSpecMintsAFreshNameEachRun guards the respawn case: a re-run must
// not collide with a container the previous run left behind.
func TestIsolateSpecMintsAFreshNameEachRun(t *testing.T) {
	s, dir := isolatedServer(t, dockerPolicy())

	names := map[string]bool{}
	for i := 0; i < 3; i++ {
		spec := ptymgr.Spec{Command: "claude", Dir: dir}
		if err := s.isolateSpec("7", dockerPolicy(), &spec, limits.Limits{}); err != nil {
			t.Fatalf("isolateSpec: %v", err)
		}
		s.mu.Lock()
		names[s.containers["7"]] = true
		s.mu.Unlock()
	}
	if len(names) != 3 {
		t.Fatalf("three runs of one panel produced %d distinct names, want 3", len(names))
	}
}

// TestStartPanelRefusesABrokenIsolation is the acceptance criterion in test form:
// a policy that cannot run fails the spawn rather than quietly falling back to an
// unisolated panel.
func TestStartPanelRefusesABrokenIsolation(t *testing.T) {
	poisoned := isolate.Policy{Invalid: `isolate "dockerr" is not a runtime baton offers`}
	s, dir := isolatedServer(t, poisoned)

	id, err := s.createPanel(proto.KindAgent, "/bin/sh", nil, dir, "walled", false, false)
	if err == nil {
		t.Fatalf("the spawn must fail; it created panel %q on the host instead", id)
	}
	if !strings.Contains(err.Error(), "dockerr") {
		t.Errorf("the failure must carry the config's own reason, got %v", err)
	}
	s.mu.Lock()
	n := len(s.panels)
	s.mu.Unlock()
	if n != 0 {
		t.Errorf("a refused spawn must leave no panel behind, got %d", n)
	}
}

// TestStartPanelUnisolatedIsUntouched is the other half of the same criterion:
// with no policy, the spawn is byte-for-byte what it was before this feature.
func TestStartPanelUnisolatedIsUntouched(t *testing.T) {
	s, dir := isolatedServer(t, dockerPolicy())

	id, err := s.createPanel(proto.KindAgent, "/bin/sh", nil, dir, "plain", false, false)
	if err != nil {
		t.Fatalf("an un-isolated profile must spawn as it always has: %v", err)
	}
	s.mu.Lock()
	name := s.containers[id]
	s.mu.Unlock()
	if name != "" {
		t.Fatalf("a profile with no isolation must claim no container, got %q", name)
	}
}

func TestWithoutFleetSocket(t *testing.T) {
	in := ptymgr.Spec{Env: []string{
		paths.EnvSocket + "=/run/user/501/baton.sock",
		paths.EnvPanelID + "=7",
		"GIT_EDITOR=vi",
	}}
	got := withoutFleetSocket(in).Env
	for _, kv := range got {
		if strings.HasPrefix(kv, paths.EnvSocket+"=") {
			t.Fatalf("the fleet socket must not cross into a container: %q", kv)
		}
	}
	if len(got) != 2 {
		t.Fatalf("only the socket goes; the panel id and the rest stay. got %v", got)
	}
	if len(in.Env) != 3 {
		t.Fatalf("the caller's spec must not be mutated, got %v", in.Env)
	}
}

func TestReleaseContainerIsSafeAndIdempotent(t *testing.T) {
	s, _ := isolatedServer(t, dockerPolicy())

	s.releaseContainer("never-isolated") // a panel with no container at all
	s.mu.Lock()
	s.containers["7"] = "baton-7-deadbeef"
	s.mu.Unlock()

	s.releaseContainer("7")
	s.releaseContainer("7") // twice: only the first caller has anything to remove
	s.mu.Lock()
	_, still := s.containers["7"]
	s.mu.Unlock()
	if still {
		t.Fatal("the name must be forgotten, or teardown would chase it forever")
	}
}

func TestSweepContainersClearsTheTable(t *testing.T) {
	s, _ := isolatedServer(t, dockerPolicy())

	s.mu.Lock()
	s.containers["1"] = "baton-1-aaaa"
	s.containers["2"] = "baton-2-bbbb"
	s.mu.Unlock()

	s.sweepContainers()
	s.sweepContainers() // an empty sweep must be a cheap no-op, not a second pass

	s.mu.Lock()
	n := len(s.containers)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("shutdown must leave no container unaccounted for, got %d", n)
	}
}

// TestIsolationModeSurvivesAReload covers the panel whose profile stopped
// isolating while it was running: the container is still there and still ours, so
// teardown must still know which runtime to ask.
func TestIsolationModeSurvivesAReload(t *testing.T) {
	s, _ := isolatedServer(t, dockerPolicy())

	s.mu.Lock()
	s.agentIsolate = nil // the reload that dropped the profile's isolation
	got := s.isolationModeLocked("7")
	s.mu.Unlock()

	if got != isolate.ModeDocker {
		t.Fatalf("mode = %q; a forgotten runtime must not become a leaked container", got)
	}
}
