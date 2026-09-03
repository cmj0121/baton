package server

import (
	"encoding/json"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/gitops"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/worktree"
)

// sweepSkip is one orphan a sweep declined to remove, with git's own reason.
// Skipped is not a failure of the sweep: a dirty tree holds work nobody has
// committed and a locked one was locked on purpose, so both are NAMED and left
// standing while the rest of the sweep goes on.
type sweepSkip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// sweepOutcome is what one sweep did, split by the two shapes an orphan comes in
// plus the ones git refused. Removed had a tree on disk that git took away;
// Dropped was already gone before the sweep looked, so only the record was
// retired. Every slice is non-nil so the JSON carries [] rather than null.
type sweepOutcome struct {
	Removed []string    `json:"removed"`
	Dropped []string    `json:"dropped"`
	Skipped []sweepSkip `json:"skipped"`
}

// worktreeEntries classifies every tree baton opened against the fleet as it
// stands right now.
//
// A nil store is persistence being switched off, and it yields NOTHING. No state
// file means no record, and no record means there is not one path baton can say
// it opened — so the honest answer is an empty list, and a sweep reading it
// retires nothing. The opposite reading of the same situation, that an empty
// record means every tree is unaccounted for, is the one that empties disks.
func (s *Server) worktreeEntries() []worktree.Entry {
	if s.wtrees == nil {
		return nil
	}

	s.mu.Lock()
	owners := make([]worktree.Owner, 0, len(s.panels))
	for _, p := range s.panels {
		owners = append(owners, worktree.Owner{
			Dir:    s.specs[p.ID].Dir,
			Exited: p.State == panel.Exited,
		})
	}
	s.mu.Unlock()

	// Paths() reads the record from disk under the store's own lock, so it is
	// called outside s.mu rather than inside it.
	return worktree.Classify(s.wtrees.Paths(), owners)
}

// sweepWorktrees removes the orphans and retires their records, leaving live
// trees and dead slots alone. It is only ever called from an operator's explicit
// command — never on panel close, never on daemon boot.
//
// The two orphan shapes end differently. A tree still on disk goes through
// gitops.WorktreeRemove, which runs plain: git refuses a dirty or locked tree,
// and that refusal is recorded as a skip rather than aborting the sweep. A tree
// whose directory is already gone — an operator's own `git worktree remove`, or
// an `rm -rf`, neither of which reaches baton — has nothing left to remove, so
// only the record is dropped; git's own stale admin entry is what `git worktree
// prune` is for, and baton does not grow a wrapper for it.
//
// git runs IN THE TREE ITSELF rather than in a repo derived from it. See
// gitops.WorktreeRemove for why that is enough.
//
// The record is retired only AFTER git says the tree is gone. A skip therefore
// leaves the path stamped, so the next `list` still shows it and a later sweep
// can finish the job once the operator has dealt with what git objected to.
func (s *Server) sweepWorktrees() sweepOutcome {
	out := sweepOutcome{Removed: []string{}, Dropped: []string{}, Skipped: []sweepSkip{}}

	for _, e := range s.worktreeEntries() {
		if e.Status != worktree.StatusOrphan {
			continue
		}
		if !e.Exists {
			s.forgetWorktree(e.Path)
			out.Dropped = append(out.Dropped, e.Path)
			continue
		}
		if err := gitops.WorktreeRemove(e.Path, e.Path); err != nil {
			out.Skipped = append(out.Skipped, sweepSkip{Path: e.Path, Reason: err.Error()})
			continue
		}
		s.forgetWorktree(e.Path)
		out.Removed = append(out.Removed, e.Path)
	}

	log.Info().
		Int("removed", len(out.Removed)).
		Int("dropped", len(out.Dropped)).
		Int("skipped", len(out.Skipped)).
		Msg("swept orphaned worktrees")
	return out
}

// worktreeJSON marshals a worktree reply payload. Like scoreJSON, the error is
// discarded: the shapes here are plain structs of strings and bools, which
// encoding/json cannot fail on.
func worktreeJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
