package tui

import (
	"strings"
	"testing"
)

// bindsOf builds a throwaway key map from "name=key" pairs, so a case reads as
// the map it is testing rather than as a struct literal.
func bindsOf(pairs ...string) []binding {
	out := make([]binding, 0, len(pairs))
	for _, p := range pairs {
		name, key, _ := strings.Cut(p, "=")
		out = append(out, binding{name: name, key: key, desc: name})
	}
	return out
}

func TestTokAndNormSeq(t *testing.T) {
	cases := []struct{ in, tok, norm string }{
		{" ", "space", "space"},
		{"p", "p", "p"},
		{"ctrl+t", "ctrl+t", "ctrl+t"},
		{"", "", ""},
		{"g  c", "g  c", "g c"},
		{" g c ", " g c ", "g c"},
	}
	for _, c := range cases {
		if got := tok(c.in); got != c.tok {
			t.Errorf("tok(%q) = %q, want %q", c.in, got, c.tok)
		}
		if got := normSeq(c.in); got != c.norm {
			t.Errorf("normSeq(%q) = %q, want %q", c.in, got, c.norm)
		}
	}
}

func TestSeqLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"p", "p"},
		{"g c", "g c"},
		{"ctrl+t", "C-t"},
		{"ctrl+t d", "C-t d"},
		{" ", "space"},
		{"alt+x", "M-x"},
	}
	for _, c := range cases {
		if got := seqLabel(c.in); got != c.want {
			t.Errorf("seqLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMatchSeq(t *testing.T) {
	binds := bindsOf("new=p", "mark=g g", "group=g c", "add=g a")
	cases := []struct {
		tokens []string
		want   seqMatch
		name   string
	}{
		{[]string{"p"}, seqExact, "new"},
		{[]string{"g"}, seqPartial, ""},
		{[]string{"g", "g"}, seqExact, "mark"},
		{[]string{"g", "c"}, seqExact, "group"},
		{[]string{"g", "z"}, seqNone, ""},
		{[]string{"z"}, seqNone, ""},
		{[]string{}, seqNone, ""},
	}
	for _, c := range cases {
		b, m := matchSeq(binds, c.tokens, nil)
		if m != c.want {
			t.Errorf("matchSeq(%v) = %v, want %v", c.tokens, m, c.want)
		}
		if b.name != c.name {
			t.Errorf("matchSeq(%v) bound %q, want %q", c.tokens, b.name, c.name)
		}
	}
}

// A binding that is the strict start of another cannot fire on its last key —
// it has to wait, because the next key may still extend it. This is the only
// shape that ever consults the timeout.
func TestMatchSeqAmbiguous(t *testing.T) {
	binds := bindsOf("add=a", "add-all=a a")

	if b, m := matchSeq(binds, []string{"a"}, nil); m != seqExactPartial || b.name != "add" {
		t.Fatalf("matchSeq([a]) = %q/%v, want add/seqExactPartial", b.name, m)
	}
	if b, m := matchSeq(binds, []string{"a", "a"}, nil); m != seqExact || b.name != "add-all" {
		t.Fatalf("matchSeq([a a]) = %q/%v, want add-all/seqExact", b.name, m)
	}

	pairs := ambiguous(binds, nil)
	if len(pairs) != 1 || pairs[0][0].name != "add" || pairs[0][1].name != "add-all" {
		t.Fatalf("ambiguous() = %v, want one add/add-all pair", pairs)
	}
}

// A rebind can put two bindings on one key. The key list shows them in key-map
// order, so the matcher fires the one it showed first.
func TestMatchSeqDuplicateKeepsFirst(t *testing.T) {
	binds := bindsOf("group=z", "lens=z")
	if b, m := matchSeq(binds, []string{"z"}, nil); m != seqExact || b.name != "group" {
		t.Fatalf("matchSeq([z]) = %q/%v, want group/seqExact", b.name, m)
	}
}

func TestMatchSeqRespectsWant(t *testing.T) {
	binds := bindsOf("new=p", "dashboard=d")
	only := func(b binding) bool { return b.name == "new" }

	if _, m := matchSeq(binds, []string{"d"}, only); m != seqNone {
		t.Errorf("a filtered-out binding still matched")
	}
	if _, m := matchSeq(binds, []string{"p"}, only); m != seqExact {
		t.Errorf("the kept binding did not match")
	}
}

func TestSeqNext(t *testing.T) {
	binds := bindsOf("mark=g g", "group=g c", "add=g a", "new=p")

	got := seqNext(binds, []string{"g"}, nil)
	want := []string{"g", "c", "a"} // key-map order, not sorted
	if len(got) != len(want) {
		t.Fatalf("seqNext([g]) = %v, want %d entries", got, len(want))
	}
	for i, w := range want {
		if got[i].key != w {
			t.Errorf("seqNext([g])[%d].key = %q, want %q", i, got[i].key, w)
		}
		if got[i].b.name == "" {
			t.Errorf("seqNext([g])[%d] should name the binding it completes", i)
		}
	}

	if n := seqNext(binds, []string{"p"}, nil); len(n) != 0 {
		t.Errorf("seqNext on a complete binding = %v, want none", n)
	}
	if n := seqNext(binds, nil, nil); len(n) == 0 {
		t.Errorf("seqNext with nothing typed should list every first token")
	}
}

// A token that is only ever a landing carries no binding name, so the hint can
// show it as a further family rather than as an action.
func TestSeqNextNestedLanding(t *testing.T) {
	binds := bindsOf("deep=v x y", "flat=v u")

	got := seqNext(binds, []string{"v"}, nil)
	if len(got) != 2 {
		t.Fatalf("seqNext([v]) = %v, want 2 entries", got)
	}
	byKey := map[string]contin{}
	for _, c := range got {
		byKey[c.key] = c
	}
	if byKey["x"].b.name != "" {
		t.Errorf("a landing continuation should carry no binding name, got %q", byKey["x"].b.name)
	}
	if byKey["u"].b.name != "flat" {
		t.Errorf("a completing continuation should name its binding, got %q", byKey["u"].b.name)
	}
}

func TestALandingResolvesAsPartial(t *testing.T) {
	binds := bindsOf("mark=g g", "new=p")
	if _, m := matchSeq(binds, []string{"g"}, nil); m != seqPartial {
		t.Errorf("g opens a family and should resolve as a landing")
	}
	if _, m := matchSeq(binds, []string{"p"}, nil); m != seqExact {
		t.Errorf("p is a binding of its own and is not a landing")
	}
	if _, m := matchSeq(binds, []string{"z"}, nil); m != seqNone {
		t.Errorf("an unbound key is not a landing")
	}
}

// The invariant the whole design rests on: no shipped binding is the strict
// start of another, in either half of the map. That is what makes the timeout
// unreachable out of the box — a key either fires at once or is a landing that
// waits for a key the status bar has already named.
func TestDefaultKeyMapHasNoAmbiguity(t *testing.T) {
	for _, half := range []struct {
		name string
		want func(binding) bool
	}{
		{"commands", func(b binding) bool { return !isEscape(b.act) }},
		{"escapes", func(b binding) bool { return isEscape(b.act) }},
	} {
		if pairs := ambiguous(bindings, half.want); len(pairs) != 0 {
			for _, p := range pairs {
				t.Errorf("%s: %q (%s) is the start of %q (%s) — this costs a timeout",
					half.name, p[0].name, p[0].key, p[1].name, p[1].key)
			}
		}
	}
}

// Every binding must parse into at least one token: a blank key would match the
// empty run and fire on nothing.
func TestDefaultKeyMapTokenises(t *testing.T) {
	for _, b := range bindings {
		if len(b.seq()) == 0 {
			t.Errorf("binding %q has no key", b.name)
		}
	}
}
