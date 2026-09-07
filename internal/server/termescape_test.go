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

	err := s.worktreeSpawn(originOperator, plain, "feature/nope", idleAgent())
	if err == nil {
		t.Fatal("a non-repo should be refused")
	}
	assertNoRawESC(t, "worktreeSpawn error", err.Error())
}

// A name is an identity, not prose, so the daemon refuses one it cannot both
// store and draw rather than filtering it.
//
// The argument is the uniqueness policy, and it is what makes refusal the right
// answer instead of a scrub. Every frontend now declines to draw a control or
// format character, so a stored "api" with a zero-width space on the end is a
// DIFFERENT name to nameTakenLocked and the SAME name on every screen — two rows
// called "api", from the one rule that exists to stop exactly that. Refusing
// keeps the stored name and the drawn name the same string.
func TestRenameRefusesANameItCannotDraw(t *testing.T) {
	s := newHostServer(t)
	for _, name := range []string{
		"api\x1b[2J\x1b[Hfake", // an escape: it would erase the operator's screen
		"api\u200b",            // invisible: it would collide with "api" on screen
		"api\u202eworker",      // a bidi override: it renders the name backwards
	} {
		err := s.rename("1", "", name)
		if err == nil {
			t.Errorf("rename to %q was accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "control character") {
			t.Errorf("rename to %q was refused for the wrong reason: %v", name, err)
		}
		if strings.ContainsRune(err.Error(), 0x1b) {
			t.Errorf("the refusal itself carried a raw ESC: %q", err.Error())
		}
	}
}

// The same rule on the other naming command, and the ordinary name still passes —
// a test that only proved names are refused would pass on a daemon that refused
// every name.
func TestGroupRefusesANameItCannotDraw(t *testing.T) {
	s := newHostServer(t)

	if err := s.groupPanels([]string{"1"}, "api\x1b[2Jfake"); err == nil ||
		!strings.Contains(err.Error(), "control character") {
		t.Errorf("panel.group should refuse a control character, got %v", err)
	}
	// No panel has id "1", so a clean name gets that far and fails on the ids —
	// which is what shows the guard let it through rather than stopping it.
	if err := s.groupPanels([]string{"1"}, "api"); err == nil ||
		strings.Contains(err.Error(), "control character") {
		t.Errorf("panel.group should accept an ordinary name, got %v", err)
	}
}
