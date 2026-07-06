package gitops

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateBranchRejectsFlagInjection is the security case: a branch name that
// begins with "-" would be read by git as a command-line flag rather than a
// branch (argument injection into `checkout -b` / `worktree add -b`). It must be
// refused before it ever reaches git.
func TestValidateBranchRejectsFlagInjection(t *testing.T) {
	for _, name := range []string{"-b", "--force", "-", "--track=evil", "-D"} {
		if err := ValidateBranch(name); err == nil {
			t.Errorf("ValidateBranch(%q) = nil, want a rejection — a leading '-' is flag injection", name)
		}
	}
}

// TestValidateBranchRejectsBadRefs covers the git ref-format rules the validator
// folds in, so an invalid name fails with a clear message here instead of a
// cryptic git error later.
func TestValidateBranchRejectsBadRefs(t *testing.T) {
	bad := []string{
		"",           // empty
		"a b",        // whitespace
		"a\tb",       // control char
		"a~1",        // metacharacter
		"a^",         // metacharacter
		"a:b",        // metacharacter
		"a?",         // metacharacter
		"a*",         // metacharacter
		"a[b",        // metacharacter
		"a\\b",       // backslash
		"a..b",       // double dot
		"a@{b",       // reflog syntax
		"/lead",      // leading slash
		"trail/",     // trailing slash
		"double//x",  // empty component
		"ends.lock",  // .lock suffix
		"ends.",      // trailing dot
		"@",          // the single-@ special case
		"ctrl\x7fch", // DEL
	}
	for _, name := range bad {
		if err := ValidateBranch(name); err == nil {
			t.Errorf("ValidateBranch(%q) = nil, want a rejection", name)
		}
	}
}

// TestValidateBranchAllowsOrdinary makes sure the validator is not so strict that
// it refuses the everyday branch shapes users actually type.
func TestValidateBranchAllowsOrdinary(t *testing.T) {
	for _, name := range []string{"main", "feature/foo-bar", "fix/issue-42", "release_1.2", "user/name/topic"} {
		if err := ValidateBranch(name); err != nil {
			t.Errorf("ValidateBranch(%q) = %v, want nil for an ordinary name", name, err)
		}
	}
}

// TestResolveBranchRejectsFlag confirms the resolver enforces the validator, so a
// flag-shaped branch never becomes an argv even through the git menu.
func TestResolveBranchRejectsFlag(t *testing.T) {
	dir := initRepo(t)
	if _, _, _, err := Resolve(OpBranch, dir, "--force", ""); err == nil {
		t.Fatal("Resolve(OpBranch, …, \"--force\") = nil error, want a rejection")
	}
}

// TestWorktreeAddRejectsFlagBranch confirms the worktree bridge refuses a
// flag-shaped branch before shelling out — no git repo needed, since validation
// precedes the exec.
func TestWorktreeAddRejectsFlagBranch(t *testing.T) {
	err := WorktreeAdd(t.TempDir(), "--force", filepath.Join(t.TempDir(), "wt"))
	if err == nil || !strings.Contains(err.Error(), "invalid branch name") {
		t.Fatalf("WorktreeAdd with a flag-shaped branch = %v, want an 'invalid branch name' rejection", err)
	}
}
