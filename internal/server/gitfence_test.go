package server

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/gitops"
	"github.com/cmj0121/baton/internal/proto"
)

// gitfence_test.go covers #96: panel.git's commit op spawns $EDITOR in a PTY on
// the daemon's host, and the conductor fence did not cover it.
//
// The distinction being drawn is "spawns an interactive program on the host",
// not "touches git". captureGit is the majority of this surface, it is
// read-shaped, and a conductor reading the state of a repo is exactly what the
// role is for — so the capture ops stay open and only commit is refused.

// conductorConn is a connection that has declared the conductor role.
func conductorConn(self string) *clientConn {
	cc := ctl(self)
	cc.role = roleConductor
	return cc
}

// TestConductorMayNotOpenTheCommitEditor is the fence. commit resolves to
// `sh -c "git add -A && git commit"` with GIT_EDITOR injected — a live PTY
// running the operator's editor — and panel.input has no ownership check at
// all (`s.pty.Write(cmd.ID, cmd.Data)`), so the ephemeral id it replies with is
// drivable. Every editor worth setting $EDITOR to can run a shell.
func TestConductorMayNotOpenTheCommitEditor(t *testing.T) {
	s := &Server{}
	cc := conductorConn("c1")

	// Against another agent, and against ITSELF: panel.git is not in the
	// self-target fence, so "it needs another agent" was never the mitigation it
	// looks like.
	for _, id := range []string{"w1", "c1", ""} {
		cmd := proto.Command{Action: "panel.git", Git: string(gitops.OpCommit), ID: id}
		reason := s.guardConductor(cc, cmd)
		if reason == "" {
			t.Errorf("commit targeting %q was allowed; a conductor must not open the commit editor", id)
			continue
		}
		if !strings.Contains(reason, "operator surface") {
			t.Errorf("commit targeting %q refused with %q, want it to say why", id, reason)
		}
	}
}

// TestConductorKeepsTheReadShapedGitOps is the line this fix must not cross.
// Fencing panel.git wholesale would take away the repo visibility the conductor
// is built to have, and none of these spawns a PTY — captureGit replies with
// text and persists nothing.
func TestConductorKeepsTheReadShapedGitOps(t *testing.T) {
	s := &Server{}
	cc := conductorConn("c1")

	for _, op := range []gitops.Op{
		gitops.OpStatus, gitops.OpLog, gitops.OpAdd, gitops.OpPush,
		gitops.OpBranch, gitops.OpWorktreeList,
	} {
		cmd := proto.Command{Action: "panel.git", Git: string(op), ID: "w1"}
		if reason := s.guardConductor(cc, cmd); reason != "" {
			t.Errorf("%s is fenced for a conductor (%q); only the commit editor should be", op, reason)
		}
	}
}

// TestWorktreeAddIsStillChargedNotForbidden is the assertion most likely to rot.
// It is easy to "simplify" the op switch into a single refusal and not notice
// that a METERED op quietly became a forbidden one — #62 charges worktree-add
// against the spawn caps precisely so a conductor can still isolate work, just
// not unboundedly.
func TestWorktreeAddIsStillChargedNotForbidden(t *testing.T) {
	s := &Server{}
	cc := conductorConn("c1")

	cmd := proto.Command{Action: "panel.git", Git: string(gitops.OpWorktreeAdd), ID: "w1", Name: "feat/x"}
	if reason := s.guardConductor(cc, cmd); reason != "" {
		t.Fatalf("worktree-add under the budget was refused with %q — it is metered, not forbidden", reason)
	}
}

// TestCockpitKeepsEveryGitOp: a plain cockpit connection declares no role and is
// never fenced by any of this.
func TestCockpitKeepsEveryGitOp(t *testing.T) {
	s := &Server{}
	for _, op := range []gitops.Op{gitops.OpCommit, gitops.OpStatus, gitops.OpWorktreeAdd} {
		cmd := proto.Command{Action: "panel.git", Git: string(op), ID: "w1"}
		if reason := s.guardConductor(ctl(""), cmd); reason != "" {
			t.Errorf("a cockpit connection was fenced from %s: %q", op, reason)
		}
	}
}
