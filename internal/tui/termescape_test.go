package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/cmj0121/baton/internal/proto"
)

// Terminal-escape injection through the text the fleet carries.
//
// A panel names itself (panel.rename), groups itself (panel.group) and is handed
// a brief, so a prompt-injected agent chooses strings that baton draws onto the
// operator's real terminal. `\x1b[2J\x1b[H` erases the screen and parks the cursor
// at the top, which is enough to forge a whole frame the operator reads as
// baton's own; `\x1b]0;…\a` retitles their window; U+202E renders what follows
// backwards, so a name can spell one command and read as another.
//
// Every case asserts on the BYTES a render actually emits, because an assertion
// that a scrubbed string equals a scrubbed string proves nothing. Each also names
// the legitimate text that must survive — a "filter" that returned the empty
// string would otherwise pass every one of them.

// evilName carries one escape of each shape a terminal acts on, wrapped in text
// that must survive: a CSI (erase + cursor home), an OSC (set window title, BEL
// terminated), and a bidi override.
const evilName = "api\x1b[2J\x1b[H\x1b]0;pwned\aworker\u202e"

// assertClean fails when a rendered surface still carries a rune a terminal would
// act on, and fails just as loudly when any of survives did not make it through.
// Newlines are exempt: a card and a preview pane are blocks of lines, and the
// row-shearing a newline could do inside one label is already gone by the time a
// block is assembled from labels.
func assertClean(t *testing.T, what, got string, survives ...string) {
	t.Helper()
	for _, r := range got {
		switch {
		case r == '\n':
		case r == 0x1b:
			t.Errorf("%s: an ESC survived the render: %q", what, got)
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			t.Errorf("%s: %U survived the render: %q", what, r, got)
		}
	}
	for _, want := range survives {
		if !strings.Contains(got, want) {
			t.Errorf("%s: %q was lost, so the assertion above proves nothing: %q", what, want, got)
		}
	}
}

// assertNoInjected is assertClean for a surface baton styles ITSELF: lipgloss
// emits its own reset when it pads a column, so "no escape at all" cannot be the
// assertion there without asserting that baton stopped styling. The escapes the
// attacker supplied are named instead — they are exactly evilName's payloads, so
// a filter that stopped working still fails here.
func assertNoInjected(t *testing.T, what, got string, survives ...string) {
	t.Helper()
	for _, bad := range []string{"\x1b[2J", "\x1b[H", "\x1b]0;", "\a", "\u202e"} {
		if strings.Contains(got, bad) {
			t.Errorf("%s: %q survived the render: %q", what, bad, got)
		}
	}
	for _, want := range survives {
		if !strings.Contains(got, want) {
			t.Errorf("%s: %q was lost, so the assertion above proves nothing: %q", what, want, got)
		}
	}
}

// A hostile panel title reaches four surfaces, and every one draws it straight to
// the terminal. They are asserted together because the snapshot is the single
// place that makes all four safe; a per-surface filter is the list that is
// incomplete the moment a fifth surface is written.
func TestPanelTitleEscapeNeverReachesTheScreen(t *testing.T) {
	m := wired([]proto.Panel{{ID: "1", Kind: proto.KindAgent, Title: evilName, State: "running"}})
	m.width, m.height = 200, 60

	it := itemForPanel(t, m, "1")
	assertClean(t, "tree row", m.rowOf(it), "api", "worker")
	assertClean(t, "card", m.renderCard(it, false), "api")
	m.cursorOnPanel(t, "1")
	assertClean(t, "preview pane", m.renderPreview(m.dashItems(), 80), "api", "worker")

	z := m.zoomInto(it.panel)
	assertClean(t, "zoom title", z.zoomTitle, "api", "worker")
}

// A group name is the other label an agent picks, and it is drawn on the group's
// own row rather than on any member's.
func TestGroupNameEscapeNeverReachesTheScreen(t *testing.T) {
	m := wired([]proto.Panel{{ID: "1", Kind: proto.KindAgent, Title: "ok", State: "running", Group: evilName}})
	m.width, m.height = 200, 60

	var rows int
	for _, it := range m.dashItems() {
		if it.kind != itemGroup {
			continue
		}
		rows++
		assertClean(t, "group row", m.rowOf(it), "api", "worker")
		assertClean(t, "group card", m.renderGroupCard(it, false), "api")
	}
	if rows == 0 {
		t.Fatal("no group row was rendered, so nothing was asserted")
	}
}

// The brief a panel was dispatched headlines its row and its card, and the
// conductor agent is what writes it — the same wire, the same trust.
func TestTaskEscapeNeverReachesTheScreen(t *testing.T) {
	m := wired([]proto.Panel{{ID: "1", Kind: proto.KindAgent, Title: "ok", State: "running", Task: evilName}})
	m.width, m.height = 200, 60

	it := itemForPanel(t, m, "1")
	assertClean(t, "tree row tail", m.rowOf(it), "api", "worker")
	assertClean(t, "card foot", m.renderCard(it, false), "api")
}

// A working directory is chosen by whoever can call mkdir, and Cwd is read off
// the LIVE process rather than off what the panel was launched with — so an agent
// that cd's into a directory it just named puts the escape on the dashboard
// without renaming anything.
//
// It is filtered where it is DRAWN rather than on the snapshot, because Cwd is the
// one field the cockpit sends back: "open a shell here" spawns in it, and folding
// the double space out of a real directory name would spawn in the wrong place.
// The exactness of the stored value is asserted alongside, so moving the filter
// into mergeFleet fails here rather than silently.
func TestCwdEscapeIsFilteredAtTheRenderNotOnTheSnapshot(t *testing.T) {
	dir := "/tmp/" + evilName
	m := wired([]proto.Panel{{ID: "1", Kind: proto.KindAgent, Title: "ok", State: "running", Cwd: dir}})
	m.width, m.height = 200, 60

	it := itemForPanel(t, m, "1")
	assertClean(t, "tree row dir", m.rowOf(it), "api")
	assertClean(t, "card dir", m.renderCard(it, false), "api")

	if it.panel.Cwd != dir {
		t.Errorf("the snapshot rewrote Cwd to %q; spawn-here would land in the wrong directory", it.panel.Cwd)
	}
}

// A profile name becomes a group heading under the profile lens, so it is drawn
// like any other label and is scrubbed like one.
func TestProfileEscapeNeverReachesTheScreen(t *testing.T) {
	fleet := mergeFleet([]proto.Panel{{ID: "1", Kind: proto.KindAgent, Title: "ok", State: "running", Profile: evilName}})
	assertClean(t, "profile", fleet[0].Profile, "api", "worker")
}

// A label is one line by construction, so a newline or a tab inside one folds to a
// space rather than shearing the row it is drawn in. This is the property that
// picks internal/scrub over sanitizeText, which keeps a tab on purpose.
func TestLabelWhitespaceFoldsToOneLine(t *testing.T) {
	fleet := mergeFleet([]proto.Panel{{ID: "1", Kind: proto.KindAgent, Title: "api\tbuild\nstep", State: "running"}})
	if got := fleet[0].Title; got != "api build step" {
		t.Errorf("title = %q, want the folded single line", got)
	}
}

// The daemon's error reply is the other way agent-chosen text reaches the
// operator: a failed git push folds git's own stderr into the message, a rename
// clash echoes the name, and applyEvent puts the lot in the status line. The
// footer draws m.status in exactly two places, which is why the filter is there
// and not on the forty-odd assignments that compose it.
func TestServerErrorEscapeNeverReachesTheFooter(t *testing.T) {
	m := wired(nil)
	m.width, m.height = 200, 60
	m.applyEvent(proto.ServerMsg{Type: "error", Error: evilName})

	assertClean(t, "status bar", m.statusBar("", ""), "api", "worker")

	m.gitConfirmOp = "push"
	assertClean(t, "git confirm popup", m.gitPickerView(), "api", "worker")
}

// A multi-line error — which git's stderr routinely is — folds to one line, so a
// long push failure is clipped by the footer's budget instead of spilling a second
// row over the frame.
func TestFooterFoldsAMultiLineError(t *testing.T) {
	m := wired(nil)
	m.applyEvent(proto.ServerMsg{Type: "error", Error: "fatal: refusing\nto push\n"})

	if got := m.statusText(); got != "error: fatal: refusing to push" {
		t.Errorf("statusText = %q, want the folded single line", got)
	}
}

// A plugin sets the footer segment from whatever it was watching, which is agent
// output often enough, and it lands in the same one-line strip.
func TestPluginFooterEscapeNeverReachesTheStrip(t *testing.T) {
	m := wired(nil)
	m.pluginFooter = evilName

	assertClean(t, "plugin footer cap", m.pluginFooterCap(), "api")
}

// The workdir picker lists real directories, and a directory name is chosen by
// whoever can call mkdir — which inside a panel is the agent. The name is drawn
// with no filter of its own, so `mkdir $'\e[2J'` beside a repo puts an escape in
// the picker the operator opens to choose where the next agent runs.
//
// The row's path is asserted untouched alongside: it is what the pick sends, so a
// filter that rewrote it would spawn in a directory that does not exist.
func TestDirPickerDrawsNoEscapeFromADirectoryName(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, evilName), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	m := wired(nil).browseDir(root)
	assertNoInjected(t, "workdir picker", m.dirPickView(), "api")

	var found bool
	for _, r := range m.dirPickRows {
		if r.path == filepath.Join(root, evilName) {
			found = true
		}
	}
	if !found {
		t.Error("the row's path was rewritten; the pick would spawn in a directory that does not exist")
	}
}
