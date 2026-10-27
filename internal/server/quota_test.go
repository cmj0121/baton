package server

import (
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/ptymgr"
	"github.com/cmj0121/baton/internal/task"
	"github.com/cmj0121/baton/internal/usage"
)

func claudePanel(id string) panel.Panel {
	return panel.Panel{ID: id, Kind: panel.Agent, State: panel.Idle}
}

func withCommand(s *Server, id, command string) {
	s.specs[id] = spawnSpec{Spec: ptymgr.Spec{Command: command}, Profile: "reviewer"}
}

func claudeLimits(used5h, used7d float64) *proto.LimitsInfo {
	info := &proto.LimitsInfo{}
	if used5h >= 0 {
		info.FiveHour = &proto.LimitWindow{UsedPercent: used5h}
	}
	if used7d >= 0 {
		info.SevenDay = &proto.LimitWindow{UsedPercent: used7d}
	}
	return info
}

func TestPauseBelowOffDoesNotHold(t *testing.T) {
	s, _, written := gateServer(claudePanel("c"))
	withCommand(s, "c", "/usr/local/bin/claude")
	s.limitsInfo = claudeLimits(99, 99)
	if _, err := s.enqueueTask(conn(""), "do it", "", nil); err != nil {
		t.Fatal(err)
	}
	s.monitorTick()
	if submittedLines(*written) != 1 {
		t.Fatalf("unset pause-below must not pause, wrote %v", *written)
	}
}

func TestLowClaudeQuotaHoldsTheStandingAgent(t *testing.T) {
	s, _, written := gateServer(claudePanel("c"))
	withCommand(s, "c", "claude")
	s.pauseBelow = 0.2
	s.limitsInfo = claudeLimits(95, 10) // remaining 0.05 and 0.90; the minimum holds
	if _, err := s.enqueueTask(conn(""), "do it", "", &task.SpawnSpec{Command: "claude"}); err != nil {
		t.Fatal(err)
	}
	s.monitorTick()
	if len(*written) != 0 {
		t.Fatalf("a held standing agent must not be assigned, wrote %v", *written)
	}
	if tk := s.tasks["t1"]; tk.Panel != "" || tk.Status != task.Queued {
		t.Fatalf("the task should stay queued, got %+v", tk)
	}
	if s.spawning["t1"] {
		t.Fatal("a held standing agent must not fall through to spawn")
	}
	if !s.quotaHold["claude"] {
		t.Fatal("entering the hold should be remembered so the log is once")
	}
}

func TestClaudeHoldUsesTheMinimumWindowAndIgnoresModelCeilings(t *testing.T) {
	s, _, _ := gateServer(claudePanel("c"))
	withCommand(s, "c", "claude")
	s.pauseBelow = 0.2
	s.limitsInfo = &proto.LimitsInfo{
		FiveHour:       &proto.LimitWindow{UsedPercent: 10}, // remaining 0.90
		SevenDayOpus:   &proto.LimitWindow{UsedPercent: 99},
		SevenDaySonnet: &proto.LimitWindow{UsedPercent: 99},
	}
	if _, err := s.enqueueTask(conn(""), "do it", "", nil); err != nil {
		t.Fatal(err)
	}
	s.monitorTick()
	if s.tasks["t1"].Panel != "c" {
		t.Fatal("opus/sonnet ceilings must not pause claude when 5h still has room")
	}

	s2, _, written := gateServer(claudePanel("c"))
	withCommand(s2, "c", "claude")
	s2.pauseBelow = 0.2
	s2.limitsInfo = claudeLimits(10, 80) // remaining 0.90 and 0.20; 0.20 is not under the line
	if _, err := s2.enqueueTask(conn(""), "do it", "", nil); err != nil {
		t.Fatal(err)
	}
	s2.monitorTick()
	if submittedLines(*written) != 1 {
		t.Fatal("remaining equal to the line must not pause")
	}
}

func TestNoReadingDoesNotPause(t *testing.T) {
	s, _, written := gateServer(claudePanel("c"))
	withCommand(s, "c", "claude")
	s.pauseBelow = 0.5
	if _, err := s.enqueueTask(conn(""), "do it", "", nil); err != nil {
		t.Fatal(err)
	}
	s.monitorTick()
	if submittedLines(*written) != 1 {
		t.Fatal("claude with no limits reading must still drain")
	}
}

func TestGrokPausesOnItsOwnWeekOnly(t *testing.T) {
	s, clk, written := gateServer(
		claudePanel("c"),
		panel.Panel{ID: "g", Kind: panel.Agent, State: panel.Idle},
	)
	withCommand(s, "c", "claude")
	withCommand(s, "g", "grok")
	s.pauseBelow = 0.2
	s.limitsInfo = claudeLimits(10, 10)
	s.usageInfo = &proto.UsageInfo{Vendors: []proto.VendorUsage{{
		Vendor: "grok",
		State:  string(usage.VendorReading),
		Windows: []proto.VendorWindow{
			{Label: usage.WindowSpend, UsedPercent: 99},
			{Label: usage.WindowFiveHour, UsedPercent: 99},
			{Label: usage.WindowWeek, UsedPercent: 96},
		},
	}}}
	if _, err := s.enqueueTask(conn(""), "claude work", "", nil); err != nil {
		t.Fatal(err)
	}
	clk.add(time.Second) // the older brief drains first; equal clocks make the order undefined
	if _, err := s.enqueueTask(conn(""), "grok work", "", nil); err != nil {
		t.Fatal(err)
	}
	s.monitorTick()
	if !typedAgentLine(*written, "c", "claude work") {
		t.Fatalf("claude should drain while grok's 7d is held, wrote %v", *written)
	}
	var queued int
	for _, tk := range s.tasks {
		if tk.Status == task.Queued {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("the grok task should stay queued, queued=%d", queued)
	}

	// A 7d window published under another name is not grok's reading.
	s2, _, wrote2 := gateServer(panel.Panel{ID: "g", Kind: panel.Agent, State: panel.Idle})
	withCommand(s2, "g", "/opt/bin/grok")
	s2.pauseBelow = 0.2
	s2.usageInfo = &proto.UsageInfo{Vendors: []proto.VendorUsage{{
		Vendor:  "reviewer",
		State:   string(usage.VendorReading),
		Windows: []proto.VendorWindow{{Label: usage.WindowWeek, UsedPercent: 99}},
	}}}
	if _, err := s2.enqueueTask(conn(""), "do it", "", nil); err != nil {
		t.Fatal(err)
	}
	s2.monitorTick()
	if submittedLines(*wrote2) != 1 {
		t.Fatal("a 7d figure that is not on the grok reading must not pause grok")
	}
}

func TestGrokSpendWindowDoesNotPause(t *testing.T) {
	s, _, written := gateServer(panel.Panel{ID: "g", Kind: panel.Agent, State: panel.Idle})
	withCommand(s, "g", "grok")
	s.pauseBelow = 0.2
	s.usageInfo = &proto.UsageInfo{Vendors: []proto.VendorUsage{{
		Vendor:  "grok",
		State:   string(usage.VendorReading),
		Windows: []proto.VendorWindow{{Label: usage.WindowSpend, UsedPercent: 99}},
	}}}
	if _, err := s.enqueueTask(conn(""), "do it", "", nil); err != nil {
		t.Fatal(err)
	}
	s.monitorTick()
	if submittedLines(*written) != 1 {
		t.Fatal("a spend window is not a quota ceiling")
	}
}

func TestHeldSpawnDoesNotStartAndAnotherVendorDoes(t *testing.T) {
	s, _, _ := gateServer()
	s.pauseBelow = 0.2
	s.limitsInfo = claudeLimits(95, -1)
	if _, err := s.enqueueTask(conn(""), "claude", "", &task.SpawnSpec{Command: "claude"}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	_, spawns := s.scheduleLocked()
	s.mu.Unlock()
	if len(spawns) != 0 || s.spawning["t1"] {
		t.Fatalf("a held spawn must stay queued, spawns=%d spawning=%v", len(spawns), s.spawning["t1"])
	}

	s2, _, _ := gateServer()
	s2.pauseBelow = 0.2
	s2.limitsInfo = claudeLimits(95, -1)
	if _, err := s2.enqueueTask(conn(""), "grok", "", &task.SpawnSpec{Command: "/usr/bin/grok"}); err != nil {
		t.Fatal(err)
	}
	s2.mu.Lock()
	_, spawns = s2.scheduleLocked()
	s2.mu.Unlock()
	if len(spawns) != 1 || spawns[0].spec.Command != "/usr/bin/grok" {
		t.Fatalf("grok should still spawn while claude is held, got %+v", spawns)
	}
}

func TestInFlightTurnIsNotPulledBack(t *testing.T) {
	s, _, _ := gateServer(claudePanel("c"))
	withCommand(s, "c", "claude")
	if _, err := s.enqueueTask(conn(""), "running", "", nil); err != nil {
		t.Fatal(err)
	}
	s.monitorTick()
	if s.tasks["t1"].Status != task.Dispatched {
		t.Fatal("setup: the first task should be in flight")
	}
	s.pauseBelow = 0.2
	s.limitsInfo = claudeLimits(99, 99)
	if _, err := s.enqueueTask(conn(""), "next", "", nil); err != nil {
		t.Fatal(err)
	}
	s.monitorTick()
	if s.tasks["t1"].Panel != "c" || s.tasks["t1"].Status != task.Dispatched {
		t.Fatalf("an in-flight turn must keep running, got %+v", s.tasks["t1"])
	}
	if s.tasks["t2"].Status != task.Queued {
		t.Fatalf("the next task waits, got %+v", s.tasks["t2"])
	}
}

func TestReloadAdoptsPauseBelow(t *testing.T) {
	s, _, _ := gateServer()
	s.Reload(Settings{PauseBelow: 0.3, QueueMax: -1})
	if s.pauseBelow != 0.3 {
		t.Fatalf("reload should adopt 0.3, got %v", s.pauseBelow)
	}
	s.Reload(Settings{PauseBelow: 1.5, QueueMax: -1})
	if s.pauseBelow != 0 {
		t.Fatalf("a line outside (0, 1] is off, got %v", s.pauseBelow)
	}
	s.Reload(Settings{PauseBelow: 1, QueueMax: -1})
	if s.pauseBelow != 1 {
		t.Fatalf("the top of the range is a real line, got %v", s.pauseBelow)
	}
}
