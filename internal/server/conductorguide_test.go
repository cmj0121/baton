package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/paths"
)

// legitimateGuide is the largest operator brief these tests claim anyone writes,
// as a figure rather than as maxConductorGuide-minus-something so a cap narrowed
// to where it bites is caught. Sixteen kibibytes is a long page of standing
// orders — several times any CONDUCTOR.md in this repo's docs.
const legitimateGuide = 16 << 10

// writeGuide points $HOME at a temp dir and leaves a CONDUCTOR.md of n bytes in
// it, carrying a marker the assertions look for.
func writeGuide(t *testing.T, marker string, n int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".baton"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := marker + "\n" + strings.Repeat("p", n-len(marker)-1)
	if err := os.WriteFile(paths.ConductorFile(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAnOversizedConductorGuideIsDropped: writeConductorFiles reads this file on
// every conductor spawn and respawn, and a spawn is a wire action — so what the
// daemon reads here is asked for by a peer. It is amplified on the way out too:
// the guide is appended to a copy of the primer and then written to three files
// in the workspace.
//
// Dropped whole rather than cut. Half an operator's instructions, ending
// mid-sentence and handed to an agent as its standing orders, is worse than the
// primer on its own.
func TestAnOversizedConductorGuideIsDropped(t *testing.T) {
	writeGuide(t, "MISSION-MARKER", maxConductorGuide+1)

	b := string(conductorBriefing("p1"))
	if strings.Contains(b, "MISSION-MARKER") {
		t.Fatal("an oversized brief was appended to the conductor's briefing")
	}
	// The primer is what survives, so the conductor still knows how to drive.
	if !strings.Contains(b, "You are the baton conductor") {
		t.Fatal("dropping the brief took the built-in primer with it")
	}
	if len(b) > 1<<20 {
		t.Fatalf("the briefing is %d bytes; the guide was read after all", len(b))
	}
}

// TestAnOrdinaryConductorGuideIsUsed is the other half: a long page of standing
// orders has to reach the agent, or the cap has silently taken the operator's
// mission away.
func TestAnOrdinaryConductorGuideIsUsed(t *testing.T) {
	writeGuide(t, "MISSION-MARKER", legitimateGuide)

	b := string(conductorBriefing("p1"))
	if !strings.Contains(b, "MISSION-MARKER") {
		t.Fatalf("a %d-byte brief should be used", legitimateGuide)
	}
}
