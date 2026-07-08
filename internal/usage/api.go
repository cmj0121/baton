package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// APIProvider reads the Anthropic Admin usage & cost API. It reports the whole
// organization's Console/API-key usage, so it only carries data for accounts
// billed through the Console — a personal Pro/Max subscription's usage never
// appears here. It needs an Admin API key (sk-ant-admin...).
type APIProvider struct {
	key    string
	base   string
	client *http.Client
	now    func() time.Time
}

// maxUsageBody caps how many bytes of an API response body are decoded, so a
// malformed or oversized reply cannot balloon the daemon's memory. The real
// reports are a few KB per page; 8 MiB is far past any legitimate page yet still
// a hard ceiling. maxUsagePages bounds the pagination loop so a reply that keeps
// reporting has_more (a bug or a bad proxy) cannot spin forever within the
// request deadline.
const (
	maxUsageBody  = 8 << 20 // 8 MiB
	maxUsagePages = 1000
)

// NewAPIProvider builds an api source authenticating with the given admin key.
func NewAPIProvider(key string) *APIProvider {
	return &APIProvider{
		key:    key,
		base:   "https://api.anthropic.com",
		client: &http.Client{Timeout: 20 * time.Second},
		now:    time.Now,
	}
}

// Source implements Provider.
func (p *APIProvider) Source() string { return "api" }

// Fetch pulls today's token usage and cost. The two reports are separate
// endpoints; a cost fetch failure is soft — the token totals still return so the
// footer shows usage even when the cost report is unavailable.
func (p *APIProvider) Fetch(ctx context.Context) (Snapshot, error) {
	start := startOfDay(p.now())
	snap := Snapshot{Since: start, Source: "api"}
	if err := p.fetchUsage(ctx, start, &snap); err != nil {
		return snap, err
	}
	if err := p.fetchCost(ctx, start, &snap); err != nil {
		return snap, fmt.Errorf("cost report: %w", err) // partial: snap still carries tokens
	}
	return snap, nil
}

// window is the [starting_at, ending_at) query pair for today, in UTC RFC 3339 as
// the API expects.
func (p *APIProvider) window(start time.Time) (string, string) {
	end := start.AddDate(0, 0, 1)
	return start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)
}

type usageReport struct {
	Data []struct {
		Results []struct {
			UncachedInputTokens  int64 `json:"uncached_input_tokens"`
			CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
			OutputTokens         int64 `json:"output_tokens"`
			CacheCreation        struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"results"`
	} `json:"data"`
	HasMore  bool   `json:"has_more"`
	NextPage string `json:"next_page"`
}

// fetchUsage sums the day's token buckets, following pagination to the last page.
func (p *APIProvider) fetchUsage(ctx context.Context, start time.Time, snap *Snapshot) error {
	from, to := p.window(start)
	page := ""
	for range maxUsagePages {
		q := url.Values{}
		q.Set("starting_at", from)
		q.Set("ending_at", to)
		q.Set("bucket_width", "1d")
		if page != "" {
			q.Set("page", page)
		}
		var rep usageReport
		if err := p.get(ctx, "/v1/organizations/usage_report/messages", q, &rep); err != nil {
			return err
		}
		for _, b := range rep.Data {
			for _, r := range b.Results {
				snap.Input += r.UncachedInputTokens
				snap.Output += r.OutputTokens
				snap.CacheRead += r.CacheReadInputTokens
				snap.CacheWrite += r.CacheCreation.Ephemeral5m + r.CacheCreation.Ephemeral1h
			}
		}
		if !rep.HasMore || rep.NextPage == "" {
			return nil
		}
		page = rep.NextPage
	}
	return nil // page ceiling hit; report what we summed rather than looping forever
}

type costReport struct {
	Data []struct {
		Results []struct {
			Amount string `json:"amount"` // lowest currency unit (cents) as a decimal string
		} `json:"results"`
	} `json:"data"`
	HasMore  bool   `json:"has_more"`
	NextPage string `json:"next_page"`
}

// fetchCost sums the day's cost amounts. Amounts are cents as decimal strings, so
// the running total is divided by 100 into dollars. Cost is daily-granularity only.
func (p *APIProvider) fetchCost(ctx context.Context, start time.Time, snap *Snapshot) error {
	from, to := p.window(start)
	page := ""
	var cents float64
	for range maxUsagePages {
		q := url.Values{}
		q.Set("starting_at", from)
		q.Set("ending_at", to)
		if page != "" {
			q.Set("page", page)
		}
		var rep costReport
		if err := p.get(ctx, "/v1/organizations/cost_report", q, &rep); err != nil {
			return err
		}
		for _, b := range rep.Data {
			for _, r := range b.Results {
				if v, err := strconv.ParseFloat(r.Amount, 64); err == nil {
					cents += v
				}
			}
		}
		if !rep.HasMore || rep.NextPage == "" {
			break
		}
		page = rep.NextPage
	}
	snap.CostUSD = cents / 100
	return nil
}

// get performs an authenticated Admin API GET and decodes the JSON body into out.
func (p *APIProvider) get(ctx context.Context, path string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", p.key)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s: %s", path, resp.Status, body)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxUsageBody)).Decode(out)
}
