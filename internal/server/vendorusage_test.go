package server

import (
	"context"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/usage"
)

// With the feature off, the payload is byte-for-byte what it always was. This is
// the "costs nothing when unconfigured" half of the additive claim.
func TestVendorUsageIsOffWithoutAWindow(t *testing.T) {
	s := &Server{}
	s.agents = []proto.AgentBackend{{Name: "claude", Command: "claude"}}
	if got, _ := s.vendorUsage(context.Background()); got != nil {
		t.Errorf("a server with no usage window produced %d vendor rows, want none", len(got))
	}
}

// Before any detection has run, the daemon must say nothing rather than say
// "there are no agent backends". The wire reads nil as "never said" and an empty
// list as a scan that found nothing, and only one of those is true here.
func TestVendorUsageSaysNothingBeforeDetection(t *testing.T) {
	s := &Server{usageWindow: time.Hour}
	if got, _ := s.vendorUsage(context.Background()); got != nil {
		t.Errorf("a server that has not detected produced %d vendor rows, want nil", len(got))
	}
}

// Every detected backend gets a row, and a backend with no reader gets a reason
// rather than a zero. This is the whole product claim, held on the server side.
func TestEveryDetectedBackendGetsAnHonestRow(t *testing.T) {
	s := &Server{usageWindow: time.Hour}
	s.agents = []proto.AgentBackend{
		{Name: "codex", Command: "codex"},                  // installed, no reader
		{Name: "gemini", Command: "gemini", Missing: true}, // not installed
	}
	rows, _ := s.vendorUsage(context.Background())
	if len(rows) != 2 {
		t.Fatalf("got %d rows for 2 backends", len(rows))
	}
	by := map[string]proto.VendorUsage{}
	for _, r := range rows {
		by[r.Vendor] = r
	}
	if by["codex"].State != "no-source" {
		t.Errorf("codex state = %q, want no-source", by["codex"].State)
	}
	if by["gemini"].State != "absent" {
		t.Errorf("gemini state = %q, want absent", by["gemini"].State)
	}
	for _, name := range []string{"codex", "gemini"} {
		r := by[name]
		if r.Reason == "" {
			t.Errorf("%s has state %q and no reason; the cockpit renders a blank line", name, r.State)
		}
		if r.Tokens != 0 || len(r.Windows) != 0 {
			t.Errorf("%s has no reading but carries %d tokens and %d windows", name, r.Tokens, len(r.Windows))
		}
	}
	if by["codex"].Reason == by["gemini"].Reason {
		t.Error("'installed but unreadable' and 'not installed' give the same reason")
	}
}

// A CLI being installed or removed moves a vendor between two states without
// changing a single number. If the change detector only looked at figures, the
// cockpit would keep showing the old state until something unrelated moved.
func TestAVendorChangingStateIsNews(t *testing.T) {
	absent := []proto.VendorUsage{{Vendor: "grok", State: "absent", Reason: "not installed on the fleet's machine"}}
	present := []proto.VendorUsage{{Vendor: "grok", State: "no-source", Reason: "baton has no usage source for this agent"}}
	if sameVendors(absent, present) {
		t.Error("a vendor moving from absent to no-source read as unchanged; the cockpit would never be told")
	}
	if !sameVendors(absent, absent) {
		t.Error("an unchanged vendor list read as changed; every poll would wake every client")
	}
	if sameVendors(absent, nil) {
		t.Error("a list and no list read as the same statement")
	}
}

// sameUsageInfo has to carry the vendor comparison, or the broadcast gate drops
// vendor news on the floor even though sameVendors would have caught it.
func TestUsageInfoChangeGateSeesVendors(t *testing.T) {
	a := &proto.UsageInfo{Tokens: 10, Vendors: []proto.VendorUsage{{Vendor: "grok", State: "absent"}}}
	b := &proto.UsageInfo{Tokens: 10, Vendors: []proto.VendorUsage{{Vendor: "grok", State: "no-source"}}}
	if sameUsageInfo(a, b) {
		t.Error("two payloads differing only in a vendor's state compared equal; the update is never broadcast")
	}
}

// attachVendors must not edit the value a client is already holding.
func TestAttachVendorsDoesNotMutateTheHeldPayload(t *testing.T) {
	held := &proto.UsageInfo{Tokens: 7}
	out := attachVendors(held, []proto.VendorUsage{{Vendor: "claude", State: "reading"}})
	if held.Vendors != nil {
		t.Error("attachVendors wrote through to the payload the caller held")
	}
	if out == held {
		t.Error("attachVendors returned the same pointer; a client's value would change under it")
	}
	if len(out.Vendors) != 1 || out.Tokens != 7 {
		t.Errorf("attachVendors lost data: %#v", out)
	}
	// A nil list is not a statement, so it leaves the payload exactly as it was.
	if got := attachVendors(held, nil); got != held {
		t.Error("a nil vendor list still rebuilt the payload")
	}
}

// Grok's weekly credit pool is grok's own statement, and it lands only on a
// grok row that already has a reading. Lending it to another vendor, or to a
// grok that is not installed, would print a ceiling next to a name that never
// published one.
func TestGrokWeekQuotaLandsOnlyOnAGrokReading(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	weekEnd := time.Date(2026, 9, 21, 0, 44, 30, 0, time.UTC)
	s := &Server{usageWindow: time.Hour}
	s.grokWeek = func(context.Context) (*usage.Window, bool) {
		return &usage.Window{UsedPercent: 36, ResetsAt: weekEnd}, true
	}
	s.agents = []proto.AgentBackend{
		{Name: "grok", Command: "grok"},
		{Name: "claude", Command: "claude"},
		{Name: "codex", Command: "codex"},
		{Name: "gemini", Command: "gemini", Missing: true},
	}
	by := map[string]proto.VendorUsage{}
	rows, _ := s.vendorUsage(context.Background())
	for _, r := range rows {
		by[r.Vendor] = r
	}
	grok := by["grok"]
	if grok.State != "reading" {
		t.Fatalf("grok state = %q, want reading so the quota has a row to land on", grok.State)
	}
	if len(grok.Windows) == 0 || grok.Windows[0].Label != usage.WindowWeek {
		t.Fatalf("grok windows = %+v, want a 7d quota first", grok.Windows)
	}
	if grok.Windows[0].UsedPercent != 36 {
		t.Errorf("grok week used = %v, want 36", grok.Windows[0].UsedPercent)
	}
	if grok.Windows[0].ResetsAt != weekEnd.Format(time.RFC3339) {
		t.Errorf("grok week reset = %q, want %s", grok.Windows[0].ResetsAt, weekEnd.Format(time.RFC3339))
	}
	for _, name := range []string{"claude", "codex", "gemini"} {
		for _, w := range by[name].Windows {
			if w.Label == usage.WindowWeek {
				t.Errorf("%s carried grok's week window: %+v", name, w)
			}
		}
	}
}
