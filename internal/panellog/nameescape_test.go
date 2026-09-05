package panellog

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAFileNameCannotLeaveItsDirectory is the traversal question asked of the one
// peer-supplied string that reaches a filename. A panel's title is whatever
// panel.rename was sent, and startLogging hands it straight to FileName, which
// joins the result under the operator's log directory.
//
// Slug already answers it, and the ALLOWLIST is the whole of the answer: it
// keeps letters, digits, '.' and '_' and turns everything else into a dash, so a
// separator can never survive into the name. Mutation-checked both ways —
// admitting '/' to that set fails this test on every title below, while dropping
// '.' from the trim at the end of Slug does not, because the date prefix means
// the name never begins with a dot and "2026-09-05-..-1.log" is a file rather
// than a step upward. The trim is tidiness; the allowlist is the guard.
//
// The assertion is on the joined path, not on the slug, because that is the
// claim worth making — the file lands inside the directory it was given, whatever
// the title was.
func TestAFileNameCannotLeaveItsDirectory(t *testing.T) {
	dir := "/var/log/baton"
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)

	titles := []string{
		"..",
		"../../etc/passwd",
		"/etc/passwd",
		"a/../../../b",
		".",
		"....//....//etc",
		"\\..\\..\\windows",
	}
	for _, title := range titles {
		got := filepath.Join(dir, FileName(title, "1", now))
		if filepath.Dir(got) != dir {
			t.Errorf("a panel titled %q logs to %q, which is outside %s", title, got, dir)
		}
		if strings.Contains(FileName(title, "1", now), "/") {
			t.Errorf("FileName(%q) = %q, want a single path segment", title, FileName(title, "1", now))
		}
	}
}

// TestAPanelIdCannotLeaveItsDirectoryEither covers the second half of the name.
// The id is the server's own counter today and cannot be strange — but it is
// slugified beside the title rather than trusted, and a test that only asked
// about the title would let a later change route something else through here
// unguarded.
func TestAPanelIdCannotLeaveItsDirectoryEither(t *testing.T) {
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"../1", "..", "a/b"} {
		if name := FileName("claude", id, now); strings.Contains(name, "/") {
			t.Errorf("FileName(_, %q) = %q, want a single path segment", id, name)
		}
	}
}
