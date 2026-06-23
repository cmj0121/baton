// Package state persists baton's fleet/layout snapshot to disk so a daemon
// restart can restore the panels, their grouping, pins, and per-group view
// settings.
//
// This package is the persistence store only: Load reads a snapshot (never
// hard-failing boot on a corrupt or future-versioned file), and Save writes one
// atomically and durably. The persisted shape is self-contained here so the
// store carries no dependency on the live server or proto packages.
package state

import (
	"encoding/json"
	"os"
	"time"

	"github.com/cmj0121/baton/internal/paths"
)

// Schema is the current on-disk schema version. Bump it on a breaking change to
// the persisted shape; a file written with a newer schema is treated as corrupt.
const Schema = 1

// State is the persisted snapshot of the server's fleet and per-group layouts.
type State struct {
	Schema   int           `json:"schema"`    // on-disk schema version (see Schema)
	SavedAt  time.Time     `json:"saved_at"`  // when this snapshot was written
	LastBoot time.Time     `json:"last_boot"` // when the server that wrote it booted
	Seq      int           `json:"seq"`       // server's id counter, restored to avoid id collisions
	Panels   []PanelState  `json:"panels"`    // the fleet
	Groups   []GroupLayout `json:"groups"`    // per-group view settings
}

// PanelState is a single panel's persisted identity plus its immutable spawn
// inputs. Live telemetry (state/activity/spark) is intentionally not persisted.
type PanelState struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Group  string `json:"group"`
	Pinned bool   `json:"pinned"`
	Spec   Spec   `json:"spec"` // immutable spawn inputs only
}

// Spec is a panel's immutable spawn inputs, frozen at create time.
type Spec struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Dir     string   `json:"dir"`
}

// GroupLayout is one group's persisted view settings. Members stream as equal-
// size tiles in an even grid; Shown is how many of them stream live before the
// rest collapse into the group's summary tile. Shown == 0 means "use the default".
type GroupLayout struct {
	Group string `json:"group"`
	Shown int    `json:"shown,omitempty"`
}

// Load reads the snapshot at path. It never returns a corrupt State: a missing
// file yields an empty State (first run); an unparsable, structurally invalid,
// or newer-schema file is renamed aside (path + ".corrupt-<timestamp>") and an
// empty State is returned. The only non-nil error paths are I/O failures that
// are not "file missing".
func Load(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{Schema: Schema}, nil
		}
		return State{Schema: Schema}, err
	}

	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return corrupt(path)
	}
	if s.Schema > Schema || !valid(s) {
		return corrupt(path)
	}
	return s, nil
}

// Save writes the snapshot to path atomically and durably (see
// paths.WriteFileAtomic): indented JSON via a temp file, fsync, rename, and a
// parent-directory fsync. Schema and SavedAt are set if unset.
func (s State) Save(path string) error {
	if s.Schema == 0 {
		s.Schema = Schema
	}
	if s.SavedAt.IsZero() {
		s.SavedAt = time.Now()
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(path, data, 0o600)
}

// corrupt renames the bad file aside and returns an empty State. A rename
// failure (e.g. the file vanished) is non-fatal: boot must still proceed.
func corrupt(path string) (State, error) {
	aside := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405Z")
	_ = os.Rename(path, aside)
	return State{Schema: Schema}, nil
}

// valid reports whether s is structurally sound enough to restore. A negative
// visible count is nonsensical and marks the file as corrupt.
func valid(s State) bool {
	for _, g := range s.Groups {
		if g.Shown < 0 {
			return false
		}
	}
	return true
}
