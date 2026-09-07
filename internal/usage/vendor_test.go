package usage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The three states are the product. This holds each to the thing that
// distinguishes it, and in particular holds the two no-figure states to carrying
// a reason — a state with no reason renders as a blank line, which reads as a
// broken cockpit rather than as an honest answer.
func TestReportStatesAreDistinguishable(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	absent := Report(ctx, VendorCandidate{Name: "gemini", Missing: true}, time.Hour, now)
	if absent.State != VendorAbsent {
		t.Errorf("a missing command reported %q, want %q", absent.State, VendorAbsent)
	}

	// codex is a catalogue name with no reader, and is present on this test's
	// machine by construction.
	noSource := Report(ctx, VendorCandidate{Name: "codex"}, time.Hour, now)
	if noSource.State != VendorNoSource {
		t.Errorf("an installed vendor with no reader reported %q, want %q", noSource.State, VendorNoSource)
	}

	for _, r := range []VendorReport{absent, noSource} {
		if r.Reason == "" {
			t.Errorf("%s is in state %q with no reason; the cockpit would print a blank line", r.Vendor, r.State)
		}
		if r.Tokens != 0 || r.CostUSD != 0 {
			t.Errorf("%s has no reading but carries %d tokens / $%v", r.Vendor, r.Tokens, r.CostUSD)
		}
		if len(r.Windows) != 0 {
			t.Errorf("%s has no reading but carries %d windows; that is the zero bar this design refuses",
				r.Vendor, len(r.Windows))
		}
	}

	if absent.Reason == noSource.Reason {
		t.Errorf("absent and no-source give the same reason %q; an operator cannot tell "+
			"'not installed' from 'installed, unreadable'", absent.Reason)
	}
}

// An installed vendor baton CAN read reports a reading, and the reading is a real
// zero rather than the absence of one. This is the distinction the whole file
// exists for, stated from the other side.
func TestReportReadingIsARealZero(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	r := Report(context.Background(), VendorCandidate{Name: "claude"}, time.Hour, now)
	if r.State != VendorReading {
		t.Fatalf("state = %q, want %q — an empty projects tree is a reading of zero, not a missing source",
			r.State, VendorReading)
	}
	if r.Reason != "" {
		t.Errorf("a reading carries reason %q; a reason means there is no figure", r.Reason)
	}
	if r.Source == "" {
		t.Error("a reading names no source")
	}
}

// A vendor whose command is missing must not be scanned even when baton has a
// reader for the name. The reader would look at a directory the uninstalled CLI
// never wrote and find nothing, and "nothing" would render as "spent nothing".
func TestAMissingCommandIsNotScanned(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	proj := filepath.Join(root, "projects", "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	// The reader runs on the wall clock, so the fixture is written against it: a
	// message a minute ago is inside whatever window is open right now.
	now := time.Now()
	line := `{"timestamp":"` + now.Add(-time.Minute).Format(time.RFC3339) + `","requestId":"r1",` +
		`"message":{"id":"m1","model":"claude-sonnet-4","usage":{"input_tokens":1000,"output_tokens":100}}}`
	if err := os.WriteFile(filepath.Join(proj, "s.jsonl"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The same vendor, with and without the command on the machine.
	present := Report(context.Background(), VendorCandidate{Name: "claude"}, time.Hour, now)
	if present.Tokens == 0 {
		t.Fatal("the fixture did not produce a reading; the rest of this test proves nothing")
	}
	missing := Report(context.Background(), VendorCandidate{Name: "claude", Missing: true}, time.Hour, now)
	if missing.State != VendorAbsent {
		t.Errorf("state = %q, want %q", missing.State, VendorAbsent)
	}
	if missing.Tokens != 0 {
		t.Errorf("a vendor reported missing still carried %d tokens; its directory was scanned anyway", missing.Tokens)
	}
}

// Registration is the whole cost of a new vendor. If this list and the readers
// ever disagree, one of them is a lie.
func TestEveryRegisteredVendorBuildsAReader(t *testing.T) {
	names := VendorsWithReaders()
	if len(names) == 0 {
		t.Fatal("no vendor has a reader")
	}
	for _, n := range names {
		if !HasVendorReader(n) {
			t.Errorf("%s is listed but HasVendorReader says no", n)
		}
		p, ok := VendorReader(n, time.Hour)
		if !ok || p == nil {
			t.Errorf("%s is listed but builds no reader", n)
			continue
		}
		if p.Source() == "" {
			t.Errorf("%s builds a reader that names no source", n)
		}
	}
	if HasVendorReader("codex") {
		t.Error("codex has a reader; this test's premise elsewhere is that it does not")
	}
}

// failing is a Provider whose scan always fails, so the report builder's failure
// branch can be reached without breaking a real reader.
type failing struct{}

func (failing) Source() string { return "failing" }
func (failing) Fetch(context.Context) (Snapshot, error) {
	return Snapshot{}, errors.New("disk on fire")
}

// A reader that exists but failed is not "no reader" and not "spent nothing". The
// reason has to say what happened, or the operator reads a scan failure as an
// idle account.
func TestAFailedScanSaysSoRatherThanReportingZero(t *testing.T) {
	prev := vendorReaders["claude"]
	vendorReaders["claude"] = func(time.Duration) Provider { return failing{} }
	t.Cleanup(func() { vendorReaders["claude"] = prev })

	r := Report(context.Background(), VendorCandidate{Name: "claude"}, time.Hour, time.Now())
	if r.State == VendorReading {
		t.Fatal("a failed scan reported a reading")
	}
	if r.Tokens != 0 {
		t.Errorf("a failed scan carried %d tokens", r.Tokens)
	}
	if r.Reason == reasonNoSource {
		t.Error("a failed scan is reported as 'no usage source'; baton has one, it did not work")
	}
	if r.Reason == "" {
		t.Fatal("a failed scan carries no reason")
	}
}

// A snapshot with no window in progress contributes no window row, rather than a
// row resting at zero.
func TestNoWindowMeansNoWindowRow(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	if got := snapshotWindows(Snapshot{Input: 10}, now); got != nil {
		t.Errorf("a snapshot with no reset produced %d window rows, want none", len(got))
	}
	open := Snapshot{
		Input:  10,
		Since:  now.Add(-time.Hour),
		Until:  now.Add(time.Hour),
		Resets: true,
	}
	rows := snapshotWindows(open, now)
	if len(rows) != 1 {
		t.Fatalf("an open window produced %d rows, want 1", len(rows))
	}
	if rows[0].Fraction < 0.49 || rows[0].Fraction > 0.51 {
		t.Errorf("half-elapsed window reported fraction %v, want ~0.5", rows[0].Fraction)
	}
	if !rows[0].ResetsAt.Equal(open.Until) {
		t.Errorf("window resets at %v, want %v", rows[0].ResetsAt, open.Until)
	}
}
