package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// The oauth source asks Anthropic directly what the account's standing is.
//
// It is the only source that can see the two things the status line cannot: the
// per-model weekly ceilings, and the extra-usage credit balance. That is the
// whole reason it exists, and it is why it is opt-in rather than the default —
// everything it buys costs something the status-line source does not:
//
//   - It reads the user's OAuth access token. Baton is a terminal multiplexer,
//     and reaching for a credential is not a thing one should do quietly. The
//     token is read, used against one fixed host, and never logged, never written
//     anywhere, and never put in an error string.
//   - The endpoint is not a documented API. It can change or disappear without
//     notice, so every failure here degrades to "no reading" rather than to a
//     wrong one.
//   - It is rate-limited, and aggressively. A poll loop that hammered it would
//     have the account's own usage endpoint start refusing — which is why the
//     interval below is a floor the caller cannot lower, and why a refusal backs
//     off rather than retries.
//
// Nothing here runs unless usage.limits is set to "oauth".

// oauthUsageURL is the endpoint the reading comes from. It is a constant, and the
// only host this package ever talks to: a credential that can only be sent one
// place is a credential that cannot be sent to the wrong place.
const oauthUsageURL = "https://api.anthropic.com/api/oauth/usage"

// oauthBetaHeader opts the request into the OAuth surface. Without it the
// endpoint answers 401 regardless of how good the token is.
const oauthBetaHeader = "oauth-2025-04-20"

// OAuthMinInterval is the shortest gap between two fetches, whatever the caller's
// poll cadence is. It is a floor rather than a default because the cost of
// getting it wrong lands on the user's own account: this endpoint refuses a
// caller that asks too often, and a cockpit that provoked that would be spending
// the very quota it exists to report on. The windows move in whole points over
// minutes; three of them is plenty of resolution.
const OAuthMinInterval = 3 * time.Minute

// The back-off applied after the endpoint refuses. It doubles from the first
// figure to the second and stays there — a refusal that has not cleared in half
// an hour is not going to clear because baton asked again sooner.
const (
	oauthBackoffMin = 5 * time.Minute
	oauthBackoffMax = 30 * time.Minute
)

// OAuthLimits reads the account's standing from the usage endpoint, holding the
// last good answer between fetches.
//
// It keeps the previous reading across a failure on purpose. A refused or
// unreachable endpoint has not made the quota untrue — it has only stopped baton
// hearing about it — and the reading carries its own age, so a cockpit can say
// how current it is without this type having to guess on its behalf.
type OAuthLimits struct {
	client *http.Client
	token  func() (string, error) // injectable in tests; never called more than once per fetch
	url    string
	now    func() time.Time

	mu       sync.Mutex
	held     Limits
	ok       bool
	fetched  time.Time // when the last fetch was attempted, good or not
	backoff  time.Duration
	blocked  time.Time // no fetch before this instant
	inflight bool      // a fetch is running; a second caller takes the held value
}

// NewOAuthLimits builds the oauth source with the default HTTP client and token
// lookup.
func NewOAuthLimits() *OAuthLimits {
	return &OAuthLimits{
		client: &http.Client{Timeout: 10 * time.Second},
		token:  oauthToken,
		url:    oauthUsageURL,
		now:    time.Now,
	}
}

// Source implements LimitsProvider.
func (p *OAuthLimits) Source() string { return LimitsOAuth }

// Limits implements LimitsProvider. It fetches at most once every
// OAuthMinInterval, serves the held reading in between, and never lets two
// callers fetch at once.
func (p *OAuthLimits) Limits(ctx context.Context) (Limits, bool) {
	now := p.now()

	p.mu.Lock()
	// Three reasons to answer from what is already in hand: it is fresh enough, the
	// endpoint has asked to be left alone, or somebody else is already asking.
	if p.inflight || now.Before(p.blocked) || now.Sub(p.fetched) < OAuthMinInterval {
		held, ok := p.held, p.ok
		p.mu.Unlock()
		return held, ok
	}
	p.inflight, p.fetched = true, now
	p.mu.Unlock()

	l, err := p.fetch(ctx)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.inflight = false
	if err != nil {
		p.penalise(err)
		return p.held, p.ok // the last good reading, which has not become untrue
	}
	p.backoff, p.blocked = 0, time.Time{}
	p.held, p.ok = l, !l.Empty()
	return p.held, p.ok
}

// penalise widens the back-off after a failure. A refusal (429) is what the
// window exists for, but every other failure backs off too: an endpoint that is
// down, or a token that has expired, will not be fixed by asking again in three
// minutes, and the retries would be the only thing baton was doing. Callers must
// hold mu.
func (p *OAuthLimits) penalise(err error) {
	switch {
	case p.backoff == 0:
		p.backoff = oauthBackoffMin
	case p.backoff < oauthBackoffMax:
		p.backoff = min(p.backoff*2, oauthBackoffMax)
	}
	p.blocked = p.now().Add(p.backoff)
	logLimitsError(err, p.backoff)
}

// logLimitsError records a failed fetch and how long baton will now leave the
// endpoint alone.
//
// Every error reaching here is built above without the token in it, which is the
// property that makes logging safe at all: a log file outlives the process, gets
// copied into bug reports, and is the last place a credential should turn up. Any
// new error path in this file has to keep that property.
func logLimitsError(err error, backoff time.Duration) {
	log.Warn().Err(err).Dur("backoff", backoff).Msg("usage limits fetch failed")
}

// oauthPayload is the endpoint's answer. Every window is a pointer because the
// endpoint sends an explicit null for one that does not apply to the plan — a
// per-model ceiling on a plan that has none — and a null must stay absent rather
// than decode as a window at zero.
type oauthPayload struct {
	FiveHour       *oauthWindow `json:"five_hour"`
	SevenDay       *oauthWindow `json:"seven_day"`
	SevenDayOpus   *oauthWindow `json:"seven_day_opus"`
	SevenDaySonnet *oauthWindow `json:"seven_day_sonnet"`
	ExtraUsage     *oauthCredit `json:"extra_usage"`
}

// oauthWindow is one window as the endpoint spells it: a utilisation figure
// scaled 0–100, like the status line's used_percentage, and an ISO 8601 reset.
type oauthWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

func (w *oauthWindow) window() *Window {
	if w == nil {
		return nil
	}
	out := &Window{UsedPercent: w.Utilization}
	// The endpoint stamps with sub-second precision and an explicit offset, which
	// RFC 3339 covers; anything else is left as no reset rather than guessed at.
	if t, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil {
		out.ResetsAt = t
	}
	return out
}

// oauthCredit is the extra-usage balance. Every amount is a pointer because the
// endpoint sends the block with all four fields null when the feature is off, and
// a null monthly limit means uncapped — the opposite reading from a limit of zero.
type oauthCredit struct {
	IsEnabled    bool     `json:"is_enabled"`
	MonthlyLimit *float64 `json:"monthly_limit"`
	UsedCredits  *float64 `json:"used_credits"`
	Utilization  *float64 `json:"utilization"`
}

// fetch performs one request and decodes it. Nothing it returns carries the
// token, including in an error: an error string ends up in a log file, and a log
// file is the last place a credential should turn up.
func (p *OAuthLimits) fetch(ctx context.Context) (Limits, error) {
	token, err := p.token()
	if err != nil {
		return Limits{}, err
	}
	if strings.TrimSpace(token) == "" {
		return Limits{}, errNoToken
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return Limits{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", oauthBetaHeader)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return Limits{}, fmt.Errorf("usage endpoint unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// The status alone, never the body: an error body from an authenticated
		// endpoint is not something to copy into a log unread.
		return Limits{}, fmt.Errorf("usage endpoint answered %d: %w", resp.StatusCode, errBadStatus)
	}

	var payload oauthPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Limits{}, fmt.Errorf("usage endpoint sent something unreadable: %w", err)
	}

	l := Limits{
		FiveHour:       payload.FiveHour.window(),
		SevenDay:       payload.SevenDay.window(),
		SevenDayOpus:   payload.SevenDayOpus.window(),
		SevenDaySonnet: payload.SevenDaySonnet.window(),
		Source:         LimitsOAuth,
		At:             p.now(),
	}
	if c := payload.ExtraUsage; c != nil {
		l.Credit = &Credit{
			Enabled:     c.IsEnabled,
			MonthlyUSD:  c.MonthlyLimit,
			UsedUSD:     c.UsedCredits,
			UsedPercent: c.Utilization,
		}
	}
	return l, nil
}

// The sentinel failures, so a caller can tell "not signed in this way" from "the
// endpoint said no" without matching on message text.
var (
	errNoToken   = errors.New("no Claude Code OAuth token available")
	errBadStatus = errors.New("unexpected status")
)

// oauthToken reads Claude Code's OAuth access token.
//
// Two places hold it, and both are Claude Code's, not baton's. The credentials
// file is the portable one and is tried first because reading a file costs
// nothing; on macOS the token usually lives in the login keychain instead, which
// takes a subprocess to reach. Neither is written to, ever.
//
// The token is returned and nothing else. It is not cached in a package variable,
// not logged, and not embedded in any error below — the whole point of fetching
// it per request is that it exists in this process for as long as one HTTP call
// takes and no longer.
func oauthToken() (string, error) {
	if tok, err := tokenFromFile(credentialsFile()); err == nil && tok != "" {
		return tok, nil
	}
	if runtime.GOOS == "darwin" {
		if tok, err := tokenFromKeychain(); err == nil && tok != "" {
			return tok, nil
		}
	}
	return "", errNoToken
}

// credentialsFile is where Claude Code keeps its OAuth credentials on disk,
// honouring the same CLAUDE_CONFIG_DIR override as everything else it writes.
func credentialsFile() string {
	return filepath.Join(claudeConfigDir(), ".credentials.json")
}

// credentialsBlob is the slice of the credentials store baton reads. Only the
// access token is taken: the refresh token is what could mint new credentials,
// and a reader that never loads it cannot leak it.
type credentialsBlob struct {
	ClaudeAiOauth struct {
		AccessToken string `json:"accessToken"`
	} `json:"claudeAiOauth"`
}

// tokenFromFile reads the access token out of a credentials file.
func tokenFromFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return parseCredentials(b)
}

// tokenFromKeychain reads the access token out of the macOS login keychain,
// where Claude Code stores it under its own service name. The lookup may prompt
// the user for keychain access the first time, which is the operating system
// doing exactly what it should: baton is asking for somebody's credential, and
// that ought to be a visible act.
func tokenFromKeychain() (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", keychainService, "-w").Output()
	if err != nil {
		// Deliberately not wrapped: the command's stderr can echo back what it was
		// asked for, and this one was asked for a credential.
		return "", errNoToken
	}
	return parseCredentials(out)
}

// keychainService is the login-keychain entry Claude Code stores its credentials
// under.
const keychainService = "Claude Code-credentials"

// parseCredentials pulls the access token out of a credentials blob. A blob that
// does not carry one is not an error worth describing in detail — describing it
// would mean saying something about the contents of a credential store.
func parseCredentials(b []byte) (string, error) {
	var c credentialsBlob
	if err := json.Unmarshal(b, &c); err != nil {
		return "", errNoToken
	}
	if tok := strings.TrimSpace(c.ClaudeAiOauth.AccessToken); tok != "" {
		return tok, nil
	}
	return "", errNoToken
}
