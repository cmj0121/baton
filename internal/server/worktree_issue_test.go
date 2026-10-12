package server

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/issues"
)

// gitBindRunner is a git-only issues.Runner. GH fails the test if the bind
// path ever shells out to gh.
type gitBindRunner struct {
	git map[string]string
	gh  int
}

func (g *gitBindRunner) Git(_ string, args ...string) ([]byte, error) {
	if g.git == nil {
		panic("git should not run")
	}
	if s, ok := g.git[strings.Join(args, " ")]; ok {
		return []byte(s), nil
	}
	return nil, errBind("no git fake")
}

func (g *gitBindRunner) GH(string, ...string) ([]byte, error) {
	g.gh++
	return nil, errBind("gh must not run")
}

type errBind string

func (e errBind) Error() string { return string(e) }

func TestBindSpawnedIssueKeysTheWorktree(t *testing.T) {
	t.Cleanup(issues.UseBindFile(filepath.Join(t.TempDir(), "binds.json")))
	run := &gitBindRunner{git: map[string]string{
		"rev-parse --show-toplevel": "/src/repo\n",
		"remote -v":                 "origin\tgit@github.com:cmj0121/baton.git (fetch)\n",
	}}
	s := &Server{issuesRun: run}
	s.bindSpawnedIssue("/src/repo", "/trees/feat-127", 127)
	if run.gh != 0 {
		t.Fatal("the bind runs git, never gh")
	}
	got := issues.LoadBinds()
	repo := issues.Repo{Owner: "cmj0121", Name: "baton"}
	key := issues.BindKey(repo, "/trees/feat-127")
	if got[key] != 127 {
		t.Fatalf("binds = %v, want %s = 127", got, key)
	}
	if _, ok := got[issues.BindKey(repo, "/src/repo")]; ok {
		t.Fatal("the bind must name the worktree, not the repo root")
	}
}

func TestBindSpawnedIssueSkipsWithoutGitHub(t *testing.T) {
	t.Cleanup(issues.UseBindFile(filepath.Join(t.TempDir(), "binds.json")))
	s := &Server{issuesRun: &gitBindRunner{git: map[string]string{
		"rev-parse --show-toplevel": "/src/repo\n",
		"remote -v":                 "origin\tssh://git@gitea.home.lab:2222/cmj/baton.git (fetch)\n",
	}}}
	s.bindSpawnedIssue("/src/repo", "/trees/feat-127", 127)
	if len(issues.LoadBinds()) != 0 {
		t.Fatal("no GitHub remote must skip the bind and keep the add")
	}
}

func TestBindSpawnedIssueIgnoresZero(t *testing.T) {
	s := &Server{issuesRun: &gitBindRunner{}}
	s.bindSpawnedIssue("/src/repo", "/trees/feat-127", 0)
}

func TestIssuesRunnerDefaultsToGit(t *testing.T) {
	if _, ok := (&Server{}).issuesRunner().(issues.ExecRunner); !ok {
		t.Fatal("a nil runner field should be the real git runner")
	}
}
