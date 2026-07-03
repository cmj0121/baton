package panel

import "testing"

func TestGroupParentLeafTop(t *testing.T) {
	cases := []struct{ path, parent, leaf, top string }{
		{"", "", "", ""},
		{"backend", "", "backend", "backend"},
		{"backend/api", "backend", "api", "backend"},
		{"backend/api/x", "backend/api", "x", "backend"},
	}
	for _, c := range cases {
		if got := GroupParent(c.path); got != c.parent {
			t.Errorf("GroupParent(%q) = %q, want %q", c.path, got, c.parent)
		}
		if got := GroupLeaf(c.path); got != c.leaf {
			t.Errorf("GroupLeaf(%q) = %q, want %q", c.path, got, c.leaf)
		}
		if got := GroupTop(c.path); got != c.top {
			t.Errorf("GroupTop(%q) = %q, want %q", c.path, got, c.top)
		}
	}
}

func TestGroupIsUnder(t *testing.T) {
	cases := []struct {
		anc, p string
		want   bool
	}{
		{"backend", "backend", true},       // self is in its own subtree
		{"backend", "backend/api", true},   // descendant
		{"backend", "backend/api/x", true}, // deep descendant
		{"backend", "back", false},         // prefix but not a path boundary
		{"backend", "backendish", false},   // not separated by '/'
		{"backend", "other", false},        // unrelated
		{"backend/api", "backend", false},  // ancestor is not under its descendant
		{"", "backend", false},             // the empty ancestor roots nothing
	}
	for _, c := range cases {
		if got := GroupIsUnder(c.anc, c.p); got != c.want {
			t.Errorf("GroupIsUnder(%q,%q) = %v, want %v", c.anc, c.p, got, c.want)
		}
	}
}

func TestGroupChildSegment(t *testing.T) {
	cases := []struct {
		path, parent, seg string
		ok                bool
	}{
		{"backend/api/x", "backend", "api", true},
		{"backend/api", "backend", "api", true},
		{"backend/api", "", "backend", true}, // top-level child of the root
		{"backend", "", "backend", true},
		{"backend", "backend", "", false},   // not a strict descendant
		{"other/api", "backend", "", false}, // different subtree
	}
	for _, c := range cases {
		seg, ok := GroupChildSegment(c.path, c.parent)
		if seg != c.seg || ok != c.ok {
			t.Errorf("GroupChildSegment(%q,%q) = (%q,%v), want (%q,%v)", c.path, c.parent, seg, ok, c.seg, c.ok)
		}
	}
}

func TestGroupJoinValid(t *testing.T) {
	if got := GroupJoin("", "backend"); got != "backend" {
		t.Errorf("GroupJoin top = %q", got)
	}
	if got := GroupJoin("backend", "api"); got != "backend/api" {
		t.Errorf("GroupJoin nested = %q", got)
	}
	valid := []string{"backend", "backend/api", "a/b/c"}
	invalid := []string{"", "/backend", "backend/", "backend//api", "/"}
	for _, p := range valid {
		if !GroupValid(p) {
			t.Errorf("GroupValid(%q) = false, want true", p)
		}
	}
	for _, p := range invalid {
		if GroupValid(p) {
			t.Errorf("GroupValid(%q) = true, want false", p)
		}
	}
}
