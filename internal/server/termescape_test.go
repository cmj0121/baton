package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A daemon error naming a directory is read by a human in a terminal — the
// cockpit's footer, and `baton ctl`'s stderr, which has no renderer in front of
// it at all. A directory is text an agent controls twice over: it can mkdir one
// and cd into it, and it can simply CLAIM one, because a panel's live Cwd is read
// from the OSC 7 report its own output emits and url.Parse percent-decodes that
// payload — so "%1b" in a report becomes a real ESC in the path baton stores.
//
// %q is the whole fix. It is not a filter: the path stays exactly what it was,
// which is what an error about a path has to say, but strconv.Quote renders the
// ESC as the four characters \x1b, so nothing executes.
const escDirName = "\x1b[2J\x1b[Hfake"

// assertNoRawESC fails on a raw ESC, and fails just as loudly when the path is
// not in the message at all — otherwise an error that dropped the path entirely
// would pass.
func assertNoRawESC(t *testing.T, what, got string) {
	t.Helper()
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("%s: a raw ESC reached the operator: %q", what, got)
	}
	if !strings.Contains(got, `\x1b`) {
		t.Errorf("%s: the path is not in the message, so the check above proves nothing: %q", what, got)
	}
}

// The worktree refusal names the repository it was handed, which arrives from the
// MCP tool's `dir` argument with no character validation of its own — unlike the
// branch beside it, which gitops.ValidateBranch already refuses control bytes in.
func TestWorktreeErrorQuotesAHostileRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	plain := filepath.Join(t.TempDir(), escDirName)
	if err := os.Mkdir(plain, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s, _ := wtServer(t)

	err := s.worktreeSpawn(plain, "feature/nope", idleAgent())
	if err == nil {
		t.Fatal("a non-repo should be refused")
	}
	assertNoRawESC(t, "worktreeSpawn error", err.Error())
}
