package usage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// grokBillingBody is a real endpoint answer, reduced to the fields the reader
// reads, with the nesting kept verbatim.
const grokBillingBody = `{
  "config": {
    "currentPeriod": {
      "type": "USAGE_PERIOD_TYPE_WEEKLY",
      "start": "2026-09-14T00:44:30.362275+00:00",
      "end": "2026-09-21T00:44:30.362275+00:00"
    },
    "creditUsagePercent": 36.0,
    "billingPeriodEnd": "2026-09-21T00:44:30.362275+00:00"
  }
}`

func newTestGrokLimits(t *testing.T, handler http.HandlerFunc) (*GrokLimits, *time.Time) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	now := limitsNow
	p := NewGrokLimits()
	p.url = srv.URL
	p.token = func() (string, error) { return "test-token", nil }
	p.now = func() time.Time { return now }
	return p, &now
}

func TestGrokLimitsFetch(t *testing.T) {
	var gotAuth string
	p, _ := newTestGrokLimits(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(grokBillingBody))
	})

	w, ok := p.Week(context.Background())
	if !ok || w == nil {
		t.Fatal("Week reported nothing for a good answer")
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if w.UsedPercent != 36 {
		t.Errorf("used = %v, want 36", w.UsedPercent)
	}
	want := time.Date(2026, 9, 21, 0, 44, 30, 362275000, time.UTC)
	if !w.ResetsAt.Equal(want) {
		t.Errorf("reset = %v, want %v", w.ResetsAt, want)
	}
}

// A period that is not the weekly pool must not be drawn as a 7d window. Mapping
// a monthly or unknown period onto that column would publish a ceiling grok did
// not name that way.
func TestGrokLimitsIgnoresANonWeeklyPeriod(t *testing.T) {
	p, _ := newTestGrokLimits(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"config":{"creditUsagePercent":10,
			"currentPeriod":{"type":"USAGE_PERIOD_TYPE_MONTHLY","end":"2026-10-01T00:00:00Z"}}}`))
	})
	if w, ok := p.Week(context.Background()); ok || w != nil {
		t.Errorf("a monthly period was drawn as a week: (%+v, %v)", w, ok)
	}
}

func TestGrokLimitsMinIntervalHolds(t *testing.T) {
	var calls int
	p, now := newTestGrokLimits(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(grokBillingBody))
	})

	for range 5 {
		if _, ok := p.Week(context.Background()); !ok {
			t.Fatal("Week went blank inside the interval")
		}
	}
	if calls != 1 {
		t.Errorf("%d requests inside GrokLimitsMinInterval, want 1", calls)
	}
	*now = now.Add(GrokLimitsMinInterval + time.Second)
	if _, ok := p.Week(context.Background()); !ok {
		t.Fatal("Week went blank after the interval")
	}
	if calls != 2 {
		t.Errorf("%d requests after the interval elapsed, want 2", calls)
	}
}

func TestGrokLimitsBacksOffAndHolds(t *testing.T) {
	var calls int
	status := http.StatusOK
	p, now := newTestGrokLimits(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"secret-ish body that must not be logged"}`))
			return
		}
		_, _ = w.Write([]byte(grokBillingBody))
	})

	if _, ok := p.Week(context.Background()); !ok {
		t.Fatal("the first fetch failed")
	}
	status = http.StatusTooManyRequests
	*now = now.Add(GrokLimitsMinInterval + time.Second)

	w, ok := p.Week(context.Background())
	if !ok || w == nil || w.UsedPercent != 36 {
		t.Errorf("a refusal blanked the held reading: (%+v, %v)", w, ok)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	*now = now.Add(oauthBackoffMin - time.Minute)
	if _, ok := p.Week(context.Background()); !ok {
		t.Error("the held reading was dropped during the back-off")
	}
	if calls != 2 {
		t.Errorf("calls = %d during the back-off, want it left alone at 2", calls)
	}
}

func TestGrokLimitsNoToken(t *testing.T) {
	p, _ := newTestGrokLimits(t, func(http.ResponseWriter, *http.Request) {
		t.Error("a request went out with no token to send")
	})
	p.token = func() (string, error) { return "  ", nil }
	if _, ok := p.Week(context.Background()); ok {
		t.Error("Week reported a reading with no token")
	}
	p.token = func() (string, error) { return "", errors.New("auth.json missing") }
	if _, ok := p.Week(context.Background()); ok {
		t.Error("Week reported a reading when the token lookup failed")
	}
}

func TestGrokLimitsSingleFlight(t *testing.T) {
	var mu sync.Mutex
	var calls int
	release := make(chan struct{})
	p, _ := newTestGrokLimits(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release
		_, _ = w.Write([]byte(grokBillingBody))
	})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Week(context.Background())
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("%d concurrent requests, want 1", calls)
	}
}

func TestParseGrokAuthTakesOnlyTheAccessToken(t *testing.T) {
	body := `{
	  "https://auth.x.ai::client": {
	    "key": "tok-abc",
	    "refresh_token": "must-not-be-read",
	    "email": "someone@example.com"
	  }
	}`
	tok, err := parseGrokAuth([]byte(body))
	if err != nil || tok != "tok-abc" {
		t.Errorf("parseGrokAuth = (%q, %v), want the access token", tok, err)
	}
	if strings.Contains(tok, "must-not-be-read") {
		t.Error("the refresh token was returned as the access token")
	}
}

func TestParseGrokAuthRejectsJunk(t *testing.T) {
	for name, junk := range map[string]string{
		"not json":    `{{{`,
		"empty map":   `{}`,
		"empty token": `{"https://auth.x.ai::c":{"key":"   "}}`,
	} {
		if _, err := parseGrokAuth([]byte(junk)); !errors.Is(err, errNoGrokToken) {
			t.Errorf("%s: err = %v, want errNoGrokToken", name, err)
		}
	}
}

func TestGrokTokenFromFileHonoursGROKHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	if err := os.WriteFile(filepath.Join(root, "auth.json"),
		[]byte(`{"https://auth.x.ai::c":{"key":"from-home"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := grokAccessToken()
	if err != nil || tok != "from-home" {
		t.Errorf("grokAccessToken = (%q, %v), want from-home", tok, err)
	}
}

func TestGrokLimitsErrorOmitsTheToken(t *testing.T) {
	p, _ := newTestGrokLimits(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"tok-should-not-leak"}`))
	})
	p.token = func() (string, error) { return "super-secret-token", nil }
	_, err := p.fetch(context.Background())
	if err == nil {
		t.Fatal("a 401 decoded as success")
	}
	if strings.Contains(err.Error(), "super-secret-token") || strings.Contains(err.Error(), "tok-should-not-leak") {
		t.Errorf("error leaked a credential: %v", err)
	}
}

func paddedGrokBody(n int) string {
	const head = `{"config":{"creditUsagePercent":36.0,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-09-21T00:00:00Z"},"pad":"`
	const tail = `"}}`
	return head + strings.Repeat("p", n-len(head)-len(tail)) + tail
}

func TestGrokLimitsBodyIsCapped(t *testing.T) {
	p, _ := newTestGrokLimits(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, paddedGrokBody(maxUsageBody+1))
	})
	if w, ok := p.Week(context.Background()); ok {
		t.Fatalf("an answer past the cap was decoded: %+v", w)
	}
}

func TestGrokLimitsBodyUnderTheCapIsRead(t *testing.T) {
	p, _ := newTestGrokLimits(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, paddedGrokBody(legitimateBody))
	})
	w, ok := p.Week(context.Background())
	if !ok || w == nil || w.UsedPercent != 36 {
		t.Fatalf("an answer under the cap should be read whole, got (%+v, %v)", w, ok)
	}
}

func TestGrokBillingURLHonoursTheCLIOverride(t *testing.T) {
	t.Setenv(grokChatProxyEnv, "https://proxy.example/v1/")
	if got, want := grokBillingURL(), "https://proxy.example/v1/billing?format=credits"; got != want {
		t.Errorf("grokBillingURL = %q, want %q", got, want)
	}
}
