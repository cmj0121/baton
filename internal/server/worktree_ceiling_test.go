package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/ptymgr"
)

// An operator's worktree-add on a full fleet is refused BEFORE git makes the tree.
//
// The road has a side effect the door is downstream of: git creates the worktree
// on disk and registers it, and a refusal after that hands the operator a tree
// they asked for an agent in. A conductor never saw this — guardConductor fences
// it before any git runs — and the operator only began paying the ceiling in #86,
// which added the payer without moving where it pays (#90).
//
// Both directions, because the filesystem assertion is the whole point and it
// would pass on a road that never ran at all: with room, the same call must make
// the tree.
func TestAFullFleetRefusesAWorktreeBeforeGitMakesIt(t *testing.T) {
	repo := wtRepo(t)
	s, _, _ := gateServer(fullFleet()...)

	spec := spawnSpec{Spec: ptymgr.Spec{Command: "/bin/sh", Args: []string{"-c", "exit 0"}}}
	err := s.worktreeSpawn(originOperator, repo, "feature/ceiling", spec)
	if err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("a full fleet answered %v, want the capacity refusal", err)
	}

	tree := filepath.Join(repo+"-worktrees", "feature-ceiling")
	if _, statErr := os.Stat(tree); statErr == nil {
		t.Errorf("the tree was created at %s despite the refusal; the operator asked for an agent and got a directory", tree)
	}

	// The refusal must not have spent anything either: room is made, and the very
	// next call goes through. A ceiling read that stamped would refuse this one for
	// the wrong reason.
	s.mu.Lock()
	s.panels = s.panels[:1]
	s.mu.Unlock()
	if err := s.worktreeSpawn(originOperator, repo, "feature/ceiling", spec); err != nil {
		t.Fatalf("with room the same call answered %v, want it admitted", err)
	}
	if _, statErr := os.Stat(tree); statErr != nil {
		t.Errorf("with room the tree should exist at %s: %v", tree, statErr)
	}
}

// budgetCeilingLocked never spends the gap, for any origin.
//
// This is the property the worktree road leans on rather than the call site it
// happens to use: it asks early to protect git's side effect, and asking twice is
// only free while the answer cannot stamp. Substituting budgetReasonLocked there
// passes today because both are the same call for the roads that pay here — and
// stops passing the day the plugin's gap exemption is lifted, which #86 left as a
// hold. So the guard is on the behaviour, where it keeps working.
func TestTheCeilingHalfNeverSpendsTheGap(t *testing.T) {
	for _, o := range []panelOrigin{originOperator, originConductor, originScheduler, originPlugin} {
		s, _, _ := gateServer()
		s.mu.Lock()
		for range 50 {
			_ = s.budgetCeilingLocked(o)
		}
		spent := s.spawnGapLocked(time.Now())
		s.mu.Unlock()
		if spent != "" {
			t.Errorf("origin %d: fifty ceiling reads spent the gap (%q); the early read on the worktree road would be charging for it", o, spent)
		}
	}
}
