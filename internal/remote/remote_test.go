package remote

import (
	"strings"
	"testing"
	"time"
)

func TestNewPasskeyIsEightUnambiguousChars(t *testing.T) {
	seen := make(map[string]bool, 64)
	for range 64 {
		key, err := NewPasskey()
		if err != nil {
			t.Fatalf("NewPasskey: %v", err)
		}
		if len([]rune(key)) != PasskeyLen {
			t.Fatalf("passkey %q has %d chars, want %d", key, len([]rune(key)), PasskeyLen)
		}
		for _, r := range key {
			if !strings.ContainsRune(passkeyAlphabet, r) {
				t.Fatalf("passkey %q holds %q, which is outside the alphabet", key, r)
			}
		}
		seen[key] = true
	}
	// Not a statistical test — just proof the RNG is actually being read rather
	// than a constant being returned.
	if len(seen) < 60 {
		t.Fatalf("64 passkeys yielded only %d distinct values", len(seen))
	}
}

func TestEqualPasskey(t *testing.T) {
	if !EqualPasskey("K7m2QxP9", "K7m2QxP9") {
		t.Fatal("the same code should compare equal")
	}
	if EqualPasskey("K7m2QxP9", "k7m2qxp9") {
		t.Fatal("the comparison is case-sensitive")
	}
	if EqualPasskey("", "") {
		t.Fatal("an empty current passkey must never match — that is 'remote is off'")
	}
	if EqualPasskey("", "anything") {
		t.Fatal("an empty current passkey must never match")
	}
	if EqualPasskey("K7m2QxP9", "") {
		t.Fatal("an empty candidate must not match")
	}
}

func TestParseAddress(t *testing.T) {
	for _, tc := range []struct {
		in   string
		user string
		host string
		port int
	}{
		{"laptop.lan", "", "laptop.lan", 22},
		{"  laptop.lan  ", "", "laptop.lan", 22},
		{"cmj@laptop.lan", "cmj", "laptop.lan", 22},
		{"laptop.lan:2222", "", "laptop.lan", 2222},
		{"cmj@laptop.lan:2222", "cmj", "laptop.lan", 2222},
		{"10.0.0.4", "", "10.0.0.4", 22},
		{"::1", "", "::1", 22},
		{"[::1]", "", "::1", 22},
		{"[::1]:2222", "", "::1", 2222},
		{"cmj@[fe80::1]:22", "cmj", "fe80::1", 22},
	} {
		got, err := ParseAddress(tc.in)
		if err != nil {
			t.Fatalf("ParseAddress(%q): %v", tc.in, err)
		}
		if got.User != tc.user || got.Host != tc.host || got.Port != tc.port {
			t.Fatalf("ParseAddress(%q) = %+v, want user=%q host=%q port=%d", tc.in, got, tc.user, tc.host, tc.port)
		}
	}
}

func TestParseAddressRejectsNonsense(t *testing.T) {
	for _, in := range []string{"", "   ", "@host", "host:0", "host:70000", "host:ssh", "host:"} {
		if got, err := ParseAddress(in); err == nil {
			t.Fatalf("ParseAddress(%q) = %+v, want an error", in, got)
		}
	}
}

func TestAddressTargetAndString(t *testing.T) {
	a, _ := ParseAddress("cmj@laptop.lan:2222")
	if got := a.Target(); got != "cmj@laptop.lan" {
		t.Fatalf("Target() = %q, want cmj@laptop.lan — the port travels as -p", got)
	}
	if got := a.String(); got != "cmj@laptop.lan:2222" {
		t.Fatalf("String() = %q", got)
	}

	b, _ := ParseAddress("laptop.lan")
	if got := b.Target(); got != "laptop.lan" {
		t.Fatalf("Target() = %q", got)
	}
	if got := b.String(); got != "laptop.lan" {
		t.Fatalf("String() = %q — the default port should not be spelled out", got)
	}

	c, _ := ParseAddress("[::1]:2222")
	if got := c.String(); got != "[::1]:2222" {
		t.Fatalf("String() = %q — an IPv6 host needs its brackets back", got)
	}
}

func TestLimiterBlocksAfterMaxAndForgetsWithTheWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	lim := NewLimiter(3, time.Minute)
	lim.SetClock(func() time.Time { return now })

	for i := range 3 {
		if !lim.Allow() {
			t.Fatalf("attempt %d should be allowed", i)
		}
		lim.Fail()
	}
	if lim.Allow() {
		t.Fatal("a fourth attempt inside the window should be refused")
	}

	// The failures age out of the sliding window.
	now = now.Add(61 * time.Second)
	if !lim.Allow() {
		t.Fatal("attempts should be allowed again once the window has passed")
	}
}

func TestLimiterFailCountsWithinTheWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	lim := NewLimiter(5, time.Minute)
	lim.SetClock(func() time.Time { return now })

	if n := lim.Fail(); n != 1 {
		t.Fatalf("first Fail() = %d, want 1", n)
	}
	if n := lim.Fail(); n != 2 {
		t.Fatalf("second Fail() = %d, want 2", n)
	}
	lim.Reset()
	if n := lim.Fail(); n != 1 {
		t.Fatalf("after Reset, Fail() = %d, want 1", n)
	}
}

func TestNewLimiterFallsBackToDefaults(t *testing.T) {
	lim := NewLimiter(0, 0)
	if lim.max != DefaultMaxAttempts || lim.window != DefaultWindow {
		t.Fatalf("NewLimiter(0,0) = max %d window %v, want the defaults", lim.max, lim.window)
	}
	// A hand-edited config must not be able to switch the limiter off.
	for range DefaultMaxAttempts {
		lim.Fail()
	}
	if lim.Allow() {
		t.Fatal("the default limiter should block past its default cap")
	}
}
