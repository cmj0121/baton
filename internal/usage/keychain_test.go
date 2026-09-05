package usage

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestKeychainLookupIsBounded drives the case the bound exists for: a keychain
// prompt nobody answers.
//
// `security` waits on that dialog for as long as it stands, so before the bound
// this call never returned. A command that sleeps far longer than the test could
// wait stands in for it — the shape is identical, a subprocess that will not
// finish on its own.
func TestKeychainLookupIsBounded(t *testing.T) {
	swapKeychain(t, []string{"sleep", "60"}, 200*time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := tokenFromKeychain()
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a lookup that never answered returned a token")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tokenFromKeychain did not return — an unanswered keychain prompt parks the usage poller for good")
	}
}

// TestKeychainStallDoesNotStrandInflight is why the bound is worth having, and
// it is the half a timeout on its own would not prove.
//
// Limits sets inflight before the token lookup and clears it only once the fetch
// returns, and every other caller short-circuits on that flag. So a lookup that
// never returns does not merely lose one reading — it leaves inflight true for
// the daemon's life, and the quota bars never update again however many times a
// cockpit asks. The second call is the assertion: it must be able to fetch.
func TestKeychainStallDoesNotStrandInflight(t *testing.T) {
	swapKeychain(t, []string{"sleep", "60"}, 200*time.Millisecond)

	p, _ := newTestOAuth(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(oauthBody))
	})
	p.token = oauthTokenNoFile(t)

	// Bounded, because on the failure this test exists to catch, Limits does not
	// return at all — and a test that hangs reports as a timeout panic blamed on
	// whatever ran last, rather than as this one failing with its reason.
	withinFive(t, "Limits with a stalled token lookup", func() {
		if _, ok := p.Limits(context.Background()); ok {
			t.Error("Limits reported a reading when the token lookup stalled out")
		}
	})

	p.mu.Lock()
	stranded := p.inflight
	p.mu.Unlock()
	if stranded {
		t.Fatal("inflight is still set after the lookup timed out — every later fetch is short-circuited forever")
	}

	// The provider is still usable: clear the failure back-off the timeout
	// earned, hand it a token, and it fetches.
	p.mu.Lock()
	p.backoff, p.blocked, p.fetched = 0, time.Time{}, time.Time{}
	p.mu.Unlock()
	p.token = func() (string, error) { return "test-token", nil }
	if _, ok := p.Limits(context.Background()); !ok {
		t.Fatal("the provider never recovered from a stalled token lookup")
	}
}

// withinFive fails if fn has not returned by then, so an unbounded call reports
// as a failure with a reason instead of hanging the suite.
func withinFive(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return — the keychain lookup is unbounded", what)
	}
}

// swapKeychain points the lookup at another command for one test and restores it
// after, so the bound can be proven in milliseconds rather than in two minutes.
func swapKeychain(t *testing.T, argv []string, wait time.Duration) {
	t.Helper()
	oldArgv, oldWait := keychainArgv, keychainWait
	keychainArgv, keychainWait = argv, wait
	t.Cleanup(func() { keychainArgv, keychainWait = oldArgv, oldWait })
}

// oauthTokenNoFile is the real token lookup with the credentials FILE pointed at
// an empty directory, so it falls through to the keychain — the path under test.
func oauthTokenNoFile(t *testing.T) func() (string, error) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	return func() (string, error) { return tokenFromKeychain() }
}
