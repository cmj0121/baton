package tui

import (
	"strings"
	"testing"
)

// The spawn keys must not borrow a word that names a panel KIND they do not
// create. "command" is spoken for twice — it is proto.KindCommand on the wire,
// and it is `ctl queue add --command`, the agent binary to provision — and
// keyNewForm ("n c") creates neither: it sends KindShell with a Path, so what
// comes back is a shell panel running a program you named.
//
// This is pinned because nothing pinned it. The label read "command" from before
// the third kind existed and simply kept the word once it did, which is how the
// key list came to answer "which key opens a command panel" with the one key that
// cannot. The CLI made the same call and got it right — cmd/baton/ctl.go renames
// its flag to --run and says why — so the rule is worth holding here too.
//
// It is deliberately about the FORBIDDEN token rather than the current wording: a
// test asserting the exact new sentence would fail on any honest rephrasing and
// pass on any dishonest one that avoided that string.
func TestNoSpawnKeyBorrowsTheCommandKindsName(t *testing.T) {
	// Taken by the command kind (proto.KindCommand, and ctl's --run for it), so a
	// binding that does not create one must not spend either word on itself.
	spoken := []string{"command", "--run"}

	for _, b := range bindings {
		if b.act != actNewForm {
			continue
		}
		for _, w := range spoken {
			if strings.Contains(strings.ToLower(b.desc), w) {
				t.Errorf("binding %q desc %q uses %q, a word the command kind owns; it spawns a shell panel", b.name, b.desc, w)
			}
			if strings.Contains(strings.ToLower(b.short), w) {
				t.Errorf("binding %q short label %q uses %q, a word the command kind owns; it spawns a shell panel", b.name, b.short, w)
			}
		}
		if b.short == "" || b.desc == "" {
			t.Fatalf("binding %q lost its label entirely, which would pass the check above for the wrong reason", b.name)
		}
		return
	}
	t.Fatalf("no binding for actNewForm; this test guards a key that no longer exists")
}
