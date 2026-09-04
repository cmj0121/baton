package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stateModel is a model wired to a state file inside the test's own temp dir, so
// nothing here can reach the real $HOME. The path is per-test rather than an
// environment override on purpose: a global another test reads is how a suite
// stops being repeatable.
func stateModel(t *testing.T) model {
	t.Helper()
	m := baseModel()
	m.viewStatePath = filepath.Join(t.TempDir(), "TUI.state.json")
	return m
}

// TestRememberedViewSurvivesTheSession is the whole issue in one assertion: press
// the two keys, throw the cockpit away, and open a new one on the same file.
func TestRememberedViewSurvivesTheSession(t *testing.T) {
	m := stateModel(t)
	m.mode = modeDashboard
	if m.showTree {
		t.Fatal("a fresh cockpit should start on the cards")
	}

	m = m.toggleLayout() // v l
	m = m.cycleLens(1)   // v g, work item -> directory
	if !m.showTree || m.lens != lensDir {
		t.Fatalf("setup: showTree=%v lens=%v, want true/directory", m.showTree, m.lens)
	}

	next := baseModel()
	next.viewStatePath = m.viewStatePath
	next = next.applyViewState(loadViewState(next.viewStatePath))

	if !next.showTree {
		t.Error("the tree was not remembered: the operator has to press v l every session, which is the bug")
	}
	if next.lens != lensDir {
		t.Errorf("lens = %v, want directory carried across the restart", next.lens)
	}
}

// TestOneKeystrokeRecordsOneOpinion is what the pointers are FOR, asserted
// rather than described.
//
// viewState's fields are pointers so that absent can mean "no opinion" — the
// operator never pressed this key, so a built-in default or a future config key
// for the layout still stands. The writer used to contradict that: it wrote both
// fields on either keystroke, so pressing `v l` recorded a lens the operator had
// never chosen, and the config key the pointer exists to protect would have been
// overridden by a file that was only recording an absence.
//
// So this reads the RAW JSON. Round-tripping through viewState cannot see the
// difference — a written lens and an unwritten one both load into a model that
// then agrees with itself — and the key's presence is the whole claim.
func TestOneKeystrokeRecordsOneOpinion(t *testing.T) {
	m := stateModel(t)
	m.mode = modeDashboard

	keys := func() map[string]any {
		t.Helper()
		data, err := os.ReadFile(m.viewStatePath)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal %s: %v", data, err)
		}
		return raw
	}

	m = m.toggleLayout() // v l, and nothing else
	got := keys()
	if _, ok := got["show_tree"]; !ok {
		t.Error("v l did not record the layout, so the rest of this proves nothing")
	}
	if _, ok := got["lens"]; ok {
		t.Error("v l recorded a lens the operator never chose; absent is what lets a config key win")
	}

	// The second keystroke ADDS to the file rather than replacing it: a merge that
	// dropped the earlier opinion would be the same bug pointing the other way.
	m = m.cycleLens(1) // v g
	got = keys()
	if _, ok := got["lens"]; !ok {
		t.Error("v g did not record the lens")
	}
	if _, ok := got["show_tree"]; !ok {
		t.Error("v g dropped the layout the operator had already chosen")
	}
}

// TestAdvisoryFileAlwaysLandsOnDefaultsSilently is the assertion the advisory
// contract actually rests on: every way this file can be unusable ends on the
// built-in defaults, and none of them says anything to the operator.
//
// The control case is what makes it worth running. Asserting "a corrupt file
// leaves showTree false" proves nothing on its own — a load path that was broken
// outright, or a path never read at all, passes every corrupt case and fails
// nobody. So each bad input is checked against a good one written to the SAME path
// through the SAME call, which flips both fields. If the control stops flipping
// them the test fails, and the corrupt cases stop being vacuous.
func TestAdvisoryFileAlwaysLandsOnDefaultsSilently(t *testing.T) {
	cases := []struct {
		name  string
		write func(t *testing.T, path string)
	}{
		{"absent", func(*testing.T, string) {}}, // first run: nothing on disk at all
		{"empty", func(t *testing.T, p string) { writeState(t, p, "") }},
		{"truncated", func(t *testing.T, p string) { writeState(t, p, `{"show_tree": tr`) }},
		{"garbage", func(t *testing.T, p string) { writeState(t, p, "\x00\xff not json at all") }},
		{"wrong shape", func(t *testing.T, p string) { writeState(t, p, `["show_tree", true]`) }},
		{"wrong field types", func(t *testing.T, p string) { writeState(t, p, `{"show_tree": "yes", "lens": 3}`) }},

		// The torn write the atomic save exists to prevent, in case it ever reaches
		// us anyway (a filesystem without an atomic rename, a restored backup).
		{"half-written", func(t *testing.T, p string) { writeState(t, p, `{"show_tree": true, "le`) }},

		// This one carries the weight, and it is the only case here that does.
		// json.Unmarshal validates the whole document before it decodes, so every
		// case above fails with NOTHING written into the struct — a loader that
		// wrongly returned its half-built value would still pass them all. A type
		// error is the exception: decoding gets as far as show_tree, sets it, and
		// only then fails. That is the one shape where "start over" and "keep what
		// landed" differ, so it is the one shape that can catch the difference.
		{"good field then bad", func(t *testing.T, p string) { writeState(t, p, `{"show_tree": true, "lens": 3}`) }},
		{"unreadable", func(t *testing.T, p string) {
			writeState(t, p, `{"show_tree": true}`)
			if err := os.Chmod(p, 0o000); err != nil {
				t.Fatalf("chmod: %v", err)
			}
		}},
		{"a directory", func(t *testing.T, p string) {
			if err := os.Mkdir(p, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "TUI.state.json")
			tc.write(t, path)

			before := baseModel()
			before.status = "attaching…"
			got := before.applyViewState(loadViewState(path))

			if got.showTree {
				t.Error("an unusable state file must leave the cards showing, not turn the tree on")
			}
			if got.lens != lensWork {
				t.Errorf("lens = %v, want the work-item default", got.lens)
			}
			if got.status != before.status {
				t.Errorf("status = %q, want it untouched (%q): a lost preference is not worth telling the operator about",
					got.status, before.status)
			}

			// The control: a good file at this same path must move both fields, or
			// every assertion above passes for the wrong reason.
			_ = os.Chmod(path, 0o600) // the unreadable case blocks its own replacement
			_ = os.RemoveAll(path)    // as does the directory
			writeState(t, path, `{"show_tree": true, "lens": "state"}`)
			ctl := before.applyViewState(loadViewState(path))
			if !ctl.showTree || ctl.lens != lensState {
				t.Fatalf("control: a readable file left showTree=%v lens=%v — the corrupt cases above prove nothing",
					ctl.showTree, ctl.lens)
			}
		})
	}
}

// TestAbsentFieldIsNoOpinionNotFalse is the pointer decision, asserted. A file
// that mentions only the lens must not also reset the layout to the default,
// because "the operator never pressed v l" and "the operator chose the cards" are
// different facts and a plain bool cannot hold both.
//
// It is what keeps a later config key for either setting from being silently
// overridden by a file that was only ever recording the absence of an opinion.
func TestAbsentFieldIsNoOpinionNotFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "TUI.state.json")
	writeState(t, path, `{"lens": "profile"}`)

	seeded := baseModel()
	seeded.showTree = true // stands in for a default, or a config key, saying "tree"

	got := seeded.applyViewState(loadViewState(path))
	if !got.showTree {
		t.Error("a file with no show_tree must leave the layout alone, not force it back to the cards")
	}
	if got.lens != lensProfile {
		t.Errorf("lens = %v, want profile — the field that IS present must still apply", got.lens)
	}
}

// TestSaveFailureLeavesTheToggleAndItsMessageAlone: the disk is not the operator's
// problem when they press v l. The layout must still change, and the status line
// must still say what the key did rather than an I/O error.
func TestSaveFailureLeavesTheToggleAndItsMessageAlone(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "wall")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	m := baseModel()
	m.mode = modeDashboard
	m.viewStatePath = filepath.Join(blocked, "TUI.state.json") // a file, not a dir: ENOTDIR

	got := m.toggleLayout()
	if !got.showTree {
		t.Error("the toggle must work whether or not it can be remembered")
	}
	if got.status == "" {
		t.Fatal("toggleLayout should still explain itself")
	}
	if strings.Contains(got.status, "save") || strings.Contains(got.status, "error") {
		t.Errorf("status = %q, want the layout message rather than an I/O complaint", got.status)
	}
}

// TestEmptyPathNeverTouchesTheDisk pins the guard that keeps every other test in
// this package from writing into the real $HOME: a model with no state path
// persists nothing and reads nothing.
func TestEmptyPathNeverTouchesTheDisk(t *testing.T) {
	m := baseModel()
	m.mode = modeDashboard
	if m.viewStatePath != "" {
		t.Fatal("baseModel must not carry a state path, or the suite writes to the operator's home")
	}

	m = m.toggleLayout() // must not panic, must not write
	if !m.showTree {
		t.Error("the toggle still works without a state file")
	}
	if v := loadViewState(""); v.ShowTree != nil || v.Lens != nil {
		t.Error("an empty path must read as no opinion")
	}
}

// TestLensIsRememberedByNameNotIndex: every lens survives the round trip through
// its own String, so the persisted value stays meaningful when lensOrder is
// reordered — which it will be, since it is a cycle somebody tunes.
func TestLensIsRememberedByNameNotIndex(t *testing.T) {
	for _, l := range lensOrder {
		if got := parseLens(l.String()); got != l {
			t.Errorf("parseLens(%q) = %v, want %v", l.String(), got, l)
		}
	}
	// An unknown name is the work items, the one lens a person must always be able
	// to get back to.
	if got := parseLens("a lens from a newer baton"); got != lensWork {
		t.Errorf("parseLens(unknown) = %v, want the work-item default", got)
	}
}

// TestRememberedFileIsOwnerOnly: the file records what one operator likes and sits
// beside the socket and the config, both of which are 0600.
func TestRememberedFileIsOwnerOnly(t *testing.T) {
	m := stateModel(t)
	m.mode = modeDashboard
	m = m.toggleLayout()

	fi, err := os.Stat(m.viewStatePath)
	if err != nil {
		t.Fatalf("the toggle should have written the file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want 0600", perm)
	}
}

// writeState drops raw bytes at path, so a test can hand loadViewState something
// json.Marshal would never produce.
func writeState(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
