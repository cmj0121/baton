package server

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/panellog"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
)

// logServer builds an in-process server with logging pointed at a temp directory
// and the given panels already in the fleet, so the lifecycle can be driven
// without a real PTY or a wall clock.
func logServer(t *testing.T, panels ...panel.Panel) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	s, _, _ := gateServer(panels...)
	s.acked = map[string]time.Time{}
	s.logs = map[string]*panellog.Sink{}
	s.groupShown = map[string]int{}
	s.groupLayout = map[string]string{}
	s.groupFavourite = map[string]bool{}
	s.logDir = dir
	s.logMaxBytes = panellog.MaxBytes(0)
	for _, p := range panels {
		s.specs[p.ID] = spawnSpec{Spec: ptymgr.Spec{Dir: dir}}
	}
	return s, dir
}

// logBody is the whole text of a panel's log, failing the test when there is none.
func logBody(t *testing.T, s *Server, id string) string {
	t.Helper()
	path := s.LogPath(id)
	if path == "" {
		t.Fatalf("panel %s is not being logged", id)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestToggleLogWritesOutput is the everyday path: the key starts logging, live
// output lands in the file with its escape sequences stripped, and the key again
// stops it.
func TestToggleLogWritesOutput(t *testing.T) {
	s, _ := logServer(t, panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running, Title: "claude"})
	cc := ctl("")

	if err := s.toggleLog(cc, "p1"); err != nil {
		t.Fatalf("toggleLog on: %v", err)
	}
	if got := replyErr(cc); got != "" {
		t.Fatalf("toggleLog replied with an error: %s", got)
	}
	path := s.LogPath("p1")
	if path == "" {
		t.Fatalf("the panel should be logging after the first press")
	}

	s.routeOutput("p1", []byte("\x1b[32mbuild ok\x1b[0m\r\n"))
	if body := logBody(t, s, "p1"); !strings.Contains(body, "build ok\n") {
		t.Errorf("live output did not reach the log:\n%s", body)
	} else if strings.Contains(body, "\x1b") {
		t.Errorf("escape sequences reached the log:\n%q", body)
	}

	if err := s.toggleLog(cc, "p1"); err != nil {
		t.Fatalf("toggleLog off: %v", err)
	}
	if s.LogPath("p1") != "" {
		t.Fatalf("the second press should stop logging")
	}
	// Output after the stop goes nowhere, and the file says why it ended.
	s.routeOutput("p1", []byte("after the stop\n"))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if body := string(data); strings.Contains(body, "after the stop") {
		t.Errorf("output kept flowing after logging was switched off:\n%s", body)
	} else if !strings.Contains(body, logMarkStopped) {
		t.Errorf("the log should close with a marker saying why:\n%s", body)
	}
}

// TestLoggingSurvivesTheSnapshot checks the badge's data path: a logging panel
// carries the flag and the file on every fleet snapshot, so a cockpit that
// attaches later still sees it — which is what makes the state survive a
// detach/reattach.
func TestLoggingSurvivesTheSnapshot(t *testing.T) {
	s, _ := logServer(t, panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running, Title: "claude"})
	if _, err := s.startLogging("p1"); err != nil {
		t.Fatalf("startLogging: %v", err)
	}
	msg := s.panelsMsg()
	if len(msg.Panels) != 1 {
		t.Fatalf("snapshot has %d panels; want 1", len(msg.Panels))
	}
	if !msg.Panels[0].Logging || msg.Panels[0].LogPath == "" {
		t.Errorf("the snapshot must carry the logging state, got %+v", msg.Panels[0])
	}
	s.stopLogging("p1", logMarkStopped)
	if msg := s.panelsMsg(); msg.Panels[0].Logging {
		t.Errorf("the flag must fall away when logging stops")
	}
}

// TestLoggingRefusedWithoutADirectory covers the empty-log-dir case: the feature
// is off until a destination is named, and pressing the key says so rather than
// appearing to work.
func TestLoggingRefusedWithoutADirectory(t *testing.T) {
	s, _ := logServer(t, panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running, Title: "claude"})
	s.logDir = ""

	err := s.toggleLog(ctl(""), "p1")
	if err == nil {
		t.Fatalf("logging with no directory configured should be refused")
	}
	if !strings.Contains(err.Error(), "log-dir") {
		t.Errorf("the refusal should name the key to set, got %q", err)
	}
	if s.LogPath("p1") != "" {
		t.Errorf("nothing should have been opened")
	}
}

// TestPerProfileLogDirWins checks the override the resource limits already model:
// a profile that names its own directory writes there, one that does not inherits
// the fleet-wide destination.
func TestPerProfileLogDirWins(t *testing.T) {
	s, fleet := logServer(t,
		panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running, Title: "claude"},
		panel.Panel{ID: "p2", Kind: panel.Agent, State: panel.Running, Title: "copilot"},
	)
	own := t.TempDir()
	s.agentLogDir = map[string]string{"claude": own}
	s.specs["p1"] = spawnSpec{Profile: "claude"}
	s.specs["p2"] = spawnSpec{Profile: "copilot"}

	for _, id := range []string{"p1", "p2"} {
		if _, err := s.startLogging(id); err != nil {
			t.Fatalf("startLogging %s: %v", id, err)
		}
	}
	if got := s.LogPath("p1"); !strings.HasPrefix(got, own) {
		t.Errorf("the profile's own log-dir should win, got %q want under %q", got, own)
	}
	if got := s.LogPath("p2"); !strings.HasPrefix(got, fleet) {
		t.Errorf("a profile with no override should inherit, got %q want under %q", got, fleet)
	}
}

// TestAutoLogFromSpawn covers panel.agents.<name>.log: true — the profile logs
// from the moment it spawns, without anyone pressing a key.
func TestAutoLogFromSpawn(t *testing.T) {
	s, _ := logServer(t,
		panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Spawning, Title: "claude"},
		panel.Panel{ID: "p2", Kind: panel.Shell, State: panel.Spawning, Title: "shell"},
	)
	s.agentLog = map[string]bool{"claude": true}

	s.autoLog("p1", "claude")
	s.autoLog("p2", "") // a shell panel takes no profile, so it is never auto-logged
	if s.LogPath("p1") == "" {
		t.Errorf("a profile configured to log should log from spawn")
	}
	if s.LogPath("p2") != "" {
		t.Errorf("a shell must not be dragged into a per-agent setting")
	}
}

// TestAutoLogSurvivesAnUnwritableDir is decision 2: a destination that cannot be
// written does not fail the spawn. The agent is the point and the log is the
// accessory, so the panel comes up unlogged rather than not at all.
func TestAutoLogSurvivesAnUnwritableDir(t *testing.T) {
	s, dir := logServer(t, panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Spawning, Title: "claude"})
	blocked := dir + "/afile"
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	s.logDir = blocked + "/logs"
	s.agentLog = map[string]bool{"claude": true}

	s.autoLog("p1", "claude") // must not panic, must not block
	if s.LogPath("p1") != "" {
		t.Errorf("an unwritable destination must leave the panel unlogged, got %q", s.LogPath("p1"))
	}
}

// TestRespawnAppendsUnderANewSession is the respawn contract: the previous run is
// usually why you are reading the file, so it stays, and the new run opens under a
// marker of its own.
func TestRespawnAppendsUnderANewSession(t *testing.T) {
	s, _ := logServer(t, panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running, Title: "claude"})
	if _, err := s.startLogging("p1"); err != nil {
		t.Fatalf("startLogging: %v", err)
	}
	path := s.LogPath("p1")
	s.routeOutput("p1", []byte("first run\n"))

	s.suspendLog("p1", 1)
	s.routeOutput("p1", []byte("while dead\n")) // the process is gone; nothing to record
	s.resumeLog("p1")
	s.routeOutput("p1", []byte("second run\n"))

	body := logBody(t, s, "p1")
	if s.LogPath("p1") != path {
		t.Errorf("a re-run must stay in the same file: %q then %q", path, s.LogPath("p1"))
	}
	for _, want := range []string{"first run\n", logMarkExited + " (code 1)", logMarkRestart, "second run\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("log is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "while dead") {
		t.Errorf("output after the process exited must not be recorded:\n%s", body)
	}
	if i, j := strings.Index(body, "first run"), strings.Index(body, "second run"); i < 0 || j < 0 || i > j {
		t.Errorf("the re-run must APPEND, not truncate:\n%s", body)
	}
}

// TestShutdownClosesEveryLog checks the daemon's sweep: no transcript is left
// open, and each says why it ended.
func TestShutdownClosesEveryLog(t *testing.T) {
	s, _ := logServer(t,
		panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running, Title: "claude"},
		panel.Panel{ID: "p2", Kind: panel.Agent, State: panel.Running, Title: "copilot"},
	)
	paths := map[string]string{}
	for _, id := range []string{"p1", "p2"} {
		if _, err := s.startLogging(id); err != nil {
			t.Fatalf("startLogging %s: %v", id, err)
		}
		paths[id] = s.LogPath(id)
	}
	s.closeAllLogs(logMarkShutdown)

	if len(s.logs) != 0 {
		t.Errorf("open logs after shutdown = %d; want 0", len(s.logs))
	}
	for id, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(data), logMarkShutdown) {
			t.Errorf("%s: the log should say the daemon went down:\n%s", id, data)
		}
	}
}

// TestClosingAPanelFinishesItsLog checks that retiring a panel finishes its
// transcript rather than abandoning it mid-line.
func TestClosingAPanelFinishesItsLog(t *testing.T) {
	s, _ := logServer(t, panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running, Title: "claude"})
	if _, err := s.startLogging("p1"); err != nil {
		t.Fatalf("startLogging: %v", err)
	}
	path := s.LogPath("p1")
	if err := s.closePanel("p1"); err != nil {
		t.Fatalf("closePanel: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), logMarkClosed) {
		t.Errorf("closing a panel should finish its log:\n%s", data)
	}
	if s.LogPath("p1") != "" {
		t.Errorf("the sink should be forgotten with the panel")
	}
}

// TestLogViewNeedsALog checks the read-back key's precondition: there is nothing
// to follow until something is being written, and the message says which key
// starts it.
func TestLogViewNeedsALog(t *testing.T) {
	s, _ := logServer(t, panel.Panel{ID: "p1", Kind: panel.Agent, State: panel.Running, Title: "claude"})
	err := s.openLogView(ctl(""), "p1")
	if err == nil || !strings.Contains(err.Error(), "not being logged") {
		t.Errorf("openLogView on an unlogged panel = %v; want a refusal saying so", err)
	}
	if err := s.openLogView(ctl(""), ""); err == nil {
		t.Errorf("openLogView with no id should be refused")
	}
	if err := s.toggleLog(ctl(""), ""); err == nil {
		t.Errorf("panel.log with no id should be refused")
	}
	if _, err := s.startLogging("nope"); err == nil {
		t.Errorf("logging an unknown panel should be refused")
	}
}

// TestConductorMayNotLog is decision 1: logging is an operator surface, on the
// same terms as the inbox. A conductor asking the daemon to write files is the
// case the fence exists for.
func TestConductorMayNotLog(t *testing.T) {
	s, _ := logServer(t,
		panel.Panel{ID: "c1", Kind: panel.Agent, State: panel.Running, Conductor: true},
		panel.Panel{ID: "w1", Kind: panel.Agent, State: panel.Idle},
	)
	cc := ctl("c1")
	cc.role = roleConductor

	for _, action := range []string{"panel.log", "panel.logview"} {
		for _, id := range []string{"c1", "w1"} {
			if reason := s.guardConductor(cc, proto.Command{Action: action, ID: id}); reason == "" {
				t.Errorf("%s on %s: a conductor must not reach panel logging", action, id)
			}
		}
	}
	// A cockpit connection is never fenced by the same rule.
	if reason := s.guardConductor(ctl(""), proto.Command{Action: "panel.log", ID: "w1"}); reason != "" {
		t.Errorf("a cockpit connection must not be fenced, got %q", reason)
	}
}

// TestFollowCommandPrefersLess documents the read-back command: less +F follows
// the file the way tail does but lets you stop and page back through it, which is
// the half that makes a still-running panel's log readable.
func TestFollowCommandPrefersLess(t *testing.T) {
	name, args := followCommand("/tmp/a.log")
	switch {
	case strings.HasSuffix(name, "less"):
		if len(args) != 2 || args[0] != "+F" || args[1] != "/tmp/a.log" {
			t.Errorf("less args = %v; want [+F /tmp/a.log]", args)
		}
	case name == "tail":
		if len(args) != 2 || args[0] != "-f" {
			t.Errorf("tail args = %v; want [-f /tmp/a.log]", args)
		}
	default:
		t.Errorf("followCommand = %q; want less or tail", name)
	}
}
