package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// openNewForm opens the new-panel form with its real key and types line into it one
// rune at a time, so a case exercises the same handleInput path an operator's
// keyboard does rather than assigning to inputBuf behind it. It returns the model
// and the recording server's command stream.
func openNewForm(t *testing.T, line string) (model, <-chan proto.Command) {
	t.Helper()
	c, cmds := recordingServer(t)
	m := baseModel()
	m.client = c
	m.fleet = sampleFleet()
	m.shellPath = "/bin/zsh"

	m = press(m, keyNewForm)
	if m.input != inputNewPanelCmd {
		t.Fatalf("%s should open the new-panel form, got %v", keyNewForm, m.input)
	}
	for _, r := range line {
		if r == ' ' {
			m = press(m, "space")
			continue
		}
		m = press(m, string(r))
	}
	if m.inputBuf != line {
		t.Fatalf("typing %q left the box holding %q", line, m.inputBuf)
	}
	return m, cmds
}

// TestNewFormEmptyStillSpawnsAShell is the promise #84 was not allowed to break:
// `n c` then enter is the keystroke it always was and still yields a plain shell
// panel. An operator with that sequence in their fingers must not find it running
// something else.
func TestNewFormEmptyStillSpawnsAShell(t *testing.T) {
	m, cmds := openNewForm(t, "")
	press(m, "enter")

	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.create" })
	if got.Kind != proto.KindShell {
		t.Fatalf("enter on an empty box should spawn a shell panel, got kind %q", got.Kind)
	}
	if got.Path != "" || len(got.Args) != 0 {
		t.Fatalf("the shell it spawns names no program, got path %q args %q", got.Path, got.Args)
	}
}

// TestNewFormSpawnsACommandPanel is the change itself. A program typed here used
// to arrive as KindShell with the whole line in Path, so the panel vanished when
// the process exited and the restart policy was free to bring it back. It is now
// a COMMAND panel, which holds when it exits and which superviseExitLocked skips
// outright — the standing that makes `make test` worth spawning here at all.
func TestNewFormSpawnsACommandPanel(t *testing.T) {
	m, cmds := openNewForm(t, "make test")
	press(m, "enter")

	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.create" })
	if got.Kind != proto.KindCommand {
		t.Fatalf("a typed program should spawn a command panel, got kind %q", got.Kind)
	}
	if got.Path != "make" {
		t.Fatalf("the program belongs in Path, got %q", got.Path)
	}
	if len(got.Args) != 1 || got.Args[0] != "test" {
		t.Fatalf("the arguments belong in Args, got %q", got.Args)
	}
}

// The arguments have to survive the trip with their quoting intact. `go test
// ./...` was not expressible at all before this — the whole line went into Path
// as one program name — and a quoted argument holding a space is exactly what
// splitting on whitespace alone would tear in half.
func TestNewFormCarriesQuotedArguments(t *testing.T) {
	m, cmds := openNewForm(t, `go test -run 'Foo Bar' ./...`)
	press(m, "enter")

	got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.create" })
	if got.Path != "go" {
		t.Fatalf("program = %q, want go", got.Path)
	}
	want := []string{"test", "-run", "Foo Bar", "./..."}
	if len(got.Args) != len(want) {
		t.Fatalf("args = %q, want %q", got.Args, want)
	}
	for i := range want {
		if got.Args[i] != want[i] {
			t.Fatalf("args = %q, want %q", got.Args, want)
		}
	}
}

// A half-quoted line, and a line that is all arguments and no program, are both
// refused in the cockpit rather than sent. Neither is something the server can do
// anything useful with — one would spawn an argv nobody typed, the other fails to
// exec inside a panel the operator then has to go and read — and the cockpit is
// holding the line, so it can say what is wrong while they are still looking at
// the box.
//
// "Nothing was sent" is asserted by sending something afterwards that must be
// FIRST: the stream is one ordered socket, so a refusal that leaked would sit
// ahead of the sentinel.
func TestNewFormRefusesWhatItCannotRun(t *testing.T) {
	for _, tc := range []struct {
		name, line, says string
	}{
		{"unterminated quote", `go test -run 'Foo`, "unterminated"},
		{"arguments with no program", `--foo bar`, "program"},
		{"an empty program with arguments", `'' --foo`, "program"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, cmds := openNewForm(t, tc.line)
			m = press(m, "enter")

			if !strings.Contains(m.status, tc.says) {
				t.Errorf("status %q never says why %q was refused", m.status, tc.line)
			}

			// p spawns a plain shell — a KindShell create, which the refused line
			// (a KindCommand create) could not be mistaken for.
			m = press(m, keyNewPanel)
			got := waitCmd(t, cmds, func(c proto.Command) bool { return c.Action == "panel.create" })
			if got.Kind != proto.KindShell {
				t.Fatalf("%q reached the server as %+v; it should have been refused", tc.line, got)
			}
		})
	}
}
