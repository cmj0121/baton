// Package issues talks to GitHub about the repo under a panel's cwd.
//
// The cockpit opens this as an overlay; the package itself has no TUI. It
// resolves which GitHub repository a working tree belongs to, loads the open
// issues and pull requests, and classifies each card into BACKLOG, IN PROCESS
// or PR. Closed issues are a count, never a card.
package issues

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ghTimeout  = 15 * time.Second
	gitTimeout = 2 * time.Second
)

// Col is one board column. The column is the card's status.
type Col int

const (
	// Backlog is an open issue with no live panel and no open PR.
	Backlog Col = iota
	// InProcess is an open issue with a live handling panel and no open PR.
	InProcess
	// PR is an open pull request (or an issue closed by one).
	PR
)

func (c Col) String() string {
	switch c {
	case InProcess:
		return "IN PROCESS"
	case PR:
		return "PR"
	default:
		return "BACKLOG"
	}
}

// Repo is a GitHub owner/name pair resolved from git remotes.
type Repo struct {
	Owner string
	Name  string
}

func (r Repo) String() string {
	if r.Owner == "" {
		return ""
	}
	return r.Owner + "/" + r.Name
}

// Issue is one open GitHub issue as the board needs it.
type Issue struct {
	Number    int
	Title     string
	Body      string
	Milestone string
	BlockedBy []int
	Parent    int
	Sub       []int
}

// Pull is one open pull request.
type Pull struct {
	Number int
	Title  string
	Body   string
	Draft  bool
	Head   string
	Closes []int
}

// Board is one fetch of a repo's open work.
type Board struct {
	Repo        Repo
	Branch      string
	Milestones  []string
	Issues      []Issue
	Pulls       []Pull
	ClosedCount int
	CurrentPR   *Pull
}

// Runner is the git/gh binary surface, swapped in tests.
type Runner interface {
	Git(dir string, args ...string) ([]byte, error)
	GH(dir string, args ...string) ([]byte, error)
}

// ExecRunner runs the real git and gh binaries.
type ExecRunner struct{}

// Git runs `git args...` in dir.
func (ExecRunner) Git(dir string, args ...string) ([]byte, error) {
	return run(gitTimeout, dir, "git", args...)
}

// GH runs `gh args...` in dir.
func (ExecRunner) GH(dir string, args ...string) ([]byte, error) {
	return run(ghTimeout, dir, "gh", args...)
}

func run(d time.Duration, dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s: %s", name, msg)
	}
	return stdout.Bytes(), nil
}

// ParseGitHubRemote extracts owner/name from a GitHub remote URL.
// Non-GitHub URLs return the zero Repo.
func ParseGitHubRemote(url string) Repo {
	s := strings.TrimSpace(url)
	s = strings.TrimSuffix(s, ".git")
	switch {
	case strings.HasPrefix(s, "git@github.com:"):
		return splitOwnerName(strings.TrimPrefix(s, "git@github.com:"))
	case strings.HasPrefix(s, "ssh://git@github.com/"):
		return splitOwnerName(strings.TrimPrefix(s, "ssh://git@github.com/"))
	case strings.Contains(s, "github.com/"):
		_, rest, ok := strings.Cut(s, "github.com/")
		if !ok {
			return Repo{}
		}
		return splitOwnerName(rest)
	default:
		return Repo{}
	}
}

func splitOwnerName(rest string) Repo {
	rest = strings.TrimPrefix(rest, "/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return Repo{}
	}
	return Repo{Owner: parts[0], Name: parts[1]}
}

// PickGitHubRemote chooses the GitHub remote from `git remote -v` output.
// A remote named GITHUB (any case) wins; otherwise the first github.com URL.
// origin is never used just because it is origin.
func PickGitHubRemote(remoteV string) Repo {
	type row struct {
		name string
		repo Repo
	}
	var rows []row
	for _, line := range strings.Split(remoteV, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		r := ParseGitHubRemote(f[1])
		if r.Owner == "" {
			continue
		}
		rows = append(rows, row{name: f[0], repo: r})
	}
	for _, r := range rows {
		if strings.EqualFold(r.name, "GITHUB") {
			return r.repo
		}
	}
	if len(rows) > 0 {
		return rows[0].repo
	}
	return Repo{}
}

// branchIssueNum matches an issue number in a branch: feat/127-docs, issue-12-fix,
// #128, or a branch that is just digits. It refuses version-like runs (v2.2.2).
var branchIssueNum = regexp.MustCompile(`(?:^|[-/#])(\d{2,})(?:[-/]|$)`)

// BranchNumber returns the issue number encoded in a branch name, or zero.
func BranchNumber(branch string) int {
	s := strings.TrimSpace(branch)
	if s == "" {
		return 0
	}
	if i := strings.LastIndex(s, "#"); i >= 0 {
		if n, err := strconv.Atoi(leadingDigits(s[i+1:])); err == nil && n > 0 {
			return n
		}
	}
	m := branchIssueNum.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// AutoChain picks the current issue number: explicit bind, then the open
// PR's Closes, then digits in the branch name.
func AutoChain(bind int, branch string, pr *Pull) int {
	if bind > 0 {
		return bind
	}
	if pr != nil && len(pr.Closes) > 0 {
		return pr.Closes[0]
	}
	if pr != nil && pr.Number > 0 {
		return pr.Number
	}
	return BranchNumber(branch)
}

// Classify puts an open issue in one column. Rightmost match wins:
// open PR → PR; live handling panel → IN PROCESS; else BACKLOG.
func Classify(hasOpenPR, hasLivePanel bool) Col {
	switch {
	case hasOpenPR:
		return PR
	case hasLivePanel:
		return InProcess
	default:
		return Backlog
	}
}

// PRFor returns the open PR that is this issue or names it in Closes.
func PRFor(pulls []Pull, issue int) (Pull, bool) {
	for _, p := range pulls {
		if p.Number == issue || slices.Contains(p.Closes, issue) {
			return p, true
		}
	}
	return Pull{}, false
}

// PRCloses reports whether any open PR is this issue or names it in Closes.
func PRCloses(pulls []Pull, issue int) bool {
	_, ok := PRFor(pulls, issue)
	return ok
}

// ResolveRepo finds the top of the working tree at dir and the GitHub repo its
// remotes point at. It runs git only, never gh.
func ResolveRepo(r Runner, dir string) (root string, repo Repo, err error) {
	top, err := r.Git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", Repo{}, fmt.Errorf("not a git repository")
	}
	root = strings.TrimSpace(string(top))
	remoteV, err := r.Git(root, "remote", "-v")
	if err != nil {
		return "", Repo{}, err
	}
	repo = PickGitHubRemote(string(remoteV))
	if repo.Owner == "" {
		return "", Repo{}, fmt.Errorf("no GitHub remote (looked for github.com or a remote named GITHUB)")
	}
	return root, repo, nil
}

// LoadBoard fetches the GitHub board for a working tree. dir must be inside a
// git repo whose remotes include GitHub. The four gh calls are independent and
// each costs a network round trip, so they run at once.
func LoadBoard(r Runner, dir string) (Board, error) {
	root, repo, err := ResolveRepo(r, dir)
	if err != nil {
		return Board{}, err
	}
	branchOut, err := r.Git(root, "branch", "--show-current")
	if err != nil {
		return Board{}, err
	}
	b := Board{Repo: repo, Branch: strings.TrimSpace(string(branchOut))}

	var (
		wg              sync.WaitGroup
		issueErr, prErr error
		pulls           Board
		milestones      []string
		closed          int
	)
	wg.Add(4)
	go func() { defer wg.Done(); issueErr = loadIssues(r, root, &b) }()
	go func() { defer wg.Done(); pulls.Repo = repo; prErr = loadPulls(r, root, &pulls) }()
	go func() { defer wg.Done(); milestones = loadMilestones(r, root, repo) }()
	go func() { defer wg.Done(); closed = loadClosedCount(r, root, repo) }()
	wg.Wait()
	if issueErr != nil {
		return Board{}, issueErr
	}
	if prErr != nil {
		return Board{}, prErr
	}
	b.Pulls = pulls.Pulls
	b.Milestones = mergeMilestones(milestones, b.Milestones)
	b.ClosedCount = closed
	setCurrentPR(&b)
	mergePRCards(&b)
	return b, nil
}

// mergePRCards adds a card for an open PR that is not already an issue, so a
// pull request with no linked issue still has a place in the PR column.
func mergePRCards(b *Board) {
	have := map[int]bool{}
	for _, i := range b.Issues {
		have[i.Number] = true
	}
	for _, p := range b.Pulls {
		if have[p.Number] {
			continue
		}
		b.Issues = append(b.Issues, Issue{Number: p.Number, Title: p.Title, Body: p.Body})
	}
}

func loadIssues(r Runner, dir string, b *Board) error {
	out, err := r.GH(dir, "issue", "list", "--repo", b.Repo.String(), "--state", "open",
		"--limit", "100", "--json", "number,title,body,milestone,parent,subIssues,blockedBy")
	if err != nil {
		return err
	}
	var raw []struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		Milestone *struct {
			Title string `json:"title"`
		} `json:"milestone"`
		Parent *struct {
			Number int `json:"number"`
		} `json:"parent"`
		SubIssues struct {
			Nodes []struct {
				Number int `json:"number"`
			} `json:"nodes"`
		} `json:"subIssues"`
		BlockedBy struct {
			Nodes []struct {
				Number int `json:"number"`
			} `json:"nodes"`
		} `json:"blockedBy"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return fmt.Errorf("gh issue list: %w", err)
	}
	seen := map[string]bool{}
	for _, it := range raw {
		iss := Issue{Number: it.Number, Title: it.Title, Body: it.Body}
		if it.Milestone != nil {
			iss.Milestone = it.Milestone.Title
		}
		if it.Parent != nil {
			iss.Parent = it.Parent.Number
		}
		for _, n := range it.SubIssues.Nodes {
			if n.Number > 0 {
				iss.Sub = append(iss.Sub, n.Number)
			}
		}
		for _, n := range it.BlockedBy.Nodes {
			if n.Number > 0 {
				iss.BlockedBy = append(iss.BlockedBy, n.Number)
			}
		}
		b.Issues = append(b.Issues, iss)
		if iss.Milestone != "" && !seen[iss.Milestone] {
			seen[iss.Milestone] = true
			b.Milestones = append(b.Milestones, iss.Milestone)
		}
	}
	return nil
}

// loadMilestones lists the repo's open milestones, in GitHub's order. A failure
// is not fatal: the board falls back to the milestones its issues carry.
func loadMilestones(r Runner, dir string, repo Repo) []string {
	out, err := r.GH(dir, "api", fmt.Sprintf("repos/%s/%s/milestones?state=open", repo.Owner, repo.Name))
	if err != nil {
		return nil
	}
	var raw []struct {
		Title string `json:"title"`
	}
	if json.Unmarshal(out, &raw) != nil {
		return nil
	}
	names := make([]string, 0, len(raw))
	for _, m := range raw {
		names = append(names, m.Title)
	}
	return names
}

// mergeMilestones puts the open milestones first, then any milestone an issue
// names that the list did not, with blanks and repeats dropped.
func mergeMilestones(open, fromIssues []string) []string {
	seen := map[string]bool{}
	var names []string
	for _, name := range slices.Concat(open, fromIssues) {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func loadPulls(r Runner, dir string, b *Board) error {
	out, err := r.GH(dir, "pr", "list", "--repo", b.Repo.String(), "--state", "open",
		"--limit", "100", "--json", "number,title,body,isDraft,headRefName,closingIssuesReferences")
	if err != nil {
		return err
	}
	var raw []struct {
		Number                  int    `json:"number"`
		Title                   string `json:"title"`
		Body                    string `json:"body"`
		IsDraft                 bool   `json:"isDraft"`
		HeadRefName             string `json:"headRefName"`
		ClosingIssuesReferences []struct {
			Number int `json:"number"`
		} `json:"closingIssuesReferences"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return fmt.Errorf("gh pr list: %w", err)
	}
	for _, it := range raw {
		p := Pull{Number: it.Number, Title: it.Title, Body: it.Body, Draft: it.IsDraft, Head: it.HeadRefName}
		for _, c := range it.ClosingIssuesReferences {
			p.Closes = append(p.Closes, c.Number)
		}
		b.Pulls = append(b.Pulls, p)
	}
	return nil
}

func loadClosedCount(r Runner, dir string, repo Repo) int {
	// Search API 404s on private repos. Counting closed issues via `gh issue list`
	// is the list we already have permission to see.
	out, err := r.GH(dir, "issue", "list", "--repo", repo.String(), "--state", "closed",
		"--limit", "100", "--json", "number")
	if err != nil {
		return 0
	}
	var raw []struct {
		Number int `json:"number"`
	}
	if json.Unmarshal(out, &raw) != nil {
		return 0
	}
	return len(raw)
}

// setCurrentPR picks the open PR whose head branch is this tree's branch.
// It does not call `gh pr view`, which waits out the 15s gh timeout when the
// branch has no PR — fediqo's main sits there, and the overlay looks dead.
func setCurrentPR(b *Board) {
	if b.Branch == "" {
		return
	}
	for i := range b.Pulls {
		if b.Pulls[i].Head == b.Branch {
			p := b.Pulls[i]
			b.CurrentPR = &p
			return
		}
	}
}

// BlockedBy records that issue is blocked by blocker, via the GitHub REST id
// (the integer `id` field, not the GraphQL node id).
func BlockedBy(r Runner, dir string, repo Repo, issue, blocker int) error {
	idOut, err := r.GH(dir, "api", fmt.Sprintf("repos/%s/%s/issues/%d", repo.Owner, repo.Name, blocker))
	if err != nil {
		return err
	}
	var id struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(idOut, &id); err != nil || id.ID == 0 {
		return fmt.Errorf("blocker #%d: no GitHub id", blocker)
	}
	path := fmt.Sprintf("repos/%s/%s/issues/%d/dependencies/blocked_by", repo.Owner, repo.Name, issue)
	_, err = r.GH(dir, "api", "-X", "POST", path, "-F", fmt.Sprintf("issue_id=%d", id.ID))
	return err
}

// RefreshDuration is the GitHub poll cadence: unset → 60s, 0 → manual only
// (a zero duration), 1–29 → 30s, otherwise the configured seconds.
func RefreshDuration(interval *int) time.Duration {
	if interval == nil {
		return 60 * time.Second
	}
	if *interval == 0 {
		return 0
	}
	if *interval < 30 {
		return 30 * time.Second
	}
	return time.Duration(*interval) * time.Second
}
