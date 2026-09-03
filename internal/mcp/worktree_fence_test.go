package mcp

import (
	"strings"
	"testing"
)

// TestMCPHasNoWorktreeSurface pins the cheapest form of the conductor fence:
// there is no worktree tool on MCP at all, so there is nothing here to fence.
//
// #68 asks that IF the sweep is reachable over MCP it be list-only for a
// conductor. Adding no worktree tool satisfies that condition rather than
// meeting it, and the issue's own framing points the same way — the operator who
// left twenty trees behind is usually at a shell, not driving the cockpit. A
// surface that does not exist cannot be walked through, and a conductor that
// could see residue it is not allowed to clear could only nag about it.
//
// This is NOT the whole fence, and the difference matters. A conductor panel has
// BATON_ROLE injected, so an agent that shells out to `baton ctl worktree sweep`
// arrives at the daemon as a conductor connection — a surface that exists and
// cannot be un-built. guardConductor refusing worktree.sweep is what stops that
// one, and TestWorktreeSweepIsFencedFromTheConductor in internal/server is where
// it is held. Deleting this file would not open that hole; deleting that fence
// would.
//
// Both directions are asserted, so this fails whichever way it stops being true:
// adding a listing tool should be a deliberate edit here rather than a silent
// widening, and adding a sweeping one should be loud.
func TestMCPHasNoWorktreeSurface(t *testing.T) {
	for _, tl := range defaultTools() {
		if strings.Contains(tl.name, "worktree") || strings.Contains(tl.name, "sweep") {
			t.Fatalf("MCP offers no worktree surface, found the tool %q", tl.name)
		}
		// A worktree tool under some other name is the same widening, so what a
		// tool SAYS it does is searched too.
		desc := strings.ToLower(tl.desc)
		if strings.Contains(desc, "sweep") || strings.Contains(desc, "orphan") {
			t.Fatalf("tool %q describes clearing worktrees: %s", tl.name, tl.desc)
		}
	}

	// baton_spawn still opens them — the conductor's half of worktrees is
	// unchanged, and only the tidying up is absent.
	var spawns bool
	for _, tl := range defaultTools() {
		if tl.name == "baton_spawn" && strings.Contains(strings.ToLower(tl.desc), "worktree") {
			spawns = true
		}
	}
	if !spawns {
		t.Fatal("a conductor should still be able to spawn into a fresh worktree")
	}
}
