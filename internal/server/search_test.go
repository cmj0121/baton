package server_test

import (
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
)

// TestFleetSearchFindsAcrossPanels drives fleet.search end-to-end: two shells echo
// distinct markers, and a search for one returns a hit in the right panel only.
func TestFleetSearchFindsAcrossPanels(t *testing.T) {
	c := startServer(t)
	ids := createShells(t, c, 2)

	echo(t, c, ids[0], "FLEETMARKER-ALPHA")
	echo(t, c, ids[1], "FLEETMARKER-BETA")

	hits := searchUntil(t, c, "FLEETMARKER-ALPHA")
	if len(hits) == 0 {
		t.Fatal("expected a hit for FLEETMARKER-ALPHA")
	}
	for _, h := range hits {
		if h.Panel != ids[0] {
			t.Fatalf("hit in the wrong panel: got %s, want %s (text %q)", h.Panel, ids[0], h.Text)
		}
		if h.Title == "" {
			t.Fatalf("a hit should carry its panel title, got %+v", h)
		}
	}
}

// TestFleetSearchNoMatchStillReplies checks a search that matches nothing still
// returns a "search" message (with no hits) so the cockpit can say "no matches"
// rather than hang waiting.
func TestFleetSearchNoMatchStillReplies(t *testing.T) {
	c := startServer(t)
	createShells(t, c, 1)

	if err := c.Send(proto.Command{Action: "fleet.search", Query: "NOPE-NOTHING-MATCHES-THIS-XYZ"}); err != nil {
		t.Fatalf("search: %v", err)
	}
	msg := recvType(t, c, "search")
	if len(msg.Hits) != 0 {
		t.Fatalf("expected no hits, got %+v", msg.Hits)
	}
}

// TestFleetSearchEmptyTermErrors checks an empty term is rejected rather than
// scanning the whole fleet for "".
func TestFleetSearchEmptyTermErrors(t *testing.T) {
	c := startServer(t)
	if err := c.Send(proto.Command{Action: "fleet.search", Query: "   "}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if msg := recvType(t, c, "error"); msg.Error == "" {
		t.Fatal("an empty search term should surface an error")
	}
}

// echo types a marker into a panel so a following search reads it from the ring.
func echo(t *testing.T, c *client.Client, id, marker string) {
	t.Helper()
	if err := c.Send(proto.Command{Action: "panel.input", ID: id, Data: []byte("echo " + marker + "\n")}); err != nil {
		t.Fatalf("input: %v", err)
	}
}

// searchUntil resends fleet.search until it returns hits or the deadline passes,
// so the test tolerates the small delay between typing an echo and its output
// landing in the replay ring.
func searchUntil(t *testing.T, c *client.Client, query string) []proto.SearchHit {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("no hits for %q before the deadline", query)
			return nil
		default:
		}
		if err := c.Send(proto.Command{Action: "fleet.search", Query: query}); err != nil {
			t.Fatalf("search: %v", err)
		}
		if msg := recvType(t, c, "search"); len(msg.Hits) > 0 {
			return msg.Hits
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// recvType reads Events until a message of the wanted type arrives, skipping the
// structural snapshots (panels) that may interleave. It fails on the deadline.
func recvType(t *testing.T, c *client.Client, want string) proto.ServerMsg {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case msg, ok := <-c.Events:
			if !ok {
				t.Fatal("event channel closed unexpectedly")
			}
			if msg.Type == want {
				return msg
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %q message", want)
			return proto.ServerMsg{}
		}
	}
}
