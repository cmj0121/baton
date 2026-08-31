package score

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkReconcileReparse measures the pass this package cannot afford to make
// expensive: score.md re-read and re-resolved with nothing in it changed.
//
// It runs on the DISPATCH path. Every brief takes a View, and while the operator
// has the file open its mtime sits inside staleWindow, so the fingerprint gate
// gives up and every dispatch parses the whole file again. Whatever folding
// costs, it is paid here, per dispatch, per entry — which is why the fold index
// is built only when the file actually carries a line that might be a repeat,
// and why an entry's folding key is computed where its text is set rather than
// once per pass.
func BenchmarkReconcileReparse(b *testing.B) {
	for _, n := range []int{100, 5000} {
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			s, err := Open(dir, defaultPromoteAt)
			if err != nil {
				b.Fatalf("Open: %v", err)
			}
			b.Cleanup(s.Close)

			var md strings.Builder
			for i := range n {
				fmt.Fprintf(&md, "- the fleet keeps doing the thing number %d\n", i)
			}
			if err := os.WriteFile(filepath.Join(dir, scoreMD), []byte(md.String()), 0o600); err != nil {
				b.Fatalf("write: %v", err)
			}
			// One pass to admit them and write their ids back; the file is left
			// with an mtime inside staleWindow, so every pass below re-parses.
			if _, err := s.Reconcile(); err != nil {
				b.Fatalf("seed reconcile: %v", err)
			}
			if s.Len() != n {
				b.Fatalf("entries = %d, want %d", s.Len(), n)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				d, err := s.Reconcile()
				if err != nil {
					b.Fatalf("Reconcile: %v", err)
				}
				if d != (Delta{}) {
					b.Fatalf("pass changed something: %+v", d)
				}
			}
		})
	}
}

// BenchmarkSubmitFoldLookup measures the other pass this package cannot afford
// to make expensive: deciding whether a submission repeats something the store
// already says.
//
// It runs under the store mutex, so its cost is not merely the submitter's — it
// is every concurrent View's too, including the ones on the dispatch path. And
// it is the one lookup that cannot use Entry.norm: a submission is matched
// against every PRIOR wording as well (invariant I4), and those keys are not
// cached anywhere. Building an index for a single lookup meant normalising every
// alias of every entry into a map that was thrown away, which is why the lookup
// is two linear scans and an allocation-free comparison instead.
//
// The store is filled to maxAliases per entry, which is the cap an operator
// reaches by rewording, not a synthetic worst case.
func BenchmarkSubmitFoldLookup(b *testing.B) {
	dir := b.TempDir()
	s, err := Open(dir, defaultPromoteAt)
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	b.Cleanup(s.Close)

	const n = 5000
	var md strings.Builder
	for i := range n {
		fmt.Fprintf(&md, "- the fleet keeps doing the thing number %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, scoreMD), []byte(md.String()), 0o600); err != nil {
		b.Fatalf("write: %v", err)
	}
	if _, err := s.Reconcile(); err != nil {
		b.Fatalf("seed reconcile: %v", err)
	}
	// Give every entry the full complement of prior wordings, without going
	// through the file: the subject here is the lookup, not the reword.
	s.mu.Lock()
	for i := range s.entries {
		for a := range maxAliases {
			s.entries[i].Aliases = append(s.entries[i].Aliases,
				fmt.Sprintf("the fleet used to do the thing number %d in way %d", i, a))
		}
	}
	s.mu.Unlock()

	// A wording nothing owns, so the scan runs to the end — the cost every
	// genuinely new submission pays.
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s.mu.Lock()
		if at := s.foldTargetLocked("something the fleet has never once said"); at >= 0 {
			b.Fatalf("folded into %d, want no target", at)
		}
		s.mu.Unlock()
	}
}
