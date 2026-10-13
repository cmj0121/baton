package main

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/proto"
)

// TestClassifierLine pins the three lines PLAN.md shows, and the two suffixes,
// so a reworded verb is a deliberate change here rather than a surprise in a
// script that greps for "classifier: on".
func TestClassifierLine(t *testing.T) {
	on := &proto.ClassifierStatus{
		Enabled: true, Scope: "baton-attention", MinConfidence: 0.5, Record: true,
		Mold: &proto.MoldStatus{PID: 4772, Version: "0.3.1", Bundle: "/home/u/.mold/bundle", Keys: 12, Device: "cpu"},
		Rows: 37, Holdout: 12,
	}
	capped := *on
	capped.Capped, capped.Record = true, false

	cases := []struct {
		name string
		st   *proto.ClassifierStatus
		want string
	}{
		{"off", &proto.ClassifierStatus{}, "classifier: off"},
		{"unreachable", &proto.ClassifierStatus{
			Enabled: true, Scope: "baton-attention", Absent: true,
			Error: "dial unix ~/.mold/mold.sock: connect: no such file or directory",
		}, "classifier: unreachable — dial unix ~/.mold/mold.sock: connect: no such file or directory (regex decides)"},
		{"on", on, "classifier: on — mold 0.3.1 pid 4772, bundle /home/u/.mold/bundle, 12 keys, cpu; scope baton-attention ≥ 0.50; rows 37, holdout 12"},
		{"capped and record off", &capped, "classifier: on — mold 0.3.1 pid 4772, bundle /home/u/.mold/bundle, 12 keys, cpu; scope baton-attention ≥ 0.50; rows 37, holdout 12 [capped] record off"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifierLine(tc.st); got != tc.want {
				t.Fatalf("line = %q\nwant   %q", got, tc.want)
			}
		})
	}
}

// TestCtlClassifierOff drives the verb against a real daemon with
// no classifier block, the one situation a test server has for free: the
// client round-trips the typed reply and the verb prints the off line.
func TestCtlClassifierOff(t *testing.T) {
	sock := ctlTestServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	printed := captureStdout(t, func() {
		if err := (ctlClassifierStatus{}).Run(c); err != nil {
			t.Fatalf("ctl classifier status: %v", err)
		}
	})
	if got := strings.TrimSpace(printed); got != "classifier: off" {
		t.Fatalf("printed %q, want the off line", got)
	}
}
