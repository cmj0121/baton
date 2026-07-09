package usage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// apiTestServer stands in for the Anthropic Admin API. The usage endpoint is
// served in two pages (has_more → next_page) so the pagination loop is exercised;
// the cost endpoint returns a single page. A page= query selects the second page.
func apiTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/organizations/usage_report/messages":
			if r.URL.Query().Get("page") == "p2" {
				_, _ = w.Write([]byte(`{"data":[{"results":[{"uncached_input_tokens":200,"output_tokens":100}]}],"has_more":false}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"results":[{"uncached_input_tokens":100,"cache_read_input_tokens":10,"output_tokens":50,"cache_creation":{"ephemeral_5m_input_tokens":5,"ephemeral_1h_input_tokens":2}}]}],"has_more":true,"next_page":"p2"}`))
		case "/v1/organizations/cost_report":
			_, _ = w.Write([]byte(`{"data":[{"results":[{"amount":"1234"}]}],"has_more":false}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

func newTestAPIProvider(base string, client *http.Client) *APIProvider {
	return &APIProvider{
		key:    "sk-ant-admin01-test",
		base:   base,
		client: client,
		now:    func() time.Time { return time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC) },
	}
}

// TestAPIFetchSumsPagesAndCost drives the whole APIProvider HTTP path: it sums the
// token buckets across both usage pages, follows pagination, and folds the cost
// report's cents into dollars.
func TestAPIFetchSumsPagesAndCost(t *testing.T) {
	srv := apiTestServer(t)
	defer srv.Close()
	p := newTestAPIProvider(srv.URL, srv.Client())

	snap, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snap.Source != "api" {
		t.Errorf("source = %q, want api", snap.Source)
	}
	if snap.Input != 300 { // 100 (page 1) + 200 (page 2)
		t.Errorf("Input = %d, want 300", snap.Input)
	}
	if snap.Output != 150 { // 50 + 100
		t.Errorf("Output = %d, want 150", snap.Output)
	}
	if snap.CacheRead != 10 {
		t.Errorf("CacheRead = %d, want 10", snap.CacheRead)
	}
	if snap.CacheWrite != 7 { // 5m + 1h
		t.Errorf("CacheWrite = %d, want 7", snap.CacheWrite)
	}
	if snap.CostUSD != 12.34 { // 1234 cents
		t.Errorf("CostUSD = %v, want 12.34", snap.CostUSD)
	}
}

// TestAPIFetchHTTPError proves a non-200 from the usage endpoint surfaces as an
// error (with the partial snapshot), so a broken key or outage is reported rather
// than silently counted as zero.
func TestAPIFetchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := newTestAPIProvider(srv.URL, srv.Client())

	if _, err := p.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch should error on a non-200 usage response")
	}
}

// TestAPICostErrorIsPartial proves a cost-report failure still returns the token
// totals (a partial snapshot with an error), matching the documented soft-fail.
func TestAPICostErrorIsPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/organizations/cost_report" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"results":[{"output_tokens":42}]}],"has_more":false}`))
	}))
	defer srv.Close()
	p := newTestAPIProvider(srv.URL, srv.Client())

	snap, err := p.Fetch(context.Background())
	if err == nil {
		t.Fatal("a cost-report failure should surface an error")
	}
	if snap.Output != 42 {
		t.Errorf("token totals should survive a cost failure, Output = %d", snap.Output)
	}
}

func TestAPIProviderSourceAndInterval(t *testing.T) {
	p := NewAPIProvider("sk-ant-admin01-test")
	if p.Source() != "api" {
		t.Errorf("Source = %q, want api", p.Source())
	}
	if got := DefaultInterval(p); got != 60*time.Second {
		t.Errorf("DefaultInterval(api) = %v, want 60s", got)
	}
}
