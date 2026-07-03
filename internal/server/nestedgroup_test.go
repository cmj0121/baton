package server_test

import (
	"testing"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
)

// nest groups a into "backend" and b into "backend/api", returning a live client and
// the two panel ids — the shared "a group with one sub-group" fixture.
func nest(t *testing.T) (c *client.Client, a, b string) {
	t.Helper()
	c = startServer(t)
	ids := createShells(t, c, 2)
	a, b = ids[0], ids[1]
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{a}, Group: "backend"}); err != nil {
		t.Fatalf("group a: %v", err)
	}
	recv(t, c)
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{b}, Group: "backend/api"}); err != nil {
		t.Fatalf("group b (nested): %v", err)
	}
	recv(t, c)
	return c, a, b
}

// TestNestedRenameMovesSubtree: renaming a group rewrites the path prefix across its
// whole subtree, so a sub-group follows its parent to the new path.
func TestNestedRenameMovesSubtree(t *testing.T) {
	c, a, b := nest(t)
	if err := c.Send(proto.Command{Action: "panel.rename", Group: "backend", Name: "infra"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	snap := recv(t, c)
	if g := panelByID(snap.Panels, a); g.Group != "infra" {
		t.Fatalf("direct member should move to infra, got %q", g.Group)
	}
	if g := panelByID(snap.Panels, b); g.Group != "infra/api" {
		t.Fatalf("nested member should follow to infra/api, got %q", g.Group)
	}
}

// TestUngroupPromotesSubtree: dissolving a group drops its path segment and promotes
// its subtree one level — direct panels go lone, sub-groups become top-level.
func TestUngroupPromotesSubtree(t *testing.T) {
	c, a, b := nest(t)
	if err := c.Send(proto.Command{Action: "panel.ungroup", Group: "backend"}); err != nil {
		t.Fatalf("ungroup: %v", err)
	}
	snap := recv(t, c)
	if g := panelByID(snap.Panels, a); g.Group != "" {
		t.Fatalf("direct member should go lone, got %q", g.Group)
	}
	if g := panelByID(snap.Panels, b); g.Group != "api" {
		t.Fatalf("nested member should promote to top-level api, got %q", g.Group)
	}
}

// TestGroupNestedPathDirectly: grouping under a slash-path creates the nesting in one
// step, with the parent implied by the path.
func TestGroupNestedPathDirectly(t *testing.T) {
	c := startServer(t)
	id := createShells(t, c, 1)[0]
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{id}, Group: "x/y/z"}); err != nil {
		t.Fatalf("group: %v", err)
	}
	snap := recv(t, c)
	if g := panelByID(snap.Panels, id); g.Group != "x/y/z" {
		t.Fatalf("panel should sit at x/y/z, got %q", g.Group)
	}
}

// TestGroupRejectsInvalidPath: malformed paths (empty segments, leading/trailing
// separators) are refused.
func TestGroupRejectsInvalidPath(t *testing.T) {
	c := startServer(t)
	id := createShells(t, c, 1)[0]
	for _, bad := range []string{"a//b", "/a", "a/"} {
		if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{id}, Group: bad}); err != nil {
			t.Fatalf("send %q: %v", bad, err)
		}
		if msg := recv(t, c); msg.Type != "error" {
			t.Fatalf("path %q should be rejected, got %+v", bad, msg)
		}
	}
}

// TestDispatchGroupRecursesIntoSubtree: dispatching to a parent group fans the brief
// to every descendant panel, nested ones included.
func TestDispatchGroupRecursesIntoSubtree(t *testing.T) {
	c, a, b := nest(t)
	const brief = "audit the module"
	if err := c.Send(proto.Command{Action: "panel.dispatch-group", Group: "backend", Prompt: brief}); err != nil {
		t.Fatalf("dispatch-group: %v", err)
	}
	snap := recv(t, c)
	if got := panelByID(snap.Panels, a).Task; got != brief {
		t.Fatalf("direct member should get the brief, got %q", got)
	}
	if got := panelByID(snap.Panels, b).Task; got != brief {
		t.Fatalf("nested member should get the brief too, got %q", got)
	}
}
