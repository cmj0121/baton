package server

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/usage"
)

// liveTable is a liveRead that answers from a table keyed by launched session.
func liveTable(m map[string]string) func(string) (string, bool) {
	return func(launch string) (string, bool) {
		sid, ok := m[launch]
		return sid, ok
	}
}

// TestFollowLiveSessionAcrossClear is the fediqo panel: launched as s-launch, then
// /clear before the first turn, so s-launch never has a transcript and every figure
// keyed on it stays blank. The status line's record moves the panel onto the new
// session, and the SYS reading follows.
func TestFollowLiveSessionAcrossClear(t *testing.T) {
	f := &fakeOpening{table: map[string]int64{"s-cleared": 44_443}}
	srv := newOpeningServer(f, map[string][]string{"p1": {"s-launch"}})
	srv.launched = map[string]string{"p1": "s-launch"}
	srv.liveRead = liveTable(map[string]string{"s-launch": "s-cleared"})
	srv.refreshUsage()

	if got := srv.sessions["p1"]; !slices.Equal(got, []string{"s-launch", "s-cleared"}) {
		t.Errorf("sessions = %v, want the cleared session appended as current", got)
	}
	if got := srv.usageMsg().UsageInfo.Opening["p1"]; got != 44_443 {
		t.Errorf("p1 opening = %d, want the cleared session's 44443", got)
	}

	srv.refreshUsage() // the record says the same thing again
	if got := srv.sessions["p1"]; len(got) != 2 {
		t.Errorf("a repeated record was appended again: %v", got)
	}
}

// TestFollowLiveSessionResumeBack: /resume onto a session the panel already ran
// under moves it to current rather than listing it twice, so its spend is summed
// once.
func TestFollowLiveSessionResumeBack(t *testing.T) {
	srv := newOpeningServer(&fakeOpening{}, map[string][]string{"p1": {"s-launch", "s-cleared"}})
	srv.launched = map[string]string{"p1": "s-launch"}
	srv.liveRead = liveTable(map[string]string{"s-launch": "s-launch"})
	srv.followLiveSessions()

	if got := srv.sessions["p1"]; !slices.Equal(got, []string{"s-cleared", "s-launch"}) {
		t.Errorf("sessions = %v, want s-launch moved to current, once", got)
	}
}

// TestFollowLiveSessionSkipsARespawn: a panel respawned while the records were read
// has a new launch, and the old run's record must not move it anywhere.
func TestFollowLiveSessionSkipsARespawn(t *testing.T) {
	srv := newOpeningServer(&fakeOpening{}, map[string][]string{"p1": {"s-old"}})
	srv.launched = map[string]string{"p1": "s-old"}
	srv.liveRead = func(launch string) (string, bool) {
		srv.mu.Lock()
		srv.sessions["p1"] = append(srv.sessions["p1"], "s-new")
		srv.launched["p1"] = "s-new"
		srv.mu.Unlock()
		return "s-old-cleared", launch == "s-old"
	}
	srv.followLiveSessions()

	if got := srv.sessions["p1"]; !slices.Equal(got, []string{"s-old", "s-new"}) {
		t.Errorf("sessions = %v, want the respawn's s-new left current", got)
	}
}

// TestFollowLiveSessionOffWithoutState: a server with no state on disk has nowhere
// the records live, and follows nothing.
func TestFollowLiveSessionOffWithoutState(t *testing.T) {
	srv := newOpeningServer(&fakeOpening{}, map[string][]string{"p1": {"s-launch"}})
	srv.launched = map[string]string{"p1": "s-launch"}
	srv.followLiveSessions()
	if got := srv.sessions["p1"]; !slices.Equal(got, []string{"s-launch"}) {
		t.Errorf("sessions = %v, want untouched", got)
	}
}

// TestLiveSessionEndToEnd drives the real path: a Claude Code panel is launched
// with its status line pointed at a record named by its own session id, the sink's
// write is stood in for, and the next poll follows it and sweeps a stale record.
//
// The mutation that kills this: drop the live argument from startPanel's
// withStatusLine call, or key the record on anything but the launched session.
func TestLiveSessionEndToEnd(t *testing.T) {
	claudeSettingsDir(t, "")
	stateF := filepath.Join(t.TempDir(), "fleet.state.json")
	command, argv := claudeShim(t)
	s, dir := identityServer(t, WithStateFile(stateF), WithUsageLimits(nil, "/bin/baton"), WithAgentMCP(false))
	id, err := s.createPanel(originOperator, proto.KindAgent, command, nil, dir, "", false, false)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	args := launchedArgs(t, argv)
	i := slices.Index(args, sessionIDFlag)
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("no %s in %v", sessionIDFlag, args)
	}
	launch := args[i+1]
	liveDir := strings.TrimSuffix(stateF, ".state.json") + ".live"
	record := filepath.Join(liveDir, launch)
	if cmd := injectedStatusLine(t, args); !strings.HasSuffix(cmd, " --live "+shellQuote(record)) {
		t.Fatalf("status line %q does not record to %s", cmd, record)
	}

	const cleared = "5203df8f-33a0-4684-ab31-86d209ec751e"
	if err := usage.WriteLiveSession(record, cleared); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(liveDir, "0000dead-0000-4000-8000-000000000000")
	if err := usage.WriteLiveSession(stale, cleared); err != nil {
		t.Fatal(err)
	}
	s.followLiveSessions()

	s.mu.Lock()
	got := slices.Clone(s.sessions[id])
	s.mu.Unlock()
	if !slices.Equal(got, []string{launch, cleared}) {
		t.Errorf("sessions = %v, want [%s %s]", got, launch, cleared)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("a record for a launch no panel runs was not swept")
	}
	if _, err := os.Stat(record); err != nil {
		t.Errorf("the running panel's record was swept: %v", err)
	}
}
