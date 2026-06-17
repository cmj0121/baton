package panel

import (
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

func TestKindString(t *testing.T) {
	if Shell.String() != "shell" || Agent.String() != "agent" {
		t.Fatalf("kind strings: %q %q", Shell.String(), Agent.String())
	}
}

func TestStateString(t *testing.T) {
	cases := map[State]string{
		Spawning:  "spawning",
		Running:   "running",
		Idle:      "idle",
		Attention: "attention",
		Exited:    "exited",
		State(99): "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestParseKind(t *testing.T) {
	if ParseKind("agent") != Agent {
		t.Error("agent should parse to Agent")
	}
	for _, s := range []string{"shell", "", "nonsense"} {
		if ParseKind(s) != Shell {
			t.Errorf("%q should default to Shell", s)
		}
	}
}

func TestParseState(t *testing.T) {
	cases := map[string]State{
		"spawning":  Spawning,
		"idle":      Idle,
		"attention": Attention,
		"exited":    Exited,
		"running":   Running,
		"":          Running, // default
		"bogus":     Running, // default
	}
	for s, want := range cases {
		if got := ParseState(s); got != want {
			t.Errorf("ParseState(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestIsAgent(t *testing.T) {
	if !(Panel{Kind: Agent}).IsAgent() || (Panel{Kind: Shell}).IsAgent() {
		t.Fatal("IsAgent mismatch")
	}
}

func TestProtoRoundTrip(t *testing.T) {
	p := Panel{ID: "7", Kind: Agent, Title: "claude", State: Attention, Group: "auth", Activity: "needs you"}
	got := FromProto(p.ToProto())
	if got != p {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, p)
	}

	// Wire encoding uses the string forms.
	w := p.ToProto()
	if w.Kind != "agent" || w.State != "attention" {
		t.Fatalf("ToProto kind/state = %q/%q", w.Kind, w.State)
	}
	if FromProto(proto.Panel{Kind: "shell", State: "idle"}).State != Idle {
		t.Fatal("FromProto state decode failed")
	}
}

func TestMockFleet(t *testing.T) {
	m := Mock()
	if len(m) < 1 {
		t.Fatal("Mock fleet should be non-empty")
	}
	seen := map[string]bool{}
	agents, shells := 0, 0
	for _, p := range m {
		if p.ID == "" || p.Title == "" {
			t.Errorf("mock panel missing id/title: %+v", p)
		}
		if seen[p.ID] {
			t.Errorf("duplicate mock id %q", p.ID)
		}
		seen[p.ID] = true
		if p.IsAgent() {
			agents++
		} else {
			shells++
		}
	}
	if agents == 0 || shells == 0 {
		t.Fatalf("mock fleet should mix agents and shells, got %d/%d", agents, shells)
	}
}
