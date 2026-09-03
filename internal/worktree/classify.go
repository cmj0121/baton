package worktree

import (
	"os"
)

// Status is what a stamped path turned out to be when the fleet was looked at.
type Status string

const (
	// StatusLive is a tree a panel that is still running works in. Removing it
	// would pull the ground out from under an agent mid-command.
	StatusLive Status = "live"

	// StatusDeadSlot is a tree an EXITED panel still names. The tree is idle, but
	// the fleet has not let go of it: the slot is still in the panel list, still
	// carries the agent's transcript, and a respawn still points here. It is not
	// an orphan, and the operator purges the slot to make it one.
	StatusDeadSlot Status = "dead-slot"

	// StatusOrphan is a tree NO panel names — neither a live one nor a dead slot.
	// Nothing in the fleet refers to it any more, so it is the only status a
	// sweep acts on.
	StatusOrphan Status = "orphan"
)

// Owner is one panel's claim on a directory: the workdir it was spawned in, and
// whether that panel has exited. A live claim and a dead one are the difference
// between a tree that must not be touched and a slot the operator has yet to
// purge, which is why Exited rides along rather than the caller pre-filtering.
type Owner struct {
	Dir    string
	Exited bool
}

// Entry is one stamped path with what became of it. Exists is whether the
// directory is still on disk: an operator can `git worktree remove` in their own
// terminal or delete the directory outright, and neither reaches baton, so a
// recorded path naming nothing is ordinary rather than exceptional.
type Entry struct {
	Path   string `json:"path"`
	Status Status `json:"status"`
	Exists bool   `json:"exists"`
}

// Classify sorts every stamped path into live, dead-slot, or orphan by matching
// it against the workdirs the fleet's panels were spawned in. The stamped set is
// the ONLY input that can produce an entry: a tree the operator made with plain
// `git worktree add` is in no record, so it is in no result, and a sweep reading
// this can never reach it.
//
// Owner directories are canonicalised before they are compared, and that is the
// load-bearing step rather than a tidy-up. The store records a path the way git
// reports it — absolute, symlinks resolved — while a panel's spawn spec keeps
// whatever spelling the caller passed. On any host where the two differ (macOS
// resolves /var to /private/var, so every tree under a temp dir differs) a raw
// string comparison would match NOTHING, and every live panel's tree would be
// classified as an orphan for a sweep to delete. The comparison is the safety
// property, not the presentation.
//
// A path claimed by both a live panel and a dead slot is live: the running agent
// is what matters, and the exited slot beside it changes nothing.
func Classify(stamped []string, owners []Owner) []Entry {
	live := make(map[string]bool, len(owners))
	for _, o := range owners {
		if o.Dir == "" {
			continue // a panel with no workdir claims nothing; "" would resolve to the daemon's cwd
		}
		dir, err := canonical(o.Dir)
		if err != nil {
			continue
		}
		live[dir] = live[dir] || !o.Exited
	}

	out := make([]Entry, 0, len(stamped))
	for _, p := range stamped {
		e := Entry{Path: p, Status: StatusOrphan, Exists: isDir(p)}
		if isLive, claimed := live[p]; claimed {
			e.Status = StatusDeadSlot
			if isLive {
				e.Status = StatusLive
			}
		}
		out = append(out, e)
	}
	return out
}

// isDir reports whether path is a directory that is still there.
func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
