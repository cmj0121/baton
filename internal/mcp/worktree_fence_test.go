package mcp

import (
	"strings"
	"testing"
)

// TestMCPWorktreeSurfaceIsListOnly is the conductor's half of the mass-delete
// fence, asserted rather than left to "we did not add one".
//
// The tool surface offers baton_worktrees and NOTHING that sweeps. That is not
// the whole fence — the daemon refuses worktree.sweep to a conductor connection,
// which is what stops an agent shelling out to `baton ctl` instead — but it is
// the half that lives here, and adding a sweeping tool later should have to
// delete this test rather than slip past it.
//
// The check is on the tool's BEHAVIOUR as well as its name: a tool called
// something else that swept would pass a name-only check, so every registered
// tool's description is searched for the verb too.
func TestMCPWorktreeSurfaceIsListOnly(t *testing.T) {
	tools := defaultTools()

	var listing bool
	for _, tl := range tools {
		if tl.name == "baton_worktrees" {
			listing = true
		}
	}
	if !listing {
		t.Fatal("a conductor should be able to see the worktrees baton opened")
	}

	for _, tl := range tools {
		if strings.Contains(tl.name, "sweep") || strings.Contains(tl.name, "worktree_remove") {
			t.Fatalf("no MCP tool may sweep worktrees, found %q", tl.name)
		}
		// A sweeping tool under a different name is the same hazard, so the
		// descriptions are searched for what such a tool would have to say it does.
		desc := strings.ToLower(tl.desc)
		if strings.Contains(desc, "sweep") || strings.Contains(desc, "remove the worktree") {
			t.Fatalf("tool %q describes sweeping worktrees: %s", tl.name, tl.desc)
		}
	}
}
