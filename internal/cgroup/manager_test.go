package cgroup

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/limits"
)

// fakeCgroup is a directory standing in for a panel's cgroup: the control files
// a delegated cgroup v2 leaf exposes, seeded with the kernel's own defaults. It
// lets the whole write path be exercised on any OS — only the fork-time
// placement is genuinely Linux-specific.
func fakeCgroup(t *testing.T, files ...string) string {
	t.Helper()
	if len(files) == 0 {
		files = []string{"cpu.max", "memory.max", "memory.high", "pids.max"}
	}
	dir := t.TempDir()
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("max\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func read(t *testing.T, dir, file string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	return strings.TrimSpace(string(raw))
}

// TestControls pins the translation from a policy to cgroup v2 control files:
// the cpu quota-and-period pair, byte counts for the memory pair, and "max"
// wherever the policy asks for no cap — because writing "max" is what actually
// removes a cap a previous write left in place.
func TestControls(t *testing.T) {
	got, err := controls(limits.Limits{CPUs: "2", Memory: "4Gi", MemoryHigh: "3Gi", Pids: "512"})
	if err != nil {
		t.Fatalf("controls: %v", err)
	}
	want := map[string]string{
		"cpu.max":     "200000 100000",
		"memory.max":  "4294967296",
		"memory.high": "3221225472",
		"pids.max":    "512",
	}
	if len(got) != len(want) {
		t.Fatalf("controls rendered %d files, want %d: %+v", len(got), len(want), got)
	}
	for _, c := range got {
		if want[c.file] != c.value {
			t.Errorf("%s = %q, want %q", c.file, c.value, want[c.file])
		}
	}

	// A fractional core allowance rounds to whole microseconds of quota.
	half, err := controls(limits.Limits{CPUs: "1.5"})
	if err != nil {
		t.Fatalf("controls: %v", err)
	}
	if half[0].value != "150000 100000" {
		t.Errorf("1.5 cores = %q, want %q", half[0].value, "150000 100000")
	}

	// Every uncapped field renders as "max", whether it was left unset or lifted.
	for _, l := range []limits.Limits{{}, {CPUs: limits.Unlimited, Memory: limits.Unlimited, Pids: limits.Unlimited}} {
		ctrls, err := controls(l)
		if err != nil {
			t.Fatalf("controls(%+v): %v", l, err)
		}
		for _, c := range ctrls {
			if !strings.HasPrefix(c.value, "max") {
				t.Errorf("%+v: %s = %q, want max", l, c.file, c.value)
			}
		}
	}
}

// TestControlsRejectsUnreadablePolicy checks a bad quantity is refused by name
// rather than written as garbage the kernel would reject with EINVAL.
func TestControlsRejectsUnreadablePolicy(t *testing.T) {
	for _, tt := range []struct {
		limits limits.Limits
		field  string
	}{
		{limits.Limits{CPUs: "lots"}, "cpus"},
		{limits.Limits{Memory: "4 gigs"}, "memory"},
		{limits.Limits{MemoryHigh: "-1"}, "memory-high"},
		{limits.Limits{Pids: "1.5"}, "pids"},
	} {
		err := func() error { _, err := controls(tt.limits); return err }()
		if err == nil {
			t.Errorf("controls(%+v) should have failed", tt.limits)
			continue
		}
		if !strings.HasPrefix(err.Error(), tt.field+":") {
			t.Errorf("controls(%+v) should name the field, got %q", tt.limits, err)
		}
	}
}

// TestWriteControls writes a policy into a stand-in cgroup and checks what
// actually lands in each file.
func TestWriteControls(t *testing.T) {
	dir := fakeCgroup(t)

	skipped, err := writeControls(dir, limits.Limits{CPUs: "2", Memory: "4Gi", Pids: "512"})
	if err != nil {
		t.Fatalf("writeControls: %v", err)
	}
	if skipped != nil {
		t.Errorf("nothing should be skipped when every control exists, got %v", skipped)
	}
	if got := read(t, dir, "cpu.max"); got != "200000 100000" {
		t.Errorf("cpu.max = %q", got)
	}
	if got := read(t, dir, "memory.max"); got != "4294967296" {
		t.Errorf("memory.max = %q", got)
	}
	if got := read(t, dir, "memory.high"); got != "max" {
		t.Errorf("an unset memory-high should write max, got %q", got)
	}
}

// TestWriteControlsReportsMissingControls is the loud-degradation rule at the
// file level: a cap the host exposes no control for is reported, while an unset
// cap on the same missing file is not — nothing was being asked for there.
func TestWriteControlsReportsMissingControls(t *testing.T) {
	dir := fakeCgroup(t, "cpu.max", "pids.max") // no memory controller delegated

	skipped, err := writeControls(dir, limits.Limits{CPUs: "2", Memory: "4Gi"})
	if err != nil {
		t.Fatalf("writeControls: %v", err)
	}
	if !slices.Contains(skipped, "memory.max") {
		t.Errorf("a requested cap with no control file should be reported, got %v", skipped)
	}
	if slices.Contains(skipped, "memory.high") {
		t.Errorf("an unset cap should not be reported as skipped, got %v", skipped)
	}
	if got := read(t, dir, "cpu.max"); got != "200000 100000" {
		t.Errorf("the caps that CAN apply still should: cpu.max = %q", got)
	}
}

// TestHandleUpdateDefersLoweredMemory is the safety rule for a live reload:
// lowering the hard memory ceiling under a running tree would make the kernel
// reclaim against it and kill the agent mid-task, so the lowering waits for a
// respawn while memory.high — which throttles instead of killing — takes the new
// value immediately.
func TestHandleUpdateDefersLoweredMemory(t *testing.T) {
	dir := fakeCgroup(t)
	if _, err := writeControls(dir, limits.Limits{Memory: "8Gi", MemoryHigh: "6Gi"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := &Handle{dir: dir, fd: -1}

	deferred, err := h.Update(limits.Limits{Memory: "2Gi", MemoryHigh: "1Gi"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !slices.Contains(deferred, "memory") {
		t.Fatalf("a lowered memory ceiling should be deferred, got %v", deferred)
	}
	if got := read(t, dir, "memory.max"); got != "8589934592" {
		t.Errorf("memory.max should still hold the old ceiling, got %q", got)
	}
	if got := read(t, dir, "memory.high"); got != "1073741824" {
		t.Errorf("memory.high should throttle at the new value now, got %q", got)
	}
}

// TestHandleUpdateAppliesEverythingElse checks the rest of a reload lands at
// once: a raised ceiling, a changed cpu allowance, and a cap being lifted.
func TestHandleUpdateAppliesEverythingElse(t *testing.T) {
	dir := fakeCgroup(t)
	if _, err := writeControls(dir, limits.Limits{CPUs: "2", Memory: "4Gi", Pids: "512"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := &Handle{dir: dir, fd: -1}

	deferred, err := h.Update(limits.Limits{CPUs: "8", Memory: "16Gi", Pids: limits.Unlimited})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if deferred != nil {
		t.Fatalf("nothing should need a respawn here, got %v", deferred)
	}
	if got := read(t, dir, "cpu.max"); got != "800000 100000" {
		t.Errorf("cpu.max = %q", got)
	}
	if got := read(t, dir, "memory.max"); got != "17179869184" {
		t.Errorf("a raised ceiling should apply at once, got %q", got)
	}
	if got := read(t, dir, "pids.max"); got != "max" {
		t.Errorf("a lifted cap should write max, got %q", got)
	}
}

// TestHandleUpdateRejectsUnreadablePolicy checks a bad value never reaches the
// control files, leaving the live caps as they were.
func TestHandleUpdateRejectsUnreadablePolicy(t *testing.T) {
	dir := fakeCgroup(t)
	if _, err := writeControls(dir, limits.Limits{CPUs: "2"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := &Handle{dir: dir, fd: -1}

	if _, err := h.Update(limits.Limits{CPUs: "heaps"}); err == nil {
		t.Fatal("an unreadable policy should be refused")
	}
	if got := read(t, dir, "cpu.max"); got != "200000 100000" {
		t.Errorf("the live cap should be untouched, got %q", got)
	}
}

// TestHandleCloseRemovesTheCgroup covers the teardown a closed panel triggers.
func TestHandleCloseRemovesTheCgroup(t *testing.T) {
	dir := t.TempDir()
	inner := filepath.Join(dir, "panel-1")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	h := &Handle{dir: inner, fd: -1}

	h.Close()
	if _, err := os.Stat(inner); !os.IsNotExist(err) {
		t.Errorf("Close should remove the cgroup directory, stat err = %v", err)
	}
	h.Close() // twice is safe: a panel can be closed and then purged
}

// TestNilHandleIsInert checks every handle method on the nil a Prepare returns
// when there is nothing to enforce — the path most spawns take.
func TestNilHandleIsInert(t *testing.T) {
	var h *Handle
	if h.Dir() != "" || h.Skipped() != nil {
		t.Error("a nil handle should describe nothing")
	}
	if deferred, err := h.Update(limits.Limits{Memory: "1Gi"}); err != nil || deferred != nil {
		t.Errorf("a nil handle should absorb an update, got %v %v", deferred, err)
	}
	h.Close()
}

// TestNilManagerIsInert covers the manager a bare (test-constructed) server
// carries: it must behave like an unsupported host, not panic.
func TestNilManagerIsInert(t *testing.T) {
	var m *Manager
	if m.Mode() != ModeNone || m.Reason() == "" || !strings.Contains(m.Describe(), "none") {
		t.Errorf("a nil manager should report no enforcement: %q", m.Describe())
	}
	h, err := m.Prepare("1", limits.Limits{Memory: "1Gi"})
	if h != nil || err != nil {
		t.Errorf("a nil manager should prepare nothing, got %v %v", h, err)
	}
	if n, deferred := m.Update(func(string) limits.Limits { return limits.Limits{} }); n != 0 || deferred != nil {
		t.Errorf("a nil manager should update nothing, got %d %v", n, deferred)
	}
	m.Release("1")
}

// TestPrepareIsANoopWithoutABackend checks the degraded host: a policy is
// accepted and simply not enforced, so a configured limit does not make every
// spawn fail on a machine that cannot hold it.
func TestPrepareIsANoopWithoutABackend(t *testing.T) {
	m := &Manager{mode: ModeNone, reason: "test", handles: map[string]*Handle{}}

	h, err := m.Prepare("1", limits.Limits{Memory: "4Gi"})
	if h != nil || err != nil {
		t.Fatalf("an unenforcing host should prepare nothing, got %v %v", h, err)
	}
	if got := m.Describe(); got != "none (test)" {
		t.Errorf("Describe should carry the reason, got %q", got)
	}
	if got := m.Unenforced(limits.Limits{NOFile: "4096"}); got != nil {
		t.Errorf("an unenforcing host reports the mode, not each field, got %v", got)
	}
}

// TestPrepareSkipsAnEmptyPolicy checks a panel that asks for no caps gets no
// cgroup at all, which is what keeps an unconfigured baton free of them.
func TestPrepareSkipsAnEmptyPolicy(t *testing.T) {
	m := &Manager{mode: ModeCgroup, root: t.TempDir(), handles: map[string]*Handle{}}

	h, err := m.Prepare("1", limits.Limits{})
	if h != nil || err != nil {
		t.Fatalf("an empty policy should prepare nothing, got %v %v", h, err)
	}
	if entries, _ := os.ReadDir(m.root); len(entries) != 0 {
		t.Errorf("no cgroup should have been created, got %v", entries)
	}
}

// TestUnenforcedNamesNofile documents the one configurable cap cgroups do not
// cover: an open-file limit is per-process rlimit territory, so it must be
// reported rather than left looking applied.
func TestUnenforcedNamesNofile(t *testing.T) {
	m := &Manager{mode: ModeCgroup, handles: map[string]*Handle{}}

	if got := m.Unenforced(limits.Limits{CPUs: "2", NOFile: "4096"}); !slices.Contains(got, "nofile") {
		t.Errorf("nofile should be reported as unenforceable, got %v", got)
	}
	if got := m.Unenforced(limits.Limits{CPUs: "2", Memory: "4Gi"}); got != nil {
		t.Errorf("caps the backend does hold should not be reported, got %v", got)
	}
}

// TestNewProbesTheHost pins the contract every caller relies on, whatever host
// it runs on: the manager either enforces and has a root to create panels under,
// or it does not and says why. There is no third state, because a manager that
// claimed to enforce without a root would cap nothing silently.
func TestNewProbesTheHost(t *testing.T) {
	m := New()
	if m.Mode() != ModeNone || m.Reason() == "" {
		t.Error("an unprobed manager enforces nothing and says why: probing is not free")
	}
	m.Probe()
	m.Probe() // probing twice is safe: startup and a reload both reach here

	switch m.Mode() {
	case ModeCgroup:
		if m.root == "" {
			t.Error("an enforcing manager needs a root to create panel cgroups under")
		}
		if m.Describe() != string(ModeCgroup) {
			t.Errorf("an enforcing manager has nothing to explain, got %q", m.Describe())
		}
	case ModeNone:
		if m.Reason() == "" {
			t.Error("a degraded manager must explain itself; a silent one reads as enforcing")
		}
		if !strings.Contains(m.Describe(), m.Reason()) {
			t.Errorf("Describe should carry the reason, got %q", m.Describe())
		}
	default:
		t.Fatalf("unknown mode %q", m.Mode())
	}
}

// TestManagerLifecycle walks a panel's cgroup from spawn to teardown against a
// stand-in root, so the bookkeeping — one handle per panel, replaced on respawn,
// updated on reload, dropped on release — is exercised on any OS.
func TestManagerLifecycle(t *testing.T) {
	m := &Manager{mode: ModeCgroup, root: t.TempDir(), handles: map[string]*Handle{}}

	h, err := m.Prepare("1", limits.Limits{Memory: "4Gi"})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if h == nil || h.Dir() == "" {
		t.Fatal("a capped panel should get a handle")
	}
	// The stand-in root exposes no control files, which is exactly what an
	// undelegated controller looks like — so the cap must be reported, not lost.
	if !slices.Contains(h.Skipped(), "memory.max") {
		t.Errorf("a cap with no control file should be reported, got %v", h.Skipped())
	}
	if err := h.Confine(&exec.Cmd{}); err != nil { // a no-op off Linux; must not fail anywhere
		t.Errorf("Confine: %v", err)
	}

	// A respawn re-prepares the same panel: one handle, same directory.
	again, err := m.Prepare("1", limits.Limits{Memory: "8Gi"})
	if err != nil {
		t.Fatalf("re-Prepare: %v", err)
	}
	if again.Dir() != h.Dir() {
		t.Errorf("a respawn should reuse the panel's cgroup: %s → %s", h.Dir(), again.Dir())
	}
	if len(m.handles) != 1 {
		t.Errorf("a respawn should not leak a second handle, got %d", len(m.handles))
	}

	// Seed the control files the stand-in root lacks, then reload through the
	// manager and check the new caps actually landed.
	for _, f := range []string{"cpu.max", "memory.max", "memory.high", "pids.max"} {
		if err := os.WriteFile(filepath.Join(again.Dir(), f), []byte("max\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	updated, deferred := m.Update(func(id string) limits.Limits {
		if id != "1" {
			t.Errorf("Update should resolve by panel id, got %q", id)
		}
		return limits.Limits{Memory: "2Gi", Pids: "256"}
	})
	if updated != 1 || deferred != nil {
		t.Fatalf("Update = %d,%v; want 1,nil", updated, deferred)
	}
	if got := read(t, again.Dir(), "pids.max"); got != "256" {
		t.Errorf("the reloaded cap should have landed, pids.max = %q", got)
	}

	// Release drops the bookkeeping here; the directory removal is an rmdir, which
	// only succeeds on a real cgroupfs whose control files are pseudo-files — the
	// dedicated Close tests cover that against an empty directory.
	m.Release("1")
	if len(m.handles) != 0 {
		t.Errorf("Release should drop the handle, got %d", len(m.handles))
	}
	m.Release("1") // releasing twice is safe: close and purge both reach here
}

// TestPrepareRefusesAnUnreadablePolicy checks a bad value never leaves a
// half-made cgroup behind for a panel that is not going to start.
func TestPrepareRefusesAnUnreadablePolicy(t *testing.T) {
	m := &Manager{mode: ModeCgroup, root: t.TempDir(), handles: map[string]*Handle{}}

	if _, err := m.Prepare("1", limits.Limits{Memory: "4 gigs"}); err == nil {
		t.Fatal("an unreadable policy should be refused")
	}
	if entries, _ := os.ReadDir(m.root); len(entries) != 0 {
		t.Errorf("a refused Prepare should leave nothing behind, got %v", entries)
	}
	if len(m.handles) != 0 {
		t.Errorf("a refused Prepare should register no handle, got %d", len(m.handles))
	}
}

// TestUpdateSkipsAFailingPanel checks one panel's failure does not stop the
// reload: the rest of the fleet still gets its new caps.
func TestUpdateSkipsAFailingPanel(t *testing.T) {
	m := &Manager{mode: ModeCgroup, root: t.TempDir(), handles: map[string]*Handle{}}
	good := fakeCgroup(t)
	m.handles["good"] = &Handle{dir: good, fd: -1}
	m.handles["bad"] = &Handle{dir: filepath.Join(t.TempDir(), "gone"), fd: -1}

	updated, _ := m.Update(func(id string) limits.Limits {
		if id == "bad" {
			return limits.Limits{Memory: "not a size"}
		}
		return limits.Limits{Pids: "128"}
	})
	if updated != 1 {
		t.Errorf("the healthy panel should still be updated, got %d", updated)
	}
	if got := read(t, good, "pids.max"); got != "128" {
		t.Errorf("pids.max = %q", got)
	}
}
