// Package queue is the on-disk backlog of tasks: one JSON file per task under a
// directory, written atomically and loaded back on boot. It is a derived mirror
// of the server's in-memory task table — the source of truth stays in memory —
// so the store never hard-fails the daemon: a malformed file is skipped and can
// be quarantined, never crashing a load.
//
// The store is self-contained (it depends only on task and paths), so the backlog
// is inspectable and editable from outside baton: a task is just <id>.json.
package queue

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/task"
)

// Schema is the queue store's on-disk schema version, independent of the fleet
// snapshot's — the two evolve separately, and one bad task file is quarantined
// rather than discarding the whole backlog.
const Schema = 1

// idRe guards every id that becomes a file path: a server-minted task id is
// "t<n>", so anything else (a hand-crafted "../escape") is refused before it
// touches the filesystem.
var idRe = regexp.MustCompile(`^t[0-9]+$`)

// record is the on-disk shape: the task plus the schema that wrote it.
type record struct {
	Schema int       `json:"schema"`
	Task   task.Task `json:"task"`
}

// Store is a task backlog rooted at a directory.
type Store struct {
	dir string
	now func() time.Time // for deterministic quarantine names in tests
}

// New opens a store at dir (created lazily on the first Save). now stamps
// quarantine file names; pass time.Now in production.
func New(dir string, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{dir: dir, now: now}
}

// Dir is the backlog directory.
func (s *Store) Dir() string { return s.dir }

// path is the file for a task id, after validating the id cannot escape the dir.
func (s *Store) path(id string) (string, error) {
	if !idRe.MatchString(id) {
		return "", fmt.Errorf("invalid task id %q", id)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// Save writes a task to its file atomically (temp + rename, see
// paths.WriteFileAtomic), creating the backlog directory on first use.
func (s *Store) Save(t task.Task) error {
	p, err := s.path(t.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record{Schema: Schema, Task: t}, "", "  ")
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(p, data, 0o600)
}

// Remove deletes a task's file. A file that is already gone is not an error — a
// drain or an external rm racing the scheduler is benign.
func (s *Store) Remove(id string) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Quarantine renames a bad file aside as "<id>.json.bad-<RFC3339>" so a load can
// skip it without losing it, mirroring the state store's corrupt-aside handling.
func (s *Store) Quarantine(id string) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	aside := p + ".bad-" + s.now().UTC().Format("20060102T150405Z")
	if err := os.Rename(p, aside); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LoadAll reads every task file in the backlog. It never hard-fails: a missing
// directory yields nothing (first run), and a malformed or future-schema file is
// left out of tasks and its id returned in bad, for the caller to quarantine.
// Files that are not "t<n>.json" (e.g. an already-quarantined .bad- file) are
// ignored entirely.
func (s *Store) LoadAll() (tasks []task.Task, bad []string, err error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if !idRe.MatchString(id) {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(s.dir, name))
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue // vanished between readdir and read — tolerate the churn
			}
			bad = append(bad, id)
			continue
		}
		var r record
		if json.Unmarshal(data, &r) != nil || r.Schema > Schema || r.Task.ID != id {
			bad = append(bad, id)
			continue
		}
		tasks = append(tasks, r.Task)
	}
	return tasks, bad, nil
}
