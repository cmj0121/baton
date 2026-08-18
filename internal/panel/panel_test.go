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
		Done:      "done",
		Stuck:     "stuck",
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

// TestParseState covers every wire string, and the deliberate default: a state
// this build does not know maps to Idle, so an older cockpit reading a newer
// daemon under-claims ("nothing is known to be happening") instead of lying
// ("work is happening").
func TestParseState(t *testing.T) {
	cases := map[string]State{
		"spawning":  Spawning,
		"idle":      Idle,
		"attention": Attention,
		"exited":    Exited,
		"running":   Running,
		"done":      Done,
		"stuck":     Stuck,
		"":          Idle, // default
		"bogus":     Idle, // a state from a newer daemon
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
	p := Panel{ID: "7", Kind: Agent, Title: "claude", State: Attention, Group: "auth", Activity: "needs you", Spark: "▂▃▅▇▆▃▁",
		ExitCode: 130, Reason: "which migration should I apply?"}
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
