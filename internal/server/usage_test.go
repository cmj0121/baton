package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/usage"
)

// stubUsage is a Provider that returns a fixed snapshot and error, so the server's
// usage plumbing can be tested without touching the filesystem or the network.
type stubUsage struct {
	snap usage.Snapshot
	err  error
}

func (s stubUsage) Fetch(context.Context) (usage.Snapshot, error) { return s.snap, s.err }
func (s stubUsage) Source() string                                { return "stub" }

func newUsageServer(p usage.Provider) *Server {
	return &Server{
		usageProvider: p,
		usageInterval: time.Second,
		clients:       make(map[*clientConn]struct{}),
	}
}

// TestRefreshUsageHoldsFormatted: a successful fetch is formatted and held, and
// usageMsg serves the held value as a "usage" message (the hello seed).
func TestRefreshUsageHoldsFormatted(t *testing.T) {
	srv := newUsageServer(stubUsage{snap: usage.Snapshot{Input: 1_000_000, CostUSD: 5}})
	srv.refreshUsage()

	if srv.usageText != "1.0M tok · ≈$5.00 API" {
		t.Fatalf("usageText = %q, want %q", srv.usageText, "1.0M tok · ≈$5.00 API")
	}
	msg := srv.usageMsg()
	if msg.Type != "usage" || msg.Usage != srv.usageText {
		t.Fatalf("usageMsg = %+v, want type usage with the held value", msg)
	}
}

// TestRefreshUsageKeepsLastOnError: a fetch error with nothing to show leaves the
// previously held value untouched — a transient failure must not blank the footer.
func TestRefreshUsageKeepsLastOnError(t *testing.T) {
	srv := newUsageServer(stubUsage{snap: usage.Snapshot{Input: 500_000}})
	srv.refreshUsage()
	held := srv.usageText
	if held == "" {
		t.Fatal("precondition: expected a held value after the first fetch")
	}

	srv.usageProvider = stubUsage{err: errors.New("boom")} // empty snapshot + error
	srv.refreshUsage()
	if srv.usageText != held {
		t.Fatalf("usageText = %q after a failed fetch, want the held %q", srv.usageText, held)
	}
}

// TestRefreshUsagePartial: a fetch that errors but still returns data (e.g. the api
// source got tokens but not cost) updates the footer with what came back.
func TestRefreshUsagePartial(t *testing.T) {
	srv := newUsageServer(stubUsage{
		snap: usage.Snapshot{Input: 2_000_000},
		err:  errors.New("cost report unavailable"),
	})
	srv.refreshUsage()
	if srv.usageText != "2.0M tok" {
		t.Fatalf("usageText = %q, want %q", srv.usageText, "2.0M tok")
	}
}

// TestUsageLoopNilProviderReturns: with no provider the loop is a no-op and returns
// at once, so an unconfigured usage footer costs nothing.
func TestUsageLoopNilProviderReturns(t *testing.T) {
	srv := newUsageServer(nil)
	done := make(chan struct{})
	go func() { srv.usageLoop(make(chan struct{})); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("usageLoop with a nil provider did not return")
	}
}
