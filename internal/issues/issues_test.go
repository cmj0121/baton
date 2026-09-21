package issues

import (
	"strings"
	"testing"
	"time"
)

func TestParseGitHubRemote(t *testing.T) {
	cases := []struct {
		in   string
		want Repo
	}{
		{"git@github.com:cmj0121/baton.git", Repo{"cmj0121", "baton"}},
		{"https://github.com/cmj0121/baton.git", Repo{"cmj0121", "baton"}},
		{"https://github.com/cmj0121/baton", Repo{"cmj0121", "baton"}},
		{"ssh://git@github.com/cmj0121/baton.git", Repo{"cmj0121", "baton"}},
		{"ssh://git@gitea.home.lab:2222/cmj/baton.git", Repo{}},
		{"git@gitea.home.lab:cmj/baton.git", Repo{}},
		{"", Repo{}},
	}
	for _, c := range cases {
		if got := ParseGitHubRemote(c.in); got != c.want {
			t.Errorf("ParseGitHubRemote(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestPickGitHubRemotePrefersNamedGITHUBNotOrigin(t *testing.T) {
	out := strings.Join([]string{
		"origin\tssh://git@gitea.home.lab:2222/cmj/baton.git (fetch)",
		"origin\tssh://git@gitea.home.lab:2222/cmj/baton.git (push)",
		"GITHUB\tgit@github.com:cmj0121/baton.git (fetch)",
		"GITHUB\tgit@github.com:cmj0121/baton.git (push)",
	}, "\n")
	got := PickGitHubRemote(out)
	if got != (Repo{"cmj0121", "baton"}) {
		t.Fatalf("got %+v, want cmj0121/baton from the GITHUB remote", got)
	}
}

func TestPickGitHubRemoteFirstGitHubURLWhenNoNamedRemote(t *testing.T) {
	out := "upstream\thttps://github.com/cmj0121/baton.git (fetch)\n"
	got := PickGitHubRemote(out)
	if got != (Repo{"cmj0121", "baton"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestPickGitHubRemoteEmptyWhenOnlyGiteaOrigin(t *testing.T) {
	out := "origin\tssh://git@gitea.home.lab:2222/cmj/baton.git (fetch)\n"
	if got := PickGitHubRemote(out); got.Owner != "" {
		t.Fatalf("origin that is not GitHub must not win, got %+v", got)
	}
}

func TestBranchNumber(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"feat/127-docs", 127},
		{"feat/127", 127},
		{"127", 127},
		{"issue-12-fix", 12},
		{"#128-foo", 128},
		{"main", 0},
		{"", 0},
		{"release/v2.2.2", 0},
		{"v2.2.2", 0},
		{"go1.22", 0},
	}
	for _, c := range cases {
		if got := BranchNumber(c.in); got != c.want {
			t.Errorf("BranchNumber(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAutoChain(t *testing.T) {
	pr := &Pull{Number: 130, Closes: []int{127}}
	if got := AutoChain(99, "feat/127-docs", pr); got != 99 {
		t.Errorf("bind wins, got %d", got)
	}
	if got := AutoChain(0, "feat/127-docs", pr); got != 127 {
		t.Errorf("Closes wins, got %d", got)
	}
	if got := AutoChain(0, "feat/wip", &Pull{Number: 130}); got != 130 {
		t.Errorf("PR number when no Closes, got %d", got)
	}
	if got := AutoChain(0, "feat/127-docs", nil); got != 127 {
		t.Errorf("branch digits, got %d", got)
	}
	if got := AutoChain(0, "main", nil); got != 0 {
		t.Errorf("nothing, got %d", got)
	}
}

func TestClassifyRightmostWins(t *testing.T) {
	if c := Classify(true, true); c != PR {
		t.Errorf("PR+panel → %s, want PR", c)
	}
	if c := Classify(false, true); c != InProcess {
		t.Errorf("panel only → %s, want IN PROCESS", c)
	}
	if c := Classify(false, false); c != Backlog {
		t.Errorf("neither → %s, want BACKLOG", c)
	}
}

func TestPRCloses(t *testing.T) {
	pulls := []Pull{{Number: 130, Closes: []int{127}}}
	if !PRCloses(pulls, 127) {
		t.Fatal("127 is closed by 130")
	}
	if PRCloses(pulls, 99) {
		t.Fatal("99 is not closed by any PR")
	}
	if !PRCloses(pulls, 130) {
		t.Fatal("the PR itself sits in the PR column")
	}
}

func TestRefreshDuration(t *testing.T) {
	if d := RefreshDuration(nil); d != 60*time.Second {
		t.Errorf("unset = %v, want 60s", d)
	}
	z := 0
	if d := RefreshDuration(&z); d != 0 {
		t.Errorf("0 = %v, want manual (0)", d)
	}
	n := 10
	if d := RefreshDuration(&n); d != 30*time.Second {
		t.Errorf("10 = %v, want 30s floor", d)
	}
	n = 90
	if d := RefreshDuration(&n); d != 90*time.Second {
		t.Errorf("90 = %v, want 90s", d)
	}
}

func TestBlockedByUsesRESTNumericID(t *testing.T) {
	r := fakeRunner{
		gh: map[string]string{
			"api repos/cmj0121/baton/issues/99":                                                   `{"id": 4242, "number": 99}`,
			"api -X POST repos/cmj0121/baton/issues/128/dependencies/blocked_by -F issue_id=4242": `{}`,
		},
	}
	if err := BlockedBy(r, "/tmp", Repo{"cmj0121", "baton"}, 128, 99); err != nil {
		t.Fatal(err)
	}
}

func TestColString(t *testing.T) {
	if Backlog.String() != "BACKLOG" || InProcess.String() != "IN PROCESS" || PR.String() != "PR" {
		t.Fatalf("column names drifted")
	}
}

// fakeRunner answers Git/GH by joining the argv, so a case reads as the command
// it is pretending to be.
type fakeRunner struct {
	git map[string]string
	gh  map[string]string
	err map[string]error
}

func (f fakeRunner) Git(_ string, args ...string) ([]byte, error) {
	return f.lookup(f.git, args)
}
func (f fakeRunner) GH(_ string, args ...string) ([]byte, error) {
	return f.lookup(f.gh, args)
}
func (f fakeRunner) lookup(m map[string]string, args []string) ([]byte, error) {
	k := strings.Join(args, " ")
	if f.err != nil {
		if err, ok := f.err[k]; ok {
			return nil, err
		}
	}
	if m != nil {
		if s, ok := m[k]; ok {
			return []byte(s), nil
		}
	}
	return nil, errNoCmd(k)
}

type errNoCmd string

func (e errNoCmd) Error() string { return "no fake for " + string(e) }

func TestLoadBoardNotAGitRepo(t *testing.T) {
	r := fakeRunner{err: map[string]error{"rev-parse --show-toplevel": errNoCmd("git")}}
	if _, err := LoadBoard(r, "/tmp"); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadBoardNoGitHubRemote(t *testing.T) {
	r := fakeRunner{git: map[string]string{
		"rev-parse --show-toplevel": "/tmp/repo",
		"remote -v":                 "origin\tssh://git@gitea.home.lab:2222/cmj/baton.git (fetch)\n",
	}}
	if _, err := LoadBoard(r, "/tmp/repo"); err == nil || !strings.Contains(err.Error(), "no GitHub remote") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadBoardHappy(t *testing.T) {
	r := fakeRunner{
		git: map[string]string{
			"rev-parse --show-toplevel": "/tmp/repo",
			"remote -v":                 "GITHUB\tgit@github.com:cmj0121/baton.git (fetch)\n",
			"branch --show-current":     "feat/127-docs\n",
		},
		gh: map[string]string{
			"issue list --repo cmj0121/baton --state open --limit 100 --json number,title,body,milestone,parent,subIssues,blockedBy":     `[{"number":128,"title":"overlay","body":"do it","milestone":{"title":"2.3.0"},"parent":{"number":127},"subIssues":{"nodes":[]},"blockedBy":{"nodes":[{"number":99}]}}]`,
			"pr list --repo cmj0121/baton --state open --limit 100 --json number,title,body,isDraft,headRefName,closingIssuesReferences": `[{"number":130,"title":"docs","body":"","isDraft":false,"headRefName":"feat/127-docs","closingIssuesReferences":[{"number":127}]}]`,
			"api repos/cmj0121/baton/milestones?state=open":                                `[{"number":1,"title":"2.3.0"}]`,
			"issue list --repo cmj0121/baton --state closed --limit 100 --json number":     `[{"number":1},{"number":2}]`,
			"pr view --json number,title,body,isDraft,headRefName,closingIssuesReferences": `{"number":130,"title":"docs","body":"","isDraft":false,"headRefName":"feat/127-docs","closingIssuesReferences":[{"number":127}]}`,
		},
	}
	b, err := LoadBoard(r, "/tmp/repo")
	if err != nil {
		t.Fatal(err)
	}
	if b.Repo != (Repo{"cmj0121", "baton"}) {
		t.Errorf("repo %+v", b.Repo)
	}
	if b.Branch != "feat/127-docs" || b.ClosedCount != 2 {
		t.Errorf("branch %q closed %d", b.Branch, b.ClosedCount)
	}
	if len(b.Milestones) != 1 || b.Milestones[0] != "2.3.0" {
		t.Errorf("milestones %v", b.Milestones)
	}
	if len(b.Issues) < 1 || b.Issues[0].Number != 128 || b.Issues[0].Milestone != "2.3.0" {
		t.Errorf("issues %+v", b.Issues)
	}
	if b.Issues[0].Parent != 127 || len(b.Issues[0].BlockedBy) != 1 || b.Issues[0].BlockedBy[0] != 99 {
		t.Errorf("relations %+v", b.Issues[0])
	}
	if len(b.Issues) != 2 || b.Issues[1].Number != 130 {
		t.Errorf("PR-only cards should merge in, got %+v", b.Issues)
	}
	if b.CurrentPR == nil || b.CurrentPR.Number != 130 || b.CurrentPR.Closes[0] != 127 {
		t.Errorf("current PR %+v", b.CurrentPR)
	}
}

func TestFediqoShapedBoard(t *testing.T) {
	r := fakeRunner{
		git: map[string]string{
			"rev-parse --show-toplevel": "/tmp/fediqo",
			"remote -v":                 "origin\tgit@github.com:cmj0121/fediqo.git (fetch)\n",
			"branch --show-current":     "main\n",
		},
		gh: map[string]string{
			"issue list --repo cmj0121/fediqo --state open --limit 100 --json number,title,body,milestone,parent,subIssues,blockedBy": `[
				{"number":1,"title":"What Fediqo is","body":"","milestone":null,"parent":null,"subIssues":{"nodes":[{"number":8},{"number":9}]},"blockedBy":{"nodes":[]}},
				{"number":8,"title":"0.2.0: a timeline you wrote is a query of the store","body":"","milestone":{"title":"0.2.0"},"parent":{"number":1},"subIssues":{"nodes":[{"number":21},{"number":31}]},"blockedBy":{"nodes":[]}},
				{"number":9,"title":"0.3.0: another of your devices can hold this store","body":"","milestone":{"title":"0.3.0"},"parent":{"number":1},"subIssues":{"nodes":[]},"blockedBy":{"nodes":[]}},
				{"number":6,"title":"a sign-in can move to a device that is nearby","body":"","milestone":null,"parent":{"number":1},"subIssues":{"nodes":[]},"blockedBy":{"nodes":[]}}
			]`,
			"pr list --repo cmj0121/fediqo --state open --limit 100 --json number,title,body,isDraft,headRefName,closingIssuesReferences": `[]`,
			"api repos/cmj0121/fediqo/milestones?state=open":                            `[{"number":2,"title":"0.2.0"},{"number":3,"title":"0.3.0"}]`,
			"issue list --repo cmj0121/fediqo --state closed --limit 100 --json number": `[{"number":2},{"number":3},{"number":4},{"number":5},{"number":7},{"number":10},{"number":11}]`,
		},
		err: map[string]error{
			"pr view --json number,title,body,isDraft,headRefName,closingIssuesReferences": errNoCmd("no pr"),
		},
	}
	b, err := LoadBoard(r, "/tmp/fediqo")
	if err != nil {
		t.Fatal(err)
	}
	if b.ClosedCount != 7 {
		t.Errorf("closed %d, want 7 (0.1.0)", b.ClosedCount)
	}
	if len(b.Milestones) != 2 || b.Milestones[0] != "0.2.0" || b.Milestones[1] != "0.3.0" {
		t.Fatalf("milestones %v, want 0.2.0 then 0.3.0 (closed 0.1.0 stays off the chips)", b.Milestones)
	}
	var byNum = map[int]Issue{}
	for _, iss := range b.Issues {
		byNum[iss.Number] = iss
	}
	if byNum[8].Parent != 1 || len(byNum[8].Sub) != 2 {
		t.Errorf("#8 relations %+v", byNum[8])
	}
	if byNum[1].Milestone != "" || byNum[8].Milestone != "0.2.0" || byNum[9].Milestone != "0.3.0" {
		t.Errorf("milestone fields %+v %+v %+v", byNum[1], byNum[8], byNum[9])
	}
	if Classify(false, false) != Backlog {
		t.Fatal("open issues with no panel sit in BACKLOG")
	}
}

func TestSetCurrentPRMatchesBranchHead(t *testing.T) {
	b := Board{
		Branch: "feat/127-docs",
		Pulls:  []Pull{{Number: 9, Head: "other"}, {Number: 130, Head: "feat/127-docs", Closes: []int{127}}},
	}
	setCurrentPR(&b)
	if b.CurrentPR == nil || b.CurrentPR.Number != 130 {
		t.Fatalf("got %+v", b.CurrentPR)
	}
	b.Branch = "main"
	b.CurrentPR = nil
	setCurrentPR(&b)
	if b.CurrentPR != nil {
		t.Fatal("main has no PR in the list")
	}
}
