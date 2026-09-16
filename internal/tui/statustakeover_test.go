package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// barModel is a cockpit whose footer has everything in it: a clock to lose, an
// endpoint to rest on, and the bindings the hint region reads.
func barModel(width int, status string) model {
	return model{
		width: width, height: 30, endpoint: "local", status: status,
		now:   time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t",
	}
}

// The resting line is the endpoint and nothing else. The cap it sits on is the
// connection indicator already — green when the backend answers — so the word
// beside it bought nothing and cost the strip's scarcest resource.
func TestTheRestingFooterIsTheEndpointAlone(t *testing.T) {
	foot := ansi.Strip(barModel(120, "").footer())
	m := barModel(120, "")
	if !strings.Contains(ansi.Strip(m.restingStatus()), "local") {
		t.Fatalf("the resting line lost the endpoint: %q", m.restingStatus())
	}
	if strings.Contains(m.restingStatus(), "attached") {
		t.Errorf("the resting line still names the connection in words: %q", m.restingStatus())
	}
	if strings.Contains(foot, "attached") {
		t.Errorf("the footer still names the connection in words:\n%s", foot)
	}
}

// longErr is a daemon error of the shape that motivated this: git's own stderr,
// folded onto one line, arriving in a strip that had a dozen cells left.
const longErr = "error: push failed: remote: permission denied to cmj0121/baton.git · fatal: unable to access the repository"

// A message too long for the room beside the caps takes the whole row. Clipped at
// the old budget it said "error: push fail…", which names neither the operation
// nor the reason.
func TestAMessageTooLongForTheStripTakesTheWholeRow(t *testing.T) {
	// Wide enough that the row can hold the message and narrow enough that the
	// space beside the caps cannot. A footer never wraps — a message longer than
	// the terminal is still elided — so the two bounds are what this tests.
	foot := ansi.Strip(barModel(120, longErr).footer())
	if lines := strings.Count(foot, "\n"); lines != 0 {
		t.Fatalf("the footer wrapped onto %d extra rows:\n%s", lines, foot)
	}
	for _, want := range []string{"permission denied", "unable to access the repository"} {
		if !strings.Contains(foot, want) {
			t.Errorf("the footer dropped %q from the message:\n%s", want, foot)
		}
	}
	if strings.Contains(foot, "12:00:00") {
		t.Errorf("the clock held its cells while the message was clipped:\n%s", foot)
	}
}

// A message that fits changes nothing. Rearranging the whole strip every time the
// status changed would be its own kind of noise, and the caps are what the strip
// is for the rest of the time.
func TestAMessageThatFitsLeavesTheStripAlone(t *testing.T) {
	foot := ansi.Strip(barModel(120, "panel closed").footer())
	if !strings.Contains(foot, "panel closed") {
		t.Fatalf("the footer lost the message:\n%s", foot)
	}
	if !strings.Contains(foot, "12:00:00") {
		t.Errorf("a message that fits should not have cost the clock its place:\n%s", foot)
	}
	if !strings.Contains(foot, "DASHBOARD") {
		t.Errorf("a message that fits should not have cost the mode cap:\n%s", foot)
	}
}

// A run in the air keeps the strip. The keys typed so far are shown nowhere else
// on the screen, and the next keystroke depends on reading them.
func TestAKeyRunOutranksTheMessage(t *testing.T) {
	m := barModel(100, longErr)
	m.pending = []string{"g"}
	foot := ansi.Strip(m.footer())
	if !strings.Contains(foot, "…") {
		t.Errorf("the run in the air was covered by the message:\n%s", foot)
	}
	if strings.Contains(foot, "unable to access the repository") {
		t.Errorf("the message took the row while a run was open:\n%s", foot)
	}
}

// An outage keeps the strip for the same reason: while it stands, every other
// reading on the screen is stale, and the cap saying so must not be covered.
func TestAnOutageOutranksTheMessage(t *testing.T) {
	m := barModel(100, longErr)
	m.backendDown = true
	if foot := ansi.Strip(m.footer()); !strings.Contains(foot, "BACKEND DOWN") {
		t.Errorf("the outage cap was covered by the message:\n%s", foot)
	}
}

// An error never clears, so without this it would hold the row for the rest of
// the session and the cockpit would never show a clock again.
func TestAnAgedMessageGivesTheRowBack(t *testing.T) {
	m := barModel(100, longErr)
	m.statusAge = statusTTL
	if foot := ansi.Strip(m.footer()); !strings.Contains(foot, "12:00:00") {
		t.Errorf("a message past its TTL is still holding the row:\n%s", foot)
	}
}

// The two things ageStatus times are different: how long an error stays in the
// strip, which is forever, and how long it holds the strip to itself, which is
// statusTTL like everything else.
func TestAnErrorAgesWithoutClearing(t *testing.T) {
	m := barModel(100, longErr)
	for range statusTTL + 1 {
		m.ageStatus()
	}
	if m.status != longErr {
		t.Errorf("the error cleared itself: %q", m.status)
	}
	if m.statusAge < statusTTL {
		t.Errorf("the error's age froze at %d; it would be fresh forever", m.statusAge)
	}
	if foot := ansi.Strip(m.footer()); !strings.Contains(foot, "12:00:00") {
		t.Errorf("an aged error is still holding the whole row:\n%s", foot)
	}
}

// A one-off message still goes back to the resting line on its own.
func TestAnOrdinaryMessageStillFades(t *testing.T) {
	m := barModel(100, "panel closed")
	for range statusTTL + 1 {
		m.ageStatus()
	}
	if m.status != "local" {
		t.Errorf("status = %q, want the footer to have settled back to the endpoint", m.status)
	}
}
