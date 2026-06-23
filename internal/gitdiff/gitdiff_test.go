package gitdiff

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGit skips the test when git is not on PATH, so the suite stays green on
// a machine (or CI image) without it. It also neutralises the developer's global
// and system git config for this process, so a machine that configures a
// diff.tool globally cannot leak into ResolveCommand (which reads the process
// environment via exec). Pointing the config files at /dev/null gives every
// freshly-init'd repo a clean, predictable config.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

// gitEnv is the author/committer identity plus the neutralised global/system
// config for the throwaway repos. It lets commits work in CI with no global git
// config, and keeps a developer's global diff.tool out of the fixtures.
var gitEnv = append(os.Environ(),
	"GIT_AUTHOR_NAME=baton",
	"GIT_AUTHOR_EMAIL=baton@example.com",
	"GIT_COMMITTER_NAME=baton",
	"GIT_COMMITTER_EMAIL=baton@example.com",
	"GIT_CONFIG_GLOBAL="+os.DevNull,
	"GIT_CONFIG_SYSTEM="+os.DevNull,
)

// runGitT runs git in dir for test setup, failing the test on any error.
func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// initRepo makes a fresh git repo in a temp dir and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitT(t, dir, "init", "-q")
	return dir
}

// commitFile writes name with content and commits it, so the repo has a HEAD.
func commitFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "add", name)
	runGitT(t, dir, "commit", "-q", "-m", "add "+name)
}

func TestIsWorkTree(t *testing.T) {
	requireGit(t)

	repo := initRepo(t)
	if !IsWorkTree(repo) {
		t.Error("a fresh git repo should be a work tree")
	}

	plain := t.TempDir()
	if IsWorkTree(plain) {
		t.Error("a non-repo dir should not be a work tree")
	}
}

func TestHasChanges(t *testing.T) {
	requireGit(t)

	repo := initRepo(t)
	commitFile(t, repo, "a.txt", "hello\n")
	if HasChanges(repo) {
		t.Error("a clean repo should report no changes")
	}

	// An untracked file must count — porcelain lists it, and it is the most
	// common agent output.
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("fresh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !HasChanges(repo) {
		t.Error("a repo with an untracked file should report changes")
	}
}

func TestResolveCommandExplicit(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)

	name, args := ResolveCommand(repo, "delta")
	if name != "sh" || len(args) != 2 || args[0] != "-c" || args[1] != "delta" {
		t.Fatalf("explicit command should run via sh -c, got %q %v", name, args)
	}
}

func TestResolveCommandDiffTool(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	runGitT(t, repo, "config", "diff.tool", "foo")

	name, args := ResolveCommand(repo, "")
	want := []string{"difftool", "-d", "--no-prompt"}
	if name != "git" || strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("a configured diff.tool should resolve to git difftool, got %q %v", name, args)
	}
}

func TestResolveCommandBuiltin(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)

	name, args := ResolveCommand(repo, "")
	if name != "sh" || len(args) != 2 || args[0] != "-c" || args[1] != builtinScript {
		t.Fatalf("with no explicit command or diff.tool the built-in script should run, got %q %v", name, args)
	}
}

// TestBuiltinScriptUntrackedAndNonMutating runs the built-in script end-to-end:
// an untracked file's name must appear in the diff (proves untracked inclusion),
// and `git status --porcelain` must be unchanged afterward (proves the script
// never touches the real index/worktree).
func TestBuiltinScriptUntrackedAndNonMutating(t *testing.T) {
	requireGit(t)

	repo := initRepo(t)
	commitFile(t, repo, "a.txt", "hello\n")
	if err := os.WriteFile(filepath.Join(repo, "newfile.txt"), []byte("brand new\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	before := porcelain(t, repo)

	cmd := exec.Command("sh", "-c", builtinScript)
	cmd.Dir = repo
	cmd.Env = gitEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("built-in script failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "newfile.txt") {
		t.Fatalf("the diff should include the untracked file, got:\n%s", out)
	}
	// Assert the file's *content* flows through, not just a filename mention — a
	// header-only diff would still contain "newfile.txt" but miss the real change.
	if !strings.Contains(string(out), "+brand new") {
		t.Fatalf("the diff should include the untracked file's added content, got:\n%s", out)
	}

	after := porcelain(t, repo)
	if before != after {
		t.Fatalf("the built-in script mutated the work tree\nbefore: %q\nafter:  %q", before, after)
	}
}

// porcelain returns `git status --porcelain` output for dir.
func porcelain(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	cmd.Env = gitEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	return string(out)
}

func TestCollect(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	commitFile(t, dir, "staged.txt", "one\n")
	commitFile(t, dir, "work.txt", "alpha\n")

	// Stage a change to staged.txt; leave a change to work.txt unstaged; add a
	// brand-new untracked file.
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("fresh\nlines\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	changes, err := Collect(dir)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	byPath := make(map[string]FileChange, len(changes))
	for _, c := range changes {
		byPath[c.Path] = c
	}

	st, ok := byPath["staged.txt"]
	if !ok || st.Index != "M" || st.Staged == "" {
		t.Fatalf("staged.txt: want index M with staged diff, got %+v", st)
	}
	if !strings.Contains(st.Staged, "+two") {
		t.Errorf("staged.txt staged diff missing the added line:\n%s", st.Staged)
	}

	wk, ok := byPath["work.txt"]
	if !ok || wk.Work != "M" || wk.Unstaged == "" {
		t.Fatalf("work.txt: want work M with unstaged diff, got %+v", wk)
	}
	if !strings.Contains(wk.Unstaged, "+beta") {
		t.Errorf("work.txt unstaged diff missing the added line:\n%s", wk.Unstaged)
	}

	nw, ok := byPath["new.txt"]
	if !ok || nw.Work != "?" {
		t.Fatalf("new.txt: want untracked (work ?), got %+v", nw)
	}
	if !strings.Contains(nw.Unstaged, "new file: new.txt") || !strings.Contains(nw.Unstaged, "+fresh") {
		t.Errorf("new.txt should render as an added file:\n%s", nw.Unstaged)
	}
}

func TestCollectNotARepo(t *testing.T) {
	requireGit(t)
	if _, err := Collect(t.TempDir()); err == nil {
		t.Fatal("Collect should fail outside a work tree")
	}
}

func TestRenderUntrackedBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.bin"), []byte{0x00, 0x01, 0x02}, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := renderUntracked(dir, "b.bin"); !strings.Contains(got, "(binary)") {
		t.Errorf("binary file should be summarised, got %q", got)
	}
	if got := renderUntracked(dir, "missing.txt"); !strings.Contains(got, "unreadable") {
		t.Errorf("missing file should be summarised, got %q", got)
	}
}
