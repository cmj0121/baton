package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/proto"
)

// The git menu (C-t g in a zoom): a keyed picker of git operations run against the
// zoomed agent panel — the same modal pop-up shape as the signal picker. It is
// zoom-only by design: you act on the one agent you are looking at. Output ops
// (diff/log/status/add/commit/push/branch/worktree-list) open a transient,
// auto-zoomed panel via the server's ephemeral path; worktree-add spawns an agent
// in a fresh tree (a fleet change); worktree-remove confirms and runs server-side.

// keyGitMenu opens the menu after the leader in a zoom (C-t g). It is a fixed zoom
// affordance rather than a rebindable command, since bare g is the dashboard's
// mark and the menu only exists in a zoom.
const keyGitMenu = "g"

// gitMenu is the menu's rows, in display order — the keycap, the label, and a
// one-line gloss. The hotkey runs the row directly; ↑↓ + enter pick it.
var gitMenu = []struct{ key, label, desc string }{
	{"d", "diff", "working tree vs HEAD"},
	{"l", "log", "recent commits, graphed"},
	{"s", "status", "working-tree status"},
	{"a", "stage all", "git add -A"},
	{"c", "commit", "stage all, then $EDITOR"},
	{"p", "push", "git push  (confirm)"},
	{"b", "branch", "create and switch to a new branch"},
	{"w", "worktree", "new worktree on a branch + an agent in it"},
	{"W", "worktrees", "list the repo's worktrees"},
	{"x", "rm worktree", "remove a worktree by path  (confirm)"},
}

// openGitPicker opens the menu for the zoomed agent panel, remembering the zoom to
// return to. It is agent-only and not available on a transient (diff/git) view.
func (m model) openGitPicker() (tea.Model, tea.Cmd) {
	if m.zoomEphemeral {
		m.status = "git: not available on this view"
		return m, nil
	}
	p, ok := m.fleetPanel(m.zoomID)
	if !ok || !p.IsAgent() {
		m.status = "git: available on agent panels"
		return m, nil
	}
	m.gitFrom = m.mode
	m.gitTarget = p
	m.gitCursor = 0
	m.gitConfirm, m.gitConfirmOp, m.gitRemovePath = false, "", ""
	m.mode = modeGit
	m.status = "git · " + p.Title + " · pick an action · esc cancels"
	return m, nil
}

// handleGitKey drives the menu: a pending confirm (push / remove) answers y/n
// first; otherwise a hotkey (or enter on the cursor row) runs that op, ↑↓ (j/k)
// move, esc cancels. Any other key is ignored so a stray press never fires.
func (m model) handleGitKey(key string) (tea.Model, tea.Cmd) {
	if m.gitConfirm {
		m.gitConfirm = false
		if key == "y" || key == "enter" {
			return m.runGitConfirmed()
		}
		m.mode = m.gitFrom
		m.status = "git: cancelled"
		return m, nil
	}
	switch key {
	case "esc":
		m.mode = m.gitFrom
		m.status = "git: cancelled"
		return m, nil
	case "up", "k":
		m.gitCursor = wrapIndex(m.gitCursor, -1, len(gitMenu))
		return m, nil
	case "down", "j":
		m.gitCursor = wrapIndex(m.gitCursor, 1, len(gitMenu))
		return m, nil
	case "enter":
		return m.runGitEntry(gitMenu[m.gitCursor].key)
	}
	for _, e := range gitMenu {
		if key == e.key {
			return m.runGitEntry(e.key)
		}
	}
	return m, nil
}

// runGitEntry acts on a menu row. The immediate ops fire now; the text ops open an
// input overlay; push and remove park a confirm.
func (m model) runGitEntry(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "d":
		// Reuse the diff path — it opens the same transient, auto-zoomed panel.
		m.mode = m.gitFrom
		m.requestDiff(m.gitTarget)
		return m, nil
	case "l":
		return m.sendGitEphemeral("log", "log", "")
	case "s":
		return m.sendGitEphemeral("status", "status", "")
	case "a":
		return m.sendGitEphemeral("add", "stage", "")
	case "c":
		return m.sendGitEphemeral("commit", "commit", "")
	case "W":
		return m.sendGitEphemeral("worktree-list", "worktrees", "")
	case "p":
		m.gitConfirm, m.gitConfirmOp = true, "push"
		m.status = "push " + m.gitTarget.Title + "? · (y/n)"
		return m, nil
	case "b":
		m.input, m.inputBuf = inputGitBranch, ""
		m.status = "new branch · type a name, enter creates"
		return m, nil
	case "w":
		m.input, m.inputBuf = inputGitWorktree, ""
		m.status = "new worktree + agent · type a branch, enter creates"
		return m, nil
	case "x":
		m.input, m.inputBuf = inputGitRemove, ""
		m.status = "remove worktree · type the path, enter then confirm"
		return m, nil
	}
	return m, nil
}

// sendGitEphemeral fires an output-producing op against the target and returns to
// the zoom; the server's "diff" reply auto-zooms the transient panel it spawns.
// label titles that zoom.
func (m model) sendGitEphemeral(op, label, arg string) (tea.Model, tea.Cmd) {
	m.pendingDiffTitle = "git " + label + " · " + m.gitTarget.Title
	m.sendf(proto.Command{Action: "panel.git", Git: op, ID: m.gitTarget.ID, Name: arg})
	m.mode = m.gitFrom
	m.status = "git " + label + " · " + m.gitTarget.Title
	return m, nil
}

// runGitConfirmed fires the op a confirm was parked on.
func (m model) runGitConfirmed() (tea.Model, tea.Cmd) {
	switch m.gitConfirmOp {
	case "push":
		return m.sendGitEphemeral("push", "push", "")
	case "remove":
		m.sendf(proto.Command{Action: "panel.git", Git: "worktree-remove", ID: m.gitTarget.ID, Dir: m.gitRemovePath})
		m.mode = m.gitFrom
		m.status = "removing worktree " + m.gitRemovePath
		return m, nil
	}
	m.mode = m.gitFrom
	return m, nil
}

// commitGitBranch creates a new branch from the typed name; an empty name reopens
// the field rather than firing an op with no name.
func (m model) commitGitBranch(name string) (tea.Model, tea.Cmd) {
	if name == "" {
		m.input, m.status = inputGitBranch, "new branch · a name is required"
		return m, nil
	}
	return m.sendGitEphemeral("branch", "branch "+name, name)
}

// commitGitWorktree creates a worktree on the typed branch and spawns an agent in
// it. It is a fleet change (no auto-zoom): the new agent appears on the dashboard,
// grouped under the branch.
func (m model) commitGitWorktree(branch string) (tea.Model, tea.Cmd) {
	if branch == "" {
		m.input, m.status = inputGitWorktree, "new worktree · a branch name is required"
		return m, nil
	}
	m.sendf(proto.Command{Action: "panel.git", Git: "worktree-add", ID: m.gitTarget.ID, Name: branch})
	m.mode = m.gitFrom
	m.status = "worktree + agent on " + branch
	return m, nil
}

// commitGitRemove takes the typed worktree path and parks a confirm, so the
// destructive step needs an explicit y.
func (m model) commitGitRemove(path string) (tea.Model, tea.Cmd) {
	if path == "" {
		m.input, m.status = inputGitRemove, "remove worktree · a path is required"
		return m, nil
	}
	m.mode = modeGit
	m.gitConfirm, m.gitConfirmOp, m.gitRemovePath = true, "remove", path
	m.status = "remove worktree " + path + "? · (y/n)"
	return m, nil
}

// gitPickerView renders the menu as a centred popup: the target panel, one
// keycap-led row per op, and a legend — or a confirm prompt when one is pending.
func (m model) gitPickerView() string {
	kc := func(s string) string { return keycapStyle.Render(s) }
	nameStyle := lipgloss.NewStyle().Foreground(colCyan).Bold(true).Width(12)
	keyCol := lipgloss.NewStyle().Width(4)
	caret := func(on bool) string {
		if on {
			return lipgloss.NewStyle().Foreground(colBrand).Bold(true).Render("▸ ")
		}
		return "  "
	}

	rows := []string{
		sectionStyle.Render(spaced("GIT")),
		"",
		mutedStyle.Render(m.gitTarget.Title),
		"",
	}
	for i, e := range gitMenu {
		rows = append(rows, caret(m.gitCursor == i)+keyCol.Render(kc(e.key))+nameStyle.Render(e.label)+mutedStyle.Render(e.desc))
	}

	legendKey := lipgloss.NewStyle().Foreground(colCyan).Bold(true)
	var legend string
	if m.gitConfirm {
		legend = legendKey.Render("y") + mutedStyle.Render(" confirm") + "   " +
			legendKey.Render("n") + mutedStyle.Render("/esc cancel")
		rows = append(rows, "", lipgloss.NewStyle().Foreground(colBrand).Bold(true).Render(m.status))
	} else {
		legend = legendKey.Render("↑↓") + mutedStyle.Render(" move") + "   " +
			legendKey.Render("enter") + mutedStyle.Render(" run") + "   " +
			legendKey.Render("esc") + mutedStyle.Render(" cancel")
	}
	rows = append(rows, "",
		mutedStyle.Render("acts on the zoomed agent · "+keyLabel(m.effPrefix())+" R reloads baton"),
		"", legend)
	return configBox(lipgloss.JoinVertical(lipgloss.Left, rows...))
}
