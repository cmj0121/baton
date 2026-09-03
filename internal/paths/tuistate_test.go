package paths

import "testing"

// TestTUIStateFileIsPerHostNotPerDaemon pins the decision that separates this
// file from StateFile and QueueDir: it takes no socket.
//
// The fleet snapshot is keyed on the socket because a snapshot IS a fleet — two
// daemons hold two different sets of panels and must not overwrite each other's.
// View preferences are the opposite: they belong to the operator at the terminal,
// the way UsageLimitsFile's reading belongs to the account. Someone who likes the
// tree likes it under every daemon they attach to, so a second daemon must resolve
// the SAME path rather than a second remembered taste.
//
// The assertion that can fail is the arity: this test stops compiling the moment
// somebody gives TUIStateFile a socket argument, which is exactly the drift it
// exists to catch.
func TestTUIStateFileIsPerHostNotPerDaemon(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	const want = "/home/tester/.baton/TUI.state.json"
	if got := TUIStateFile(); got != want {
		t.Fatalf("TUIStateFile() = %q, want %q", got, want)
	}

	// Two daemons, one taste: nothing about the socket reaches the path.
	t.Setenv("BATON_SOCK", "/run/baton/other.sock")
	if got := TUIStateFile(); got != want {
		t.Fatalf("TUIStateFile() under a second socket = %q, want the same %q", got, want)
	}
}

// TestTUIStateFileSitsBesideTUIConfig checks the state file is a SIBLING of the
// hand-edited TUI.yaml rather than the same file. The whole design rests on the
// two being separable — one the operator writes and comments, one the program
// rewrites — so a change that collapsed them into one path must fail here.
func TestTUIStateFileSitsBesideTUIConfig(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	if TUIStateFile() == TUIConfigFile() {
		t.Fatal("TUIStateFile must not be TUIConfigFile: the program rewrites one and the operator owns the other")
	}
}
