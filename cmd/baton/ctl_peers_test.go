package main

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/control"
)

// TestCtlGroupPeers drives `baton ctl group peers <group> on|off` through the
// entry point, and reads the flag back the only way it shows from outside the
// cockpit: whether a panel of that group is allowed its peer list.
func TestCtlGroupPeers(t *testing.T) {
	sock := ctlTestServer(t)
	t.Setenv("BATON_PANEL_ID", "") // the operator's shell: this test may itself run inside a panel
	t.Setenv("BATON_ROLE", "")

	admin, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = admin.Close() }()
	var ids []string
	for range 2 {
		id, err := admin.SpawnPanel("", nil, "")
		if err != nil {
			t.Fatalf("spawn: %v", err)
		}
		ids = append(ids, id)
	}
	if code := ctlMain(append([]string{"group", "work"}, ids...)); code != 0 {
		t.Fatalf("ctl group work %v = %d, want 0", ids, code)
	}

	worker, err := control.DialSocket(sock, "", ids[0], "")
	if err != nil {
		t.Fatalf("dial worker: %v", err)
	}
	defer func() { _ = worker.Close() }()
	peersOn := func() bool {
		t.Helper()
		_, err := worker.PeerList()
		if err != nil && !strings.Contains(err.Error(), "peer interaction is off") {
			t.Fatalf("peer list: %v", err)
		}
		return err == nil
	}
	if peersOn() {
		t.Fatal("setup: peers should be off for a new group")
	}

	if code := ctlMain([]string{"group", "peers", "work", "on"}); code != 0 || !peersOn() {
		t.Fatalf("ctl group peers work on = %d, peers on = %v; want 0 and on", code, peersOn())
	}

	// Anything but on|off is a usage error: non-zero, and nothing changes — in
	// particular it is not read as "file panels work and onn under 'peers'".
	for _, bad := range [][]string{
		{"group", "peers", "work", "onn"},
		{"group", "peers", "work"},
		{"group", "peers", "work", "off", "extra"},
		{"group", "peers", "work", "ON"},
	} {
		if code := ctlMain(bad); code != 1 || !peersOn() {
			t.Fatalf("ctl %v = %d, peers on = %v; want exit 1 and the flag untouched", bad, code, peersOn())
		}
	}

	if code := ctlMain([]string{"group", "peers", "work", "off"}); code != 0 || peersOn() {
		t.Fatalf("ctl group peers work off = %d, peers on = %v; want 0 and off", code, peersOn())
	}

	// The daemon's refusal is a failure of the command, not a usage error.
	if code := ctlMain([]string{"group", "peers", "nobody-here", "on"}); code != 1 {
		t.Fatalf("enabling a group with no panels = %d, want 1", code)
	}
}

// TestCtlGroupPeersParse holds the line between the two verbs that share the
// word, without a daemon.
func TestCtlGroupPeersParse(t *testing.T) {
	for _, tc := range []struct {
		name      string
		in        ctlGroup
		group     string
		on, peers bool
		usage     bool
	}{
		{name: "on", in: ctlGroup{Name: "peers", IDs: []string{"work", "on"}}, group: "work", on: true, peers: true},
		{name: "off", in: ctlGroup{Name: "peers", IDs: []string{"work", "off"}}, group: "work", peers: true},
		{name: "a nested group", in: ctlGroup{Name: "peers", IDs: []string{"backend/api", "on"}}, group: "backend/api", on: true, peers: true},
		{name: "a group named like a panel id", in: ctlGroup{Name: "peers", IDs: []string{"7", "off"}}, group: "7", peers: true},
		// The old verb is untouched: a work item called "peers", and every other name.
		{name: "filing panels under a work item called peers", in: ctlGroup{Name: "peers", IDs: []string{"3", "4"}}},
		{name: "one panel under peers", in: ctlGroup{Name: "peers", IDs: []string{"3"}}},
		{name: "another work item", in: ctlGroup{Name: "work", IDs: []string{"peers", "on"}}},
		{name: "a typo", in: ctlGroup{Name: "peers", IDs: []string{"work", "onn"}}, peers: true, usage: true},
		{name: "no state", in: ctlGroup{Name: "peers", IDs: []string{"work"}}, peers: true, usage: true},
		{name: "too many", in: ctlGroup{Name: "peers", IDs: []string{"work", "on", "3"}}, peers: true, usage: true},
	} {
		group, on, peers, err := tc.in.peers()
		if group != tc.group || on != tc.on || peers != tc.peers || (err != nil) != tc.usage {
			t.Errorf("%s: peers() = %q, %v, %v, %v; want %q, %v, %v, usage=%v", tc.name, group, on, peers, err, tc.group, tc.on, tc.peers, tc.usage)
		}
		if tc.usage && err.Error() != "usage: baton ctl group peers <group> on|off" {
			t.Errorf("%s: the usage error should be the usage line, got %q", tc.name, err)
		}
	}
}
