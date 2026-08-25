package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmj0121/baton/internal/panel"
)

// pickerModel opens the workdir picker over a tree the test owns, with a fleet
// whose panels are working in two of its directories.
func pickerModel(t *testing.T) (model, string) {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"api", "baton", "scratch", ".git"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	m := model{
		mode:  modeDashboard,
		input: inputAgentDir,
		fleet: []panel.Panel{
			{ID: "1", Kind: panel.Agent, State: panel.Running, Cwd: filepath.Join(root, "baton")},
			{ID: "2", Kind: panel.Agent, State: panel.Idle, Cwd: filepath.Join(root, "baton")},
			{ID: "3", Kind: panel.Shell, State: panel.Running, Cwd: filepath.Join(root, "api")},
			{ID: "c", Kind: panel.Agent, State: panel.Running, Cwd: "/tmp/ws", Conductor: true},
			{ID: "g", Kind: panel.Shell, State: panel.Running, Cwd: "/home/nobody", GlobalShell: true},
			{ID: "4", Kind: panel.Agent, State: panel.Running}, // no cwd reported yet
		},
		inputBuf: root,
	}
	return m.openDirPicker(modeDashboard), root
}

// dirPickLabels is the picker's rows as plain strings, for asserting shape.
func dirPickLabels(m model) []string {
	out := make([]string, 0, len(m.dirPickRows))
	for _, r := range m.dirPickRows {
		out = append(out, r.label)
	}
	return out
}

// TestDirPickerOpensOnTheTypedDirectory checks C-o lands in what the field
// already names and takes the keyboard from the prompt.
func TestDirPickerOpensOnTheTypedDirectory(t *testing.T) {
	m, root := pickerModel(t)
	if m.mode != modeDirPick {
		t.Fatalf("mode = %v, want modeDirPick", m.mode)
	}
	if m.input != inputNone {
		t.Fatalf("the picker should own the keyboard, input = %v", m.input)
	}
	if m.dirPickDir != root {
		t.Fatalf("browsing %q, want the typed directory %q", m.dirPickDir, root)
	}
}

// TestDirPickerListsDirectoriesOnly checks a workdir picker offers only things
// that can be a workdir, and hides dot directories until asked.
func TestDirPickerListsDirectoriesOnly(t *testing.T) {
	m, _ := pickerModel(t)
	labels := strings.Join(dirPickLabels(m), " ")
	if strings.Contains(labels, "notes.md") {
		t.Fatalf("a file cannot be a workdir, got rows: %s", labels)
	}
	if !strings.Contains(labels, "baton/") || !strings.Contains(labels, "scratch/") {
		t.Fatalf("the subdirectories should be listed, got rows: %s", labels)
	}
	if strings.Contains(labels, ".git/") {
		t.Fatalf("dot directories are hidden until asked, got rows: %s", labels)
	}

	m = press(m, keyDirPickHidden)
	if !strings.Contains(strings.Join(dirPickLabels(m), " "), ".git/") {
		t.Fatal("the hidden toggle should reveal dot directories")
	}
}

// TestDirPickerInUseSection checks the shortcut that makes this a baton picker:
// the directories the fleet is already working in, busiest first, with the two
// singletons and the panels that have reported no directory left out.
func TestDirPickerInUseSection(t *testing.T) {
	m, root := pickerModel(t)
	used := m.inUseDirs()
	if len(used) != 2 {
		t.Fatalf("in-use dirs = %v, want the two the fleet is working in", dirPickLabels(model{dirPickRows: used}))
	}
	if used[0].path != filepath.Join(root, "baton") || used[0].panels != 2 {
		t.Fatalf("busiest first: got %q with %d panel(s)", used[0].path, used[0].panels)
	}
	if used[1].path != filepath.Join(root, "api") || used[1].panels != 1 {
		t.Fatalf("second row: got %q with %d panel(s)", used[1].path, used[1].panels)
	}
	for _, r := range used {
		if strings.Contains(r.path, "/tmp/ws") || strings.Contains(r.path, "nobody") {
			t.Fatalf("the conductor and the global shell are not places the fleet works: %q", r.path)
		}
	}
}

// TestDirPickerWalksTheTree checks → descends, ← climbs, and the cursor never
// rests on a section header.
func TestDirPickerWalksTheTree(t *testing.T) {
	m, root := pickerModel(t)
	for m.dirPickRows[m.dirPickCursor].label != "baton/" {
		next := m.moveDirPick(1)
		if next.dirPickCursor == m.dirPickCursor {
			t.Fatal("never reached the baton/ row")
		}
		m = next
	}
	m = press(m, "right")
	if m.dirPickDir != filepath.Join(root, "baton") {
		t.Fatalf("→ should descend, browsing %q", m.dirPickDir)
	}
	if r := m.dirPickRows[m.dirPickCursor]; !r.selectable() {
		t.Fatalf("the cursor landed on an unselectable row %q", r.label)
	}
	m = press(m, "left")
	if m.dirPickDir != root {
		t.Fatalf("← should climb, browsing %q", m.dirPickDir)
	}
}

// TestDirPickerFillsTheField checks the pick lands in the prompt rather than
// spawning: the field carries the path, the prompt has the keyboard back, and the
// user can still edit before enter.
func TestDirPickerFillsTheField(t *testing.T) {
	m, root := pickerModel(t)
	target := filepath.Join(root, "scratch")
	for m.dirPickRows[m.dirPickCursor].path != target {
		next := m.moveDirPick(1)
		if next.dirPickCursor == m.dirPickCursor {
			t.Fatal("never reached the scratch/ row")
		}
		m = next
	}
	m = press(m, "enter")

	if m.mode != modeDashboard || m.input != inputAgentDir {
		t.Fatalf("the prompt should be back: mode %v, input %v", m.mode, m.input)
	}
	if expandDir(m.inputBuf) != target {
		t.Fatalf("field = %q, want the picked directory %q", m.inputBuf, target)
	}
}

// TestDirPickerEscapeKeepsTheTypedPath checks a cancel is a cancel: whatever was
// typed before the picker opened is still there.
func TestDirPickerEscapeKeepsTheTypedPath(t *testing.T) {
	m, root := pickerModel(t)
	m = press(m, "esc")
	if m.input != inputAgentDir {
		t.Fatalf("esc should return to the prompt, input = %v", m.input)
	}
	if m.inputBuf != root {
		t.Fatalf("esc rewrote the field to %q", m.inputBuf)
	}
}

// TestDirPickerFilter checks the filter narrows the listing as it is typed and
// esc drops it.
func TestDirPickerFilter(t *testing.T) {
	m, _ := pickerModel(t)
	m = press(m, keyDirPickFilter)
	if !m.dirPickTyping {
		t.Fatal("/ should open the filter field")
	}
	m = press(m, "s", "c")
	labels := strings.Join(dirPickLabels(m), " ")
	if !strings.Contains(labels, "scratch/") || strings.Contains(labels, "baton/") {
		t.Fatalf("the filter should narrow to scratch/, got rows: %s", labels)
	}
	m = press(m, "esc")
	if m.dirPickTyping || m.dirPickFilter != "" {
		t.Fatal("esc should drop the filter")
	}
	if !strings.Contains(strings.Join(dirPickLabels(m), " "), "baton/") {
		t.Fatal("dropping the filter should restore the full listing")
	}
}

// TestDirPickerOnlyForDirectoryPrompts checks C-o is wired to the workdir prompt
// and to nothing that edits a path to an executable.
func TestDirPickerOnlyForDirectoryPrompts(t *testing.T) {
	if !inputIsDir(inputAgentDir) {
		t.Fatal("the workdir prompt should offer the picker")
	}
	for _, p := range []inputPurpose{inputNewPanelCmd, inputShellPath, inputRename} {
		if inputIsDir(p) {
			t.Fatalf("%v edits something that is not a directory", p)
		}
	}

	m := model{mode: modeDashboard, input: inputNewPanelCmd, inputBuf: "/bin/ba"}
	next, _ := m.handleInput(tea.KeyMsg{Type: tea.KeyCtrlO})
	if next.(model).mode == modeDirPick {
		t.Fatal("a command prompt should not open a directory picker")
	}
}

// TestDirPickerView checks the overlay renders both sections and the legend, so a
// wiring mistake shows up as a failing test rather than as a blank popup.
func TestDirPickerView(t *testing.T) {
	m, _ := pickerModel(t)
	m.width, m.height = 100, 40
	out := m.dirPickView()
	for _, want := range []string{"W O R K D I R", "I N   U S E", "B R O W S E", "panel(s)", "pick"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the picker view should show %q, got:\n%s", want, out)
		}
	}
}
