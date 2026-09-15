package server_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
	"github.com/cmj0121/baton/internal/server"
)

// scoreedit_test.go drives #93's `n s` end to end, with a real PTY and a real
// editor process. The editor is a script rather than a mock because the failure
// this guards is a property of how editors WORK — a save is a write-back of the
// buffer that was opened, so anything appended in between is not in it — and a
// mock that simply rewrote the file would be asserting the fix rather than the
// problem.

// fakeEditor writes a script that behaves like an editor holding a file open:
// it snapshots its argument, signals that it has it, waits to be told to save,
// writes the snapshot back and exits. The test drives the window in between.
func fakeEditor(t *testing.T) (path, ready, save, buffer string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "editor.sh")
	snap := filepath.Join(dir, "buffer")
	ready, save = filepath.Join(dir, "ready"), filepath.Join(dir, "save")
	script := "#!/bin/sh\n" +
		"cp \"$1\" " + snap + "\n" +
		"touch " + ready + "\n" +
		// Bounded, so a wedged test fails as a timeout rather than hanging CI.
		"i=0; while [ ! -f " + save + " ] && [ $i -lt 300 ]; do sleep 0.05; i=$((i+1)); done\n" +
		"cp " + snap + " \"$1\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write editor script: %v", err)
	}
	return path, ready, save, snap
}

// waitFor polls until cond holds, failing the test with why if it never does.
func waitUntil(t *testing.T, why string, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

func scoreServer(t *testing.T, opts ...server.Option) (*server.Server, string, *score.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := score.Open(dir, score.Policy{})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	srv, sock := startDiffServer(t, append(opts,
		server.WithScore(server.ScoreState{Store: st, Enabled: true}))...)
	return srv, sock, st, dir
}

// TestScoreEditOpens is the surface itself: score.edit replies
// with a "score:"-prefixed transient panel id, which is what the cockpit
// auto-zooms.
func TestScoreEditOpens(t *testing.T) {
	editor, ready, save, _ := fakeEditor(t)
	srv, sock, _, _ := scoreServer(t, server.WithEditor(editor))
	c := dialReady(t, sock)
	defer func() { _ = os.WriteFile(save, nil, 0o600) }() // let the editor exit

	if err := c.Send(proto.Command{Action: "score.edit"}); err != nil {
		t.Fatalf("score.edit: %v", err)
	}
	reply := recvUntil(t, c, "ephemeral")
	if !strings.HasPrefix(reply.ID, "score:") {
		t.Fatalf("a score ephemeral id should be score:-prefixed, got %q", reply.ID)
	}
	if got := srv.EphemeralCount(); got != 1 {
		t.Fatalf("expected 1 tracked ephemeral panel, got %d", got)
	}
	waitUntil(t, "the editor to open the file", func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})
}

// TestScoreEditRestores is the whole point of the
// issue, driven through the daemon: an agent submits while the operator's
// editor is open, the editor writes back the buffer it opened with, and the
// entry must still be there afterwards.
func TestScoreEditRestores(t *testing.T) {
	editor, ready, save, _ := fakeEditor(t)
	srv, sock, st, dir := scoreServer(t, server.WithEditor(editor))
	c := dialReady(t, sock)

	if err := c.Send(proto.Command{Action: "score.edit"}); err != nil {
		t.Fatalf("score.edit: %v", err)
	}
	reply := recvUntil(t, c, "ephemeral")
	waitUntil(t, "the editor to snapshot score.md", func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})

	// The fleet learns something while the operator is typing.
	late, _, err := st.Submit("the linter runs before the tests", score.Provenance{Source: "agent"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// :w and :q — the buffer goes back, without the line the operator never saw.
	if err := os.WriteFile(save, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the editor panel to be reaped", func() bool { return srv.EphemeralCount() == 0 })
	waitUntil(t, "the entry to be restored", func() bool {
		return strings.Contains(readMD(t, dir), late.Id)
	})

	if _, ok := srv.ScoreEditOpen(reply.ID); ok {
		t.Errorf("the editing session %q outlived its panel", reply.ID)
	}
}

// TestScoreEditRefused pins the store-off half: cmd/baton's reason
// string is what reaches the operator, and no panel is spawned.
func TestScoreEditRefused(t *testing.T) {
	srv, sock := startDiffServer(t, server.WithScore(server.ScoreState{
		Reason: "score is switched off in the config (score.enabled: false)",
	}))
	c := dialReady(t, sock)

	if err := c.Send(proto.Command{Action: "score.edit"}); err != nil {
		t.Fatalf("score.edit: %v", err)
	}
	msg := recvUntil(t, c, "error")
	if !strings.Contains(msg.Error, "score.enabled: false") {
		t.Fatalf("the refusal should carry the configured reason, got %q", msg.Error)
	}
	if got := srv.EphemeralCount(); got != 0 {
		t.Fatalf("a refused score.edit spawned %d panels", got)
	}
}

// readMD reads the score.md under dir.
func readMD(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "score.md"))
	if err != nil {
		t.Fatalf("read score.md: %v", err)
	}
	return string(data)
}

// TestScoreEditDropped covers the operator whose cockpit went away mid-edit —
// a dropped ssh session, a killed client. The editor is SIGKILLed with every
// other ephemeral, so nothing will ever write the buffer back; but a save they
// already made is on disk and is now their last word on the file, and the
// session measuring the window has to close or its restore is never made.
//
// Without the fold on the disconnect path this passes right up to the last
// assertion: the file keeps the save, and the entry submitted during the window
// is simply gone.
func TestScoreEditDropped(t *testing.T) {
	editor, ready, _, buffer := fakeEditor(t)
	srv, sock, st, dir := scoreServer(t, server.WithEditor(editor))
	c := dialReady(t, sock)

	if err := c.Send(proto.Command{Action: "score.edit"}); err != nil {
		t.Fatalf("score.edit: %v", err)
	}
	recvUntil(t, c, "ephemeral")
	waitUntil(t, "the editor to snapshot score.md", func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})

	late, _, err := st.Submit("the linter runs before the tests", score.Provenance{Source: "agent"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// :w, and no :q — the buffer is on disk and the editor is still sitting there.
	buf, err := os.ReadFile(buffer)
	if err != nil {
		t.Fatalf("read the editor buffer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "score.md"), buf, 0o600); err != nil {
		t.Fatalf("simulate the save: %v", err)
	}
	if strings.Contains(readMD(t, dir), late.Id) {
		t.Fatal("the save did not drop the late entry, so this test proves nothing")
	}

	_ = c.Close() // the cockpit goes away

	waitUntil(t, "the editor panel to be reaped", func() bool { return srv.EphemeralCount() == 0 })
	waitUntil(t, "the entry to be restored", func() bool {
		return strings.Contains(readMD(t, dir), late.Id)
	})
}
