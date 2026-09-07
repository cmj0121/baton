package usage

import (
	"context"
	"sort"
	"time"
)

// The per-vendor view of account usage.
//
// baton knows six agent CLIs and can read the usage of two of them. That ratio is
// the whole design problem here, and the answer is to say so rather than to hide
// it. An operator looking at a fleet has to be able to tell three things apart:
//
//   - baton read this vendor's books, and here is the figure;
//   - baton can see this vendor is installed and has no way to read its books;
//   - baton knows this vendor and it is not on this machine at all.
//
// A bar at zero says none of those. It says "nothing has been spent", which for
// the middle case is a claim baton has no evidence for and which happens to be
// the most reassuring of the three. So there are no zero bars: a vendor with no
// reading carries a stated reason instead, and the cockpit prints the reason.

// VendorState is what baton can honestly say about one vendor's usage.
type VendorState string

const (
	// VendorReading — the CLI is installed and a reader produced a figure. The
	// figure may be zero, and a zero here is a real zero: it means the reader
	// looked and the window is empty.
	VendorReading VendorState = "reading"

	// VendorNoSource — the CLI is installed and baton has no reader for it. This
	// is the state that must never be drawn as a quota bar.
	VendorNoSource VendorState = "no-source"

	// VendorAbsent — baton knows the name and the command is not on the fleet's
	// machine. Nothing is running under it, so there is nothing to read.
	VendorAbsent VendorState = "absent"
)

// VendorWindow is one usage window in vendor-neutral terms: what to call it, how
// much of it is gone, and when it resets.
//
// It is deliberately not LimitsInfo's shape. Those fields are Anthropic's plan —
// a seven-day Opus ceiling is not a thing grok has — and a per-vendor list built
// on them could only ever describe one vendor. A label, a fraction and an instant
// are what every vendor's windows have in common, and a vendor that reports none
// contributes none rather than four empty ones.
type VendorWindow struct {
	Label    string
	Fraction float64   // 0–1 of the window spent
	ResetsAt time.Time // zero when the vendor states no reset
}

// VendorReport is one vendor's standing: which of the three things baton can say
// about it, the reason when there is no figure, and the figure when there is.
type VendorReport struct {
	Vendor string
	State  VendorState

	// Reason is why there is no reading, in words meant for an operator. It is
	// empty exactly when State is VendorReading, and non-empty otherwise — the
	// cockpit prints it in place of a bar, so a state with no reason would render
	// as a blank line that looks like a rendering bug.
	Reason string

	Source  string // the reader that produced the figure; empty unless VendorReading
	Tokens  int64
	CostUSD float64
	Windows []VendorWindow
}

// vendorReaders is the registry: a catalogue name to the reader that can see its
// usage. Adding a vendor is one vendorFormat and one line here — no wire change,
// no server change, no cockpit change, because everything downstream is driven by
// what this map contains rather than by a list of its own.
//
// A name absent from this map is not a bug and is not a TODO. It is the honest
// state VendorNoSource, and it stays that way until somebody has read the
// vendor's files and found books to keep.
var vendorReaders = map[string]func(window time.Duration) Provider{
	"claude": func(w time.Duration) Provider { return NewLocalProvider(w) },
	"grok":   func(w time.Duration) Provider { return NewGrokProvider(w) },
}

// VendorReader returns the reader for one vendor's usage, and whether baton has
// one at all.
func VendorReader(vendor string, window time.Duration) (Provider, bool) {
	build, ok := vendorReaders[vendor]
	if !ok {
		return nil, false
	}
	return build(window), true
}

// HasVendorReader reports whether baton can read a vendor's usage, without
// building the reader. The cockpit and the report builder both need to ask
// before they have any reason to scan.
func HasVendorReader(vendor string) bool {
	_, ok := vendorReaders[vendor]
	return ok
}

// VendorsWithReaders is every vendor baton can read, sorted. It exists so a test
// and a doc page can both be written against the registry rather than against a
// second hand-maintained list that drifts from it.
func VendorsWithReaders() []string {
	out := make([]string, 0, len(vendorReaders))
	for name := range vendorReaders {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// The reasons a vendor has no figure. They are phrased as what baton did or could
// not do, never as a claim about the vendor's own spend — "baton has no usage
// source for codex" is true and checkable, "codex has used nothing" would be a
// guess wearing the same clothes.
const (
	reasonNotInstalled = "not installed on the fleet's machine"
	reasonNoSource     = "baton has no usage source for this agent"
)

// VendorCandidate is one entry of the catalogue as the caller sees it: the name,
// and whether the machine actually has the command. It is the server's
// already-scanned agent list, reduced to what a usage report needs, so the report
// builder does not have to depend on internal/agents or on proto.
type VendorCandidate struct {
	Name    string
	Missing bool
}

// Report builds one vendor's standing.
//
// Order matters and is not arbitrary. A missing command is reported as absent
// even when baton has a reader for the name, because the reader would be pointed
// at a directory that a CLI which was never installed did not write — and an
// empty scan of an empty directory reads as "nothing spent", which is exactly the
// confusion this whole file exists to prevent.
func Report(ctx context.Context, c VendorCandidate, window time.Duration, now time.Time) VendorReport {
	if c.Missing {
		return VendorReport{Vendor: c.Name, State: VendorAbsent, Reason: reasonNotInstalled}
	}
	p, ok := VendorReader(c.Name, window)
	if !ok {
		return VendorReport{Vendor: c.Name, State: VendorNoSource, Reason: reasonNoSource}
	}
	snap, err := p.Fetch(ctx)
	if err != nil && snap.Empty() {
		// The reader exists and this poll did not work. That is neither "no source"
		// nor "nothing spent", and saying either would be wrong in a way the operator
		// cannot see through, so the failure is the reason.
		return VendorReport{Vendor: c.Name, State: VendorNoSource, Reason: "usage scan failed: " + err.Error()}
	}
	return VendorReport{
		Vendor:  c.Name,
		State:   VendorReading,
		Source:  p.Source(),
		Tokens:  snap.TotalTokens(),
		CostUSD: snap.CostUSD,
		Windows: snapshotWindows(snap, now),
	}
}

// snapshotWindows is the generic window list for a snapshot: the one window the
// local readers can see, and nothing when the snapshot has no reset to speak of.
//
// It reports the window's ELAPSED fraction, not a share of a quota, and the label
// says so. baton is measuring how far through a window of its own reckoning the
// account is; it is not the vendor's statement about a ceiling. Conflating the
// two is how a bar ends up asserting a limit nobody published.
func snapshotWindows(s Snapshot, now time.Time) []VendorWindow {
	spent, ok := s.Spent(now)
	if !ok {
		return nil
	}
	return []VendorWindow{{Label: "window", Fraction: spent, ResetsAt: s.Until}}
}
