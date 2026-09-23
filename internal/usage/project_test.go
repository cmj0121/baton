package usage

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// cwdLine is assistantLine stating the cwd the session was in, as every Claude
// Code usage line does. An empty cwd leaves the field out.
func cwdLine(cwd, id string, ts time.Time, in int64) string {
	l := assistantLine(id, "r-"+id, "claude-opus-4-8", ts, in, 0, 0, 0, 0)
	if cwd == "" {
		return l
	}
	return strings.Replace(l, `{"type":"assistant",`, `{"type":"assistant","cwd":`+strconv.Quote(cwd)+`,`, 1)
}

// projectsSum is the Projects values added up, for holding them to the totals.
func projectsSum(s Snapshot) (tokens int64, cost float64) {
	for _, p := range s.Projects {
		tokens += p.Tokens
		cost += p.CostUSD
	}
	return tokens, cost
}

// fetchClaude runs a windowed Claude fetch over root's projects tree.
func fetchClaude(t *testing.T, root string) Snapshot {
	t.Helper()
	snap, err := newLocalWindow(filepath.Join(root, "projects"), 5*time.Hour).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// onlyProject holds a snapshot to exactly one project directory, with the given
// spend and hint.
func onlyProject(t *testing.T, snap Snapshot, dir string, tokens int64, hint ProjectHint) {
	t.Helper()
	if len(snap.Projects) != 1 || snap.Projects[dir].Tokens != tokens {
		t.Errorf("projects = %v, want {%s: %d}", snap.Projects, dir, tokens)
	}
	if got := snap.ProjectHints[dir]; got != hint {
		t.Errorf("hint[%s] = %+v, want %+v", dir, got, hint)
	}
}

// A session's cwd wanders — into worktrees, into subdirectories — and every line
// says where it was. The project is the session's, not the line's: one
// directory, the subagents' spend in it, and a hint that is the cwd the
// directory is named after.
func TestAWanderingSessionIsOneProject(t *testing.T) {
	root := t.TempDir()
	ts := fixedNow.Add(-time.Hour)
	writeTranscript(t, root, "-p-repo", "s1", fixedNow,
		cwdLine("/p/repo", "a", ts, 100),
		cwdLine("/p/repo/sub", "b", ts, 20),
		cwdLine("/q/elsewhere", "c", ts, 3),
	)
	writeSubagentTranscript(t, root, "-p-repo", "s1", "agent-x", fixedNow,
		cwdLine("/p/repo", "d", ts, 4000),
		cwdLine("/q/elsewhere", "e", ts, 50000),
	)
	onlyProject(t, fetchClaude(t, root), "-p-repo", 54123, ProjectHint{Path: "/p/repo", Exact: true})
}

// A subagent's transcript is walked before its parent's, and states wherever the
// parent had wandered to. The cwd the directory is named after wins anyway,
// though it turns up later in the walk — and once found, it is final.
func TestTheCwdTheDirectoryIsNamedAfterWins(t *testing.T) {
	root := t.TempDir()
	ts := fixedNow.Add(-time.Hour)
	writeSubagentTranscript(t, root, "-p-my-repo", "s1", "agent-x", fixedNow,
		cwdLine("/p/my.repo/internal", "a", ts, 10))
	writeTranscript(t, root, "-p-my-repo", "s1", fixedNow,
		cwdLine("/p/my.repo/cmd", "b", ts, 1),
		cwdLine("/p/my.repo", "c", ts, 5),
		cwdLine("/p/my-repo", "d", ts, 2), // matches too, but a match is final
	)
	onlyProject(t, fetchClaude(t, root), "-p-my-repo", 18, ProjectHint{Path: "/p/my.repo", Exact: true})
}

// When no cwd encodes to the directory's name — a directory renamed, a long name
// the CLI truncated and hashed — the first cwd seen stands, marked inexact.
func TestTheFirstCwdStandsWhenNoneMatches(t *testing.T) {
	root := t.TempDir()
	ts := fixedNow.Add(-time.Hour)
	writeSubagentTranscript(t, root, "-p-gone", "s1", "agent-x", fixedNow,
		cwdLine("/p/first", "a", ts, 10))
	writeTranscript(t, root, "-p-gone", "s1", fixedNow, cwdLine("/p/second", "b", ts, 5))
	onlyProject(t, fetchClaude(t, root), "-p-gone", 15, ProjectHint{Path: "/p/first"})
}

// A line below the scan floor is not counted, and still teaches its directory a
// path: the hint is a fact about the directory, so an in-range line with no cwd
// of its own belongs to the path the older line stated.
func TestAnOutOfRangeLineStillHintsTheProject(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-p-repo", "s1", fixedNow,
		cwdLine("/p/repo", "old", fixedNow.Add(-48*time.Hour), 999),
		cwdLine("", "new", fixedNow.Add(-time.Hour), 7),
	)
	onlyProject(t, fetchClaude(t, root), "-p-repo", 7, ProjectHint{Path: "/p/repo", Exact: true})
}

// A directory no line stated a cwd for still has its spend, and a hint with no
// path — never a path decoded out of its name.
func TestAnUnstatedDirectoryHasAnEmptyHint(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-p-my-repo", "s1", fixedNow, cwdLine("", "a", fixedNow.Add(-time.Hour), 9))
	onlyProject(t, fetchClaude(t, root), "-p-my-repo", 9, ProjectHint{})
}

// A fork replays the parent's turns into a transcript that may sit under another
// project directory. The replayed turn is counted once, in the directory walked
// first, and the other holds only its own fresh spend.
func TestAForkIsCountedOnceAcrossProjects(t *testing.T) {
	root := t.TempDir()
	ts := fixedNow.Add(-time.Hour)
	shared := cwdLine("/p/a", "shared", ts, 100)
	writeTranscript(t, root, "-p-a", "parent", fixedNow, shared)
	writeTranscript(t, root, "-p-b", "fork", fixedNow,
		strings.Replace(shared, `"cwd":"/p/a"`, `"cwd":"/p/b"`, 1), // same ids, new cwd
		cwdLine("/p/b", "fresh", ts, 5),
	)
	snap := fetchClaude(t, root)
	if a, b := snap.Projects["-p-a"].Tokens, snap.Projects["-p-b"].Tokens; a != 100 || b != 5 {
		t.Errorf("-p-a = %d, -p-b = %d; want 100 and 5 — the replayed turn counted once", a, b)
	}
}

// Every message lands in one project directory, a log outside the layout
// included, so the projects add up to the totals — tokens and cost alike.
func TestProjectsSumToTheTotals(t *testing.T) {
	root := t.TempDir()
	ts := fixedNow.Add(-time.Hour)
	writeTranscript(t, root, "-p-a", "s1", fixedNow,
		assistantLine("a", "ra", "claude-opus-4-8", ts, 100, 50, 30, 20, 0))
	writeTranscript(t, root, "-p-b", "s2", fixedNow,
		assistantLine("b", "rb", "claude-sonnet-4", ts, 7, 3, 0, 0, 1))
	loose := filepath.Join(root, "projects", "loose.jsonl")
	if err := os.WriteFile(loose, []byte(assistantLine("c", "rc", "claude-opus-4-8", ts, 11, 0, 0, 0, 0)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap := fetchClaude(t, root)
	if got := snap.Projects[UnattributedProject].Tokens; got != 11 {
		t.Errorf("%s = %d tokens, want 11 (the loose log)", UnattributedProject, got)
	}
	if _, ok := snap.ProjectHints[UnattributedProject]; ok {
		t.Errorf("%s carries a hint; it is not a directory", UnattributedProject)
	}
	tokens, cost := projectsSum(snap)
	if tokens != snap.TotalTokens() || tokens != 11+200+11 {
		t.Errorf("projects sum to %d tokens, totals are %d, want 222", tokens, snap.TotalTokens())
	}
	if !nearly(cost, snap.CostUSD) || cost == 0 {
		t.Errorf("projects sum to $%v, totals are $%v", cost, snap.CostUSD)
	}
}

// grok names a session directory by URL-escaping its cwd, so the name alone is
// an exact hint. A name that does not unescape gives an empty one, not a
// dropped directory.
func TestGrokHintsComeFromTheDirectoryName(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-30 * time.Minute).Unix()
	write := func(dir, body string) {
		sess := filepath.Join(root, dir, "01a0")
		if err := os.MkdirAll(sess, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sess, "updates.jsonl"), []byte(body+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("%2Fu%2Fmylab%2Fbaton", grokLine(ts, "e1", "p1", 100, 0, 0, 0, 0))
	write("%zz", grokLine(ts, "e2", "p2", 3, 0, 0, 0, 0))

	p := NewGrokProvider(5 * time.Hour)
	p.dir, p.now = root, func() time.Time { return now }
	snap, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := snap.ProjectHints["%2Fu%2Fmylab%2Fbaton"], (ProjectHint{Path: "/u/mylab/baton", Exact: true}); got != want {
		t.Errorf("hint = %+v, want %+v", got, want)
	}
	if got, ok := snap.ProjectHints["%zz"]; !ok || got != (ProjectHint{}) {
		t.Errorf("hint[%%zz] = %+v (present %v), want an empty hint", got, ok)
	}
	if snap.Projects["%2Fu%2Fmylab%2Fbaton"].Tokens != 100 || snap.Projects["%zz"].Tokens != 3 {
		t.Errorf("projects = %v", snap.Projects)
	}
}

// Since sums a named period: nothing before it, nothing stamped after now, and
// no window chain — a message from two days back, long past any window, counts.
func TestSinceSumsTheNamedPeriod(t *testing.T) {
	root := t.TempDir()
	since := fixedNow.Add(-72 * time.Hour)
	writeTranscript(t, root, "-p-a", "s1", fixedNow,
		cwdLine("/p/a", "before", since.Add(-time.Minute), 1000),
		cwdLine("/p/a", "edge", since, 1),
		cwdLine("/p/a", "old", fixedNow.Add(-48*time.Hour), 20),
		cwdLine("/p/a", "recent", fixedNow.Add(-time.Hour), 300),
		cwdLine("/p/a", "ahead", fixedNow.Add(time.Minute), 4000),
	)

	snap, err := newLocalWindow(filepath.Join(root, "projects"), 5*time.Hour).Since(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.TotalTokens(); got != 321 {
		t.Errorf("total = %d, want 321 (edge + old + recent; not before, not ahead)", got)
	}
	if got := snap.Projects["-p-a"].Tokens; got != 321 {
		t.Errorf("-p-a = %d, want 321", got)
	}
	if !snap.Since.Equal(since) || !snap.Until.IsZero() || snap.Resets {
		t.Errorf("since=%v until=%v resets=%v; want the named start and no countdown", snap.Since, snap.Until, snap.Resets)
	}
}

// Since skips a file last written before the period, the same as Fetch: one
// walk, one mtime rule.
func TestSinceSkipsAFileWrittenBeforeThePeriod(t *testing.T) {
	root := t.TempDir()
	since := fixedNow.Add(-24 * time.Hour)
	// The line is stamped inside the period; only the mtime says the file is stale.
	writeTranscript(t, root, "-p-a", "s1", since.Add(-time.Hour), cwdLine("/p/a", "x", fixedNow.Add(-time.Hour), 5))

	snap, err := newLocalWindow(filepath.Join(root, "projects"), 5*time.Hour).Since(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Empty() {
		t.Errorf("a file last written before the period was read: %+v", snap)
	}
}

// A halted walk reports nothing from Since either, rather than a fraction of a
// week that reads as the week.
func TestSinceReportsNothingFromAHaltedWalk(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-p-a", "s1", fixedNow, cwdLine("/p/a", "x", fixedNow.Add(-time.Hour), 5))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	snap, err := newLocalWindow(filepath.Join(root, "projects"), 5*time.Hour).Since(ctx, fixedNow.Add(-24*time.Hour))
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !snap.Empty() || snap.Projects != nil {
		t.Errorf("a halted walk reported %+v", snap)
	}
}

// VendorSince reaches a registered reader's Since; a vendor with no reader, and a
// reader that cannot sum a period, both say so rather than reading as zero.
func TestVendorSince(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	recent := time.Now().Add(-time.Hour)
	writeTranscript(t, root, "-p-a", "s1", recent, cwdLine("/p/a", "x", recent, 42))

	snap, ok, err := VendorSince(context.Background(), "claude", recent.Add(-time.Hour))
	if err != nil || !ok {
		t.Fatalf("claude: ok=%v err=%v, want a reading", ok, err)
	}
	if snap.Projects["-p-a"].Tokens != 42 || snap.ProjectHints["-p-a"].Path != "/p/a" {
		t.Errorf("claude week = %v / %v, want {-p-a: 42} hinted /p/a", snap.Projects, snap.ProjectHints)
	}

	if _, ok, _ := VendorSince(context.Background(), "codex", recent); ok {
		t.Error("codex has no reader, yet VendorSince reported one")
	}

	prev := vendorReaders["claude"]
	vendorReaders["claude"] = func(time.Duration) Provider { return failing{} }
	t.Cleanup(func() { vendorReaders["claude"] = prev })
	if _, ok, _ := VendorSince(context.Background(), "claude", recent); ok {
		t.Error("a reader with no Since reported a week figure")
	}
}

// Report carries the window's per-directory figure and hints through.
func TestReportCarriesProjects(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	now := time.Now()
	writeTranscript(t, root, "-p-a", "s1", now, cwdLine("/p/a", "x", now.Add(-time.Minute), 42))

	r := Report(context.Background(), VendorCandidate{Name: "claude"}, 5*time.Hour, now)
	if r.Projects["-p-a"].Tokens != 42 {
		t.Errorf("report projects = %v, want {-p-a: 42}", r.Projects)
	}
	if got, want := r.ProjectHints["-p-a"], (ProjectHint{Path: "/p/a", Exact: true}); got != want {
		t.Errorf("report hint = %+v, want %+v", got, want)
	}
}

// LabelProjects names each directory from its best hint, marks one with no path
// unresolved, leaves the unattributed bucket alone, and folds across vendors: a
// grok worktree of a repo seen only through a Claude worktree joins that repo.
func TestLabelProjects(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("GROK_HOME", "")
	cl := func(dir string) ProjectKey { return ProjectKey{Vendor: "claude", Dir: dir} }
	gk := func(dir string) ProjectKey { return ProjectKey{Vendor: "grok", Dir: dir} }
	hints := map[ProjectKey][]ProjectHint{
		cl("-u-mylab-baton-worktrees-feat"):              {{Path: "/u/mylab/baton-worktrees/feat", Exact: true}},
		gk("%2Fh%2F.grok%2Fworktrees%2Fmylab-baton%2Fx"): {{Path: "/h/.grok/worktrees/mylab-baton/x", Exact: true}},
		// Two scans saw this one: the exact hint beats the inexact, whichever came first.
		cl("-p-repo"):           {{Path: "/p/repo/sub"}, {Path: "/p/repo", Exact: true}},
		cl("-p-other"):          {{Path: "/p/zzz"}, {Path: "/p/aaa"}, {}},
		cl("-p-my-repo"):        {{}},
		cl("-p-none"):           nil,
		cl(UnattributedProject): nil,
		gk(UnattributedProject): {{Path: "/should/not/matter"}},
		cl("-tmp-scratch"):      {{Path: "/tmp/scratch", Exact: true}},
		gk("%2Fh%2F.grok%2Fworktrees%2Fnobody%2Fx"): {{Path: "/h/.grok/worktrees/nobody/x", Exact: true}},
	}
	want := map[ProjectKey]string{
		cl("-u-mylab-baton-worktrees-feat"):              "/u/mylab/baton",
		gk("%2Fh%2F.grok%2Fworktrees%2Fmylab-baton%2Fx"): "/u/mylab/baton",
		cl("-p-repo"):           "/p/repo",
		cl("-p-other"):          "/p/aaa",
		cl("-p-my-repo"):        "(unresolved) -p-my-repo",
		cl("-p-none"):           "(unresolved) -p-none",
		cl(UnattributedProject): UnattributedProject,
		gk(UnattributedProject): UnattributedProject,
		cl("-tmp-scratch"):      TemporaryProject,
		gk("%2Fh%2F.grok%2Fworktrees%2Fnobody%2Fx"): "grok worktree nobody",
	}
	got := LabelProjects(hints)
	if len(got) != len(want) {
		t.Errorf("labelled %d directories, want %d: %v", len(got), len(want), got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("label %v = %q, want %q", k, got[k], w)
		}
	}
}

// The hints for one directory may arrive in any order — two scans, merged by a
// caller — and the label must not follow the order.
func TestLabelProjectsIgnoresHintOrder(t *testing.T) {
	k := ProjectKey{Vendor: "claude", Dir: "-p-x"}
	a := []ProjectHint{{Path: "/p/b"}, {Path: "/p/a"}, {Path: "/p/x", Exact: true}, {Path: "/p/c", Exact: true}}
	for i := range a {
		rot := append(append([]ProjectHint{}, a[i:]...), a[:i]...)
		if got := LabelProjects(map[ProjectKey][]ProjectHint{k: rot})[k]; got != "/p/c" {
			t.Errorf("rotation %d labelled %q, want /p/c (the smallest exact path)", i, got)
		}
	}
}

// Two known projects share a grok worktree name: /a/my-lab/baton and
// /b/my/lab-baton both give "my-lab-baton". That is no answer, so the worktree
// stays unfolded — in every order the known set can come in — rather than
// joining whichever happened to be listed first.
func TestAnAmbiguousGrokWorktreeDoesNotFold(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("GROK_HOME", "")
	wt := "/h/.grok/worktrees/my-lab-baton/x"
	for _, known := range [][]string{
		{"/a/my-lab/baton", "/b/my/lab-baton"},
		{"/b/my/lab-baton", "/a/my-lab/baton"},
		{"/a/my-lab/baton", "/a/my-lab/baton", "/b/my/lab-baton"},
	} {
		if got := FoldProject(wt, known); got != "grok worktree my-lab-baton" {
			t.Errorf("FoldProject with known %v = %q, want it left unfolded", known, got)
		}
	}
	// A duplicate of the one match is still one match.
	if got := FoldProject(wt, []string{"/a/my-lab/baton", "/a/my-lab/baton"}); got != "/a/my-lab/baton" {
		t.Errorf("a repeated single match = %q, want /a/my-lab/baton", got)
	}

	cl := func(dir, path string) (ProjectKey, []ProjectHint) {
		return ProjectKey{Vendor: "claude", Dir: dir}, []ProjectHint{{Path: path, Exact: true}}
	}
	hints := map[ProjectKey][]ProjectHint{}
	for _, p := range [][2]string{{"-a", "/a/my-lab/baton"}, {"-b", "/b/my/lab-baton"}} {
		k, h := cl(p[0], p[1])
		hints[k] = h
	}
	gk := ProjectKey{Vendor: "grok", Dir: "wt"}
	hints[gk] = []ProjectHint{{Path: wt, Exact: true}}
	if got := LabelProjects(hints)[gk]; got != "grok worktree my-lab-baton" {
		t.Errorf("LabelProjects folded an ambiguous grok worktree to %q", got)
	}
}

// Each folding rule, and paths that look close to one and must not fold.
func TestFoldProject(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("GROK_HOME", "/g")
	known := []string{"/u/mylab/baton", "/u/other/repo"}
	cases := []struct{ in, want string }{
		{"/u/mylab/baton", "/u/mylab/baton"},
		{"/u/mylab/baton/.claude/worktrees/feat", "/u/mylab/baton"},
		{"/u/mylab/baton/.claude/worktrees/feat/internal/x", "/u/mylab/baton"},
		{"/u/mylab/baton-worktrees/feat", "/u/mylab/baton"},
		{"/u/mylab/baton-worktrees/feat/deep/dir", "/u/mylab/baton"},
		{"/u/mylab/baton-worktrees/feat/.claude/worktrees/x", "/u/mylab/baton"},
		{"/g/worktrees/mylab-baton/sub", "/u/mylab/baton"},
		{"/h/.grok/worktrees/other-repo", "/u/other/repo"},
		{"/h/.grok/worktrees/nobody-known/x", "grok worktree nobody-known"},
		{"/tmp/scratch", TemporaryProject},
		{"/private/tmp/a/b", TemporaryProject},
		{"/var/folders/xy/T/repo", TemporaryProject},
		{"/private/var/folders/xy/T/repo", TemporaryProject},
		{filepath.Join(os.TempDir(), "x"), TemporaryProject},
		// Near misses: none of these is a worktree or a temp path.
		{"/tmpfoo/repo", "/tmpfoo/repo"},
		{"/u/baton-worktrees", "/u/baton-worktrees"},
		{"/u/-worktrees/leaf", "/u/-worktrees/leaf"},
		{"/u/repo/.claude/worktreesx/y", "/u/repo/.claude/worktreesx/y"},
		{"/g/worktrees", "/g/worktrees"},
		{"/.claude/worktrees/x", "/.claude/worktrees/x"},
		{"/u/mylab/baton/../baton", "/u/mylab/baton"},
		{"(unresolved) -tmp-x", "(unresolved) -tmp-x"},
		{UnattributedProject, UnattributedProject},
	}
	for _, c := range cases {
		if got := FoldProject(c.in, known); got != c.want {
			t.Errorf("FoldProject(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// os.TempDir() is a temp root of its own, not only through the fixed list: a
// $TMPDIR pointed somewhere else still folds.
func TestFoldProjectHonoursTMPDIR(t *testing.T) {
	t.Setenv("TMPDIR", "/scratch")
	if got := FoldProject("/scratch/repo", nil); got != TemporaryProject {
		t.Errorf("FoldProject under $TMPDIR = %q, want %q", got, TemporaryProject)
	}
}

func TestShortenHome(t *testing.T) {
	t.Setenv("HOME", "/h/me")
	cases := map[string]string{
		"/h/me":        "~",
		"/h/me/x/repo": "~/x/repo",
		"/h/meow/repo": "/h/meow/repo",
		"/other":       "/other",
	}
	for in, want := range cases {
		if got := ShortenHome(in); got != want {
			t.Errorf("ShortenHome(%q) = %q, want %q", in, got, want)
		}
	}
}

// Claude Code's encoding, per UTF-16 code unit: one "-" for a BMP character such
// as CJK, two for one past U+FFFF.
func TestClaudeDirName(t *testing.T) {
	cases := map[string]string{
		"/Users/me/junkProject/mylab/baton": "-Users-me-junkProject-mylab-baton",
		"/p/fediqo/.claude/worktrees/x_y":   "-p-fediqo--claude-worktrees-x-y",
		"/p/專案":                             "-p---",
		"/p/a🚀b":                            "-p-a--b",
	}
	for in, want := range cases {
		if got := claudeDirName(in); got != want {
			t.Errorf("claudeDirName(%q) = %q, want %q", in, got, want)
		}
	}
}
