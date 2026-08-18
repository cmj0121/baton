// Package cwd is the policy around a panel's live working directory: how it is
// learned, and which panels are put back in it when they are re-run.
//
// The directory itself is read elsewhere — the shell's own OSC 7 report through
// internal/vtquery, the process table through internal/proctree. This package
// holds only the two decisions the config expresses, so both the daemon and the
// config parser can share them without either owning the other's job.
package cwd

import "strings"

// Track is how a panel's working directory is learned.
type Track string

const (
	// Auto prefers the shell's own report and falls back to the process table. It
	// is the default: the report is free and exact when it comes, and most shells
	// on Linux send one, while macOS bash does not.
	Auto Track = "auto"

	// OSC7 uses only the shell's report — no process-table sampling at all.
	OSC7 Track = "osc7"

	// Proc uses only the process table, ignoring what a shell reports.
	Proc Track = "proc"

	// Off tracks nothing. A panel's directory stays the one it was spawned in.
	Off Track = "off"
)

// Restore is which panels a re-run puts back where they were.
type Restore string

const (
	// Shells restores a shell panel's directory and leaves agents in the directory
	// they were launched in. It is the default; see the config for why.
	Shells Restore = "shells"

	// All restores every panel's directory, agents included.
	All Restore = "all"

	// NoRestore always re-runs a panel where it was originally spawned.
	NoRestore Restore = "off"
)

// ParseTrack maps a config value to a Track, reporting ok=false for anything
// else. An unreadable setting falls back to Auto rather than Off: failing to
// track a directory is a lost convenience, not a risk, so the safe direction here
// is the useful one.
func ParseTrack(s string) (Track, bool) {
	switch t := Track(strings.ToLower(strings.TrimSpace(s))); t {
	case Auto, OSC7, Proc, Off:
		return t, true
	default:
		return Auto, false
	}
}

// ParseRestore maps a config value to a Restore, reporting ok=false for anything
// else. An unreadable setting falls back to Shells, the default.
func ParseRestore(s string) (Restore, bool) {
	switch r := Restore(strings.ToLower(strings.TrimSpace(s))); r {
	case Shells, All, NoRestore:
		return r, true
	default:
		return Shells, false
	}
}

// ReadsReport reports whether this mode listens to the shell's own OSC 7.
func (t Track) ReadsReport() bool { return t == Auto || t == OSC7 }

// ReadsProcess reports whether this mode may sample the process table.
func (t Track) ReadsProcess() bool { return t == Auto || t == Proc }

// Restores reports whether a panel of this kind is re-run where it last was.
// isAgent separates the two cases the default distinguishes.
func (r Restore) Restores(isAgent bool) bool {
	switch r {
	case All:
		return true
	case Shells:
		return !isAgent
	default:
		return false
	}
}
