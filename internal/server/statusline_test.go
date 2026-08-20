package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/ptymgr"
)

// claudeSettingsDir points CLAUDE_CONFIG_DIR at a fresh directory, optionally
// writing a status line into it, so a test never reads the developer's own.
func claudeSettingsDir(t *testing.T, statusLine string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	body := `{"theme":"dark"}`
	if statusLine != "" {
		body = `{"statusLine":{"type":"command","command":` + mustJSON(t, statusLine) + `}}`
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// injectedStatusLine pulls the statusLine command out of the --settings argument
// the injection appended, failing the test if there is none.
func injectedStatusLine(t *testing.T, args []string) string {
	t.Helper()
	for i, a := range args {
		if a != settingsFlag || i+1 >= len(args) {
			continue
		}
		var got struct {
			StatusLine struct{ Type, Command string } `json:"statusLine"`
		}
		if err := json.Unmarshal([]byte(args[i+1]), &got); err != nil {
			t.Fatalf("--settings is not valid JSON: %v (%s)", err, args[i+1])
		}
		if got.StatusLine.Type != "command" {
			t.Errorf("injected statusLine type = %q, want %q", got.StatusLine.Type, "command")
		}
		return got.StatusLine.Command
	}
	t.Fatalf("no %s argument in %q", settingsFlag, args)
	return ""
}

// A panel whose user had no status line gains one: the sink runs alone and
// prints baton's own quota line.
func TestWithStatusLineNoUserStatusLine(t *testing.T) {
	claudeSettingsDir(t, "")
	spec, ok := withStatusLine(ptymgr.Spec{Command: "claude", Dir: t.TempDir()}, "/usr/local/bin/baton")
	if !ok {
		t.Fatal("withStatusLine did not inject for a Claude Code panel")
	}
	got := injectedStatusLine(t, spec.Args)
	if want := `'/usr/local/bin/baton' usage-sink`; got != want {
		t.Errorf("statusLine command = %q, want %q", got, want)
	}
}

// The user's own status line is wrapped, never replaced — the panel renders
// exactly what it would have without baton in the way.
func TestWithStatusLineWrapsTheUsers(t *testing.T) {
	claudeSettingsDir(t, "bash ~/.claude/statusline.sh")
	spec, ok := withStatusLine(ptymgr.Spec{Command: "/opt/homebrew/bin/claude", Dir: t.TempDir()}, "/usr/local/bin/baton")
	if !ok {
		t.Fatal("withStatusLine did not inject over a configured status line")
	}
	got := injectedStatusLine(t, spec.Args)
	want := `'/usr/local/bin/baton' usage-sink --wrap 'bash ~/.claude/statusline.sh'`
	if got != want {
		t.Errorf("statusLine command = %q, want %q", got, want)
	}
}

// Both halves reach a shell, so both are quoted: baton's path may sit under a
// directory with a space in it, and the wrapped command is arbitrary shell.
func TestWithStatusLineQuoting(t *testing.T) {
	claudeSettingsDir(t, `sh -c 'echo it'\''s fine'`)
	spec, ok := withStatusLine(ptymgr.Spec{Command: "claude", Dir: t.TempDir()}, "/Users/a b/bin/baton")
	if !ok {
		t.Fatal("withStatusLine did not inject")
	}
	got := injectedStatusLine(t, spec.Args)
	if !strings.HasPrefix(got, `'/Users/a b/bin/baton' usage-sink --wrap `) {
		t.Errorf("baton's path was not quoted as one word: %q", got)
	}
	if quoted := shellQuote(`it's`); quoted != `'it'\''s'` {
		t.Errorf("shellQuote = %q, want %q", quoted, `'it'\''s'`)
	}
}

// The quoting is checked against a real shell rather than by inspecting the
// string, because the only property that matters is the one a shell decides: the
// value has to come back out as exactly one word, unchanged, however many quotes
// and spaces went in.
func TestShellQuoteRoundTrip(t *testing.T) {
	for _, in := range []string{
		"plain",
		"/Users/a b/bin/baton",
		`it's`,
		`sh -c 'echo it'\''s fine'`,
		`$(rm -rf /) ` + "`whoami`" + ` "quoted" \\ $HOME`,
		`nested '"'"' quoting`,
	} {
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+shellQuote(in)).Output()
		if err != nil {
			t.Fatalf("%q: shell refused the quoted form: %v", in, err)
		}
		if string(out) != in {
			t.Errorf("round trip of %q came back as %q", in, out)
		}
	}
}

// Each of these is a case where injecting would change something that is not
// baton's to change.
func TestWithStatusLineDeclines(t *testing.T) {
	dir := t.TempDir()

	t.Run("not claude code", func(t *testing.T) {
		claudeSettingsDir(t, "")
		if _, ok := withStatusLine(ptymgr.Spec{Command: "codex", Dir: dir}, "/bin/baton"); ok {
			t.Error("injected into a panel that is not Claude Code")
		}
	})
	t.Run("no binary path", func(t *testing.T) {
		claudeSettingsDir(t, "")
		if _, ok := withStatusLine(ptymgr.Spec{Command: "claude", Dir: dir}, "  "); ok {
			t.Error("injected with nothing to point the status line at")
		}
	})
	t.Run("user set --settings", func(t *testing.T) {
		claudeSettingsDir(t, "")
		spec := ptymgr.Spec{Command: "claude", Dir: dir, Args: []string{"--settings", "/tmp/mine.json"}}
		if _, ok := withStatusLine(spec, "/bin/baton"); ok {
			t.Error("injected a second settings source over the user's own")
		}
		spec.Args = []string{"--settings=/tmp/mine.json"}
		if _, ok := withStatusLine(spec, "/bin/baton"); ok {
			t.Error("the --settings=VALUE form was not recognised")
		}
	})
	t.Run("status line baton cannot reproduce", func(t *testing.T) {
		cfg := t.TempDir()
		t.Setenv("CLAUDE_CONFIG_DIR", cfg)
		body := `{"statusLine":{"type":"something-else"}}`
		if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := withStatusLine(ptymgr.Spec{Command: "claude", Dir: dir}, "/bin/baton"); ok {
			t.Error("replaced a status line baton cannot reproduce")
		}
	})
}

// The caller keeps the original spec to replay on respawn, so the injection must
// land on a copy — exactly as the session id does.
func TestWithStatusLineDoesNotMutateTheOriginal(t *testing.T) {
	claudeSettingsDir(t, "")
	original := ptymgr.Spec{Command: "claude", Dir: t.TempDir(), Args: []string{"--model", "opus"}}
	got, ok := withStatusLine(original, "/bin/baton")
	if !ok {
		t.Fatal("withStatusLine did not inject")
	}
	if len(original.Args) != 2 {
		t.Errorf("the caller's spec was mutated: %q", original.Args)
	}
	if len(got.Args) != 4 || got.Args[0] != "--model" {
		t.Errorf("the injected spec lost the original arguments: %q", got.Args)
	}
}
