package tui

import (
	"encoding/json"
	"os"

	"github.com/cmj0121/baton/internal/paths"
)

// The cockpit's remembered view preferences: how you like the dashboard drawn,
// carried from one session to the next.
//
// Two settings live here, and the boundary is worth stating because it decides
// what may ever join them. These are HOW YOU LIKE IT — the cards-or-tree choice
// and the group-by lens, both of which a person sets once and then stops thinking
// about. Where you were is a different thing entirely: the scroll position, the
// focused panel, the open mode. Restoring those on launch drops somebody into a
// screen they did not ask for, so they are deliberately not here.
//
// Client-side and nothing else. These are properties of this operator at this
// terminal, not of the fleet — two cockpits on one daemon may want different ones
// and a remote cockpit must not inherit the local one's taste — so nothing in this
// file reaches internal/proto. Group layout is the neighbour on the other side of
// that line and stays server-side: a group's layout is a property of the GROUP,
// and every viewer should see it laid out the same way.

// viewState is the on-disk shape, written to paths.TUIStateFile.
//
// Every field is a pointer, and that is the precedence rule made mechanical
// rather than merely commented. Absent means "no opinion": the operator has never
// pressed this key, so whatever the built-in default or a future config key says
// still stands. Present means they pressed it, and a deliberate keystroke outranks
// a default. Plain values could not express the difference — a missing show_tree
// would be indistinguishable from a remembered `false`, and the first person to add
// a config key for the layout would find it silently overridden by a file that was
// only ever recording the absence of an opinion.
//
// There is no schema version, unlike internal/state. That file is a fleet and a
// breaking change to it is worth migrating; this one is two toggles, and the right
// answer to a shape it cannot read is to ignore the file and let the next keystroke
// rewrite it — which is what a failed unmarshal already does.
type viewState struct {
	ShowTree *bool   `json:"show_tree,omitempty"` // dashboard drawn as the tree rather than the cards
	Lens     *string `json:"lens,omitempty"`      // the group-by lens, by its stable name (see lens.String)
}

// loadViewState reads the remembered preferences at path.
//
// It returns no error, and the missing signature is the design. This file is
// ADVISORY: a missing one (first run), an unreadable one (bad perms, a vanished
// home), and a half-written one (killed mid-rename on a filesystem without an
// atomic one) must all land on the built-in defaults without a word to the
// operator. A lost preference is not worth a startup error, and giving the caller
// an error to handle is how it would eventually become one.
//
// An empty path is the "do not persist" case — a model built without one, as the
// tests do — and reads as no opinion.
func loadViewState(path string) viewState {
	if path == "" {
		return viewState{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return viewState{} // missing, unreadable, a directory: all the same answer
	}
	var v viewState
	if err := json.Unmarshal(data, &v); err != nil {
		return viewState{} // truncated or garbage; the next toggle overwrites it
	}
	return v
}

// save writes the preferences to path, atomically and owner-only, via the same
// helper internal/state uses — a temp file, fsync, rename, and a parent-directory
// fsync — so a cockpit killed mid-write leaves either the old file or the new one
// and never a torn one.
//
// An empty path is a no-op, which is what keeps the toggles inert in tests and in
// any model built without a state file.
func (v viewState) save(path string) error {
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := paths.EnsureDir(path); err != nil {
		return err
	}
	return paths.WriteFileAtomic(path, data, 0o600)
}

// applyViewState overlays remembered preferences onto the model, leaving anything
// the file has no opinion about exactly as it was.
//
// It touches no status line. Landing on the defaults is the silent half of the
// advisory contract, and a model that announced "could not read your preferences"
// would break it just as surely as returning an error would.
func (m model) applyViewState(v viewState) model {
	if v.ShowTree != nil {
		m.showTree = *v.ShowTree
	}
	if v.Lens != nil {
		m.lens = parseLens(*v.Lens)
	}
	return m
}

// rememberView persists the model's current view preferences.
//
// A save failure is dropped on purpose. The sibling toggles that write the config
// report one in the status line, but they are overwriting a message that says
// nothing else; these two keys have something better to say — toggleLayout
// explains which fleets the cards can draw, and cycleLens names the lens you
// landed on — and trading that for an I/O error is a bad bargain over a preference.
// The operator is not left in the dark either: this file shares a directory with
// the config, so a home that cannot be written announces itself the next time any
// setting is saved.
func (m model) rememberView() {
	showTree, lens := m.showTree, m.lens.String()
	_ = viewState{ShowTree: &showTree, Lens: &lens}.save(m.viewStatePath)
}
