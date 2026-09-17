package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Grok's weekly credit pool. It is a different measurement from the local
// session-log reader in grok.go, not a better one: that file counts tokens baton
// can attribute to a machine, and this one asks grok what is left of the
// subscription week. The two stay apart for the same reason Snapshot and Limits
// do. A fleet needs both.
//
// The endpoint is the one the grok CLI's /usage modal uses. It is not a
// documented API, it can change without notice, and reaching it means reading
// the operator's OAuth access token. Those are the same costs the Anthropic
// oauth source already accepted, and the same rules apply:
//
//   - The token is read, sent to one host, never logged, never written back,
//     and never put in an error string. The refresh token is not loaded at all.
//   - Every failure degrades to "no reading" rather than to a wrong one.
//   - The poll is floored and a refusal backs off, so baton cannot spend the
//     quota it exists to report on.
//
// There is no five-hour Grok window. A weekly pool mapped onto a session
// throttle would be a limit grok never stated.

const grokChatProxyDefault = "https://cli-chat-proxy.grok.com/v1"

const grokBillingPath = "/billing?format=credits"

// grokChatProxyEnv is the same override the grok CLI honours. A corporate
// gateway that already serves the CLI should serve billing too; inventing a
// second baton-only URL would send the token to a host the operator never
// pointed grok at.
const grokChatProxyEnv = "GROK_CLI_CHAT_PROXY_BASE_URL"

// GrokLimitsMinInterval is the shortest gap between two fetches. The windows
// move in whole points over minutes; three of them is plenty of resolution.
const GrokLimitsMinInterval = 3 * time.Minute

const grokPeriodWeekly = "USAGE_PERIOD_TYPE_WEEKLY"

// GrokLimits reads grok's weekly credit standing, holding the last good answer
// between fetches. A refused or unreachable endpoint has not made the quota
// untrue — it has only stopped baton hearing about it.
type GrokLimits struct {
	client *http.Client
	token  func() (string, error)
	url    string
	now    func() time.Time

	mu       sync.Mutex
	held     *Window
	ok       bool
	fetched  time.Time
	backoff  time.Duration
	blocked  time.Time
	inflight bool
}

// NewGrokLimits builds the grok weekly-quota source with the default HTTP
// client and token lookup.
func NewGrokLimits() *GrokLimits {
	return &GrokLimits{
		client: &http.Client{Timeout: 10 * time.Second},
		token:  grokAccessToken,
		url:    grokBillingURL(),
		now:    time.Now,
	}
}

// grokBillingURL is the billing endpoint, honouring the CLI's own proxy
// override so the token never goes somewhere grok itself would not send it.
func grokBillingURL() string {
	base := grokChatProxyDefault
	if v := strings.TrimSpace(os.Getenv(grokChatProxyEnv)); v != "" {
		base = strings.TrimRight(v, "/")
	}
	return base + grokBillingPath
}

// Week is grok's weekly credit window, and whether there is one. It fetches at
// most once every GrokLimitsMinInterval, serves the held reading in between,
// and never lets two callers fetch at once.
func (p *GrokLimits) Week(ctx context.Context) (*Window, bool) {
	// A test binary that booted a real daemon would otherwise send the operator's
	// token at production the first time grok appears in the agent list. The
	// httptest sources point p.url at loopback and still fetch.
	if testing.Testing() && strings.Contains(p.url, "cli-chat-proxy.grok.com") {
		return nil, false
	}
	now := p.now()

	p.mu.Lock()
	if p.inflight || now.Before(p.blocked) || now.Sub(p.fetched) < GrokLimitsMinInterval {
		held, ok := p.held, p.ok
		p.mu.Unlock()
		return held, ok
	}
	p.inflight, p.fetched = true, now
	p.mu.Unlock()

	w, err := p.fetch(ctx)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.inflight = false
	if err != nil {
		p.penalise(err)
		return p.held, p.ok
	}
	p.backoff, p.blocked = 0, time.Time{}
	p.held, p.ok = w, w != nil
	return p.held, p.ok
}

func (p *GrokLimits) penalise(err error) {
	switch {
	case p.backoff == 0:
		p.backoff = oauthBackoffMin
	case p.backoff < oauthBackoffMax:
		p.backoff = min(p.backoff*2, oauthBackoffMax)
	}
	p.blocked = p.now().Add(p.backoff)
	logLimitsError(err, p.backoff)
}

// grokBillingPayload is the slice of the billing answer this reader wants. The
// rest of the object — on-demand caps, product splits, prepaid balance — is a
// different question and is left unread rather than drawn as a window grok did
// not name that way.
type grokBillingPayload struct {
	Config grokBillingConfig `json:"config"`
}

type grokBillingConfig struct {
	CreditUsagePercent float64         `json:"creditUsagePercent"`
	CurrentPeriod      grokUsagePeriod `json:"currentPeriod"`
	BillingPeriodEnd   string          `json:"billingPeriodEnd"`
}

type grokUsagePeriod struct {
	Type string `json:"type"`
	End  string `json:"end"`
}

func (p grokBillingPayload) week() *Window {
	cfg := p.Config
	if cfg.CurrentPeriod.Type != grokPeriodWeekly {
		return nil
	}
	w := &Window{UsedPercent: cfg.CreditUsagePercent}
	end := cfg.CurrentPeriod.End
	if end == "" {
		end = cfg.BillingPeriodEnd
	}
	if t, err := time.Parse(time.RFC3339, end); err == nil {
		w.ResetsAt = t
	}
	return w
}

func (p *GrokLimits) fetch(ctx context.Context) (*Window, error) {
	token, err := p.token()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, errNoGrokToken
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("grok billing unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("grok billing answered %d: %w", resp.StatusCode, errBadStatus)
	}

	var payload grokBillingPayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxUsageBody)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("grok billing sent something unreadable: %w", err)
	}
	return payload.week(), nil
}

var errNoGrokToken = errors.New("no Grok OAuth token available")

// grokAccessToken reads the grok CLI's OAuth access token from auth.json.
//
// Only the access token is taken. The refresh token is what could mint new
// credentials, and a reader that never loads it cannot leak it or race the CLI
// that owns rotation. The file is never written.
func grokAccessToken() (string, error) {
	return grokTokenFromFile(grokAuthFile())
}

func grokTokenFromFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", errNoGrokToken
	}
	return parseGrokAuth(b)
}

// grokAuthBlob is one credentials file: a map of issuer::client_id to an entry
// that carries the access token under "key". Other fields exist and are not
// declared, so encoding/json drops them — including refresh_token.
type grokAuthBlob map[string]struct {
	Key string `json:"key"`
}

func parseGrokAuth(b []byte) (string, error) {
	var blob grokAuthBlob
	if json.Unmarshal(b, &blob) != nil || len(blob) == 0 {
		return "", errNoGrokToken
	}
	keys := make([]string, 0, len(blob))
	for k := range blob {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if tok := strings.TrimSpace(blob[k].Key); tok != "" {
			return tok, nil
		}
	}
	return "", errNoGrokToken
}
