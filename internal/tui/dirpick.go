package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The workdir picker (modeDirPick, C-o from the workdir prompt). The prompt it
// opens from is a text field with tab completion, and it stays that way: someone
// who knows the path types it and never sees this. The picker is for the other
// case — knowing the directory when you see it, not when you spell it.
//
// It lists two things. BROWSE is the filesystem, directories only, since a
// workdir is a directory. IN USE is above it: the directories the fleet's panels
// are already working in, with how many are there. That section is the reason
// this is a baton picker rather than a file dialog — spawning a second agent
// beside the first, on the same tree, is the move a multiplexer exists for, and
// the fleet snapshot already carries every panel's cwd, so the shortcut costs a
// walk over a list already in hand.
//
// Only directories are listed. A file cannot be a workdir, and showing one only
// to refuse it is a worse answer than not showing it.

// The picker's own keys, named rather than spelled inline. They are the overlay
// set the rest of the cockpit uses: jk/arrows move, esc closes.
const (
	keyDirPickHidden = "." // show / hide dot directories
	keyDirPickFilter = "/" // narrow the listing by substring
)

// dirRow is one line of the picker. A header is a section title and a caption is
// the browse path under it — neither can be selected, so the cursor steps over
// them and a stray enter can never pick one.
type dirRow struct {
	path    string // absolute directory this row stands for
	label   string // what the row shows
	panels  int    // live panels already working here (IN USE rows only)
	header  bool   // a section title
	caption bool   // the browsed directory shown under BROWSE
	up      bool   // the ".." row
}

// selectable reports whether the cursor may land on this row.
func (r dirRow) selectable() bool { return !r.header && !r.caption }

// openDirPicker opens the picker over the workdir prompt, remembering both the
// view to restore and the overlay to hand the chosen path back to. It starts in
// whatever the field already names — the directory itself when it exists, its
// parent when the user is midway through typing a leaf — so the picker opens
// where the typing left off instead of resetting to home.
func (m model) openDirPicker(from mode) model {
	m.dirPickFrom, m.dirPickInput = from, m.input
	m.input, m.inputHint = inputNone, "" // the picker owns the keyboard now
	m.dirPickHidden, m.dirPickFilter, m.dirPickTyping = false, "", false
	m.mode = modeDirPick
	return m.browseDir(startDir(m.inputBuf, m.defaultWorkdir()))
}

// startDir picks the directory a freshly opened picker should list: what the
// field names if that is a directory, else its parent, else the fallback. A
// half-typed leaf ("~/mylab/bat") lists ~/mylab, which is where its candidates
// are.
func startDir(typed, fallback string) string {
	for _, cand := range []string{typed, filepath.Dir(typed), fallback} {
		if cand == "" {
			continue
		}
		abs := expandDir(cand)
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
			return abs
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "/"
}

// browseDir points the browse half at dir and rebuilds the list, leaving the
// cursor on the first selectable row. An unreadable directory keeps the previous
// listing and says why, rather than dropping the user into an empty box.
func (m model) browseDir(dir string) model {
	entries, err := os.ReadDir(dir)
	if err != nil {
		m.status = "cannot read " + dirLabel(dir)
		return m
	}
	m.dirPickDir = dir
	m.dirPickRows = m.dirRows(entries)
	m.dirPickCursor = m.firstSelectable()
	m.status = m.dirPickLegend()
	return m
}

// dirRows builds the whole list: the IN USE section (when the fleet is working
// anywhere), then BROWSE with the current directory, its parent, and its
// subdirectories.
func (m model) dirRows(entries []os.DirEntry) []dirRow {
	var rows []dirRow
	if used := m.inUseDirs(); len(used) > 0 {
		rows = append(rows, dirRow{label: "IN USE", header: true})
		rows = append(rows, used...)
	}
	rows = append(rows,
		dirRow{label: "BROWSE", header: true},
		dirRow{label: dirLabel(m.dirPickDir), caption: true})
	if parent := filepath.Dir(m.dirPickDir); parent != m.dirPickDir {
		rows = append(rows, dirRow{path: parent, label: "..", up: true})
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue // a workdir is a directory
		}
		name := e.Name()
		if !m.dirPickHidden && strings.HasPrefix(name, ".") {
			continue
		}
		if f := m.dirPickFilter; f != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(f)) {
			continue
		}
		rows = append(rows, dirRow{path: filepath.Join(m.dirPickDir, name), label: name + "/"})
	}
	return rows
}

// inUseDirs is the IN USE section: one row per distinct directory the fleet's
// live panels are working in, busiest first, then by path so the order is stable
// between two directories with the same count.
//
// The two singletons are left out. The conductor's cwd is a workspace baton
// manages, not a tree anyone would spawn into, and the global shell opens in
// $HOME by definition — neither is a place the fleet is working.
func (m model) inUseDirs() []dirRow {
	counts := map[string]int{}
	for _, p := range m.fleet {
		if p.Conductor || p.GlobalShell || p.Cwd == "" {
			continue
		}
		counts[p.Cwd]++
	}
	rows := make([]dirRow, 0, len(counts))
	for dir, n := range counts {
		rows = append(rows, dirRow{path: dir, label: dirLabel(dir), panels: n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].panels != rows[j].panels {
			return rows[i].panels > rows[j].panels
		}
		return rows[i].path < rows[j].path
	})
	return rows
}

// firstSelectable is the index of the first row the cursor may sit on.
func (m model) firstSelectable() int {
	for i, r := range m.dirPickRows {
		if r.selectable() {
			return i
		}
	}
	return 0
}

// moveDirPick steps the cursor by delta, skipping headers and captions and
// stopping at the ends rather than wrapping: a list with two sections reads as a
// column, and wrapping from the last directory back up into IN USE loses the
// place more often than it saves a keystroke.
func (m model) moveDirPick(delta int) model {
	i := m.dirPickCursor
	for {
		i += delta
		if i < 0 || i >= len(m.dirPickRows) {
			return m // off the end: stay put
		}
		if m.dirPickRows[i].selectable() {
			m.dirPickCursor = i
			return m
		}
	}
}

// handleDirPickKey drives the picker. The filter takes the keyboard while it is
// open (any rune types into it), so its keys are read first.
func (m model) handleDirPickKey(key string, k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.dirPickTyping {
		return m.handleDirPickFilter(key, k)
	}

	switch key {
	case "esc", "q":
		return m.closeDirPick(""), nil
	case "up", "k":
		return m.moveDirPick(-1), nil
	case "down", "j":
		return m.moveDirPick(1), nil
	case "left", "h":
		if parent := filepath.Dir(m.dirPickDir); parent != m.dirPickDir {
			return m.browseDir(parent), nil
		}
		return m, nil
	case "right", "l":
		// Descend. On an IN USE row this moves the browse half to that directory
		// rather than picking it, so the shortcut is also a way in.
		if r, ok := m.dirRowUnderCursor(); ok && r.path != "" {
			return m.browseDir(r.path), nil
		}
		return m, nil
	case "enter":
		r, ok := m.dirRowUnderCursor()
		if !ok || r.path == "" {
			return m, nil
		}
		return m.closeDirPick(r.path), nil
	case keyDirPickFilter:
		m.dirPickTyping, m.dirPickFilter = true, ""
		m.status = "filter · type to narrow · enter keeps it · esc clears"
		return m, nil
	case keyDirPickHidden:
		m.dirPickHidden = !m.dirPickHidden
		return m.browseDir(m.dirPickDir), nil
	}
	return m, nil // stay in the picker on any other key
}

// handleDirPickFilter feeds the filter field: runes narrow the listing as they
// are typed, enter keeps the narrowed list and hands the keyboard back, esc drops
// the filter entirely.
func (m model) handleDirPickFilter(key string, k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyEsc:
		m.dirPickTyping, m.dirPickFilter = false, ""
		return m.browseDir(m.dirPickDir), nil
	case tea.KeyEnter:
		m.dirPickTyping = false
		m.status = m.dirPickLegend()
		return m, nil
	case tea.KeyBackspace:
		if r := []rune(m.dirPickFilter); len(r) > 0 {
			m.dirPickFilter = string(r[:len(r)-1])
		}
		return m.browseDir(m.dirPickDir), nil
	case tea.KeyRunes:
		if k.Alt {
			return m, nil
		}
		m.dirPickFilter += printableRunes(k.Runes)
		return m.browseDir(m.dirPickDir), nil
	}
	_ = key
	return m, nil
}

// dirRowUnderCursor is the row the cursor sits on, if it is one that can be used.
func (m model) dirRowUnderCursor() (dirRow, bool) {
	if m.dirPickCursor < 0 || m.dirPickCursor >= len(m.dirPickRows) {
		return dirRow{}, false
	}
	r := m.dirPickRows[m.dirPickCursor]
	return r, r.selectable()
}

// closeDirPick returns to the workdir prompt. A chosen path is written into the
// field (shortened with ~, the same shape the prompt is seeded with) and left
// editable — the pick fills the field, it does not spawn. An empty pick is a
// cancel and leaves whatever was typed alone.
func (m model) closeDirPick(pick string) model {
	m.mode, m.input = m.dirPickFrom, m.dirPickInput
	m.dirPickRows, m.dirPickTyping = nil, false
	if pick == "" {
		m.status = "workdir · cancelled the picker · enter spawns"
		return m
	}
	m.inputBuf = dirLabel(pick)
	m.status = "workdir · " + m.inputBuf + " · enter spawns"
	return m
}

// dirPickLegend is the status line for the picker's resting state.
func (m model) dirPickLegend() string {
	hidden := "show"
	if m.dirPickHidden {
		hidden = "hide"
	}
	return "workdir · jk move · → enter dir · ← up · ⏎ pick · " +
		keyDirPickFilter + " filter · " + keyDirPickHidden + " " + hidden + " dotdirs · esc closes"
}

// dirPickView renders the picker: the two sections, the cursor, and the legend.
func (m model) dirPickView() string {
	caret := func(on bool) string {
		if on {
			return lipgloss.NewStyle().Foreground(colBrand).Bold(true).Render("▸ ")
		}
		return "  "
	}
	nameCol := lipgloss.NewStyle().Width(38)

	rows := []string{sectionStyle.Render(spaced("WORKDIR")), ""}
	for i, r := range m.dirPickRows {
		switch {
		case r.header:
			rows = append(rows, "", sectionStyle.Render(spaced(r.label)))
		case r.caption:
			rows = append(rows, mutedStyle.Render("  "+r.label))
		case r.up:
			rows = append(rows, caret(m.dirPickCursor == i)+mutedStyle.Render(r.label))
		default:
			line := caret(m.dirPickCursor == i) + nameCol.Render(truncate(r.label, 36))
			if r.panels > 0 {
				line += mutedStyle.Render(fmt.Sprintf("%d panel(s)", r.panels))
			}
			rows = append(rows, line)
		}
	}
	if m.firstSelectable() == 0 && len(m.dirPickRows) > 0 && !m.dirPickRows[0].selectable() {
		rows = append(rows, "", mutedStyle.Render("no subdirectories here"))
	}

	rows = append(rows, "")
	if m.dirPickTyping {
		rows = append(rows,
			inkStyle.Render("filter  "+m.dirPickFilter+"▏"),
			"", legend("⏎", "keep", "esc", "clear"))
	} else {
		rows = append(rows, legend("jk", "move", "→", "enter", "←", "up", "⏎", "pick",
			keyDirPickFilter, "filter", keyDirPickHidden, "dotdirs", "esc", "close"))
	}
	return m.popupBox(lipgloss.JoinVertical(lipgloss.Left, rows...))
}
