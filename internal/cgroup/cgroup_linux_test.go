//go:build linux

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

// enforcingManager is a real manager on this host, or a skip. A CI container or
// a session without a delegated cgroup subtree cannot enforce, and that is a
// legitimate environment rather than a failure — the manager is expected to say
// so, which the assertions below check before skipping.
func enforcingManager(t *testing.T) *Manager {
	t.Helper()
	m := New()
	m.Probe()
	if m.Mode() == ModeNone {
		if m.Reason() == "" {
			t.Fatal("a degraded manager must explain itself, so the daemon can report it")
		}
		t.Skipf("no cgroup v2 enforcement here: %s", m.Reason())
	}
	if m.root == "" {
		t.Fatal("an enforcing manager needs a root cgroup to create panels under")
	}
	return m
}

// TestDetectRootPreparesADelegatedSubtree checks what detectRoot leaves behind:
// a baton.slice whose parent is free of processes and whose own subtree_control
// offers the panels their controllers — the shape cgroup v2's "no internal
// processes" rule forces, since a cgroup cannot both hold processes and enable
// controllers for its children.
func TestDetectRootPreparesADelegatedSubtree(t *testing.T) {
	m := enforcingManager(t)

	if filepath.Base(m.root) != batonSlice {
		t.Errorf("the panel root should be %s, got %s", batonSlice, m.root)
	}
	if hasProcs(filepath.Dir(m.root)) {
		t.Errorf("%s still holds processes; it cannot enable controllers for baton.slice", filepath.Dir(m.root))
	}
	if hasProcs(m.root) {
		t.Errorf("%s holds processes directly; panels are its leaves", m.root)
	}
	enabled, err := os.ReadFile(filepath.Join(m.root, "cgroup.subtree_control"))
	if err != nil {
		t.Fatalf("read subtree_control: %v", err)
	}
	if len(strings.Fields(string(enabled))) == 0 {
		t.Error("the panel root should have at least one controller enabled")
	}
}

// TestDetectRootIsIdempotent guards the re-probe: a daemon that migrated itself
// into baton.slice/daemon must not, on the next probe, treat that as its home and
// nest a second baton.slice inside the first.
func TestDetectRootIsIdempotent(t *testing.T) {
	m := enforcingManager(t)

	again, err := detectRoot()
	if err != nil {
		t.Fatalf("a second probe should find the same subtree: %v", err)
	}
	if again != m.root {
		t.Fatalf("re-probing nested a new root: %s → %s", m.root, again)
	}
	if strings.Count(again, batonSlice) != 1 {
		t.Fatalf("the root should hold one baton.slice, got %s", again)
	}
}

// TestPrepareWritesTheCapsAndConfinesTheChild is the end-to-end proof: a policy
// becomes real control files, and a process forked with the handle's Confine
// hook is BORN inside that cgroup — read back from the child's own view of its
// membership, not from the parent's bookkeeping.
func TestPrepareWritesTheCapsAndConfinesTheChild(t *testing.T) {
	m := enforcingManager(t)

	h, err := m.Prepare("cgtest", limits.Limits{CPUs: "1", Memory: "512Mi", Pids: "64"})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if h == nil {
		t.Fatal("a policy with caps should have produced a handle")
	}
	t.Cleanup(func() { m.Release("cgtest") })

	if got := read(t, h.Dir(), "cpu.max"); got != "100000 100000" && !slices.Contains(h.Skipped(), "cpu.max") {
		t.Errorf("cpu.max = %q", got)
	}

	cmd := exec.Command("/bin/cat", "/proc/self/cgroup")
	if err := h.Confine(cmd); err != nil {
		t.Fatalf("Confine: %v", err)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run the confined child: %v", err)
	}
	if !strings.Contains(string(out), "panel-cgtest") {
		t.Fatalf("the child should have been born inside its cgroup, got %q", out)
	}
}

// TestReleaseRemovesTheCgroup checks the teardown against a real cgroupfs, where
// removal is an rmdir the kernel accepts only once the group is empty.
func TestReleaseRemovesTheCgroup(t *testing.T) {
	m := enforcingManager(t)

	h, err := m.Prepare("cgtemp", limits.Limits{Pids: "64"})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	dir := h.Dir()
	m.Release("cgtemp")

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("Release should have removed %s, stat err = %v", dir, err)
	}
}

// TestStripDaemonLeaf covers the re-probe guard in isolation: only baton's own
// daemon leaf is walked back off, and any other path is left alone.
func TestStripDaemonLeaf(t *testing.T) {
	tests := map[string]string{
		"/user.slice/user@1000.service/baton.slice/daemon": "/user.slice/user@1000.service",
		"/baton.slice/daemon":                              "/",
		"/user.slice/app.scope":                            "/user.slice/app.scope",
		"/baton.slice/panel-3":                             "/baton.slice/panel-3",
		"/somewhere/daemon":                                "/somewhere/daemon",
		"/":                                                "/",
	}
	for in, want := range tests {
		if got := stripDaemonLeaf(in); got != want {
			t.Errorf("stripDaemonLeaf(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestOwnCgroupReadsTheUnifiedLine checks the daemon can locate itself: every
// Linux with cgroup v2 carries a "0::<path>" line for this process.
func TestOwnCgroupReadsTheUnifiedLine(t *testing.T) {
	own, err := ownCgroup()
	if err != nil {
		t.Skipf("no cgroup v2 membership here: %v", err)
	}
	if !strings.HasPrefix(own, "/") {
		t.Errorf("a cgroup path should be absolute within the mount, got %q", own)
	}
}

// TestHasProcs covers the occupancy check the "no internal processes" rule turns
// on, including its deliberate bias: a cgroup we cannot read counts as occupied,
// since acting on one we cannot inspect is how the rule gets violated.
func TestHasProcs(t *testing.T) {
	dir := t.TempDir()
	if !hasProcs(dir) {
		t.Error("a directory with no cgroup.procs at all should read as occupied")
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if hasProcs(dir) {
		t.Error("an empty cgroup.procs means no processes")
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte("42\n77\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasProcs(dir) {
		t.Error("a populated cgroup.procs means processes")
	}
}

// TestEnableControllers drives the subtree_control write against a stand-in
// cgroup: the controllers the host offers are turned on, ones already enabled by
// an ancestor count without a write, and a host offering none is an error rather
// than a silent success.
func TestEnableControllers(t *testing.T) {
	dir := t.TempDir()
	write := func(file, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("cgroup.controllers", "cpuset cpu memory pids\n")
	write("cgroup.subtree_control", "")
	if err := enableControllers(dir); err != nil {
		t.Fatalf("enableControllers: %v", err)
	}
	if got := read(t, dir, "cgroup.subtree_control"); got != "+pids" {
		t.Errorf("each controller is written on its own; the last write should be +pids, got %q", got)
	}

	// Already enabled upstream: nothing to write, still a success.
	write("cgroup.subtree_control", "cpu memory pids")
	if err := enableControllers(dir); err != nil {
		t.Errorf("controllers already enabled should not be an error: %v", err)
	}

	// A host offering only cpuset cannot cap anything baton asks for.
	write("cgroup.controllers", "cpuset\n")
	write("cgroup.subtree_control", "")
	if err := enableControllers(dir); err == nil {
		t.Error("a host with none of the wanted controllers should be reported")
	}
	if err := enableControllers(t.TempDir()); err == nil {
		t.Error("a directory that is not a cgroup should be reported")
	}
}

// TestPrepareUnderRejectsUnusableParents covers the two ways an ancestor is
// turned down during the walk, which is what makes the walk continue upwards.
func TestPrepareUnderRejectsUnusableParents(t *testing.T) {
	// Not delegated: a path baton cannot create a child under at all.
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareUnder(notADir); err == nil || !strings.Contains(err.Error(), "not delegated") {
		t.Errorf("an undelegated parent should be reported as such, got %v", err)
	}

	// Occupied by someone else: the daemon moves itself out, the cgroup is still
	// not empty, so this ancestor cannot enable controllers for baton.slice.
	busy := t.TempDir()
	if err := os.WriteFile(filepath.Join(busy, "cgroup.procs"), []byte("1\n2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := prepareUnder(busy)
	if err == nil || !strings.Contains(err.Error(), "other processes") {
		t.Errorf("an occupied parent should be reported as such, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(busy, batonSlice, daemonLeaf, "cgroup.procs")); statErr != nil {
		t.Errorf("the daemon should have tried to move out first: %v", statErr)
	}
}

// TestHandleFDLifecycle covers the descriptor the confinement rides on, against
// a plain directory: taking it, using it, and dropping it.
func TestHandleFDLifecycle(t *testing.T) {
	h := &Handle{dir: t.TempDir(), fd: -1}
	if err := h.open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if h.fd < 0 {
		t.Fatal("open should have taken a descriptor")
	}

	cmd := &exec.Cmd{}
	if err := h.Confine(cmd); err != nil {
		t.Fatalf("Confine: %v", err)
	}
	if !cmd.SysProcAttr.UseCgroupFD || cmd.SysProcAttr.CgroupFD != h.fd {
		t.Errorf("Confine should hand the fork the cgroup descriptor, got %+v", cmd.SysProcAttr)
	}

	if err := h.closeFD(); err != nil {
		t.Errorf("closeFD: %v", err)
	}
	if err := h.closeFD(); err != nil {
		t.Errorf("closing twice should be safe: %v", err)
	}
	closed := &exec.Cmd{}
	if err := h.Confine(closed); err != nil {
		t.Fatalf("Confine: %v", err)
	}
	if closed.SysProcAttr != nil && closed.SysProcAttr.UseCgroupFD {
		t.Error("a closed handle must not hand out a stale descriptor")
	}
}
