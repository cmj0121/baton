package tui

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/issues"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// The GitHub issues overlay (modeIssues, I / C-t I). It is the cockpit's window
// onto the selected panel's repo: compact cards in BACKLOG / IN PROCESS / PR,
// a detail frame in the same box, and T/t/b/B on that detail. The overlay
// owns the keyboard until esc. GitHub is fetched on the daemon (the operator's
// gh); handler titles follow the fleet snapshot without waiting for the next fetch.

const (
	issueCols    = 3
	cardBlock    = 4  // one compact card, in rows
	issueBoxCols = 72 // popup width before the first WindowSizeMsg
	issueBoxRows = 18 // popup body rows before the first WindowSizeMsg
	issueMarginX = 4  // blank columns between the box and each side of the terminal
	issueMarginY = 2  // blank rows above and below the box (a cell is ~2:1 tall)
)

type issuesLoadedMsg struct {
	cwd   string
	board issues.Board
	err   error
}

// refreshIssues asks the daemon for the board again. The reply arrives as an
// "issues" event, not a tea.Cmd.
func (m *model) refreshIssues() {
	m.sendf(proto.Command{Action: "issues.board", Dir: m.issuesCwd})
	m.issuesLoading = true
	m.status = m.tr("issues.status.loading", "issues · fetching GitHub")
}

// issuesPanelID is the one panel T dispatches to; empty when T fans out to the
// work item.
func (m model) issuesPanelID() string {
	if m.issuesFanout {
		return ""
	}
	return m.issuesCwdFrom
}

func (m model) openIssues(from mode) (tea.Model, tea.Cmd) {
	cwd, id, group, fanout, why := m.issuesTarget()
	if why != "" {
		m.status = why
		return m, nil
	}
	m.issuesFrom = from
	m.issuesCwd = cwd
	m.issuesCwdFrom = id
	m.issuesFanout = fanout
	m.issuesGroup = group
	m.issuesMile = 0
	m.issuesMilePick = false
	m.issuesDetail = false
	m.issuesCol = 0
	m.issuesIdx = [3]int{}
	m.issuesOff = [3]int{}
	m.issuesBoard = issues.Board{}
	m.issuesErr = ""
	m.issuesFetched = time.Time{}
	if m.issuesBind == nil {
		m.issuesBind = map[string]int{}
	}
	m.mode = modeIssues
	m.sendf(proto.Command{Action: "task.list"})
	m.refreshIssues()
	return m, nil
}

func (m model) closeIssues() model {
	m.mode = m.issuesFrom
	m.issuesLoading = false
	m.issuesDetail = false
	m.issuesMilePick = false
	m.status = m.tr("issues.status.closed", "issues closed")
	return m
}

// issuesTarget is the working tree and panel the overlay is about. The
// conductor's workspace is not a project repo; a fold row is not a panel.
func (m model) issuesTarget() (cwd, id, group string, fanout bool, why string) {
	var p panel.Panel
	ok := false
	switch m.mode {
	case modeZoom:
		p, ok = m.fleetPanel(m.zoomID)
	case modeGroupZoom:
		p, ok = m.focusedMember()
		group = m.groupName
	default:
		it, got := m.selectedItem()
		if !got {
			return "", "", "", false, m.tr("issues.status.select-panel", "issues: select a panel")
		}
		if it.kind == itemFold {
			return "", "", "", false, m.tr("status.expand-quiet-group-first", "expand the quiet group first")
		}
		if it.kind == itemGroup {
			if len(it.members) == 0 {
				return "", "", "", false, m.tr("issues.status.select-panel", "issues: select a panel")
			}
			p, ok, fanout = it.members[0], true, true
			group = it.name
		} else {
			p, ok = it.panel, true
			group = p.Group
		}
	}
	if !ok {
		return "", "", "", false, m.tr("issues.status.select-panel", "issues: select a panel")
	}
	if p.Conductor {
		return "", "", "", false, m.tr("issues.status.not-conductor", "issues: the conductor workspace is not a project repo")
	}
	cwd = strings.TrimSpace(p.Cwd)
	if cwd == "" {
		return "", "", "", false, m.tr("issues.status.no-cwd", "issues: panel has no working directory")
	}
	return cwd, p.ID, group, fanout, ""
}

func (m model) applyIssuesLoaded(msg issuesLoadedMsg) model {
	if m.mode != modeIssues || msg.cwd != m.issuesCwd {
		return m
	}
	m.issuesLoading = false
	if msg.err != nil {
		m.issuesErr = msg.err.Error()
		m.status = m.tr("issues.status.failed", "issues: ") + msg.err.Error()
		return m
	}
	m.issuesErr = ""
	m.issuesBoard = msg.board
	m.issuesFetched = m.now
	m.issuesPersist = issues.LoadBinds()
	found := m.selectCurrentIssue()
	m.clampIssuesCursor()
	if found {
		n := len(msg.board.Issues)
		m.status = fmt.Sprintf(m.tr("issues.status.open-n", "issues · %s · %d open"), msg.board.Repo.String(), n)
	} else {
		m.status = m.tr("issues.status.no-current", "no issue or PR for this branch · space detail · b bind")
	}
	return m
}

func (m model) handleIssuesKey(key string) (tea.Model, tea.Cmd) {
	if m.issuesMilePick {
		return m.handleIssuesMilePickKey(key)
	}
	if m.issuesDetail {
		return m.handleIssuesDetailKey(key)
	}
	if m.handleIssuesMileKey(key) {
		return m, nil
	}
	switch key {
	case "esc", "q":
		return m.closeIssues(), nil
	case "m":
		m.enterIssuesMilePick()
		return m, nil
	case "left", "h", "shift+tab":
		m.moveIssuesCol(-1)
		return m, nil
	case "right", "l", "tab":
		m.moveIssuesCol(1)
		return m, nil
	case "up", "k":
		m.moveIssuesCard(-1)
		return m, nil
	case "down", "j":
		m.moveIssuesCard(1)
		return m, nil
	case "enter", "space":
		if _, ok := m.issuesSelected(); !ok {
			m.status = m.tr("issues.status.empty-column", "issues: no card in this column")
			return m, nil
		}
		m.issuesDetail = true
		return m, nil
	case "p":
		return m.jumpIssuesPanel()
	case "r":
		m.refreshIssues()
		return m, nil
	}
	return m, nil
}

func (m model) handleIssuesDetailKey(key string) (tea.Model, tea.Cmd) {
	if m.handleIssuesMileKey(key) {
		return m, nil
	}
	switch key {
	case "esc", "q":
		m.issuesDetail = false
		return m, nil
	case "m":
		m.enterIssuesMilePick()
		return m, nil
	case "p":
		return m.jumpIssuesPanel()
	case "r":
		m.refreshIssues()
		return m, nil
	case "enter":
		c, ok := m.issuesSelected()
		if !ok {
			return m, nil
		}
		return m, openIssueWeb(m.issuesBoard.Repo, c.Number)
	case "T":
		return m.dispatchIssuesCard()
	case "t":
		return m.enqueueIssuesCard()
	case "b":
		return m.bindIssuesCard()
	case "B":
		return m.startIssuesBlock()
	}
	return m, nil
}

func (m *model) moveIssuesCard(delta int) {
	cards := m.issuesColumn(issues.Col(m.issuesCol))
	if len(cards) == 0 {
		m.status = m.tr("issues.status.empty-column", "issues: no card in this column")
		return
	}
	m.issuesIdx[m.issuesCol] = clampInt(m.issuesIdx[m.issuesCol]+delta, 0, len(cards)-1)
	m.issuesEnsureVisible(m.issuesCol)
}

// moveIssuesCol steps across status columns, skipping empty ones so left/right
// and tab do not land in IN PROCESS / PR on a board that is all BACKLOG
// (fediqo today) and then look like up/down is broken.
func (m *model) moveIssuesCol(delta int) {
	cols := m.issuesColumns()
	for c, i := m.issuesCol, 0; i < issueCols; i++ {
		c = wrapIndex(c, delta, issueCols)
		if len(cols[c]) > 0 {
			m.issuesCol = c
			return
		}
	}
}

func (m *model) clampIssuesCursor() {
	cols := m.issuesColumns()
	for c, cards := range cols {
		if len(cards) == 0 {
			m.issuesIdx[c] = 0
			m.issuesOff[c] = 0
			continue
		}
		m.issuesIdx[c] = clampInt(m.issuesIdx[c], 0, len(cards)-1)
		m.issuesEnsureVisible(c)
	}
	if len(cols[m.issuesCol]) == 0 {
		m.moveIssuesCol(1)
	}
}

func (m *model) issuesEnsureVisible(col int) {
	vis := m.issuesVisibleCards()
	idx := m.issuesIdx[col]
	if idx < m.issuesOff[col] {
		m.issuesOff[col] = idx
	}
	if idx >= m.issuesOff[col]+vis {
		m.issuesOff[col] = idx - vis + 1
	}
	if m.issuesOff[col] < 0 {
		m.issuesOff[col] = 0
	}
}

func (m model) issuesVisibleCards() int {
	// Column title + gap sit in the middle pane; the overlay header and legend
	// are pinned outside it, so they do not steal card rows and then clip the
	// footer off.
	body := m.issuesMidH() - 2
	if body < cardBlock {
		return 1
	}
	return body / cardBlock
}

func (m model) issuesColumn(col issues.Col) []issues.Issue {
	return m.issuesColumns()[col]
}

// issuesColumns sorts the milestone's cards into the three columns in one pass:
// the fleet and the pulls are each walked once, not once per card.
func (m model) issuesColumns() [issueCols][]issues.Issue {
	var cols [issueCols][]issues.Issue
	mile := m.issuesMileName()
	hs := m.issuesHandlers()
	hasPR := map[int]bool{}
	for _, p := range m.issuesBoard.Pulls {
		hasPR[p.Number] = true
		for _, n := range p.Closes {
			hasPR[n] = true
		}
	}
	for _, iss := range m.issuesBoard.Issues {
		if mile != "" && iss.Milestone != mile {
			continue
		}
		col := issues.Classify(hasPR[iss.Number], hs[iss.Number].live)
		cols[col] = append(cols[col], iss)
	}
	return cols
}

func (m model) issuesMileName() string {
	if m.issuesMile <= 0 || m.issuesMile > len(m.issuesBoard.Milestones) {
		return ""
	}
	return m.issuesBoard.Milestones[m.issuesMile-1]
}

func (m *model) handleIssuesMileKey(key string) bool {
	switch key {
	case "[":
		m.cycleIssuesMile(-1)
		return true
	case "]":
		m.cycleIssuesMile(1)
		return true
	}
	return false
}

func (m *model) enterIssuesMilePick() {
	m.issuesMilePick = true
	m.issuesMileStatus()
}

func (m model) handleIssuesMilePickKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "tab", "]":
		m.cycleIssuesMile(1)
		return m, nil
	case "shift+tab", "[":
		m.cycleIssuesMile(-1)
		return m, nil
	case "m", "esc", "enter":
		m.issuesMilePick = false
		m.issuesMileStatus()
		return m, nil
	case "q":
		return m.closeIssues(), nil
	}
	m.issuesMilePick = false
	return m.handleIssuesKey(key)
}

func (m *model) cycleIssuesMile(delta int) {
	n := len(m.issuesBoard.Milestones) + 1
	if n <= 1 {
		return
	}
	m.applyIssuesMile(wrapIndex(m.issuesMile, delta, n))
}

func (m *model) applyIssuesMile(i int) {
	n := len(m.issuesBoard.Milestones) + 1
	if i < 0 || i >= n {
		return
	}
	m.issuesMile = i
	m.issuesIdx = [3]int{}
	m.issuesOff = [3]int{}
	if m.issuesDetail {
		if _, ok := m.issuesSelected(); !ok {
			m.issuesDetail = false
		}
	}
	m.clampIssuesCursor()
	m.issuesMileStatus()
}

func (m *model) issuesMileStatus() {
	cols := m.issuesColumns()
	name := m.issuesMileName()
	if name == "" {
		name = m.tr("issues.milestone.none", "(none)")
	}
	m.status = fmt.Sprintf(m.tr("issues.status.mile", "issues · %s · BACKLOG %d · IN PROCESS %d · PR %d"),
		name, len(cols[issues.Backlog]), len(cols[issues.InProcess]), len(cols[issues.PR]))
	if m.issuesMilePick {
		m.status += " · " + m.tr("issues.status.mile-pick", "tab rotate · esc done")
	}
}

func (m *model) selectCurrentIssue() bool {
	bind := 0
	if m.issuesBind != nil {
		if m.issuesCwdFrom != "" {
			bind = m.issuesBind[m.issuesCwdFrom]
		}
		if bind == 0 && m.issuesGroup != "" {
			for _, p := range m.fleet {
				if p.Group == m.issuesGroup && m.issuesBind[p.ID] > 0 {
					bind = m.issuesBind[p.ID]
					break
				}
			}
		}
	}
	if bind == 0 {
		bind = issues.LookupBind(m.issuesBoard.Repo, m.issuesCwd, m.issuesPersist)
	}
	n := issues.AutoChain(bind, m.issuesBoard.Branch, m.issuesBoard.CurrentPR)
	if n <= 0 {
		return false
	}
	for c, cards := range m.issuesColumns() {
		for i, iss := range cards {
			if iss.Number != n {
				continue
			}
			m.issuesCol = c
			m.issuesIdx[c] = i
			m.issuesEnsureVisible(c)
			return true
		}
	}
	return false
}

// issueHandler is the panel working an issue.
type issueHandler struct {
	title, id string
	live      bool
}

func (m model) issuesHandler(num int) (title, id string, live bool) {
	h := m.issuesHandlers()[num]
	return h.title, h.id, h.live
}

// issuesHandlers maps each issue to the panel working it: the first live panel
// bound to it, else an in-flight task with that issue number, else the last
// exited bind.
func (m model) issuesHandlers() map[int]issueHandler {
	hs := map[int]issueHandler{}
	chain := issues.AutoChain(0, m.issuesBoard.Branch, m.issuesBoard.CurrentPR)
	for _, p := range m.fleet {
		if p.Conductor {
			continue
		}
		n := m.issuesBind[p.ID]
		if n == 0 {
			n = issues.LookupBind(m.issuesBoard.Repo, p.Cwd, m.issuesPersist)
		}
		if n == 0 && p.ID == m.issuesCwdFrom {
			n = chain
		}
		if n <= 0 || hs[n].live {
			continue
		}
		hs[n] = issueHandler{title: p.Title, id: p.ID, live: p.State != panel.Exited}
	}
	// An in-flight task (dispatched/running) with issue N is a live handler even
	// without a bind. A queued task is not — that card stays in BACKLOG.
	for _, t := range m.tasks {
		if t.Issue <= 0 || hs[t.Issue].live {
			continue
		}
		if t.Status != "dispatched" && t.Status != "running" {
			continue
		}
		p, ok := m.fleetPanel(t.Panel)
		if !ok || p.Conductor {
			continue
		}
		hs[t.Issue] = issueHandler{title: p.Title, id: p.ID, live: p.State != panel.Exited}
	}
	return hs
}

func (m model) issuesSelected() (issues.Issue, bool) {
	cards := m.issuesColumn(issues.Col(m.issuesCol))
	i := m.issuesIdx[m.issuesCol]
	if i < 0 || i >= len(cards) {
		return issues.Issue{}, false
	}
	return cards[i], true
}

func (m model) jumpIssuesPanel() (tea.Model, tea.Cmd) {
	var p panel.Panel
	c, ok := m.issuesSelected()
	if ok {
		_, id, live := m.issuesHandler(c.Number)
		p, ok = m.fleetPanel(id)
		ok = ok && live
	}
	if !ok {
		m.status = m.tr("issues.status.no-handling-panel", "no handling panel")
		return m, nil
	}
	m.issuesDetail = false
	return m.zoomInto(p), nil
}

func (m model) bindIssuesCard() (tea.Model, tea.Cmd) {
	c, ok := m.issuesSelected()
	if !ok {
		return m, nil
	}
	if m.issuesBind == nil {
		m.issuesBind = map[string]int{}
	}
	if m.issuesFanout && m.issuesGroup != "" {
		for _, p := range m.fleet {
			if p.Group == m.issuesGroup && p.IsAgent() && !p.Conductor {
				m.issuesBind[p.ID] = c.Number
			}
		}
	} else if m.issuesCwdFrom != "" {
		m.issuesBind[m.issuesCwdFrom] = c.Number
	} else {
		m.status = m.tr("issues.status.select-panel", "issues: select a panel")
		return m, nil
	}
	m.status = fmt.Sprintf(m.tr("issues.status.bound", "bound #%d to this panel"), c.Number)
	issues.SaveBind(m.issuesBoard.Repo, m.issuesCwd, c.Number)
	if m.issuesPersist == nil {
		m.issuesPersist = map[string]int{}
	}
	m.issuesPersist[issues.BindKey(m.issuesBoard.Repo, m.issuesCwd)] = c.Number
	m.selectCurrentIssue()
	m.clampIssuesCursor()
	return m, nil
}

func (m model) dispatchIssuesCard() (tea.Model, tea.Cmd) {
	c, ok := m.issuesSelected()
	if !ok {
		return m, nil
	}
	body := strings.TrimSpace(c.Title + "\n\n" + c.Body)
	if body == "" {
		m.status = m.tr("status.task-cannot-be-empty", "a task cannot be empty")
		return m, nil
	}
	if m.issuesFanout && m.issuesGroup != "" {
		m.dispatchID, m.dispatchGroup = "", m.issuesGroup
		return m.commitDispatch(body), nil
	}
	p, ok := m.fleetPanel(m.issuesPanelID())
	if !ok || !p.IsAgent() {
		m.status = m.tr("status.dispatch-select-agent-panel", "dispatch: select an agent panel")
		return m, nil
	}
	m.dispatchID, m.dispatchGroup = p.ID, ""
	return m.commitDispatch(body), nil
}

func (m model) enqueueIssuesCard() (tea.Model, tea.Cmd) {
	c, ok := m.issuesSelected()
	if !ok {
		return m, nil
	}
	body := strings.TrimSpace("#" + strconv.Itoa(c.Number) + " " + c.Title + "\n\n" + c.Body)
	m.enqueueGroup = m.issuesGroup
	m.enqueueIssue = c.Number
	return m.commitEnqueue(body), nil
}

func (m model) startIssuesBlock() (tea.Model, tea.Cmd) {
	c, ok := m.issuesSelected()
	if !ok {
		return m, nil
	}
	m.issuesBlockFor = c.Number
	m.input = inputIssueBlock
	m.inputBuf = ""
	m.status = m.tr("issues.status.block-prompt", "blocked-by · issue number")
	return m, nil
}

func (m model) commitIssuesBlock(buf string) (tea.Model, tea.Cmd) {
	issue := m.issuesBlockFor
	m.issuesBlockFor = 0
	n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(buf), "#"))
	if err != nil || n <= 0 {
		m.status = m.tr("issues.status.block-need-number", "blocked-by: need an issue number")
		return m, nil
	}
	m.sendf(proto.Command{Action: "issues.block", Dir: m.issuesCwd, Issue: issue, Blocker: n})
	m.issuesLoading = true
	m.status = m.tr("issues.status.loading", "issues · fetching GitHub")
	return m, nil
}

func openIssueWeb(repo issues.Repo, n int) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("gh", "issue", "view", strconv.Itoa(n), "--repo", repo.String(), "--web")
		_ = cmd.Start()
		return nil
	}
}

func (m model) maybeRefreshIssues() (model, tea.Cmd) {
	if m.mode != modeIssues || m.issuesLoading {
		return m, nil
	}
	d := m.issuesInterval
	if d <= 0 || m.issuesFetched.IsZero() || m.now.Sub(m.issuesFetched) < d {
		return m, nil
	}
	m.sendf(proto.Command{Action: "issues.board", Dir: m.issuesCwd})
	m.issuesLoading = true // silent: an auto-refresh leaves the status line alone
	return m, nil
}

// issuesInner is the box's outer width and its body rows. The box fills the
// terminal less issueMarginX columns and issueMarginY rows on every side; the
// footer row is not the box's to take.
func (m model) issuesInner() (w, h int) {
	w, h = issueBoxCols, issueBoxRows
	if m.width > 0 {
		w = m.width - 2*issueMarginX
	}
	if minW := popupMinCols + 2*popupPadX + 2; w < minW {
		w = minW
	}
	if m.height > 0 {
		h = m.height - 1 - 2*issueMarginY - popupChrome
	}
	if h < 12 {
		h = 12
	}
	return w, h
}

func (m model) issuesBox() (inner, h int) {
	w, h := m.issuesInner()
	inner = w - 2 - 2*popupPadX
	if inner < 20 {
		inner = 20
	}
	return inner, h
}

func (m model) issuesChrome() (head, legend string) {
	if m.issuesMilePick {
		legend = mutedStyle.Render(m.tr("issues.legend.mile-pick", "tab rotate  ·  S-tab back  ·  m/esc done"))
	} else if m.issuesDetail {
		legend = mutedStyle.Render(m.tr("issues.legend.detail", "T dispatch  ·  t enqueue  ·  b bind  ·  B block  ·  p panel  ·  r refresh  ·  enter  ·  esc"))
	} else {
		legend = mutedStyle.Render(m.tr("issues.legend.board", "tab/←→ column  ·  ↑/↓ card  ·  m milestone  ·  space/enter detail  ·  p panel  ·  r refresh  ·  esc"))
	}
	if m.issuesDetail && !m.issuesMilePick {
		return mutedStyle.Render(m.tr("issues.detail.back", "← esc board")), legend
	}
	head = m.issuesHeaderLine()
	if m.issuesErr != "" {
		head += "\n" + lipgloss.NewStyle().Foreground(colRed).Render(m.issuesErr)
	}
	return head, legend
}

func (m model) issuesMidH() int {
	_, h := m.issuesBox()
	head, legend := m.issuesChrome()
	midH := h - lipgloss.Height(head) - lipgloss.Height(legend) - 2
	if midH < 1 {
		return 1
	}
	return midH
}

func (m model) issuesView() string {
	inner, h := m.issuesBox()
	head, legend := m.issuesChrome()
	midH := m.issuesMidH()
	var mid string
	if m.issuesDetail {
		mid = m.issuesDetailView(inner, midH)
	} else {
		mid = m.issuesBoardView(inner)
	}
	headL := strings.Split(head, "\n")
	legL := strings.Split(legend, "\n")
	midL := padBlock(strings.Split(clipRows(mid, midH), "\n"), midH, inner)
	rows := make([]string, 0, h)
	rows = append(rows, headL...)
	rows = append(rows, "")
	rows = append(rows, midL...)
	rows = append(rows, "")
	rows = append(rows, legL...)
	if len(rows) > h {
		keep := h - len(legL)
		if keep < 0 {
			keep = 0
		}
		rows = append(rows[:keep], legL...)
	}
	return popupBoxAt(m.fitPopup(strings.Join(rows, "\n")), inner)
}

func (m model) issuesHeaderLine() string {
	if m.issuesLoading && m.issuesBoard.Repo.Owner == "" {
		return inkStyle.Render(m.tr("issues.banner.loading", "fetching GitHub…"))
	}
	repo := m.issuesBoard.Repo.String()
	if repo == "" {
		repo = "—"
	}
	branch := m.issuesBoard.Branch
	if branch == "" {
		branch = "—"
	}
	age := m.tr("issues.fetched.never", "not yet")
	if !m.issuesFetched.IsZero() && !m.now.IsZero() {
		age = fmt.Sprintf(m.tr("issues.fetched.ago", "fetched %s ago"), m.now.Sub(m.issuesFetched).Truncate(time.Second))
	}
	if m.issuesLoading {
		age = m.tr("issues.fetched.loading", "fetching")
	}
	openN := len(m.issuesBoard.Issues)
	closed := m.issuesBoard.ClosedCount
	ms := m.issuesMilestoneChips()
	counts := fmt.Sprintf(m.tr("issues.header.counts", "milestone  %s     open %d  ·  closed %d  ·  %s"), ms, openN, closed, age)
	if m.issuesMilePick {
		counts = lipgloss.NewStyle().Foreground(colBrand).Bold(true).Render(counts)
	} else {
		counts = mutedStyle.Render(counts)
	}
	return counts + "\n" + inkStyle.Render(fmt.Sprintf(m.tr("issues.header.repo", "%s  ·  GitHub  ·  %s"), repo, branch))
}

func (m model) issuesMilestoneChips() string {
	none := m.tr("issues.milestone.none", "(none)")
	chip := func(on bool, s string) string {
		if on {
			return "[" + s + "]"
		}
		return s
	}
	parts := []string{chip(m.issuesMile == 0, none)}
	for i, name := range m.issuesBoard.Milestones {
		parts = append(parts, chip(m.issuesMile == i+1, name))
	}
	return strings.Join(parts, "  ")
}

func (m model) issuesBoardView(inner int) string {
	vis := m.issuesVisibleCards()
	colW := (inner - 4) / 3
	if colW < 12 {
		colW = 12
	}
	hs := m.issuesHandlers()
	var cols []string
	for i, cards := range m.issuesColumns() {
		cols = append(cols, m.renderIssueColumn(issues.Col(i), cards, hs, colW, vis, i == m.issuesCol))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cols[0], "  ", cols[1], "  ", cols[2])
}

func (m model) renderIssueColumn(col issues.Col, cards []issues.Issue, hs map[int]issueHandler, width, vis int, focused bool) string {
	idx := int(col)
	title := col.String()
	if focused && len(cards) > vis {
		title = fmt.Sprintf("%s %d/%d", title, m.issuesIdx[idx]+1, len(cards))
	} else {
		title = fmt.Sprintf("%s %d", title, len(cards))
	}
	headStyle := sectionStyle.Width(width).Align(lipgloss.Center)
	if focused {
		headStyle = headStyle.Foreground(colBrand).Bold(true)
	}
	head := headStyle.Render(title)
	off := m.issuesOff[idx]
	if off > len(cards) {
		off = 0
	}
	end := off + vis
	if end > len(cards) {
		end = len(cards)
	}
	var rows []string
	for i := off; i < end; i++ {
		iss := cards[i]
		handler := hs[iss.Number].title
		if !hs[iss.Number].live || handler == "" {
			handler = "—"
		}
		sel := focused && i == m.issuesIdx[idx]
		rows = append(rows, issueCard(iss.Number, handler, width, sel))
	}
	if len(cards) > end {
		rows = append(rows, mutedStyle.Width(width).Align(lipgloss.Center).Render(m.tr("issues.clipped", "↕ more")))
	}
	if len(rows) == 0 {
		rows = append(rows, mutedStyle.Width(width).Align(lipgloss.Center).Render("·"))
	}
	return lipgloss.JoinVertical(lipgloss.Left, head, "", lipgloss.JoinVertical(lipgloss.Left, rows...))
}

func issueCard(num int, handler string, width int, selected bool) string {
	inner := width - 2
	if inner < 6 {
		inner = 6
	}
	top := fmt.Sprintf("#%d", num)
	bot := truncate(handler, inner)
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Width(inner)
	if selected {
		style = style.
			BorderForeground(colBrand).
			Foreground(colDark).
			Background(colBrand).
			Bold(true)
		top = "▸ " + top
	} else {
		style = style.BorderForeground(colMuted)
	}
	return style.Render(top + "\n" + bot)
}

func issueNumList(ns []int) string {
	parts := make([]string, 0, len(ns))
	for _, n := range ns {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("#%d", n))
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, ",")
}

func (m model) issuesDetailView(inner, bodyH int) string {
	c, ok := m.issuesSelected()
	if !ok {
		return mutedStyle.Render("—")
	}
	handler, _, live := m.issuesHandler(c.Number)
	if !live || handler == "" {
		handler = "—"
	}
	pull, hasPR := issues.PRFor(m.issuesBoard.Pulls, c.Number)
	col := issues.Classify(hasPR, live)
	pr := "—"
	if hasPR {
		pr = fmt.Sprintf("#%d", pull.Number)
		if pull.Draft {
			pr += " draft"
		}
	}
	parent, blocked, sub := "—", "—", "—"
	if c.Parent > 0 {
		parent = fmt.Sprintf("#%d", c.Parent)
	}
	if len(c.BlockedBy) > 0 {
		blocked = issueNumList(c.BlockedBy)
	}
	if len(c.Sub) > 0 {
		sub = issueNumList(c.Sub)
	}
	meta := fmt.Sprintf(m.tr("issues.detail.meta", "#%d    %s\n%s   ·  %s  ·  %s\nparent  %s     PR %s     blocked-by  %s     sub  %s"),
		c.Number, truncate(c.Title, inner-10),
		col.String(), handler, m.issuesBoard.Branch, parent, pr, blocked, sub)
	body := strings.TrimSpace(c.Body)
	if body == "" {
		body = mutedStyle.Render("—")
	} else {
		lines := strings.Split(body, "\n")
		max := bodyH - lipgloss.Height(meta) - 1
		if max < 3 {
			max = 3
		}
		if len(lines) > max {
			lines = append(lines[:max-1], "…")
		}
		body = strings.Join(lines, "\n")
	}
	return lipgloss.JoinVertical(lipgloss.Left, inkStyle.Render(meta), "", body)
}
