package tui

import (
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
)

// The 50-panel scale check.
//
// The issue promises that "a fleet of 50 is no harder than a fleet of 5", and
// nothing else in this feature produces evidence for it. TestInboxAt50 is that
// evidence, expressed as the one number the promise reduces to: clearing a queue
// costs no screen swaps. Thirty rows, thirty acknowledgements, ZERO attaches.
//
// That last assertion is the whole design decision behind §4.4 made testable. An
// inbox that attached to reply would have moved the cost of handling an item, not
// removed it — twenty rows would be twenty attach/detach cycles, twenty replay
// flushes, and twenty repaints, which is exactly the "navigate, enter, read, type,
// C-t d" the queue exists to replace.

// fleetOf50 is a realistic fleet at the size this feature was built for: thirty
// panels wanting a human, spread over all four buckets, and twenty getting on
// with their work.
func fleetOf50() []proto.Panel {
	var out []proto.Panel
	add := func(id int, state string, ago time.Duration, code int) {
		p := wire(itoa(id), state, ago)
		p.ExitCode = code
		out = append(out, p)
	}
	for i := 1; i <= 12; i++ { // the ones actually asking
		add(i, "attention", time.Duration(i)*time.Minute, 0)
	}
	for i := 13; i <= 20; i++ {
		add(i, "stuck", time.Duration(i)*time.Minute, 0)
	}
	for i := 21; i <= 25; i++ {
		add(i, "exited", time.Duration(i)*time.Minute, 1) // failed
	}
	for i := 26; i <= 30; i++ {
		add(i, "done", time.Duration(i)*time.Minute, 0)
	}
	for i := 31; i <= 50; i++ { // busy, quiet, or finished cleanly: not the queue's business
		add(i, []string{"running", "idle", "exited"}[i%3], time.Minute, 0)
	}
	return out
}

// itoa keeps the fleet builder readable without pulling strconv into a test that
// otherwise has no numbers in it.
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// TestInboxAt50 is the issue's promise as a test.
func TestInboxAt50(t *testing.T) {
	fleet := fleetOf50()
	c, cmds := recordingServer(t)
	m := inboxModel(fleet...)
	m.client = c
	out, _ := m.openInbox()
	m = out.(model)

	if len(m.inboxRows) != 30 {
		t.Fatalf("30 of 50 panels should want a human, got %d", len(m.inboxRows))
	}
	// The buckets hold: twelve questions, then eight wedges, then five failures,
	// then five finished turns — never a finished turn above a question.
	wantBuckets := []int{12, 8, 5, 5}
	at := 0
	for b, n := range wantBuckets {
		for i := range n {
			row := m.inboxRows[at+i]
			p := proto.Panel{ID: row.id, State: row.state.String(), ExitCode: row.code}
			if got, ok := inboxQualifies(p, true); !ok || got != b {
				t.Fatalf("row %d (%s) is in bucket %d, want %d", at+i, row.id, got, b)
			}
		}
		at += n
	}
	// Oldest first WITHIN each bucket, all thirty rows of it. Asserting only the
	// first row would pass a queue that had the right buckets in the right order
	// and every age inside them backwards, which is the half of the rule a human
	// actually feels: the thing that has been waiting longest is the thing they
	// should be looking at.
	//
	// The fleet gives panel n an age of n minutes, so each bucket counts DOWN from
	// its highest id: 12…1 questions, 20…13 wedges, 25…21 failures, 30…26 finished.
	var want []string
	for _, b := range [][2]int{{12, 1}, {20, 13}, {25, 21}, {30, 26}} {
		for id := b[0]; id >= b[1]; id-- {
			want = append(want, itoa(id))
		}
	}
	if got := rowIDs(m); !eqIDs(got, want...) {
		t.Fatalf("queue order\n got %v\nwant %v", got, want)
	}

	// A telemetry tick over the whole fleet, folded into the open queue, must not
	// allocate per row beyond the one map and the one growing slice it needs. The
	// bound is loose on purpose — this guards against a quadratic or per-row-alloc
	// regression, not against a byte.
	if got := testing.AllocsPerRun(5, func() { m.reconcileInbox() }); got > 120 {
		t.Errorf("reconciling a 50-panel tick allocated %.0f times, want a bounded handful", got)
	}
	if len(m.inboxRows) != 30 {
		t.Fatalf("reconciling must not change the queue, got %d rows", len(m.inboxRows))
	}

	// Now clear the whole thing: thirty presses of x, each one row.
	for i := range 30 {
		if len(m.inboxRows) != 30-i {
			t.Fatalf("press %d: expected %d rows left, got %d", i, 30-i, len(m.inboxRows))
		}
		m = press(m, "x")
	}
	if len(m.inboxRows) != 0 {
		t.Fatalf("the queue should be empty, got %v", rowIDs(m))
	}

	acks, attaches, others := drainCommands(t, cmds, 30)
	if acks != 30 {
		t.Errorf("clearing 30 rows should issue exactly 30 panel.ack, got %d", acks)
	}
	if attaches != 0 {
		t.Fatalf("clearing the queue must cost ZERO panel.attach — that is the whole promise; got %d", attaches)
	}
	// The only other traffic is the tail pulls, one per row the cursor rested on,
	// which is the pull-on-demand detail pane doing exactly what it was designed to.
	for action, n := range others {
		if action != "panel.tail" {
			t.Errorf("unexpected traffic while clearing the queue: %d × %s", n, action)
		}
	}
}

// drainCommands waits for at least wantAcks acknowledgements and then tallies
// everything the cockpit sent, by action.
func drainCommands(t *testing.T, cmds <-chan proto.Command, wantAcks int) (acks, attaches int, others map[string]int) {
	t.Helper()
	others = map[string]int{}
	deadline := time.After(5 * time.Second)
	for acks < wantAcks {
		select {
		case c := <-cmds:
			switch c.Action {
			case "panel.ack":
				acks++
			case "panel.attach":
				attaches++
			default:
				others[c.Action]++
			}
		case <-deadline:
			t.Fatalf("only %d of %d acknowledgements arrived", acks, wantAcks)
			return
		}
	}
	// Anything still queued is counted too, so a stray attach cannot hide behind
	// the last ack.
	for {
		select {
		case c := <-cmds:
			switch c.Action {
			case "panel.ack":
				acks++
			case "panel.attach":
				attaches++
			default:
				others[c.Action]++
			}
		default:
			return acks, attaches, others
		}
	}
}
