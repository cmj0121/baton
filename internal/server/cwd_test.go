package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/cwd"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/ptymgr"
	"github.com/cmj0121/baton/internal/task"
)

func cwdServer(track cwd.Track, restore cwd.Restore, panels ...panel.Panel) *Server {
	mo, _ := newTestMonitor()
	if len(panels) == 0 {
		panels = []panel.Panel{{ID: "p1", Kind: panel.Shell, Title: "shell #1", State: panel.Running}}
	}
	s := &Server{
		pty:             ptymgr.New(),
		clients:         map[*clientConn]struct{}{},
		mon:             mo,
		panels:          panels,
		pendingDispatch: map[string][]byte{},
		tasks:           map[string]*task.Task{},
		panelTask:       map[string]string{},
		spawning:        map[string]bool{},
		specs:           map[string]spawnSpec{},
		osc7Tail:        map[string][]byte{},
		reportedCwd:     map[string]bool{},
		trackCwd:        track,
		restoreCwd:      restore,
	}
	// Stand in for the PTY manager's pid map: these panels have no live process,
	// so the pid the tests care about is the one they put on the struct.
	s.pidOf = func(id string) int {
		for _, p := range s.panels {
			if p.ID == id {
				return p.Pid
			}
		}
		return 0
	}
	return s
}

func osc7(host, path string) []byte { return []byte("\x1b]7;file://" + host + path + "\x07") }

func self() string { h, _ := os.Hostname(); return h }

// TestOSC7ReportIsRecorded: the shell says where it is and the panel remembers,
// with no syscall and no sampling.
func TestOSC7ReportIsRecorded(t *testing.T) {
	s := cwdServer(cwd.Auto, cwd.Shells)
	s.mu.Lock()
	s.noteOutputCwdLocked("p1", osc7(self(), "/tmp/here"))
	got, exempt := s.panels[0].Cwd, s.reportedCwd["p1"]
	s.mu.Unlock()

	if got != "/tmp/here" {
		t.Fatalf("Cwd = %q, want /tmp/here", got)
	}
	if !exempt {
		t.Error("a shell that reports its own directory should never be asked again")
	}
}

// TestOSC7FromAnotherHostIsIgnored: inside an ssh session the shell that speaks
// is the remote one. Believing its path would put a re-run in a same-named local
// directory — landing somewhere else in silence, the failure worth avoiding most.
func TestOSC7FromAnotherHostIsIgnored(t *testing.T) {
	s := cwdServer(cwd.Auto, cwd.Shells)
	s.mu.Lock()
	s.noteOutputCwdLocked("p1", osc7("some-other-box", "/srv/app"))
	got := s.panels[0].Cwd
	s.mu.Unlock()

	if got != "" {
		t.Fatalf("a remote host's directory was believed: %q", got)
	}
}

// TestOSC7SplitAcrossReads: the PTY hands over whatever has arrived, not whole
// sequences. A report seen half at a time is a report missed unless a tail is
// carried between reads.
func TestOSC7SplitAcrossReads(t *testing.T) {
	full := osc7(self(), "/tmp/split")
	cut := len(full) / 2

	s := cwdServer(cwd.Auto, cwd.Shells)
	s.mu.Lock()
	s.noteOutputCwdLocked("p1", full[:cut])
	mid := s.panels[0].Cwd
	s.noteOutputCwdLocked("p1", full[cut:])
	got := s.panels[0].Cwd
	s.mu.Unlock()

	if mid != "" {
		t.Errorf("half a report should say nothing yet, got %q", mid)
	}
	if got != "/tmp/split" {
		t.Fatalf("Cwd = %q, want the report reassembled across the two reads", got)
	}
}

// TestTrackOffIgnoresReports: "off" means the panel's directory stays the one it
// was spawned in, report or no report.
func TestTrackOffIgnoresReports(t *testing.T) {
	s := cwdServer(cwd.Off, cwd.Shells)
	s.mu.Lock()
	s.noteOutputCwdLocked("p1", osc7(self(), "/tmp/here"))
	got := s.panels[0].Cwd
	s.mu.Unlock()
	if got != "" {
		t.Fatalf("track-cwd off still recorded %q", got)
	}
}

// TestProcModeIgnoresReports: "proc" is for a fleet that does not trust the
// shell's report, so the report must not slip in through the back door.
func TestProcModeIgnoresReports(t *testing.T) {
	s := cwdServer(cwd.Proc, cwd.Shells)
	s.mu.Lock()
	s.noteOutputCwdLocked("p1", osc7(self(), "/tmp/here"))
	got := s.panels[0].Cwd
	s.mu.Unlock()
	if got != "" {
		t.Fatalf("proc mode recorded a shell report: %q", got)
	}
}

// TestRelativePathIsRejected: a report that is not an absolute path names no
// directory anyone can return to.
func TestRelativePathIsRejected(t *testing.T) {
	s := cwdServer(cwd.Auto, cwd.Shells)
	s.mu.Lock()
	s.setCwdLocked("p1", "relative/dir")
	got := s.panels[0].Cwd
	s.mu.Unlock()
	if got != "" {
		t.Fatalf("a relative path was recorded: %q", got)
	}
}

// TestIsLocalHost: an empty host is this terminal, and the two sides disagree
// about the domain suffix more often than about the machine.
func TestIsLocalHost(t *testing.T) {
	if !isLocalHost("") || !isLocalHost("localhost") || !isLocalHost("LOCALHOST") {
		t.Error("the local forms should all count as here")
	}
	if !isLocalHost(self()) {
		t.Errorf("this host (%q) should count as here", self())
	}
	if short, _, _ := strings.Cut(self(), "."); short != "" && !isLocalHost(short+".elsewhere.example") {
		t.Errorf("the first label should match: %q", short)
	}
	if isLocalHost("definitely-not-this-machine.example") {
		t.Error("a foreign host should not count as here")
	}
}

// TestWantsCwdSample: the process table is asked only for panels that have not
// reported, have a live process, and run under a mode that allows it.
func TestWantsCwdSample(t *testing.T) {
	s := cwdServer(cwd.Auto, cwd.Shells)
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.wantsCwdSampleLocked("p1", 1234) {
		t.Error("an unreported panel with a live process should be sampled")
	}
	if s.wantsCwdSampleLocked("p1", 0) {
		t.Error("a panel with no live process cannot be asked")
	}
	s.reportedCwd["p1"] = true
	if s.wantsCwdSampleLocked("p1", 1234) {
		t.Error("a panel that reports for itself should never be sampled")
	}
	s.trackCwd = cwd.OSC7
	if s.wantsCwdSampleLocked("p2", 1234) {
		t.Error("osc7 mode must cost no syscalls")
	}
}

// TestRespawnDirRestoresAShell: the half of the promise that only exists because
// both parts are here — the panel comes back where it was, not where it started.
func TestRespawnDirRestoresAShell(t *testing.T) {
	live := t.TempDir()
	s := cwdServer(cwd.Auto, cwd.Shells)
	s.mu.Lock()
	s.setCwdLocked("p1", live)
	s.mu.Unlock()

	dir, notice := s.respawnDir("p1", ptymgr.Spec{Dir: "/spawned/in"}, false)
	if dir != live || notice != "" {
		t.Fatalf("respawnDir = %q/%q, want the live directory and no notice", dir, notice)
	}
}

// TestRespawnDirLeavesAgentsAlone: an agent's task was set relative to the
// directory it was launched in; one that wandered into /tmp before dying should
// not come back in /tmp.
func TestRespawnDirLeavesAgentsAlone(t *testing.T) {
	live := t.TempDir()
	s := cwdServer(cwd.Auto, cwd.Shells)
	s.mu.Lock()
	s.setCwdLocked("p1", live)
	s.mu.Unlock()

	if dir, _ := s.respawnDir("p1", ptymgr.Spec{Dir: "/spawned/in"}, true); dir != "/spawned/in" {
		t.Fatalf("an agent was restored into %q under the shells-only default", dir)
	}
	// …unless the fleet asked for it.
	s.restoreCwd = cwd.All
	if dir, _ := s.respawnDir("p1", ptymgr.Spec{Dir: "/spawned/in"}, true); dir != live {
		t.Fatalf("restore-cwd: all should cover agents, got %q", dir)
	}
}

// TestRespawnDirSaysWhenTheDirectoryIsGone: a worktree that has been removed
// falls back to where the panel started — and says so. Coming back somewhere else
// in silence looks exactly like the restore having worked.
func TestRespawnDirSaysWhenTheDirectoryIsGone(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "removed")
	s := cwdServer(cwd.Auto, cwd.Shells)
	s.mu.Lock()
	s.setCwdLocked("p1", gone)
	s.mu.Unlock()

	dir, notice := s.respawnDir("p1", ptymgr.Spec{Dir: "/spawned/in"}, false)
	if dir != "/spawned/in" {
		t.Fatalf("dir = %q, want the fallback", dir)
	}
	if !strings.Contains(notice, gone) {
		t.Fatalf("the notice should name the directory that is gone: %q", notice)
	}
}

// TestRespawnDirOffAlwaysUsesTheSpawnDir: "off" is the setting for anyone who
// wants a re-run to be exactly the original launch.
func TestRespawnDirOffAlwaysUsesTheSpawnDir(t *testing.T) {
	live := t.TempDir()
	s := cwdServer(cwd.Auto, cwd.NoRestore)
	s.mu.Lock()
	s.setCwdLocked("p1", live)
	s.mu.Unlock()

	if dir, notice := s.respawnDir("p1", ptymgr.Spec{Dir: "/spawned/in"}, false); dir != "/spawned/in" || notice != "" {
		t.Fatalf("respawnDir = %q/%q, want the spawn dir silently", dir, notice)
	}
}

// TestTargetDirFollowsTheAgent: the git and diff menus target where the agent is
// now, so they follow it into a worktree instead of staying pinned to the launch
// directory.
func TestTargetDirFollowsTheAgent(t *testing.T) {
	live := t.TempDir()
	s := cwdServer(cwd.Auto, cwd.Shells)
	s.mu.Lock()
	s.setCwdLocked("p1", live)
	s.mu.Unlock()

	if got := s.targetDir("p1", ptymgr.Spec{Dir: "/spawned/in"}); got != live {
		t.Fatalf("targetDir = %q, want the live directory %q", got, live)
	}
	// A directory that has gone falls back rather than handing git a bad path.
	s.mu.Lock()
	s.setCwdLocked("p1", filepath.Join(t.TempDir(), "removed"))
	s.mu.Unlock()
	if got := s.targetDir("p1", ptymgr.Spec{Dir: "/spawned/in"}); got != "/spawned/in" {
		t.Fatalf("targetDir = %q, want the spawn dir once the live one is gone", got)
	}
}

// TestPanelCwdSamplesTheProcessTable: a panel that has reported nothing is
// answered for from the process table — and only when something asks.
func TestPanelCwdSamplesTheProcessTable(t *testing.T) {
	s := cwdServer(cwd.Auto, cwd.Shells, panel.Panel{ID: "p1", Kind: panel.Shell, State: panel.Running, Pid: os.Getpid()})

	got := s.panelCwd("p1")
	wd, _ := os.Getwd()
	want, err := filepath.EvalSymlinks(wd)
	if err != nil {
		want = wd
	}
	if got != want {
		t.Fatalf("panelCwd = %q, want this process's directory %q", got, want)
	}

	// The sample is remembered, so the next ask costs nothing.
	s.mu.Lock()
	cached := s.panels[0].Cwd
	s.mu.Unlock()
	if cached != want {
		t.Errorf("the sample was not recorded: %q", cached)
	}
}

// TestPanelCwdOSC7ModeDoesNotSample: "osc7" means no syscalls at all, so a shell
// that reports nothing simply has no known directory.
func TestPanelCwdOSC7ModeDoesNotSample(t *testing.T) {
	s := cwdServer(cwd.OSC7, cwd.Shells, panel.Panel{ID: "p1", Kind: panel.Shell, State: panel.Running, Pid: os.Getpid()})
	if got := s.panelCwd("p1"); got != "" {
		t.Fatalf("osc7 mode sampled the process table: %q", got)
	}
}

// TestPanelCwdUnknownPanel: asking about a panel that is gone answers nothing
// rather than guessing.
func TestPanelCwdUnknownPanel(t *testing.T) {
	s := cwdServer(cwd.Auto, cwd.Shells)
	if got := s.panelCwd("nope"); got != "" {
		t.Fatalf("panelCwd of an unknown panel = %q", got)
	}
}

// TestEveryGitTargetFollowsTheAgent: the git menu resolves its directory in five
// places — the per-file diff, a captured op, an op opened in a pane, and the two
// worktree ops. A menu where "log" follows the agent into a worktree but "commit"
// does not is worse than one that follows nowhere, so this pins that none of them
// is left behind.
func TestEveryGitTargetFollowsTheAgent(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "ptymgr.PanelDir(spec.Dir)"); n != 0 {
		t.Errorf("%d git target(s) still resolve the spawn directory directly", n)
	}
	if n := strings.Count(string(src), "s.targetDir(targetID, spec.Spec)"); n != 5 {
		t.Errorf("%d targets resolve through targetDir, want all 5", n)
	}
}
