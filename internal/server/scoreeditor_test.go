package server

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// TestEditorCommandResolutionOrder pins the chain an operator's score.md opens
// through, and that each step only applies when the one before it is empty.
// VISUAL before EDITOR is deliberate: the panel is a terminal, which is the
// distinction the two variables exist to draw.
func TestEditorCommandResolutionOrder(t *testing.T) {
	for _, tc := range []struct {
		name               string
		configured, visual string
		editor             string
		want               string
	}{
		{"configured wins", "kak", "vim", "nano", "kak"},
		{"then VISUAL", "", "vim", "nano", "vim"},
		{"then EDITOR", "", "", "nano", "nano"},
		{"vi is the floor", "", "", "", "vi"},
		{"blank env does not win", "", "   ", "nano", "nano"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VISUAL", tc.visual)
			t.Setenv("EDITOR", tc.editor)
			name, args := editorCommand(tc.configured, "/tmp/score.md")
			if name != "sh" {
				t.Fatalf("command = %q, want sh", name)
			}
			if len(args) != 3 || args[0] != "-c" {
				t.Fatalf("args = %q, want a -c script plus the path", args)
			}
			if !strings.HasPrefix(args[1], tc.want+" ") {
				t.Errorf("script = %q, want it to run %q", args[1], tc.want)
			}
		})
	}
}

// TestEditorCommandPassesThePathAsAnArgument is the injection guard. A
// directory with a space, a quote or a semicolon in it is an ordinary path, and
// it must reach the editor as one argument rather than as more script.
func TestEditorCommandPassesThePathAsAnArgument(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	const nasty = `/tmp/a dir"; rm -rf $HOME; echo '/score.md`
	_, args := editorCommand("vi", nasty)
	if args[2] != nasty {
		t.Fatalf("the path should be passed verbatim as $0, got %q", args[2])
	}
	if strings.Contains(args[1], nasty) {
		t.Fatalf("the path was interpolated into the script: %q", args[1])
	}
	if args[1] != `vi "$0"` {
		t.Fatalf("script = %q, want the path referenced as $0", args[1])
	}
}

// TestEditorCommandKeepsTheEditorsOwnFlags covers the values people actually
// set: "code -w" and "nvim -u NONE" are command LINES, and splitting them on
// the first space would run a program called "code" with no flags — which for
// a GUI editor returns immediately and ends the editing window before it began.
func TestEditorCommandKeepsTheEditorsOwnFlags(t *testing.T) {
	name, args := editorCommand("code -w", "/tmp/score.md")
	if name != "sh" || args[1] != `code -w "$0"` {
		t.Fatalf("editorCommand dropped the editor's flags: %q %q", name, args)
	}
}

// TestConductorMayNotOpenTheScoreEditor is #93's fence. The memory itself is
// open to an agent in every direction that matters — score.submit is
// deliberately ungated and the reads answer anyone — so what this protects is
// the EDITOR: an interactive program, on the daemon's host, as the user.
//
// The sharper half is that the reply hands back an ephemeral id which is not
// cc.self, so the self-fence does not cover it and panel.input would drive the
// editor. Every editor worth setting $EDITOR to can run a shell.
func TestConductorMayNotOpenTheScoreEditor(t *testing.T) {
	s := &Server{}
	cc := ctl("c1")
	cc.role = roleConductor

	if reason := s.guardConductor(cc, proto.Command{Action: "score.edit"}); reason == "" {
		t.Error("a conductor must not reach the score editor")
	}
	// The MEMORY is not what is fenced: the ungated verbs stay ungated, or #38's
	// "the memory is fed by agents and operators alike" stops being true.
	for _, open := range []string{"score.submit", "score.list", "score.status"} {
		if reason := s.guardConductor(cc, proto.Command{Action: open}); reason != "" {
			t.Errorf("%s is fenced for a conductor (%q); only the editor should be", open, reason)
		}
	}
	// A cockpit connection declares no role and is never fenced.
	if reason := s.guardConductor(ctl(""), proto.Command{Action: "score.edit"}); reason != "" {
		t.Errorf("a cockpit connection must not be fenced, got %q", reason)
	}
}
