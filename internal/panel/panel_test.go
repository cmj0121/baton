package panel

import (
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

func TestKindString(t *testing.T) {
	cases := map[Kind]string{
		Shell:    "shell",
		Agent:    "agent",
		Command:  "command",
		Kind(99): "shell", // an unknown kind renders as the floor, never as an agent
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, want %q", k, got, want)
		}
	}
}

// TestKindZeroValueIsShell holds the appended-never-inserted rule: a panel.create
// that names no kind, and any Panel built without one, is a shell.
func TestKindZeroValueIsShell(t *testing.T) {
	var k Kind
	if k != Shell {
		t.Fatalf("the zero Kind must stay Shell, got %v", k)
	}
	if (Panel{}).IsAgent() || (Panel{}).IsCommand() {
		t.Fatal("a zero Panel is neither an agent nor a command")
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
	cases := map[string]Kind{
		"agent":   Agent,
		"command": Command,
		"shell":   Shell,
	}
	for s, want := range cases {
		if got := ParseKind(s); got != want {
			t.Errorf("ParseKind(%q) = %v, want %v", s, got, want)
		}
	}
}

// TestParseKindUnknownIsShell is the forward-compatibility rule the wire relies
// on, and the reason adding "command" to the vocabulary did not bump the protocol
// (see internal/proto). A kind this build does not know maps to Shell, so an
// older cockpit UNDER-claims: it draws a plainer panel than the truth, and never
// one that offers the agent-only surfaces.
func TestParseKindUnknownIsShell(t *testing.T) {
	for _, s := range []string{"", "nonsense", "command-v2", "AGENT", "Command"} {
		if got := ParseKind(s); got != Shell {
			t.Errorf("ParseKind(%q) = %v, want Shell", s, got)
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

// TestCommandIsNotAnAgent is the assertion the whole third kind exists to make.
// Every agent-only surface in the codebase gates on IsAgent (or on the same
// Kind == Agent comparison), so this one inequality is what keeps a command panel
// out of the scheduler, off the attention ladder, and away from the diff and
// worktree menus. If it ever comes back true, #54's defect is back under a new
// name.
func TestCommandIsNotAnAgent(t *testing.T) {
	p := Panel{Kind: Command}
	if p.IsAgent() {
		t.Fatal("a command panel must never report as an agent")
	}
	if !p.IsCommand() {
		t.Fatal("a command panel must report as a command")
	}
	for _, other := range []Kind{Shell, Agent} {
		if (Panel{Kind: other}).IsCommand() {
			t.Errorf("Kind %v must not report as a command", other)
		}
	}
}

// TestCommandKindSurvivesTheWire holds the round trip every frontend depends on:
// a command panel encoded by the daemon and decoded by a cockpit of the same
// build is still a command, not a shell.
func TestCommandKindSurvivesTheWire(t *testing.T) {
	p := Panel{ID: "3", Kind: Command, Title: "time · baton", State: Exited, ExitCode: 2}
	if got := FromProto(p.ToProto()); got.Kind != Command {
		t.Fatalf("round-tripped kind = %v, want Command", got.Kind)
	}
	if got := p.ToProto().Kind; got != proto.KindCommand {
		t.Fatalf("wire kind = %q, want %q", got, proto.KindCommand)
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
