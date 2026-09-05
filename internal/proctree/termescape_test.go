package proctree

import (
	"strings"
	"testing"
	"unicode"

	"github.com/cmj0121/baton/internal/proto"
)

// `baton ctl tree` draws this tree straight to a terminal with fmt.Print, and
// three of the strings on a line are chosen by the thing being watched: a panel's
// title (an agent renames itself), its group (an agent groups itself), and the
// comm of a live process (an agent spawns whatever argv0 it likes). The --json
// form is safe because encoding/json escapes a control byte; the drawn form had
// nothing between the fleet and the operator's screen.
//
// evilName carries a CSI (erase + cursor home), an OSC (set window title, BEL
// terminated) and a bidi override, wrapped in text that must survive.
const evilName = "api\x1b[2J\x1b[H\x1b]0;pwned\aworker\u202e"

// assertNoEscape fails on the first rune a terminal would act on, and fails just
// as loudly when the legitimate text did not survive — an assertion that only
// looked for absence would pass on the empty string.
func assertNoEscape(t *testing.T, what, got string) {
	t.Helper()
	for _, r := range got {
		if r == '\n' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Errorf("%s: %U survived: %q", what, r, got)
			break
		}
	}
	if !strings.Contains(got, "api") || !strings.Contains(got, "worker") {
		t.Errorf("%s: the legitimate text was lost, so the check above proves nothing: %q", what, got)
	}
}

// A title, a group name and a process comm, all hostile, on one tree.
func TestRenderDrawsNoEscape(t *testing.T) {
	panels := []proto.Panel{{ID: "1", Title: evilName, State: "running", Group: evilName, Pid: 41180}}
	root := Build(41022, panels,
		map[int][]int{41022: {41180}, 41180: {41199}},
		map[int]string{41022: "baton", 41180: evilName, 41199: evilName},
		nil)

	assertNoEscape(t, "ctl tree", Render(root))
}

// LabelText is what the cockpit's overlay pairs with its own resource columns, so
// it carries the filter rather than Render — one function covers both surfaces
// that turn a node into a plaintext line.
func TestLabelTextDrawsNoEscape(t *testing.T) {
	for _, n := range []*Node{
		{Kind: KindGroup, Label: "[group: " + evilName + "]"},
		{Kind: KindProc, Pid: 7, Comm: evilName},
		{Kind: KindPanel, Label: "[" + evilName + "/running]", Pid: 7, Comm: evilName},
	} {
		assertNoEscape(t, string(n.Kind), LabelText(n))
	}
}
