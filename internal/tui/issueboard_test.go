package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/cmj0121/baton/internal/issues"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

func useTempIssueBinds(t *testing.T) {
	t.Helper()
	t.Cleanup(issues.UseBindFile(filepath.Join(t.TempDir(), "issue-bind.json")))
}

func issuesModel() model {
	m := baseModel()
	m.width, m.height = 120, 40
	m.now = time.Unix(1_700_000_000, 0)
	m.fleet = []panel.Panel{{
		ID: "1", Title: "hale", Kind: panel.Agent, State: panel.Running,
		Cwd: "/tmp/repo",
	}}
	m.issuesBoard = issues.Board{
		Repo:        issues.Repo{Owner: "cmj0121", Name: "baton"},
		Branch:      "feat/127-docs",
		ClosedCount: 14,
		Issues: []issues.Issue{
			{Number: 118, Title: "passkey"},
			{Number: 128, Title: "overlay", Body: "do the overlay"},
			{Number: 127, Title: "docs"},
		},
		Pulls: []issues.Pull{{Number: 130, Closes: []int{127}}},
	}
	m.issuesCwdFrom = "1"
	m.issuesCwd = "/tmp/repo"
	m.issuesFrom = modeDashboard
	m.mode = modeIssues
	m.issuesBind = map[string]int{"1": 128}
	m.issuesFetched = m.now
	return m
}

func TestOpenIssuesRefusesConductor(t *testing.T) {
	m := baseModel()
	m.mode = modeZoom
	m.zoomID = "1"
	m.fleet = []panel.Panel{{ID: "1", Title: "conductor", Conductor: true, Cwd: "/tmp/ws", Kind: panel.Agent, State: panel.Running}}
	nm, _ := m.openIssues(modeZoom)
	got := nm.(model)
	if got.mode == modeIssues {
		t.Fatal("conductor must not open the overlay")
	}
	if !strings.Contains(got.status, "conductor") {
		t.Errorf("status = %q", got.status)
	}
}

func TestOpenIssuesRefusesEmptyCwd(t *testing.T) {
	m := baseModel()
	m.mode = modeZoom
	m.zoomID = "1"
	m.fleet = []panel.Panel{{ID: "1", Title: "hale", Kind: panel.Agent, Cwd: "", State: panel.Running}}
	nm, _ := m.openIssues(modeZoom)
	got := nm.(model)
	if got.mode == modeIssues {
		t.Fatal("empty cwd must refuse")
	}
}

func TestIssuesTabSkipsEmptyColumns(t *testing.T) {
	m := issuesModel()
	m.issuesBind = map[string]int{}
	m.issuesCwdFrom = ""
	m.issuesBoard.Pulls = nil
	m.issuesCol = 0
	nm, _ := m.handleIssuesKey("tab")
	m = nm.(model)
	if m.issuesCol != 0 {
		t.Fatalf("tab with only BACKLOG cards should stay on BACKLOG, col=%d", m.issuesCol)
	}
	nm, _ = m.handleIssuesKey("right")
	m = nm.(model)
	if m.issuesCol != 0 {
		t.Fatalf("right should skip empty IN PROCESS/PR, col=%d", m.issuesCol)
	}
}

func TestIssuesArrowsMoveColumnAndCard(t *testing.T) {
	m := issuesModel()
	// 118 backlog, 128 in process (bound+live), 127 PR
	if col := issues.Classify(true, false); col != issues.PR {
		t.Fatal("sanity")
	}
	nm, _ := m.handleIssuesKey("right")
	m = nm.(model)
	if m.issuesCol != 1 {
		t.Fatalf("right → col %d, want IN PROCESS (1)", m.issuesCol)
	}
	nm, _ = m.handleIssuesKey("right")
	m = nm.(model)
	if m.issuesCol != 2 {
		t.Fatalf("right → col %d, want PR (2)", m.issuesCol)
	}
	nm, _ = m.handleIssuesKey("left")
	m = nm.(model)
	if m.issuesCol != 1 {
		t.Fatalf("left back to IN PROCESS, got %d", m.issuesCol)
	}
}

func TestIssuesSpaceOpensDetailAndEscReturns(t *testing.T) {
	m := issuesModel()
	m.issuesCol = 1 // IN PROCESS has #128
	nm, _ := m.handleIssuesKey("space")
	m = nm.(model)
	if !m.issuesDetail {
		t.Fatal("space should open detail")
	}
	nm, _ = m.handleIssuesKey("esc")
	m = nm.(model)
	if m.issuesDetail {
		t.Fatal("esc should return to the board")
	}
	if m.mode != modeIssues {
		t.Fatal("esc on the board is a second press")
	}
	nm, _ = m.handleIssuesKey("esc")
	m = nm.(model)
	if m.mode != modeDashboard {
		t.Fatalf("second esc closes, mode=%v", m.mode)
	}
}

func TestIssuesBindMovesCardToInProcess(t *testing.T) {
	useTempIssueBinds(t)
	m := issuesModel()
	m.issuesBind = map[string]int{}
	// without bind, 128 is backlog (no live match except auto-chain from branch 127)
	m.issuesBoard.CurrentPR = &issues.Pull{Number: 130, Closes: []int{127}}
	m.issuesCol = 0
	// pick 128 in backlog
	cards := m.issuesColumn(issues.Backlog)
	for i, c := range cards {
		if c.Number == 128 {
			m.issuesIdx[0] = i
		}
	}
	m.issuesDetail = true
	nm, _ := m.bindIssuesCard()
	m = nm.(model)
	if m.issuesBind["1"] != 128 {
		t.Fatalf("bind = %v", m.issuesBind)
	}
	if _, _, live := m.issuesHandler(128); !live {
		t.Fatal("bound live panel should make 128 IN PROCESS")
	}
	sel, ok := m.issuesSelected()
	if !ok || sel.Number != 128 {
		t.Fatalf("cursor should follow the bound card, got %+v", sel)
	}
	if issues.Col(m.issuesCol) != issues.InProcess {
		t.Fatalf("col %d, want IN PROCESS", m.issuesCol)
	}
}

func TestIssuesPRefusesUnbound(t *testing.T) {
	m := issuesModel()
	m.issuesBind = map[string]int{}
	m.issuesBoard.CurrentPR = nil
	m.issuesBoard.Branch = "main"
	m.issuesCol = 0
	nm, _ := m.handleIssuesKey("p")
	m = nm.(model)
	if m.mode == modeZoom {
		t.Fatal("p on an unbound card must not zoom")
	}
	if !strings.Contains(m.status, "handling") {
		t.Errorf("status = %q", m.status)
	}
}

func TestIssuesDispatchSendsBody(t *testing.T) {
	var got proto.Command
	sendHook = func(c proto.Command) { got = c }
	t.Cleanup(func() { sendHook = nil })
	m := issuesModel()
	m.issuesCol = 1
	m.issuesDetail = true
	nm, _ := m.dispatchIssuesCard()
	out := nm.(model)
	if !strings.Contains(out.status, "dispatched") {
		t.Fatalf("status = %q", out.status)
	}
	if got.Action != "panel.dispatch" || got.ID != "1" {
		t.Fatalf("sent %+v, want panel.dispatch to panel 1", got)
	}
	if !strings.Contains(got.Prompt, "do the overlay") {
		t.Fatalf("prompt = %q", got.Prompt)
	}
}

func TestIssuesColumnScrollKeepsSelection(t *testing.T) {
	m := issuesModel()
	var many []issues.Issue
	for n := 1; n <= 20; n++ {
		many = append(many, issues.Issue{Number: n, Title: "x"})
	}
	m.issuesBoard.Issues = many
	m.issuesBoard.Pulls = nil
	m.issuesBind = map[string]int{}
	m.issuesCol = 0
	m.width, m.height = 120, 24
	vis := m.issuesVisibleCards()
	if vis < 1 {
		t.Fatal("vis")
	}
	for i := 0; i < vis+2; i++ {
		nm, _ := m.handleIssuesKey("down")
		m = nm.(model)
	}
	if m.issuesIdx[0] != vis+2 {
		t.Fatalf("down ×%d → idx %d, want %d", vis+2, m.issuesIdx[0], vis+2)
	}
	if want := m.issuesIdx[0] - vis + 1; m.issuesOff[0] != want {
		t.Errorf("idx %d vis %d: off %d, want %d so the card stays in view", m.issuesIdx[0], vis, m.issuesOff[0], want)
	}
	nm, _ := m.handleIssuesKey("up")
	m = nm.(model)
	if m.issuesIdx[0] != vis+1 {
		t.Errorf("up → idx %d, want %d", m.issuesIdx[0], vis+1)
	}
}

func TestIssuesErrorWhileLoadingShowsInOverlay(t *testing.T) {
	m := issuesModel()
	m.issuesBoard = issues.Board{}
	m.issuesLoading = true
	m.issuesFetched = time.Time{}
	m.applyEvent(proto.ServerMsg{Type: "error", Error: `unknown action "issues.board"`})
	if !strings.Contains(m.issuesErr, "issues.board") {
		t.Fatalf("overlay err = %q", m.issuesErr)
	}
	if m.issuesLoading {
		t.Fatal("fetch must stop after the error")
	}
}

func TestIssuesViewFits(t *testing.T) {
	m := issuesModel()
	view := m.issuesView()
	if !strings.Contains(view, "BACKLOG") || !strings.Contains(view, "IN PROCESS") || !strings.Contains(view, "PR") {
		t.Fatalf("missing columns:\n%s", view)
	}
	if lipgloss.Height(view) > m.height {
		t.Errorf("overlay taller than the terminal: %d > %d", lipgloss.Height(view), m.height)
	}
}

func lastOverlayInnerLine(view string) string {
	lines := strings.Split(ansi.Strip(view), "\n")
	bottom := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "╰") {
			bottom = i
			break
		}
	}
	if bottom < 1 {
		return ""
	}
	for i := bottom - 1; i >= 0; i-- {
		s := strings.TrimSpace(strings.Trim(lines[i], "│ "))
		if s != "" {
			return s
		}
	}
	return ""
}

func TestIssuesLegendIsTheOverlayFooter(t *testing.T) {
	m := issuesModel()
	m.width, m.height = 120, 24
	var many []issues.Issue
	for n := 1; n <= 30; n++ {
		many = append(many, issues.Issue{Number: n, Title: "x", Body: strings.Repeat("paragraph\n", 40)})
	}
	m.issuesBoard.Issues = many
	m.issuesBoard.Pulls = nil
	m.issuesBind = map[string]int{}

	foot := lastOverlayInnerLine(m.issuesView())
	if !strings.Contains(foot, "esc") || !strings.Contains(foot, "space") {
		t.Fatalf("board legend should be the foot of the box, got %q", foot)
	}

	m.issuesDetail = true
	m.issuesCol = 0
	m.issuesIdx[0] = 0
	foot = lastOverlayInnerLine(m.issuesView())
	if !strings.Contains(foot, "esc") || !strings.Contains(foot, "dispatch") {
		t.Fatalf("detail legend should be the foot of the box, got %q", foot)
	}

	frame := m.frame()
	lines := strings.Split(ansi.Strip(frame), "\n")
	if len(lines) != m.height {
		t.Fatalf("frame is %d rows, terminal is %d", len(lines), m.height)
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "DASHBOARD") && !strings.Contains(last, "local") {
		t.Fatalf("cockpit footer should stay the last row, got %q", last)
	}
}

func TestIssuesLoadedIgnoresStaleCwd(t *testing.T) {
	m := issuesModel()
	m = m.applyIssuesLoaded(issuesLoadedMsg{cwd: "/other", board: issues.Board{ClosedCount: 99}})
	if m.issuesBoard.ClosedCount == 99 {
		t.Fatal("stale fetch must not replace the board")
	}
}

func TestIssuesAutoChainSelectsCurrentCard(t *testing.T) {
	useTempIssueBinds(t)
	m := issuesModel()
	m.issuesBind = map[string]int{}
	m.issuesBoard.CurrentPR = &issues.Pull{Number: 130, Closes: []int{127}}
	m.issuesCwd = "/tmp/repo"
	m = m.applyIssuesLoaded(issuesLoadedMsg{cwd: "/tmp/repo", board: m.issuesBoard})
	sel, ok := m.issuesSelected()
	if !ok || sel.Number != 127 {
		t.Fatalf("auto-chain should land on #127, got %+v col=%d", sel, m.issuesCol)
	}
	if issues.Col(m.issuesCol) != issues.PR {
		t.Fatalf("col %d, want PR", m.issuesCol)
	}
}

func TestIssuesDispatchFansOutOnWorkItem(t *testing.T) {
	var got proto.Command
	sendHook = func(c proto.Command) { got = c }
	t.Cleanup(func() { sendHook = nil })
	m := issuesModel()
	m.issuesFanout = true
	m.issuesGroup = "docs/flagship"
	m.issuesCol = 1
	m.issuesDetail = true
	nm, _ := m.dispatchIssuesCard()
	out := nm.(model)
	if !strings.Contains(out.status, "docs/flagship") {
		t.Fatalf("fan-out status = %q", out.status)
	}
	if got.Action != "panel.dispatch-group" || got.Group != "docs/flagship" {
		t.Fatalf("sent %+v, want panel.dispatch-group", got)
	}
}

func TestIssuesEnqueueStampsIssueNumber(t *testing.T) {
	var got proto.Command
	sendHook = func(c proto.Command) { got = c }
	t.Cleanup(func() { sendHook = nil })
	m := issuesModel()
	m.issuesCol = 1
	m.issuesDetail = true
	nm, _ := m.enqueueIssuesCard()
	out := nm.(model)
	if !strings.Contains(out.status, "enqueued") {
		t.Fatalf("status = %q", out.status)
	}
	if got.Action != "task.enqueue" || got.Issue != 128 {
		t.Fatalf("sent %+v, want task.enqueue issue 128", got)
	}
}

func TestIssuesMilestoneFilterHidesOtherCards(t *testing.T) {
	m := issuesModel()
	m.issuesBoard.Milestones = []string{"2.3.0"}
	m.issuesBoard.Issues[1].Milestone = "2.3.0" // 128
	m.issuesBind = map[string]int{}
	m.issuesCwdFrom = ""
	m.issuesMile = 1
	if got := m.issuesColumn(issues.Backlog); len(got) == 0 {
		t.Fatal("filtered backlog should still have unassigned cards in 2.3.0 or be empty only if none match")
	}
	var nums []int
	for c := 0; c < 3; c++ {
		for _, iss := range m.issuesColumn(issues.Col(c)) {
			nums = append(nums, iss.Number)
			if iss.Milestone != "2.3.0" {
				t.Fatalf("#%d leaked through the 2.3.0 filter", iss.Number)
			}
		}
	}
	if len(nums) != 1 || nums[0] != 128 {
		t.Fatalf("filter = %v, want only #128", nums)
	}
}

func TestIssuesMThenTabSelectsMilestoneLive(t *testing.T) {
	m := issuesModel()
	m.issuesBoard.Milestones = []string{"2.3.0", "v2.2.2"}
	m.issuesBoard.Issues[1].Milestone = "2.3.0" // 128
	m.issuesBind = map[string]int{}
	m.issuesCwdFrom = ""
	nm, _ := m.handleIssuesKey("m")
	m = nm.(model)
	if !m.issuesMilePick {
		t.Fatal("m should enter milestone pick")
	}
	nm, _ = m.handleIssuesKey("tab")
	m = nm.(model)
	if m.issuesMile != 1 {
		t.Fatalf("tab should rotate to the first named milestone, mile=%d", m.issuesMile)
	}
	var nums []int
	for c := 0; c < 3; c++ {
		for _, iss := range m.issuesColumn(issues.Col(c)) {
			nums = append(nums, iss.Number)
		}
	}
	if len(nums) != 1 || nums[0] != 128 {
		t.Fatalf("2.3.0 should show only its cards, got %v", nums)
	}
	if !strings.Contains(m.status, "2.3.0") || !strings.Contains(m.status, "BACKLOG") {
		t.Fatalf("status should report the filtered columns, got %q", m.status)
	}
	if !strings.Contains(m.status, "tab") {
		t.Fatalf("pick hint should stay on the status, got %q", m.status)
	}

	nm, _ = m.handleIssuesKey("tab")
	m = nm.(model)
	if m.issuesMile != 2 {
		t.Fatalf("second tab → v2.2.2, mile=%d", m.issuesMile)
	}
	nm, _ = m.handleIssuesKey("shift+tab")
	m = nm.(model)
	if m.issuesMile != 1 {
		t.Fatalf("S-tab back to 2.3.0, mile=%d", m.issuesMile)
	}

	nm, _ = m.handleIssuesKey("esc")
	m = nm.(model)
	if m.issuesMilePick || m.mode != modeIssues {
		t.Fatalf("esc should leave pick and keep the overlay, pick=%v mode=%v", m.issuesMilePick, m.mode)
	}
	if m.issuesMile != 1 {
		t.Fatalf("filter should stay, mile=%d", m.issuesMile)
	}

	nm, _ = m.handleIssuesKey("1")
	m = nm.(model)
	if m.issuesMile != 1 {
		t.Fatalf("digits are not shortcuts, mile=%d", m.issuesMile)
	}
}

func TestIssuesTabWithoutMStillMovesColumns(t *testing.T) {
	m := issuesModel()
	m.issuesBoard.Milestones = []string{"2.3.0"}
	m.issuesBoard.Issues[1].Milestone = "2.3.0"
	nm, _ := m.handleIssuesKey("tab")
	m = nm.(model)
	if m.issuesMile != 0 {
		t.Fatalf("tab without m must not change the milestone, mile=%d", m.issuesMile)
	}
}

func TestIssuesMilestonePickFromDetail(t *testing.T) {
	m := issuesModel()
	m.issuesBoard.Milestones = []string{"2.3.0"}
	m.issuesBoard.Issues[1].Milestone = "2.3.0"
	m.issuesCol = 0 // #118 has no milestone
	m.issuesDetail = true
	nm, _ := m.handleIssuesKey("m")
	m = nm.(model)
	if !m.issuesMilePick {
		t.Fatal("m from detail should enter pick")
	}
	nm, _ = m.handleIssuesKey("tab")
	m = nm.(model)
	if m.issuesMile != 1 {
		t.Fatalf("mile=%d", m.issuesMile)
	}
	if m.issuesDetail {
		t.Fatal("detail should close when the card is not in the selected milestone")
	}
}

func TestMatchIssuesMile(t *testing.T) {
	names := []string{"2.3.0", "0.2.0", "0.2.1"}
	none := "(none)"
	cases := []struct {
		q       string
		want    int
		wantOK  bool
		comment string
	}{
		{"", 0, true, "blank is all"},
		{"none", 0, true, "none aliases (none)"},
		{"2.3.0", 1, true, "exact"},
		{"2.3", 1, true, "prefix"},
		{"0.2", 0, false, "0.2.0 and 0.2.1 share a prefix"},
		{"0.2.1", 3, true, "exact beats the sibling prefix"},
		{"nope", 0, false, "unknown"},
	}
	for _, c := range cases {
		got, ok := matchIssuesMile(c.q, none, names)
		if ok != c.wantOK || (ok && got != c.want) {
			t.Errorf("%s: matchIssuesMile(%q) = %d,%v want %d,%v", c.comment, c.q, got, ok, c.want, c.wantOK)
		}
	}
}

func TestIssuesSlashFindsMilestone(t *testing.T) {
	m := issuesModel()
	m.issuesBoard.Milestones = []string{"2.3.0", "v2.2.2"}
	m.issuesBoard.Issues[1].Milestone = "2.3.0"
	m.issuesBind = map[string]int{}
	m.issuesCwdFrom = ""
	nm, _ := m.handleIssuesKey("/")
	m = nm.(model)
	if m.mode != modeIssues || m.input != inputIssueMile {
		t.Fatalf(" / should open the milestone prompt, mode=%v input=%v", m.mode, m.input)
	}
	m.inputBuf = "2.3"
	nm, _ = m.commitInput()
	m = nm.(model)
	if m.input != inputNone {
		t.Fatalf("enter should close the prompt, input=%v", m.input)
	}
	if m.issuesMile != 1 {
		t.Fatalf("2.3 should select 2.3.0, mile=%d", m.issuesMile)
	}
	var nums []int
	for c := 0; c < 3; c++ {
		for _, iss := range m.issuesColumn(issues.Col(c)) {
			nums = append(nums, iss.Number)
		}
	}
	if len(nums) != 1 || nums[0] != 128 {
		t.Fatalf("filter should show only 2.3.0 cards, got %v", nums)
	}

	nm, _ = m.handleIssuesKey("/")
	m = nm.(model)
	m.inputBuf = "v2"
	nm, _ = m.commitInput()
	m = nm.(model)
	if m.issuesMile != 2 {
		t.Fatalf("v2 should select v2.2.2, mile=%d", m.issuesMile)
	}

	nm, _ = m.handleIssuesKey("/")
	m = nm.(model)
	m.inputBuf = "zzz"
	nm, _ = m.commitInput()
	m = nm.(model)
	if m.input != inputIssueMile {
		t.Fatal("a miss should keep the prompt open")
	}
	if m.issuesMile != 2 {
		t.Fatalf("a miss must not change the filter, mile=%d", m.issuesMile)
	}
}

func TestIssuesDetailShowsRelations(t *testing.T) {
	m := issuesModel()
	m.issuesBoard.Issues[1].Parent = 127
	m.issuesBoard.Issues[1].BlockedBy = []int{99}
	m.issuesBoard.Issues[1].Sub = []int{129}
	m.issuesCol = 1
	m.issuesDetail = true
	view := m.issuesDetailView(80, 20)
	for _, want := range []string{"#128", "parent  #127", "blocked-by  #99", "sub  #129"} {
		if !strings.Contains(view, want) {
			t.Errorf("detail missing %q in:\n%s", want, view)
		}
	}
}

func TestIssuesInFlightTaskMovesToInProcess(t *testing.T) {
	m := issuesModel()
	m.issuesBind = map[string]int{}
	m.issuesCwdFrom = ""
	m.issuesBoard.Pulls = nil
	m.issuesBoard.CurrentPR = nil
	m.issuesBoard.Branch = "main"
	m.fleet = []panel.Panel{{
		ID: "9", Title: "hale", Kind: panel.Agent, State: panel.Running, Cwd: "/tmp/repo",
	}}
	m.tasks = []proto.Task{{Issue: 118, Status: "running", Panel: "9"}}
	cards := m.issuesColumn(issues.InProcess)
	if len(cards) != 1 || cards[0].Number != 118 {
		t.Fatalf("in-flight #118 should sit in IN PROCESS, got %+v", cards)
	}
	title, id, live := m.issuesHandler(118)
	if !live || id != "9" || title != "hale" {
		t.Fatalf("handler = %q %q live=%v", title, id, live)
	}
}

func TestIssuesQueuedTaskStaysBacklog(t *testing.T) {
	m := issuesModel()
	m.issuesBind = map[string]int{}
	m.issuesCwdFrom = ""
	m.issuesBoard.Pulls = nil
	m.issuesBoard.CurrentPR = nil
	m.issuesBoard.Branch = "main"
	m.fleet = []panel.Panel{{
		ID: "9", Title: "hale", Kind: panel.Agent, State: panel.Running, Cwd: "/tmp/repo",
	}}
	m.tasks = []proto.Task{{Issue: 118, Status: "queued"}}
	for _, iss := range m.issuesColumn(issues.InProcess) {
		if iss.Number == 118 {
			t.Fatal("queued #118 must stay in BACKLOG")
		}
	}
}

func TestOpenIssuesAsksForTaskList(t *testing.T) {
	var got []string
	sendHook = func(c proto.Command) { got = append(got, c.Action) }
	t.Cleanup(func() { sendHook = nil })
	m := baseModel()
	m.mode = modeZoom
	m.zoomID = "1"
	m.fleet = []panel.Panel{{
		ID: "1", Title: "hale", Kind: panel.Agent, State: panel.Running, Cwd: "/tmp/repo",
	}}
	nm, _ := m.openIssues(modeZoom)
	out := nm.(model)
	if out.mode != modeIssues {
		t.Fatalf("mode = %v", out.mode)
	}
	var board, list bool
	for _, a := range got {
		if a == "issues.board" {
			board = true
		}
		if a == "task.list" {
			list = true
		}
	}
	if !board || !list {
		t.Fatalf("open sent %v, want issues.board and task.list", got)
	}
}

func TestIssuesMissedChainSetsStatus(t *testing.T) {
	useTempIssueBinds(t)
	m := issuesModel()
	m.issuesBind = map[string]int{}
	m.issuesBoard.CurrentPR = nil
	m.issuesBoard.Branch = "main"
	m.issuesBoard.Pulls = nil
	m = m.applyIssuesLoaded(issuesLoadedMsg{cwd: "/tmp/repo", board: m.issuesBoard})
	if !strings.Contains(m.status, "no issue or PR") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestIssuesBoxKeepsFixedMargin(t *testing.T) {
	for _, sz := range [][2]int{{120, 40}, {200, 60}, {80, 30}} {
		m := issuesModel()
		m.width, m.height = sz[0], sz[1]
		view := m.issuesView()
		if got, want := lipgloss.Width(view), m.width-2*issueMarginX; got != want {
			t.Errorf("%dx%d: box width %d, want %d", m.width, m.height, got, want)
		}
		if got, want := lipgloss.Height(view), m.height-1-2*issueMarginY; got != want {
			t.Errorf("%dx%d: box height %d, want %d", m.width, m.height, got, want)
		}
	}
}
