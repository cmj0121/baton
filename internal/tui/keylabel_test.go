package tui

import (
	"strings"
	"testing"
)

// keyNewForm ("n c") is the one spawn key that opens two kinds: enter on the
// empty box spawns a shell panel, and a program typed into it spawns
// proto.KindCommand with that program and its arguments (#84).
//
// This is #85's rule, turned around rather than dropped. #85 pinned the label
// against the word "command" because the key could not create one, so the key
// list answered "which key opens a command panel" with the one key that could
// not — a user asked exactly that and got the wrong key. The key can now, and a
// label naming only the shell tells the same lie from the other side: the
// operator who wants `make test` to hold when it finishes reads past the one key
// that does it. So both kinds it spawns must be named.
//
// The two words it must NOT spend are the ones it still does not own. "agent" is
// a kind this key never creates — that is `A` — and "--run" is ctl's flag for the
// same job on a different surface, which an operator reading a key list has no
// way to type. Naming either sends someone to the wrong place, which is the
// failure #85 was written against.
//
// The forbidden half is a token check so an honest rephrasing stays green. The
// required half cannot be: the whole claim is that the label says which kinds
// this key makes, and there is no wording of that which omits their names.
func TestNewFormLabelNamesBothKindsItSpawns(t *testing.T) {
	for _, b := range bindings {
		if b.act != actNewForm {
			continue
		}
		desc, short := strings.ToLower(b.desc), strings.ToLower(b.short)

		for _, w := range []string{"shell", "command"} {
			if !strings.Contains(desc, w) {
				t.Errorf("binding %q desc %q never says %q, one of the two kinds this key spawns", b.name, b.desc, w)
			}
		}
		for _, w := range []string{"agent", "--run"} {
			if strings.Contains(desc, w) {
				t.Errorf("binding %q desc %q uses %q, which this key neither spawns nor offers", b.name, b.desc, w)
			}
			if strings.Contains(short, w) {
				t.Errorf("binding %q short label %q uses %q, which this key neither spawns nor offers", b.name, b.short, w)
			}
		}
		// The desc checks above already fail on an empty desc; the short label has
		// nothing holding it, and an empty one would slip past the forbidden-token
		// check for the wrong reason.
		if b.short == "" {
			t.Fatalf("binding %q lost its short label, leaving its column in the key list blank", b.name)
		}
		return
	}
	t.Fatalf("no binding for actNewForm; this test guards a key that no longer exists")
}
