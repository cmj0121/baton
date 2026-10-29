package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skillEnv gives a test its own HOME and cwd, both temp dirs, so neither the
// operator's ~/.claude/skills nor this repository's .claude is ever touched.
func skillEnv(t *testing.T) (home, cwd string) {
	t.Helper()
	home, cwd = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(cwd)
	return home, cwd
}

func runSkill(args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = skillMain(args, &out, &errb)
	return code, out.String(), errb.String()
}

func mustExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err != nil {
		t.Errorf("%s: %v", p, err)
	}
}

// resolved is p with symlinks in its directory resolved, because the cwd a
// test chdirs into may be reached through one (macOS's /var → /private/var)
// and the command prints the absolute path it resolved.
func resolved(t *testing.T, p string) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(d, filepath.Base(p))
}

// TestSkillInstallDefaultsToTheProject: no flag writes the outside set and its
// manifest under ./.claude/skills, prints each path, and leaves HOME alone.
func TestSkillInstallDefaultsToTheProject(t *testing.T) {
	home, cwd := skillEnv(t)
	code, out, errs := runSkill("install")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	dir := filepath.Join(cwd, ".claude", "skills")
	for _, name := range []string{"baton", "conduct", "drive"} {
		p := filepath.Join(dir, name, "SKILL.md")
		mustExist(t, p)
		if !strings.Contains(out, resolved(t, p)) && !strings.Contains(out, p) {
			t.Errorf("output does not print %s:\n%s", p, out)
		}
	}
	mustExist(t, filepath.Join(dir, ".baton-skills"))
	if strings.Contains(out, "warning") {
		t.Errorf("a project install printed the --user warning:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
		t.Error("a project install wrote under HOME")
	}
}

// TestSkillInstallUserWritesHomeAndWarns: --user lands under HOME and says the
// personal copies shadow the conductor's and must be re-run after an upgrade.
func TestSkillInstallUserWritesHomeAndWarns(t *testing.T) {
	home, cwd := skillEnv(t)
	code, out, errs := runSkill("install", "--user")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	for _, name := range []string{"baton", "conduct", "drive"} {
		mustExist(t, filepath.Join(home, ".claude", "skills", name, "SKILL.md"))
	}
	for _, want := range []string{"shadow", "conductor", "after upgrading"} {
		if !strings.Contains(out, want) {
			t.Errorf("--user output lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(cwd, ".claude")); err == nil {
		t.Error("a --user install wrote under cwd")
	}
}

// TestSkillInstallRefusesAForeignSkillUntilForced: the operator's conduct/ is
// refused with exit 1 and nothing written, then replaced under --force.
func TestSkillInstallRefusesAForeignSkillUntilForced(t *testing.T) {
	_, cwd := skillEnv(t)
	dir := filepath.Join(cwd, ".claude", "skills")
	foreign := filepath.Join(dir, "conduct", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(foreign), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, _, errs := runSkill("install")
	if code != 1 || !strings.Contains(errs, "conduct") {
		t.Fatalf("exit %d, stderr %q; want 1 naming conduct", code, errs)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("a refused install wrote something: %v", entries)
	}
	if b, _ := os.ReadFile(foreign); string(b) != "mine" {
		t.Errorf("a refused install touched the foreign skill: %q", b)
	}

	if code, _, errs := runSkill("install", "--force"); code != 0 {
		t.Fatalf("--force: exit %d, stderr %q", code, errs)
	}
	if b, _ := os.ReadFile(foreign); string(b) == "mine" {
		t.Error("--force did not replace the foreign skill")
	}
}

// TestSkillUninstallRemovesOnlyOwnedNames: owned names and the manifest go and
// are printed; the operator's own skill beside them stays.
func TestSkillUninstallRemovesOnlyOwnedNames(t *testing.T) {
	_, cwd := skillEnv(t)
	if code, _, errs := runSkill("install"); code != 0 {
		t.Fatalf("install: exit %d, stderr %q", code, errs)
	}
	dir := filepath.Join(cwd, ".claude", "skills")
	mine := filepath.Join(dir, "mine", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(mine), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errs := runSkill("uninstall")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	for _, name := range []string{"baton", "conduct", "drive", ".baton-skills"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s survived uninstall", name)
		}
		if !strings.Contains(out, name) {
			t.Errorf("output does not print %s:\n%s", name, out)
		}
	}
	mustExist(t, mine)

	if code, out, _ := runSkill("uninstall"); code != 0 || !strings.Contains(out, "nothing to remove") {
		t.Errorf("second uninstall: exit %d, out %q", code, out)
	}
}

// TestSkillUninstallUser removes from HOME, not cwd.
func TestSkillUninstallUser(t *testing.T) {
	home, _ := skillEnv(t)
	if code, _, errs := runSkill("install", "--user"); code != 0 {
		t.Fatalf("install: exit %d, stderr %q", code, errs)
	}
	if code, _, errs := runSkill("uninstall", "--user"); code != 0 {
		t.Fatalf("uninstall: exit %d, stderr %q", code, errs)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "baton")); err == nil {
		t.Error("uninstall --user left baton/ under HOME")
	}
}

// TestSkillUsageErrorsExitTwo: a bad command line is 2, distinct from a
// command that ran and failed, and it prints usage as ctl does.
func TestSkillUsageErrorsExitTwo(t *testing.T) {
	skillEnv(t)
	for _, args := range [][]string{
		{},
		{"frobnicate"},
		{"install", "--project", "--user"},
		{"uninstall", "--force"},
	} {
		code, out, errs := runSkill(args...)
		if code != 2 {
			t.Errorf("%v: exit %d, stderr %q; want 2", args, code, errs)
		}
		if !strings.Contains(out, "Usage: baton skill") {
			t.Errorf("%v: no usage printed:\n%s", args, out)
		}
	}
}

// TestSkillUninstallFailureExitsOne: an unreadable manifest is a failure of
// the command, not of its command line.
func TestSkillUninstallFailureExitsOne(t *testing.T) {
	_, cwd := skillEnv(t)
	m := filepath.Join(cwd, ".claude", "skills", ".baton-skills")
	if err := os.MkdirAll(filepath.Dir(m), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m, bytes.Repeat([]byte("x"), 8<<10), 0o600); err != nil { // over the cap
		t.Fatal(err)
	}
	if code, _, errs := runSkill("uninstall"); code != 1 {
		t.Errorf("exit %d, stderr %q; want 1", code, errs)
	}
}

// TestSkillProjectRefusesAClaudeLinkedOutside: a cloned repository commits
// .claude as a symlink out of the tree. Install and uninstall both refuse,
// naming the path and where it resolves, and nothing out there changes — a
// dangling link included, which MkdirAll would otherwise follow.
func TestSkillProjectRefusesAClaudeLinkedOutside(t *testing.T) {
	_, cwd := skillEnv(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "skills")
	for _, name := range []string{"baton", "conduct", "drive"} {
		if err := os.MkdirAll(filepath.Join(victim, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(victim, ".baton-skills"), []byte("baton\nconduct\ndrive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cwd, ".claude")); err != nil {
		t.Fatal(err)
	}
	snap := func() []string {
		var out []string
		_ = filepath.WalkDir(outside, func(p string, _ os.DirEntry, _ error) error {
			out = append(out, p)
			return nil
		})
		return out
	}
	before := strings.Join(snap(), "\n")

	for _, args := range [][]string{{"install"}, {"install", "--force"}, {"uninstall"}} {
		code, _, errs := runSkill(args...)
		if code != 1 {
			t.Errorf("%v: exit %d, stderr %q; want 1", args, code, errs)
		}
		if !strings.Contains(errs, resolved(t, victim)) || !strings.Contains(errs, filepath.Join(cwd, ".claude", "skills")) {
			t.Errorf("%v: refusal does not name both paths: %q", args, errs)
		}
	}
	if after := strings.Join(snap(), "\n"); after != before {
		t.Errorf("a refused command changed the link's target:\nbefore %s\nafter  %s", before, after)
	}

	if err := os.Remove(filepath.Join(cwd, ".claude")); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(outside, "not-yet")
	if err := os.Symlink(dangling, filepath.Join(cwd, ".claude")); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runSkill("install"); code != 1 {
		t.Errorf("dangling .claude: exit %d, stderr %q; want 1", code, errs)
	}
	if _, err := os.Lstat(dangling); err == nil {
		t.Error("install created the dangling link's target")
	}
}

// TestSkillProjectAcceptsALinkInsideCwd: a .claude linked to somewhere inside
// the repository is still the repository, so install writes there and prints
// the resolved dir first.
func TestSkillProjectAcceptsALinkInsideCwd(t *testing.T) {
	_, cwd := skillEnv(t)
	inner := filepath.Join(cwd, "config", "claude")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inner, filepath.Join(cwd, ".claude")); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runSkill("install")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	want := filepath.Join(resolved(t, inner), "skills")
	if first, _, _ := strings.Cut(out, "\n"); first != "skills dir: "+want {
		t.Errorf("first line %q; want the resolved dir %s", first, want)
	}
	mustExist(t, filepath.Join(inner, "skills", "conduct", "SKILL.md"))
	if code, _, errs := runSkill("uninstall"); code != 0 {
		t.Fatalf("uninstall: exit %d, stderr %q", code, errs)
	}
	if _, err := os.Stat(filepath.Join(inner, "skills", "conduct")); err == nil {
		t.Error("uninstall through an inside link left conduct/")
	}
}
