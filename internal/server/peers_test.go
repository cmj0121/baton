package server_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/state"
)

// groupPeers returns the Peers flag the snapshot carries for the named group.
func groupPeers(msg proto.ServerMsg, group string) bool {
	for _, g := range msg.Groups {
		if g.Group == group {
			return g.Peers
		}
	}
	return false
}

// listPeers asks for a fresh snapshot and reports the named group's Peers flag.
// It reads by type because a refused command leaves no broadcast to count past.
func listPeers(t *testing.T, c *client.Client, group string) bool {
	t.Helper()
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	return groupPeers(recvType(t, c, "panels"), group)
}

// setPeers flips the flag over c and returns the reply: the fleet broadcast on
// success, the error on a refusal. The broadcast goes to every client, so a
// caller with a second connection must have drained that one's queue first.
func setPeers(t *testing.T, c *client.Client, group string, on bool) proto.ServerMsg {
	t.Helper()
	if err := c.Send(proto.Command{Action: "group.peers", Group: group, Enable: on}); err != nil {
		t.Fatalf("group.peers %q %v: %v", group, on, err)
	}
	for {
		if msg := recv(t, c); msg.Type == "panels" || msg.Type == "error" {
			return msg
		}
	}
}

// groupedPanel spawns one panel of the given kind, files it under group and
// returns its id.
func groupedPanel(t *testing.T, c *client.Client, kind, group string) string {
	t.Helper()
	cmd := proto.Command{Action: "panel.create", Kind: kind}
	if kind != "shell" {
		cmd.Path = "/bin/cat" // an agent or a command panel has to name what it runs
	}
	if err := c.Send(cmd); err != nil {
		t.Fatalf("create %s: %v", kind, err)
	}
	panels := recvType(t, c, "panels").Panels
	id := panels[len(panels)-1].ID
	if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{id}, Group: group}); err != nil {
		t.Fatalf("group: %v", err)
	}
	recvType(t, c, "panels")
	return id
}

// TestGroupPeers confirms the flag is off for a group nobody switched on, that
// group.peers turns it on and off on the snapshot's own reply, that it is keyed
// on the exact path, and that an empty group name is rejected.
func TestGroupPeers(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	serve(t, server.New(ln, server.WithStateFile(stateF)))

	c := dial(t, sock)
	groupedPanel(t, c, "shell", "work")
	groupedPanel(t, c, "shell", "work/sub")
	if listPeers(t, c, "work") {
		t.Fatalf("a new group must start with peers off")
	}

	// The reply to the flip is the fleet broadcast, and it already carries the flag.
	if msg := setPeers(t, c, "work", true); msg.Type != "panels" || !groupPeers(msg, "work") {
		t.Fatalf("group.peers on should broadcast the flag, got %+v", msg)
	}
	if !listPeers(t, c, "work") {
		t.Fatalf("group work should have peers on")
	}
	// Exact path only: the flag on "work" says nothing about "work/sub".
	if listPeers(t, c, "work/sub") {
		t.Fatalf("peers on a group must not reach its sub-group")
	}

	if msg := setPeers(t, c, "work", false); msg.Type != "panels" || groupPeers(msg, "work") {
		t.Fatalf("group.peers off should broadcast the cleared flag, got %+v", msg)
	}
	if listPeers(t, c, "work") {
		t.Fatalf("group work should have peers off again")
	}

	if msg := setPeers(t, c, "  ", true); msg.Type != "error" {
		t.Fatalf("a blank group name should error, got %+v", msg)
	}
}

// TestGroupPeersPersistsAndRestores confirms the flag survives a save and
// reload: it is written to the snapshot file and a fresh server's Restore
// surfaces it, while a group that never had it stays off.
func TestGroupPeersPersistsAndRestores(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))
	serve(t, srv)

	c := dial(t, sock)
	groupedPanel(t, c, "shell", "work")
	setPeers(t, c, "work", true)
	// A second group with a different setting, so "every restored group is on"
	// cannot pass for a restore.
	if err := c.Send(proto.Command{Action: "group.favourite", Group: "quiet"}); err != nil {
		t.Fatalf("favourite: %v", err)
	}
	recvType(t, c, "panels")

	srv.SaveNow()
	st, err := state.Load(stateF)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	saved := map[string]bool{}
	for _, g := range st.Groups {
		saved[g.Group] = g.Peers
	}
	if on, ok := saved["work"]; !ok || !on {
		t.Fatalf("group peers not persisted: %+v", st.Groups)
	}
	if on, ok := saved["quiet"]; !ok || on {
		t.Fatalf("a group without the flag must be saved without it: %+v", st.Groups)
	}

	// A snapshot is a file and can say anything: give it a peers-on group none of
	// its panels sit in, which a restore must not bring back switched on.
	st.Groups = append(st.Groups, state.GroupLayout{Group: "ghost", Peers: true})
	if err := st.Save(stateF); err != nil {
		t.Fatalf("save edited state: %v", err)
	}

	ln2, sock2, _ := listen(t)
	srv2 := server.New(ln2, server.WithStateFile(stateF))
	srv2.Restore()
	serve(t, srv2)

	c2 := dial(t, sock2)
	if !listPeers(t, c2, "work") {
		t.Fatalf("group peers not restored")
	}
	if listPeers(t, c2, "quiet") {
		t.Fatalf("a group saved with peers off must restore off")
	}
	if listPeers(t, c2, "ghost") {
		t.Fatalf("a persisted flag on a group with no panels must not restore")
	}
}

// TestGroupPeersFollowsRename checks renameGroup carries the flag to the new
// name and takes it off the old one. This is the pure rename only; that a name
// which EMPTIES some other way loses the flag is TestGroupPeersDropsWithLastMember.
func TestGroupPeersFollowsRename(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	serve(t, server.New(ln, server.WithStateFile(stateF)))

	c := dial(t, sock)
	groupedPanel(t, c, "shell", "old")
	setPeers(t, c, "old", true)

	if err := c.Send(proto.Command{Action: "panel.rename", Group: "old", Name: "new"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	snap := recvType(t, c, "panels")
	if !groupPeers(snap, "new") {
		t.Fatalf("peers should follow the rename to new: %+v", snap.Groups)
	}
	if groupPeers(snap, "old") {
		t.Fatalf("old name should no longer carry peers: %+v", snap.Groups)
	}
}

// TestGroupPeersMergeNeverWidens pins the rule that the flag follows a pure
// rename only. Opening the channel is the operator's decision per group, so a
// peers-on group folded into one that already had members of its own and was
// off must come out OFF — by a rename onto it or by a nested dissolve into it —
// while a destination the operator had already switched on stays on.
func TestGroupPeersMergeNeverWidens(t *testing.T) {
	cases := []struct {
		name   string
		dest   string // the populated group the peers-on one lands in
		src    string // the peers-on group
		destOn bool
		merge  proto.Command
		want   bool
	}{
		{"rename onto peers-off", "y", "x", false,
			proto.Command{Action: "panel.rename", Group: "x", Name: "y"}, false},
		{"rename onto peers-on", "y", "x", true,
			proto.Command{Action: "panel.rename", Group: "x", Name: "y"}, true},
		{"nested dissolve into peers-off parent", "a", "a/b", false,
			proto.Command{Action: "panel.ungroup", Group: "a/b"}, false},
		{"nested dissolve into peers-on parent", "a", "a/b", true,
			proto.Command{Action: "panel.ungroup", Group: "a/b"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHELL", "/bin/sh")
			ln, sock, stateF := listen(t)
			// A rename onto a taken name is refused unless the operator allows
			// name conflicts, so that setting is what makes the merge reachable.
			serve(t, server.New(ln, server.WithStateFile(stateF), server.WithAllowNameConflict(true)))

			c := dial(t, sock)
			groupedPanel(t, c, "shell", tc.dest)
			moved := groupedPanel(t, c, "shell", tc.src)
			setPeers(t, c, tc.src, true)
			if tc.destOn {
				setPeers(t, c, tc.dest, true)
			}

			if err := c.Send(tc.merge); err != nil {
				t.Fatalf("merge: %v", err)
			}
			snap := recvType(t, c, "panels")
			// The merge itself must have happened, or "off" proves nothing.
			if got := panelByID(snap.Panels, moved).Group; got != tc.dest {
				t.Fatalf("panel should have merged into %q, sits in %q", tc.dest, got)
			}
			if got := groupPeers(snap, tc.dest); got != tc.want {
				t.Fatalf("peers on %q after the merge = %v, want %v (%+v)", tc.dest, got, tc.want, snap.Groups)
			}
			if groupPeers(snap, tc.src) {
				t.Fatalf("the merged-away name must not keep peers: %+v", snap.Groups)
			}
		})
	}
}

// TestGroupPeersDissolveIntoEmptyParentKeeps is the other side of the merge
// rule: a parent with no members of its own receives exactly the panels the
// operator switched on, so the flag follows them instead of being dropped.
func TestGroupPeersDissolveIntoEmptyParentKeeps(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	serve(t, server.New(ln, server.WithStateFile(stateF)))

	c := dial(t, sock)
	groupedPanel(t, c, "shell", "a/b")
	setPeers(t, c, "a/b", true)

	if err := c.Send(proto.Command{Action: "panel.ungroup", Group: "a/b"}); err != nil {
		t.Fatalf("ungroup: %v", err)
	}
	if snap := recvType(t, c, "panels"); !groupPeers(snap, "a") {
		t.Fatalf("peers should follow its panels into an empty parent: %+v", snap.Groups)
	}
}

// TestGroupPeersDissolveDropsFlag checks that dissolving a group removes its
// flag, so the name does not come back peers-on when it is reused.
func TestGroupPeersDissolveDropsFlag(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	serve(t, server.New(ln, server.WithStateFile(stateF)))

	c := dial(t, sock)
	groupedPanel(t, c, "shell", "doomed")
	setPeers(t, c, "doomed", true)

	if err := c.Send(proto.Command{Action: "panel.ungroup", Group: "doomed"}); err != nil {
		t.Fatalf("ungroup: %v", err)
	}
	// No group at all, not just not "doomed": the panel is ungrouped now, and a
	// flag re-keyed onto the empty path would switch on every lone panel.
	for _, g := range recvType(t, c, "panels").Groups {
		if g.Peers {
			t.Fatalf("a dissolved group must not leave peers on anything, got %+v", g)
		}
	}
}

// TestGroupPeersRefusedToConductor confirms the conductor fence covers the verb
// in both directions: a conductor can neither open the channel nor close one
// the operator opened, and the refused command changes nothing.
func TestGroupPeersRefusedToConductor(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	serve(t, server.New(ln, server.WithStateFile(stateF)))

	cockpit := dial(t, sock)
	self := groupedPanel(t, cockpit, "shell", "work")
	conductor := dialAs(t, sock, self, "conductor")

	msg := setPeers(t, conductor, "work", true)
	if msg.Type != "error" || !strings.Contains(msg.Error, "conductor role") {
		t.Fatalf("a conductor must be refused group.peers, got %+v", msg)
	}
	if listPeers(t, cockpit, "work") {
		t.Fatalf("a refused group.peers must not turn the flag on")
	}

	setPeers(t, cockpit, "work", true)
	// The cockpit's flip is broadcast to every client; take the conductor's copy
	// off its queue so the reply read next is the one to its own command.
	recvType(t, conductor, "panels")
	if msg := setPeers(t, conductor, "work", false); msg.Type != "error" {
		t.Fatalf("a conductor must be refused turning peers off too, got %+v", msg)
	}
	if !listPeers(t, cockpit, "work") {
		t.Fatalf("a refused group.peers must not turn the flag off")
	}
}

// TestGroupPeersRefusedToAgentSelf confirms the half of the fence that is not
// the conductor's: a connection that declared an AGENT panel as itself — a
// worker shelling out to `baton ctl` — is refused, while one that declared a
// SHELL panel is not, because a shell inside baton is the operator typing.
func TestGroupPeersRefusedToAgentSelf(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	serve(t, server.New(ln, server.WithStateFile(stateF)))

	cockpit := dial(t, sock)
	agentID := groupedPanel(t, cockpit, "agent", "work")
	shellID := groupedPanel(t, cockpit, "shell", "work")

	agent := dialAs(t, sock, agentID, "")
	msg := setPeers(t, agent, "work", true)
	if msg.Type != "error" || !strings.Contains(msg.Error, "agent panel") {
		t.Fatalf("an agent panel must be refused group.peers, got %+v", msg)
	}
	if listPeers(t, cockpit, "work") {
		t.Fatalf("a refused group.peers must not turn the flag on")
	}

	shell := dialAs(t, sock, shellID, "")
	if msg := setPeers(t, shell, "work", true); msg.Type == "error" {
		t.Fatalf("a shell panel should be allowed group.peers, got %q", msg.Error)
	}
	if !listPeers(t, cockpit, "work") {
		t.Fatalf("a shell panel's group.peers should have turned the flag on")
	}
}

// TestGroupPeersEmitsGroupChange pins the plugin event a flip fires: a hook
// watching group.change is how anything outside the cockpit learns a channel
// between agents was opened or closed, so it must name the group and the new
// state — and a refused flip must fire nothing.
func TestGroupPeersEmitsGroupChange(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	srv := server.New(ln, server.WithStateFile(stateF))

	type flip struct {
		group string
		peers bool
	}
	var mu sync.Mutex
	var flips []flip
	srv.SetEventSink(func(name string, fields map[string]any) {
		on, ok := fields["peers"].(bool)
		if name != "group.change" || !ok {
			return
		}
		group, _ := fields["group"].(string)
		mu.Lock()
		defer mu.Unlock()
		flips = append(flips, flip{group, on})
	})
	serve(t, srv)

	c := dial(t, sock)
	groupedPanel(t, c, "shell", "work")
	// The reply follows the emit on the same goroutine, so each read below is
	// also the wait for its event.
	setPeers(t, c, "work", true)
	setPeers(t, c, "work", false)
	if msg := setPeers(t, c, "", true); msg.Type != "error" {
		t.Fatalf("an empty group should be refused, got %+v", msg)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []flip{{"work", true}, {"work", false}}
	if len(flips) != len(want) || flips[0] != want[0] || flips[1] != want[1] {
		t.Fatalf("group.change peers events = %+v, want %+v", flips, want)
	}
}

// TestGroupPeersNeedsMembersToEnable confirms the flag cannot be parked on an
// empty name for whoever is filed there next: switching on a group with no
// direct members is refused, a sub-group's panels do not count, and switching
// OFF a group that is not there stays an allowed no-op.
func TestGroupPeersNeedsMembersToEnable(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	serve(t, server.New(ln, server.WithStateFile(stateF)))

	c := dial(t, sock)
	groupedPanel(t, c, "shell", "a/b")

	for _, group := range []string{"nobody", "a"} {
		msg := setPeers(t, c, group, true)
		if msg.Type != "error" || !strings.Contains(msg.Error, "no panels in group") {
			t.Fatalf("enabling peers on empty group %q should be refused, got %+v", group, msg)
		}
		if listPeers(t, c, group) {
			t.Fatalf("a refused enable must not set the flag on %q", group)
		}
	}
	if msg := setPeers(t, c, "nobody", false); msg.Type != "panels" {
		t.Fatalf("turning peers off for an unknown group should be a no-op, got %+v", msg)
	}
}

// TestGroupPeersDropsWithLastMember is the rule that the flag belongs to the
// panels and not to the name: however a group loses its last direct member,
// the flag goes with it, so a group re-created under that name starts off.
func TestGroupPeersDropsWithLastMember(t *testing.T) {
	// refill files a fresh panel under "work" and reports the flag it comes up with.
	refill := func(t *testing.T, c *client.Client) bool {
		t.Helper()
		groupedPanel(t, c, "shell", "work")
		return listPeers(t, c, "work")
	}
	cases := []struct {
		name  string
		empty func(t *testing.T, c *client.Client, id string)
	}{
		{"ungrouped by id", func(t *testing.T, c *client.Client, id string) {
			if err := c.Send(proto.Command{Action: "panel.ungroup", IDs: []string{id}}); err != nil {
				t.Fatalf("ungroup: %v", err)
			}
			recvType(t, c, "panels")
		}},
		{"regrouped elsewhere", func(t *testing.T, c *client.Client, id string) {
			if err := c.Send(proto.Command{Action: "panel.group", IDs: []string{id}, Group: "other"}); err != nil {
				t.Fatalf("group: %v", err)
			}
			recvType(t, c, "panels")
		}},
		{"closed", func(t *testing.T, c *client.Client, id string) {
			if err := c.Send(proto.Command{Action: "panel.close", ID: id}); err != nil {
				t.Fatalf("close: %v", err)
			}
			recvType(t, c, "panels")
		}},
		{"exited and purged", func(t *testing.T, c *client.Client, id string) {
			if err := c.Send(proto.Command{Action: "panel.input", ID: id, Data: []byte("exit\n")}); err != nil {
				t.Fatalf("input: %v", err)
			}
			deadline := time.After(15 * time.Second)
			for exited := false; !exited; {
				select {
				case msg := <-c.Events:
					exited = msg.Type == "panels" && panelByID(msg.Panels, id).State == "exited"
				case <-deadline:
					t.Fatal("timed out waiting for the panel to exit")
				}
			}
			// Exited is still a member: the card is on the dashboard in that group.
			if !listPeers(t, c, "work") {
				t.Fatalf("an exited member should still hold the flag")
			}
			if err := c.Send(proto.Command{Action: "panel.purge"}); err != nil {
				t.Fatalf("purge: %v", err)
			}
			recvType(t, c, "panels")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHELL", "/bin/sh")
			ln, sock, stateF := listen(t)
			serve(t, server.New(ln, server.WithStateFile(stateF)))

			c := dial(t, sock)
			id := groupedPanel(t, c, "shell", "work")
			setPeers(t, c, "work", true)
			if !listPeers(t, c, "work") {
				t.Fatalf("setup: peers should be on")
			}

			tc.empty(t, c, id)
			if refill(t, c) {
				t.Fatalf("a re-created group must not inherit the emptied one's peers flag")
			}
		})
	}
}

// TestGroupPeersNotInheritedByAgentRegroup is the hole the rule above closes,
// driven the way it would be reached: the operator switches "work" on, the
// group empties, and an AGENT — which may not flip the flag, but may file
// panels into groups like anyone else — re-creates "work" around itself and
// another panel. It must not come up able to type into that panel.
func TestGroupPeersNotInheritedByAgentRegroup(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	serve(t, server.New(ln, server.WithStateFile(stateF)))

	cockpit := dial(t, sock)
	only := groupedPanel(t, cockpit, "shell", "work")
	agentID := groupedPanel(t, cockpit, "agent", "elsewhere")
	victim := groupedPanel(t, cockpit, "shell", "elsewhere")
	setPeers(t, cockpit, "work", true)
	if err := cockpit.Send(proto.Command{Action: "panel.ungroup", IDs: []string{only}}); err != nil {
		t.Fatalf("ungroup: %v", err)
	}
	recvType(t, cockpit, "panels")

	agent := dialAs(t, sock, agentID, "")
	if err := agent.Send(proto.Command{Action: "panel.group", IDs: []string{agentID, victim}, Group: "work"}); err != nil {
		t.Fatalf("agent regroup: %v", err)
	}
	snap := recvType(t, agent, "panels")
	if panelByID(snap.Panels, victim).Group != "work" {
		t.Fatalf("setup: the agent's regroup should have landed, got %+v", snap.Panels)
	}
	if groupPeers(snap, "work") {
		t.Fatalf("a group an agent re-created must not inherit peers: %+v", snap.Groups)
	}
}

// TestGroupPeersNestedDissolveRekeys pins the case the merge rule must NOT
// catch: dissolving a group whose child is peers-on re-keys the child onto the
// dissolved group's own path. The panels sitting on that path are moving out
// in the same step, so nobody is there to be widened onto and the flag follows.
func TestGroupPeersNestedDissolveRekeys(t *testing.T) {
	cases := []struct {
		name     string
		dissolve string // has a direct member of its own, peers off
		child    string // peers on; re-keyed onto dissolve's path
		parent   string // where dissolve's own member lands ("" = ungrouped)
	}{
		{"nested", "a/b", "a/b/b", "a"},
		{"top level", "a", "a/a", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHELL", "/bin/sh")
			ln, sock, stateF := listen(t)
			serve(t, server.New(ln, server.WithStateFile(stateF)))

			c := dial(t, sock)
			own := groupedPanel(t, c, "shell", tc.dissolve)
			kid := groupedPanel(t, c, "shell", tc.child)
			setPeers(t, c, tc.child, true)

			if err := c.Send(proto.Command{Action: "panel.ungroup", Group: tc.dissolve}); err != nil {
				t.Fatalf("ungroup: %v", err)
			}
			snap := recvType(t, c, "panels")
			if got := panelByID(snap.Panels, kid).Group; got != tc.dissolve {
				t.Fatalf("the child's panel should now sit in %q, sits in %q", tc.dissolve, got)
			}
			if got := panelByID(snap.Panels, own).Group; got != tc.parent {
				t.Fatalf("the dissolved group's own panel should sit in %q, sits in %q", tc.parent, got)
			}
			if !groupPeers(snap, tc.dissolve) {
				t.Fatalf("peers should follow the child onto %q: %+v", tc.dissolve, snap.Groups)
			}
			if groupPeers(snap, tc.child) || groupPeers(snap, tc.parent) {
				t.Fatalf("peers must sit on %q alone: %+v", tc.dissolve, snap.Groups)
			}
		})
	}
}

// TestGroupPeersAllowedToNonAgentSelf pins the branches of the self check that
// let a caller through. The refusal is for a self that RESOLVES to an agent:
// an id naming no live panel has no kind to read (and refusing it would only
// teach a caller to declare nothing — a self is declared, not proven), and a
// command panel is, like a shell, not an agent.
func TestGroupPeersAllowedToNonAgentSelf(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ln, sock, stateF := listen(t)
	serve(t, server.New(ln, server.WithStateFile(stateF)))

	cockpit := dial(t, sock)
	commandID := groupedPanel(t, cockpit, "command", "work")
	if kind := panelByID(recvList(t, cockpit), commandID).Kind; kind != "command" {
		t.Fatalf("setup: want a command-kind panel, got %q", kind)
	}

	for _, self := range []string{"no-such-panel", commandID} {
		c := dialAs(t, sock, self, "")
		// Read off the caller's own reply: the cockpit's queue also holds these
		// broadcasts, so a snapshot read there could be a flip behind.
		if msg := setPeers(t, c, "work", true); msg.Type == "error" || !groupPeers(msg, "work") {
			t.Fatalf("self %q should be allowed to turn peers on, got %+v", self, msg)
		}
		if msg := setPeers(t, c, "work", false); msg.Type == "error" || groupPeers(msg, "work") {
			t.Fatalf("self %q should be allowed to turn peers off, got %+v", self, msg)
		}
		_ = c.Close()
	}
}

// recvList asks for a fresh snapshot and returns its panels.
func recvList(t *testing.T, c *client.Client) []proto.Panel {
	t.Helper()
	if err := c.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	return recvType(t, c, "panels").Panels
}
