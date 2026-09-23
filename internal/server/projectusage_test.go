package server

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/usage"
)

// appendClaudeLine appends one Claude Code usage line, stating cwd, to
// <root>/projects/<dir>/<session>.jsonl.
func appendClaudeLine(t *testing.T, root, dir, session, cwd, id string, ts time.Time, tokens int64) {
	t.Helper()
	d := filepath.Join(root, "projects", dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"type":"assistant","cwd":%q,"requestId":%q,"timestamp":%q,"message":{"id":%q,"model":"claude-opus-4-8","usage":{"input_tokens":%d,"output_tokens":0}}}`+"\n",
		cwd, id, ts.UTC().Format(time.RFC3339), id, tokens)
	f, err := os.OpenFile(filepath.Join(d, session+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

// writeGrokTurn writes one grok turn_completed line under <home>/sessions/<dir>.
func writeGrokTurn(t *testing.T, home, dir, id string, ts time.Time, tokens int64) {
	t.Helper()
	d := filepath.Join(home, "sessions", dir, "01a0")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"timestamp":%d,"params":{"update":{"sessionUpdate":"turn_completed","prompt_id":%q,"usage":{"inputTokens":%d,"outputTokens":0,"cachedReadTokens":0,"cacheCreationTokens":0,"costUsdTicks":0}}},"_meta":{"eventId":%q}}`+"\n",
		ts.Unix(), id, tokens, id)
	if err := os.WriteFile(filepath.Join(d, "updates.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The quota week is remembered: a reset read once holds through reads that fail,
// steps forward a week once it passes — saying so, so the held figures are
// rescanned — and a reset read stale is stepped forward too. Only a vendor that
// never stated one gets the rolling seven days.
func TestWeekStartRemembersTheReset(t *testing.T) {
	var w weekUsage
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	r := t0.Add(48 * time.Hour)
	type want struct {
		since         time.Time
		quota, passed bool
	}
	steps := []struct {
		name      string
		seen, now time.Time
		vendor    string
		want      want
	}{
		{"a reset is read", r, t0, "claude", want{r.Add(-week), true, false}},
		{"the next read fails", time.Time{}, t0.Add(24 * time.Hour), "claude", want{r.Add(-week), true, false}},
		{"the reset passes unread", time.Time{}, r.Add(time.Hour), "claude", want{r, true, true}},
		{"the stepped reset holds", time.Time{}, r.Add(2 * time.Hour), "claude", want{r, true, false}},
		{"never stated: rolling", time.Time{}, t0, "grok", want{t0.Add(-week), false, false}},
		{"a stale reading steps forward", t0.Add(-8 * 24 * time.Hour), t0, "codex", want{t0.Add(-24 * time.Hour), true, false}},
	}
	for _, st := range steps {
		since, quota, passed := w.start(st.vendor, st.seen, st.now)
		if got := (want{since, quota, passed}); got != st.want {
			t.Errorf("%s: start = %+v, want %+v", st.name, got, st.want)
		}
	}
}

// weekServer is a server whose week scan runs on a clock the test moves, with
// an hour-long cadence so only the test decides when one is due.
func weekServer(cur *time.Time) *Server {
	s := &Server{}
	s.week.now = func() time.Time { return *cur }
	s.week.every = time.Hour
	return s
}

// The week is scanned on the first poll and then held: a tick inside the cadence
// does not rescan, the cadence does, and so does a remembered reset passing —
// even when the limits read that would have restated it failed.
func TestTheWeekScanRunsOnItsOwnCadence(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	base := time.Now().Truncate(time.Second)
	appendClaudeLine(t, root, "-p-a", "s1", "/p/a", "m1", base.Add(-2*time.Hour), 10)

	cur := base
	s := weekServer(&cur)
	reports := []usage.VendorReport{{Vendor: "claude", State: usage.VendorReading}}
	reset := base.Add(3 * time.Hour)
	lim := &proto.LimitsInfo{SevenDay: &proto.LimitWindow{ResetsAt: reset.UTC().Format(time.RFC3339)}}

	tokens := func(lim *proto.LimitsInfo) (int64, weekFigure) {
		f := s.refreshWeek(reports, lim)["claude"]
		return f.snap.TotalTokens(), f
	}
	if got, f := tokens(lim); got != 10 || !f.quota || !f.since.Equal(reset.Add(-week)) {
		t.Fatalf("first poll: %d tokens since %v (quota %v), want 10 since %v", got, f.since, f.quota, reset.Add(-week))
	}

	appendClaudeLine(t, root, "-p-a", "s1", "/p/a", "m2", base.Add(-time.Hour), 5)
	cur = base.Add(time.Minute)
	if got, _ := tokens(lim); got != 10 {
		t.Errorf("a tick inside the cadence read %d tokens, want the held 10 — it rescanned", got)
	}
	cur = base.Add(time.Hour + time.Second)
	if got, _ := tokens(lim); got != 15 {
		t.Errorf("the cadence came round and read %d tokens, want 15", got)
	}

	// The reset passes with the limits read failing: the remembered reset steps
	// on, the week restarts at it, and both lines are before it.
	cur = reset.Add(time.Second)
	got, f := tokens(nil)
	if !f.quota || !f.since.Equal(reset) {
		t.Errorf("after the reset: since %v (quota %v), want %v as a quota week", f.since, f.quota, reset)
	}
	if got != 0 {
		t.Errorf("after the reset: %d tokens, want 0 — the held week was not rescanned", got)
	}
}

// A vendor that states its first reset has its rolling week rescanned at once as
// the quota week, not five minutes later.
func TestAFirstStatedResetForcesARescan(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	base := time.Now().Truncate(time.Second)
	cur := base
	s := weekServer(&cur)
	reports := []usage.VendorReport{{Vendor: "claude", State: usage.VendorReading}}

	if f := s.refreshWeek(reports, nil)["claude"]; f.quota || !f.since.Equal(base.Add(-week)) {
		t.Fatalf("no reset stated: since %v quota %v, want a rolling week from %v", f.since, f.quota, base.Add(-week))
	}
	cur = base.Add(time.Minute)
	reset := base.Add(24 * time.Hour)
	lim := &proto.LimitsInfo{SevenDay: &proto.LimitWindow{ResetsAt: reset.UTC().Format(time.RFC3339)}}
	if f := s.refreshWeek(reports, lim)["claude"]; !f.quota || !f.since.Equal(reset.Add(-week)) {
		t.Errorf("first reset: since %v quota %v, want the quota week from %v", f.since, f.quota, reset.Add(-week))
	}
}

// grok's week starts at its own credit pool's reset, carried on its report.
func TestGrokWeekStartsAtItsOwnReset(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	base := time.Now().Truncate(time.Second)
	cur := base
	s := weekServer(&cur)
	reset := base.Add(2 * time.Hour)
	reports := []usage.VendorReport{{Vendor: "grok", State: usage.VendorReading,
		Windows: []usage.VendorWindow{{Label: usage.WindowWeek, ResetsAt: reset}}}}
	lim := &proto.LimitsInfo{SevenDay: &proto.LimitWindow{ResetsAt: base.Add(time.Hour).UTC().Format(time.RFC3339)}}
	if f := s.refreshWeek(reports, lim)["grok"]; !f.quota || !f.since.Equal(reset.Add(-week)) {
		t.Errorf("grok week since %v quota %v, want %v — not Claude's reset", f.since, f.quota, reset.Add(-week))
	}
}

// A vendor that is not installed, or has no reader, is never week-scanned.
func TestTheWeekSkipsVendorsItCannotRead(t *testing.T) {
	cur := time.Now()
	s := weekServer(&cur)
	got := s.refreshWeek([]usage.VendorReport{
		{Vendor: "claude", State: usage.VendorAbsent},
		{Vendor: "codex", State: usage.VendorNoSource},
	}, nil)
	if len(got) != 0 {
		t.Errorf("week figures for vendors it cannot read: %v", got)
	}
}

// scope is a projectScope literal with hints taken as dir -> exact path.
func scope(vendor string, week bool, spend map[string]int64, paths map[string]string) projectScope {
	sc := projectScope{vendor: vendor, week: week, projects: map[string]usage.SessionUsage{}, hints: map[string]usage.ProjectHint{}}
	for d, n := range spend {
		sc.projects[d] = usage.SessionUsage{Tokens: n, CostUSD: float64(n) / 100}
	}
	for d, p := range paths {
		sc.hints[d] = usage.ProjectHint{Path: p, Exact: true}
	}
	return sc
}

// Two raw directories that fold to one checkout are one row, their spend summed,
// in both scopes; a directory known only to the week is still named from the
// week's hint; and one no scope hinted is named unresolved, not dropped.
func TestProjectRowsSumDirectoriesThatFoldTogether(t *testing.T) {
	paths := map[string]string{"-u-repo": "/u/repo", "-u-repo--claude-worktrees-x": "/u/repo/.claude/worktrees/x"}
	rows := projectRows([]projectScope{
		scope("claude", false, map[string]int64{"-u-repo": 1, "-u-repo--claude-worktrees-x": 2, "-q": 4}, paths),
		scope("claude", true, map[string]int64{"-u-repo": 10, "-u-repo--claude-worktrees-x": 20, "-v": 40},
			map[string]string{"-u-repo": "/u/repo", "-u-repo--claude-worktrees-x": "/u/repo/.claude/worktrees/x", "-v": "/v"}),
	}, maxProjectRows)
	want := []proto.ProjectUsage{
		{Project: "/v", Vendor: "claude", WeekTokens: 40, WeekCostUSD: 0.4},
		{Project: "/u/repo", Vendor: "claude", SessionTokens: 3, SessionCostUSD: 0.03, WeekTokens: 30, WeekCostUSD: 0.3},
		{Project: "(unresolved) -q", Vendor: "claude", SessionTokens: 4, SessionCostUSD: 0.04},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v, want %+v", rows, want)
	}
	for i := range want {
		g, w := rows[i], want[i]
		if g.Project != w.Project || g.Vendor != w.Vendor || g.SessionTokens != w.SessionTokens || g.WeekTokens != w.WeekTokens ||
			!near(g.SessionCostUSD, w.SessionCostUSD) || !near(g.WeekCostUSD, w.WeekCostUSD) {
			t.Errorf("row %d = %+v, want %+v", i, g, w)
		}
	}
}

func near(a, b float64) bool { d := a - b; return d < 1e-9 && d > -1e-9 }

// A grok worktree of a repository only Claude has a hint for folds onto it: the
// labeller sees every vendor's directories at once.
func TestProjectRowsFoldAGrokWorktreeAcrossVendors(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("GROK_HOME", "")
	rows := projectRows([]projectScope{
		scope("claude", true, map[string]int64{"-u-mylab-baton": 5}, map[string]string{"-u-mylab-baton": "/u/mylab/baton"}),
		scope("grok", true, map[string]int64{"wt": 7}, map[string]string{"wt": "/h/.grok/worktrees/mylab-baton/x"}),
	}, maxProjectRows)
	want := []proto.ProjectUsage{
		{Project: "/u/mylab/baton", Vendor: "claude", WeekTokens: 5, WeekCostUSD: 0.05},
		{Project: "/u/mylab/baton", Vendor: "grok", WeekTokens: 7, WeekCostUSD: 0.07},
	}
	if !slices.Equal(rows, want) {
		t.Errorf("rows = %+v, want %+v", rows, want)
	}
}

// Past the cap, each vendor's remaining spend is one (other) row; a kept project
// keeps every vendor's row even when one vendor's share is small; and the rank is
// by week tokens across vendors, ties by name.
func TestProjectRowsCapIntoOtherPerVendor(t *testing.T) {
	paths := map[string]string{"a": "/a", "b": "/b", "c": "/c", "d": "/d", "e": "/e"}
	rows := projectRows([]projectScope{
		scope("claude", true, map[string]int64{"a": 100, "b": 50, "c": 5, "d": 3}, paths),
		scope("grok", true, map[string]int64{"b": 1, "e": 50, "d": 2}, paths),
		scope("grok", false, map[string]int64{"c": 9}, paths),
	}, 2)
	// Week totals: a 100, b 51, e 50, c 5, d 5. Kept: a, b. Other: c, d, e.
	want := []proto.ProjectUsage{
		{Project: "/a", Vendor: "claude", WeekTokens: 100, WeekCostUSD: 1},
		{Project: "/b", Vendor: "claude", WeekTokens: 50, WeekCostUSD: 0.5},
		{Project: "/b", Vendor: "grok", WeekTokens: 1, WeekCostUSD: 0.01},
		{Project: proto.ProjectOther, Vendor: "claude", WeekTokens: 8, WeekCostUSD: 0.08},
		{Project: proto.ProjectOther, Vendor: "grok", SessionTokens: 9, SessionCostUSD: 0.09, WeekTokens: 52, WeekCostUSD: 0.52},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v\nwant %+v", rows, want)
	}
	for i := range want {
		g, w := rows[i], want[i]
		if g.Project != w.Project || g.Vendor != w.Vendor || g.SessionTokens != w.SessionTokens || g.WeekTokens != w.WeekTokens ||
			!near(g.WeekCostUSD, w.WeekCostUSD) || !near(g.SessionCostUSD, w.SessionCostUSD) {
			t.Errorf("row %d = %+v, want %+v", i, g, w)
		}
	}
}

// Equal week totals rank by name, and the same input gives the same rows every
// time — bit for bit, costs included — or the broadcast gate would flap.
func TestProjectRowsAreDeterministic(t *testing.T) {
	spend := map[string]int64{}
	paths := map[string]string{}
	for i := range 40 {
		d := fmt.Sprintf("d%02d", i)
		spend[d], paths[d] = 7, "/p/"+d
	}
	first := projectRows([]projectScope{scope("claude", true, spend, paths)}, 3)
	if first[0].Project != "/p/d00" || first[1].Project != "/p/d01" || first[2].Project != "/p/d02" {
		t.Errorf("tied projects ranked %q, %q, %q; want by name", first[0].Project, first[1].Project, first[2].Project)
	}
	for range 20 {
		if again := projectRows([]projectScope{scope("claude", true, spend, paths)}, 3); !slices.Equal(first, again) {
			t.Fatalf("rows changed between identical calls:\n%+v\n%+v", first, again)
		}
	}
}

// Many directories folding into one row sum their costs in one fixed order —
// float addition is not associative, and map order would move the last bits —
// and many vendors under one project come out by name.
func TestProjectRowsSumAndOrderStably(t *testing.T) {
	spend := map[string]int64{}
	paths := map[string]string{}
	for i := range 40 {
		d := fmt.Sprintf("d%02d", i)
		spend[d], paths[d] = int64(i*i+1), "/p/one/.claude/worktrees/"+d
	}
	var scopes []projectScope
	for _, v := range []string{"v7", "v3", "v5", "v1", "v6", "v2", "v4", "v0"} {
		scopes = append(scopes, scope(v, true, spend, paths))
	}
	first := projectRows(scopes, maxProjectRows)
	for i, r := range first {
		if want := fmt.Sprintf("v%d", i); r.Vendor != want || r.Project != "/p/one" {
			t.Fatalf("row %d = %s/%s, want /p/one/%s — vendors out of order", i, r.Project, r.Vendor, want)
		}
	}
	for range 50 {
		if again := projectRows(scopes, maxProjectRows); !slices.Equal(first, again) {
			t.Fatalf("the same spend summed to different rows:\n%+v\n%+v", first, again)
		}
	}
}

// Nothing to attribute is no rows at all — nil, the wire's "never said".
func TestProjectRowsOfNothingIsNil(t *testing.T) {
	if rows := projectRows(nil, maxProjectRows); rows != nil {
		t.Errorf("rows = %+v, want nil", rows)
	}
}

// End to end: a poll puts both vendors' rows on the wire, named once across them
// (the grok worktree joins Claude's checkout), with each vendor's week start.
func TestProjectsReachTheWire(t *testing.T) {
	claudeRoot, grokRoot := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeRoot)
	t.Setenv("GROK_HOME", grokRoot)
	t.Setenv("HOME", "/h")
	now := time.Now()
	appendClaudeLine(t, claudeRoot, "-u-mylab-baton", "s1", "/u/mylab/baton", "m1", now.Add(-time.Minute), 10)
	writeGrokTurn(t, grokRoot, "%2Fh%2F.grok%2Fworktrees%2Fmylab-baton%2Fx", "e1", now.Add(-time.Minute), 20)

	s := &Server{usageWindow: 5 * time.Hour, usageInterval: time.Minute, clients: make(map[*clientConn]struct{})}
	s.agents = []proto.AgentBackend{{Name: "claude", Command: "claude"}, {Name: "grok", Command: "grok"}}
	s.refreshUsage()

	info := s.usageInfo
	if info == nil || len(info.Projects) != 2 {
		t.Fatalf("payload = %+v, want two project rows", info)
	}
	want := []proto.ProjectUsage{
		{Project: "/u/mylab/baton", Vendor: "claude", SessionTokens: 10, SessionCostUSD: info.Projects[0].SessionCostUSD, WeekTokens: 10, WeekCostUSD: info.Projects[0].WeekCostUSD},
		{Project: "/u/mylab/baton", Vendor: "grok", SessionTokens: 20, WeekTokens: 20},
	}
	if !slices.Equal(info.Projects, want) {
		t.Errorf("projects = %+v\nwant %+v", info.Projects, want)
	}
	for _, v := range info.Vendors {
		if v.WeekSince == "" || v.WeekQuota {
			t.Errorf("%s: week since %q quota %v, want a rolling week start", v.Vendor, v.WeekSince, v.WeekQuota)
		}
	}
}

// With the per-vendor list off, a poll sends exactly what an older daemon did:
// no vendor rows and no project rows.
func TestProjectsAreOffWithoutAWindow(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	s := newUsageServer(stubUsage{snap: usage.Snapshot{Input: 5}})
	s.agents = []proto.AgentBackend{{Name: "claude", Command: "claude"}}
	s.refreshUsage()
	if s.usageInfo == nil || s.usageInfo.Projects != nil || s.usageInfo.Vendors != nil {
		t.Errorf("payload = %+v, want totals only", s.usageInfo)
	}
	if !s.week.ranAt.IsZero() {
		t.Error("the week was scanned with the feature off")
	}
}

// A project row or a week start changing alone is news; the same payload again
// is not.
func TestUsageInfoChangeGateSeesProjects(t *testing.T) {
	base := func() *proto.UsageInfo {
		return &proto.UsageInfo{
			Tokens:   1,
			Vendors:  []proto.VendorUsage{{Vendor: "claude", State: "reading", WeekSince: "2026-09-01T00:00:00Z", WeekQuota: true}},
			Projects: []proto.ProjectUsage{{Project: "/p", Vendor: "claude", WeekTokens: 5}},
		}
	}
	if !sameUsageInfo(base(), base()) {
		t.Error("an identical payload compared changed; every poll would wake every client")
	}
	for name, edit := range map[string]func(*proto.UsageInfo){
		"a row's figure": func(u *proto.UsageInfo) { u.Projects[0].WeekTokens = 6 },
		"a row added": func(u *proto.UsageInfo) {
			u.Projects = append(u.Projects, proto.ProjectUsage{Project: "/q", Vendor: "claude"})
		},
		"the week start": func(u *proto.UsageInfo) { u.Vendors[0].WeekSince = "2026-09-08T00:00:00Z" },
		"the week kind":  func(u *proto.UsageInfo) { u.Vendors[0].WeekQuota = false },
	} {
		b := base()
		edit(b)
		if sameUsageInfo(base(), b) {
			t.Errorf("%s changed alone and compared equal; it is never broadcast", name)
		}
	}
}

// A week scan that fails keeps the last figures, and the start they were read
// from: the wire must not pair the old tokens with a new WeekSince, nor swap in
// the empty reading of a scan that never finished.
func TestAFailedWeekScanKeepsTheLastFigures(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	base := time.Now().Truncate(time.Second)
	appendClaudeLine(t, root, "-p-a", "s1", "/p/a", "m1", base.Add(-2*time.Hour), 10)

	cur := base
	s := weekServer(&cur)
	s.usageWindow, s.usageInterval = 5*time.Hour, time.Minute
	s.clients = make(map[*clientConn]struct{})
	s.agents = []proto.AgentBackend{{Name: "claude", Command: "claude"}}
	resetAt := func(r time.Time) *proto.LimitsInfo {
		return &proto.LimitsInfo{SevenDay: &proto.LimitWindow{ResetsAt: r.UTC().Format(time.RFC3339)}}
	}
	s.limitsInfo = resetAt(base.Add(24 * time.Hour))
	s.refreshUsage()
	before := s.usageInfo
	if before == nil || len(before.Projects) != 1 || before.Projects[0].WeekTokens != 10 ||
		len(before.Vendors) != 1 || !before.Vendors[0].WeekQuota {
		t.Fatalf("first poll = %+v, want one project with 10 week tokens on a quota week", before)
	}

	// The next scan is due (cadence passed), would read more (a new line) from a
	// new start (a new reset) — and cannot finish: a nanosecond is its budget.
	appendClaudeLine(t, root, "-p-a", "s1", "/p/a", "m2", base.Add(-time.Hour), 5)
	s.limitsInfo = resetAt(base.Add(48 * time.Hour))
	cur = base.Add(2 * time.Hour)
	s.usageInterval = time.Nanosecond
	s.refreshUsage()
	after := s.usageInfo
	if after == nil || len(after.Projects) != 1 || after.Projects[0].WeekTokens != 10 {
		t.Errorf("after a failed scan: projects %+v, want the held 10 week tokens", after.Projects)
	}
	if len(after.Vendors) != 1 || after.Vendors[0].WeekSince != before.Vendors[0].WeekSince ||
		after.Vendors[0].WeekQuota != before.Vendors[0].WeekQuota {
		t.Errorf("after a failed scan: vendor %+v, want week since %q quota %v unchanged",
			after.Vendors, before.Vendors[0].WeekSince, before.Vendors[0].WeekQuota)
	}
}

// The week scan's budget is the cadence capped at two usage ticks, and the
// cadence alone when there is no tick.
func TestTheWeekScanTimeoutIsCappedByTheTick(t *testing.T) {
	for _, c := range []struct{ every, tick, want time.Duration }{
		{0, 30 * time.Second, time.Minute},
		{0, 10 * time.Minute, defaultWeekEvery},
		{0, 0, defaultWeekEvery},
		{time.Hour, 0, time.Hour},
		{90 * time.Second, time.Minute, 90 * time.Second},
	} {
		s := &Server{usageInterval: c.tick}
		s.week.every = c.every
		if got := s.weekScanTimeout(); got != c.want {
			t.Errorf("every %v, tick %v: timeout %v, want %v", c.every, c.tick, got, c.want)
		}
	}
}
