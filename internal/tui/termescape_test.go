package tui

import (
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
