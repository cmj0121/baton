// Package worktree records the git worktrees baton itself created, so a later
// sweep can tell its own trees from the ones the operator made by hand with
// `git worktree add`.
//
// The record is a sibling of the fleet snapshot (see paths.StateFile): derived
// from the control socket, machine-written, never hand-edited. Nothing is written
// INTO the worktree — a marker file there would sit in front of the agent running
// in that tree, which might well commit it.
//
// It is a separate file from the fleet snapshot because the lifetimes differ. A
// close, a purge, and a daemon restart all leave the tree standing, so the set
// outlives the fleet the snapshot describes.
//
// Like the queue store, this is a derived mirror rather than a source of truth:
// a missing, unreadable, or newer-schema file reads back as an EMPTY set. That
// is the fail-safe direction — a sweep retires only what is in the set, so a
// record baton cannot read costs an abandoned tree, never a deleted one.
package worktree

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/cmj0121/baton/internal/paths"
)

// Schema is the current on-disk schema version, independent of the fleet
// snapshot's — the two evolve separately. Bump it on a breaking change to the
// persisted shape; a file written with a newer schema reads back as empty.
const Schema = 1

// record is the on-disk shape: the schema that wrote it plus the paths.
type record struct {
	Schema int      `json:"schema"`
	Paths  []string `json:"paths"`
}

// Store is the set of worktree paths baton opened, backed by one JSON file.
// It is safe for concurrent use: two clients can add a tree at once.
type Store struct {
	mu   sync.Mutex
	path string
}

// New opens the store at path. The file is created on the first Add.
func New(path string) *Store { return &Store{path: path} }

// Path is the file the set is persisted to.
func (s *Store) Path() string { return s.path }

// Add files one worktree path in the set. It is idempotent, and stores the path
// in the form `git worktree list` reports, which is what a sweep compares against.
// The write is atomic and durable (see paths.WriteFileAtomic).
func (s *Store) Add(p string) error {
	abs, err := canonical(p)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	set := s.loadLocked()
	for _, have := range set {
		if have == abs {
			return nil
		}
	}
	set = append(set, abs)
	sort.Strings(set) // a stable file: the same set always serialises the same way
	return s.saveLocked(set)
}

// Remove drops one worktree path from the set, so the record names the trees
// baton owns NOW rather than every tree it ever opened. Removing a path that is
// not in the set is not an error, and costs no write.
//
// Call it AFTER git has removed the tree but with the path git was given: the
// canonical form is recovered from the parent directory, so a tree that is
// already gone still matches the entry recorded while it existed.
func (s *Store) Remove(p string) error {
	abs, err := canonical(p)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	set := s.loadLocked()
	kept := make([]string, 0, len(set))
	for _, have := range set {
		if have != abs {
			kept = append(kept, have)
		}
	}
	if len(kept) == len(set) {
		return nil
	}
	return s.saveLocked(kept)
}

// saveLocked writes the set out atomically and durably. Caller holds s.mu.
func (s *Store) saveLocked(set []string) error {
	data, err := json.MarshalIndent(record{Schema: Schema, Paths: set}, "", "  ")
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(s.path, data, 0o600)
}

// Paths is the recorded set, sorted. An unreadable or newer-schema file yields
// no paths rather than an error: see the package comment for why that is the
// safe direction.
func (s *Store) Paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// canonical is the form a path is recorded in: absolute, with every symlink in
// it resolved. git reports a worktree that way — on macOS a tree under
// /var/folders comes back as /private/var/folders — so a record kept any other
// way would never match the list a sweep compares it against.
//
// A tree that no longer exists cannot be resolved whole, which is the ordinary
// case for Remove: git has just deleted it. Resolving the PARENT and rejoining
// the leaf recovers the same spelling the tree had while it stood, so an entry
// added and an entry removed agree. Only a path whose parent is gone too falls
// back to merely absolute.
func canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real, nil
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs)), nil
	}
	return abs, nil
}

// loadLocked reads the file into a slice of paths. Caller holds s.mu.
func (s *Store) loadLocked() []string {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil || r.Schema > Schema {
		return nil
	}
	return r.Paths
}
