package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/worktree"
)

// `baton ctl` is the surface with nothing in front of it: the cockpit renders a
// daemon error into a footer it lays out, and ctl hands the bytes to the
// operator's terminal. What it prints is not the daemon's prose either — gitops
// folds git's own stderr into an error, so a push refused by a hook reports
// whatever that hook printed.
const escText = "api\x1b[2J\x1b[H\x1b]0;pwned\aworker\u202e"

// captureStderr is captureStdout's twin; the prompts and the failures in this
// package all go to stderr so that `--yes | jq` still gets clean JSON.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	os.Stderr = old
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// Every `baton ctl` failure prints through ctlFail, so this is the one place the
// filter has to hold. The legitimate half of the message is asserted alongside,
// because a "filter" that printed nothing would otherwise pass.
func TestCtlFailPrintsNothingATerminalActsOn(t *testing.T) {
	var code int
	out := captureStderr(t, func() { code = ctlFail(errors.New(escText), 1) })

	if code != 1 {
		t.Errorf("ctlFail returned %d, want the exit code it was given", code)
	}
	for _, r := range strings.TrimSuffix(out, "\n") {
		if r == 0x1b || r == 0x07 || r == '\u202e' {
			t.Fatalf("%U reached the operator's terminal: %q", r, out)
		}
	}
	if !strings.Contains(out, "api") || !strings.Contains(out, "worker") {
		t.Fatalf("the message was lost, so the check above proves nothing: %q", out)
	}
}

// The sweep names each orphan and asks y/N. The path is agent-chosen text — it is
// built from the directory the spawn asked for, which nothing validates the way
// ValidateBranch validates the branch half — so a raw escape in one could repaint
// the very question being asked.
//
// The record is seeded directly rather than through a spawn: a recorded tree that
// no longer exists on disk is an orphan the prompt lists, which is the state under
// test, and reaching it through git would only test git.
func TestCtlSweepPromptQuotesAPath(t *testing.T) {
	sock := ctlStateServer(t)
	c := ctlDial(t, sock)

	tree := filepath.Join(os.Getenv("HOME"), escText)
	rec := worktree.New(filepath.Join(os.Getenv("HOME"), "b.worktrees.json"))
	if err := rec.Add(tree); err != nil {
		t.Fatalf("seed the worktree record: %v", err)
	}
	withTTY(t, true, "n\n")

	var ok bool
	var runErr error
	out := captureStderr(t, func() { ok, runErr = (ctlWorktreeSweep{}).confirm(c) })
	if runErr != nil {
		t.Fatalf("confirm: %v", runErr)
	}
	if ok {
		t.Fatal("the prompt was answered no; confirm should say so")
	}
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("a raw ESC reached the operator: %q", out)
	}
	if !strings.Contains(out, `\x1b`) {
		t.Errorf("the path is not in the prompt, so the check above proves nothing: %q", out)
	}
}
