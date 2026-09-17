package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
)

// statusTool unmarshals the write half of score.status.
func statusTool(t *testing.T, s *Server) memoryToolReport {
	t.Helper()
	var got struct {
		AgentMCP memoryToolReport `json:"agent_mcp"`
	}
	if err := json.Unmarshal(s.scoreStatus(), &got); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	return got.AgentMCP
}

// TestStatusNamesThePanelsThatCannotWrite is the gap this section closes. A fleet
// where no agent can reach the memory answered `enabled: true, available: true,
// feedback: true, entries: 0` — every field healthy, the loop wide open, and the
// only trace of it a Debug log line.
//
// Every panel below is an agent and every one of them is running, so the state
// and the kind cannot account for the split: the only thing separating them is
// the verdict recorded when each was launched, which is the fact that had no
// surface at all.
func TestStatusNamesThePanelsThatCannotWrite(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)
	s.agentMCP = true
	s.panels = []panel.Panel{
		{ID: "1", Kind: panel.Agent, State: panel.Running},
		{ID: "2", Kind: panel.Agent, State: panel.Running},
		{ID: "3", Kind: panel.Agent, State: panel.Running},
		{ID: "4", Kind: panel.Agent, State: panel.Running},
	}
	s.memoryTool = map[string]string{
		"1": memoryToolWired,
		"2": memoryToolNoFlag,     // a grok panel on a fleet of claude and grok
		"3": memoryToolNoFlag,     // …and its twin, so the grouping is not a coincidence
		"4": memoryToolUnwritable, // baton could not write its own config
	}

	got := statusTool(t, s)
	if !got.Enabled {
		t.Error("the setting is on and the report said otherwise")
	}
	if !reflect.DeepEqual(got.Wired, []string{"1"}) {
		t.Errorf("wired = %v, want only the panel that got the tool", got.Wired)
	}
	want := map[string][]string{
		memoryToolNoFlag:     {"2", "3"},
		memoryToolUnwritable: {"4"},
	}
	if !reflect.DeepEqual(got.Unwired, want) {
		t.Errorf("unwired = %v, want %v", got.Unwired, want)
	}
	if got.Config == "" {
		t.Error("the report should name the config a wired panel is pointed at")
	}
}

// TestStatusReportsOnPanelsThatCanStillWrite: a dead slot writes nothing whatever
// its last launch did, and a shell was never in scope. Listing either would be an
// answer to a question nobody asked — and, worse, would pad `unwired` on a
// healthy fleet until nobody read it.
func TestStatusReportsOnPanelsThatCanStillWrite(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)
	s.agentMCP = true
	s.panels = []panel.Panel{
		{ID: "1", Kind: panel.Agent, State: panel.Running},
		{ID: "2", Kind: panel.Agent, State: panel.Exited}, // a dead slot
		{ID: "3", Kind: panel.Shell, State: panel.Idle},
		{ID: "4", Kind: panel.Command, State: panel.Running},
	}
	s.memoryTool = map[string]string{
		"1": memoryToolWired,
		"2": memoryToolNoFlag,
		"3": memoryToolNotAgent,
		"4": memoryToolNotAgent,
	}

	got := statusTool(t, s)
	if !reflect.DeepEqual(got.Wired, []string{"1"}) {
		t.Errorf("wired = %v, want only the live agent", got.Wired)
	}
	if len(got.Unwired) != 0 {
		t.Errorf("unwired = %v, want nothing: no live agent panel is missing the tool", got.Unwired)
	}
}

// TestStatusSaysTheSectionEvenWhenItIsEmpty pins the field that must not elide
// itself, for the reason `feedback` has no omitempty: a fleet where nothing can
// write is exactly when this is worth reading, and that is the case where every
// list inside it is empty.
func TestStatusSaysTheSectionEvenWhenItIsEmpty(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)
	s.agentMCP = false

	raw := string(s.scoreStatus())
	if !strings.Contains(raw, `"agent_mcp"`) {
		t.Fatalf("status = %s, want the write half present", raw)
	}
	if !strings.Contains(raw, `"enabled":false`) {
		t.Errorf("status = %s, want it to say the setting is off rather than omit it", raw)
	}
	if got := statusTool(t, s); got.Wired == nil {
		t.Error("wired should be an empty list rather than null: a reader counting it must not special-case both")
	}
}

// TestALiveAgentWithNoVerdictIsNotCountedAsWired: Restore cannot produce a live
// agent panel — everything it rebuilds is exited — so an agent that is running
// with no recorded verdict is one whose verdict went missing, not one that was
// never asked. Reading it as wired would be the report inventing the answer it
// exists to supply.
func TestALiveAgentWithNoVerdictIsNotCountedAsWired(t *testing.T) {
	st, _ := scoreStore(t)
	s, _, _ := scoreServer(st)
	s.agentMCP = true
	s.panels = []panel.Panel{{ID: "9", Kind: panel.Agent, State: panel.Running}}

	got := statusTool(t, s)
	if len(got.Wired) != 0 {
		t.Errorf("wired = %v, want nothing claimed for a panel with no verdict", got.Wired)
	}
	if !reflect.DeepEqual(got.Unwired, map[string][]string{"unknown": {"9"}}) {
		t.Errorf("unwired = %v, want the panel named under an honest reason", got.Unwired)
	}
}
