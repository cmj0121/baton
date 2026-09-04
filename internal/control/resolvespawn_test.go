package control_test

import (
	"errors"
	"testing"

	"github.com/cmj0121/baton/internal/control"
)

// TestResolveSpawnRefusesInOneOrder pins the ORDER of the spawn refusals, which
// is the part two front ends cannot each own a copy of.
//
// The rules used to be expanded into a switch in `ctl spawn` and another in the
// baton_spawn tool, in different orders, so a request that broke two of them at
// once was refused for a different reason depending on which interface asked.
// Nobody chose that. The order is `ctl`'s, because the operator surface is where
// a refusal is read by a person, and it lives in ResolveSpawn so that there is
// one of it.
//
// Every case below breaks MORE than one rule, or would have been resolved rather
// than refused under the order the tool used to apply — the single-rule cases are
// the same either way and prove nothing about ordering. The client is never
// dialed: a refusal is decided before anything reaches the socket, which is
// itself part of the contract.
func TestResolveSpawnRefusesInOneOrder(t *testing.T) {
	var c *control.Client

	cases := []struct {
		name string
		req  control.SpawnRequest
		want error
	}{
		{
			// Breaks two rules. The tool used to answer ErrAgentAndRun here and `ctl`
			// answered ErrBranchNeedsWorktree; this is the case that settled it.
			name: "branch without worktree outranks agent with run",
			req:  control.SpawnRequest{Agent: "claude", Run: "make", Branch: "feat/x"},
			want: control.ErrBranchNeedsWorktree,
		},
		{
			// The tool used to reach `run` first and spawn a COMMAND panel, silently
			// dropping the branch — into the shared checkout, which is the outcome the
			// whole worktree verb exists to prevent.
			name: "branch without worktree is refused, not dropped for run",
			req:  control.SpawnRequest{Run: "make", Branch: "feat/x"},
			want: control.ErrBranchNeedsWorktree,
		},
		{
			// With worktree set, the branch rule does not apply and the pair does.
			name: "agent with run outranks run with worktree",
			req:  control.SpawnRequest{Agent: "claude", Run: "make", Worktree: true, Branch: "feat/x"},
			want: control.ErrAgentAndRun,
		},
		{
			name: "run with worktree, once the others do not apply",
			req:  control.SpawnRequest{Run: "make", Worktree: true, Branch: "feat/x"},
			want: control.ErrRunHasNoWorktree,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := c.ResolveSpawn(tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			if id != "" {
				t.Fatalf("a refused spawn should name no panel, got %q", id)
			}
		})
	}
}
