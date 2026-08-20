package usage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// oauthBody is a full endpoint answer: both plain windows, one per-model ceiling
// present and one explicitly null, and an enabled credit balance.
const oauthBody = `{
  "five_hour":       {"utilization": 37.0, "resets_at": "2026-08-20T14:00:00.000000+00:00"},
  "seven_day":       {"utilization": 26.0, "resets_at": "2026-08-23T12:00:00.771647+00:00"},
  "seven_day_opus":  null,
  "seven_day_sonnet":{"utilization": 1.0,  "resets_at": "2026-08-23T12:00:00.771655+00:00"},
  "extra_usage":     {"is_enabled": true, "monthly_limit": 65.0, "used_credits": 11.7, "utilization": 18.0}
}`

// newTestOAuth wires an OAuth source at a test server, with a pinned clock and a
// token that never touches the real credential store.
func newTestOAuth(t *testing.T, handler http.HandlerFunc) (*OAuthLimits, *time.Time) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	now := limitsNow
	p := NewOAuthLimits()
	p.url = srv.URL
	p.token = func() (string, error) { return "test-token", nil }
	p.now = func() time.Time { return now }
	return p, &now
}

func TestOAuthFetch(t *testing.T) {
	var gotAuth, gotBeta string
	p, _ := newTestOAuth(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotBeta = r.Header.Get("Authorization"), r.Header.Get("anthropic-beta")
		_, _ = w.Write([]byte(oauthBody))
	})

	l, ok := p.Limits(context.Background())
	if !ok {
		t.Fatal("Limits reported nothing for a good answer")
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBeta != oauthBetaHeader {
		t.Errorf("anthropic-beta = %q, want %q — the endpoint 401s without it", gotBeta, oauthBetaHeader)
	}
	if l.Source != LimitsOAuth {
		t.Errorf("Source = %q, want %q", l.Source, LimitsOAuth)
	}
	if l.FiveHour == nil || l.FiveHour.UsedPercent != 37 {
		t.Errorf("five_hour = %+v, want 37%%", l.FiveHour)
	}
	if want := time.Date(2026, 8, 20, 14, 0, 0, 0, time.UTC); !l.FiveHour.ResetsAt.Equal(want) {
		t.Errorf("five_hour reset = %v, want %v", l.FiveHour.ResetsAt, want)
	}
	// An explicit null is a ceiling the plan does not have; it must stay absent
	// rather than decode as a window at zero.
	if l.SevenDayOpus != nil {
		t.Errorf("seven_day_opus = %+v, want absent for an explicit null", l.SevenDayOpus)
	}
	if l.SevenDaySonnet == nil || l.SevenDaySonnet.UsedPercent != 1 {
		t.Errorf("seven_day_sonnet = %+v, want 1%%", l.SevenDaySonnet)
	}
	if l.Credit == nil || !l.Credit.Enabled || l.Credit.MonthlyUSD == nil || *l.Credit.MonthlyUSD != 65 {
		t.Fatalf("credit = %+v, want the enabled $65 balance", l.Credit)
	}
	if f, ok := l.Credit.Fraction(); !ok || f != 0.18 {
		t.Errorf("credit fraction = (%v, %v), want (0.18, true)", f, ok)
	}
}

// A disabled balance decodes with every amount null, and must not read as a
// balance of zero out of a limit of zero.
func TestOAuthDisabledCredit(t *testing.T) {
	p, _ := newTestOAuth(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":5,"resets_at":"2026-08-20T14:00:00Z"},
			"extra_usage":{"is_enabled":false,"monthly_limit":null,"used_credits":null,"utilization":null}}`))
	})
	l, ok := p.Limits(context.Background())
	if !ok {
		t.Fatal("Limits reported nothing")
	}
	if l.Credit == nil || l.Credit.Enabled {
		t.Fatalf("credit = %+v, want a present-but-disabled balance", l.Credit)
	}
	if _, ok := l.Credit.Fraction(); ok {
		t.Error("a disabled balance offered a fraction to draw")
	}
}

// The endpoint refuses a caller that asks too often, and the quota it would be
// spending is the user's own. The floor holds whatever the caller's cadence is.
func TestOAuthMinIntervalHolds(t *testing.T) {
	var calls int
	p, now := newTestOAuth(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(oauthBody))
	})

	for range 5 {
		if _, ok := p.Limits(context.Background()); !ok {
			t.Fatal("Limits went blank inside the interval")
		}
	}
	if calls != 1 {
		t.Errorf("%d requests inside OAuthMinInterval, want 1", calls)
	}
	*now = now.Add(OAuthMinInterval + time.Second)
	if _, ok := p.Limits(context.Background()); !ok {
		t.Fatal("Limits went blank after the interval")
	}
	if calls != 2 {
		t.Errorf("%d requests after the interval elapsed, want 2", calls)
	}
}

// A refusal has not made the quota untrue — it has only stopped baton hearing
// about it. The held reading stays, and the endpoint is left alone.
func TestOAuthBacksOffAndHolds(t *testing.T) {
	var calls int
	var status = http.StatusOK
	p, now := newTestOAuth(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"secret-ish body that must not be logged"}`))
			return
		}
		_, _ = w.Write([]byte(oauthBody))
	})

	if _, ok := p.Limits(context.Background()); !ok {
		t.Fatal("the first fetch failed")
	}
	status = http.StatusTooManyRequests
	*now = now.Add(OAuthMinInterval + time.Second)

	l, ok := p.Limits(context.Background())
	if !ok || l.FiveHour == nil || l.FiveHour.UsedPercent != 37 {
		t.Errorf("a refusal blanked the held reading: (%+v, %v)", l, ok)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	// Inside the back-off the endpoint is not asked again, even well past the
	// ordinary interval.
	*now = now.Add(oauthBackoffMin - time.Minute)
	if _, ok := p.Limits(context.Background()); !ok {
		t.Error("the held reading was dropped during the back-off")
	}
	if calls != 2 {
		t.Errorf("calls = %d during the back-off, want it left alone at 2", calls)
	}
	// …and it widens on the next refusal rather than settling.
	*now = now.Add(oauthBackoffMin)
	_, _ = p.Limits(context.Background())
	if calls != 3 {
		t.Fatalf("calls = %d after the back-off elapsed, want 3", calls)
	}
	if p.backoff != 2*oauthBackoffMin {
		t.Errorf("backoff = %v after a second refusal, want %v", p.backoff, 2*oauthBackoffMin)
	}
	// A good answer clears it outright.
	status = http.StatusOK
	*now = now.Add(2*oauthBackoffMin + time.Second)
	if _, ok := p.Limits(context.Background()); !ok {
		t.Fatal("the recovery fetch failed")
	}
	if p.backoff != 0 || !p.blocked.IsZero() {
		t.Errorf("backoff = %v / blocked = %v after a good answer, want both cleared", p.backoff, p.blocked)
	}
}

func TestOAuthBackoffCaps(t *testing.T) {
	p, now := newTestOAuth(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	for range 12 {
		_, _ = p.Limits(context.Background())
		*now = now.Add(oauthBackoffMax + time.Second)
	}
	if p.backoff != oauthBackoffMax {
		t.Errorf("backoff = %v, want it capped at %v", p.backoff, oauthBackoffMax)
	}
}

// No token is not a failure to describe at length — it is the ordinary state of a
// machine signed in some other way.
func TestOAuthNoToken(t *testing.T) {
	p, _ := newTestOAuth(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a request went out with no token to send")
	})
	p.token = func() (string, error) { return "  ", nil }
	if _, ok := p.Limits(context.Background()); ok {
		t.Error("Limits reported a reading with no token")
	}
	p.token = func() (string, error) { return "", errors.New("keychain locked") }
	if _, ok := p.Limits(context.Background()); ok {
		t.Error("Limits reported a reading when the token lookup failed")
	}
}

// Two cockpits polling at once must not turn into two requests.
func TestOAuthSingleFlight(t *testing.T) {
	var mu sync.Mutex
	var calls int
	release := make(chan struct{})
	p, _ := newTestOAuth(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release
		_, _ = w.Write([]byte(oauthBody))
	})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Limits(context.Background())
		}()
	}
	// Give the racers time to pile up behind the one in flight, then let it finish.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("%d concurrent requests, want 1", calls)
	}
}

func TestParseCredentials(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	if _, err := tokenFromFile(credentialsFile()); err == nil {
		t.Error("a missing credentials file was read as a token")
	}
	path := filepath.Join(dir, ".credentials.json")
	// Only the access token is taken: a reader that never loads the refresh token
	// cannot leak it.
	body := `{"claudeAiOauth":{"accessToken":"tok-abc","refreshToken":"must-not-be-read","expiresAt":123}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := tokenFromFile(path)
	if err != nil || tok != "tok-abc" {
		t.Errorf("tokenFromFile = (%q, %v), want the access token", tok, err)
	}
	for name, junk := range map[string]string{
		"not json":     `{{{`,
		"no oauth key": `{"other":{}}`,
		"empty token":  `{"claudeAiOauth":{"accessToken":"   "}}`,
	} {
		if _, err := parseCredentials([]byte(junk)); !errors.Is(err, errNoToken) {
			t.Errorf("%s: err = %v, want errNoToken", name, err)
		}
	}
}
