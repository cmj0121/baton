// Package remote holds the pieces the SSH remote attach is built from that are
// neither the server nor a frontend: the passkey a fleet owner hands out, the
// address a cockpit dials, and the limiter that slows a wrong passkey down.
//
// It is deliberately transport-thin. The transport is ssh(1) — baton opens no
// port, ships no TLS and invents no key exchange — so what is left here is the
// small amount of policy both ends have to agree on.
package remote

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PasskeyLen is how many characters a passkey has. Eight is short enough to
// read off one screen and type into another, which is the whole job: it proves
// the owner deliberately enabled remote for this window and gives them a
// revocation handle. It is NOT an authentication boundary — see docs/REMOTE.md.
const PasskeyLen = 8

// passkeyAlphabet is the character set a passkey is minted from: digits and
// letters minus the pairs that are read wrong across a room or a video call
// (0/O, 1/l/I). 56 symbols over 8 characters is ~46 bits, which is far more
// than the rate limiter in front of it needs.
// It is spelled in three runs — digits, capitals, lower case — so the omissions
// are readable as omissions rather than as a typo in a wall of characters.
const passkeyAlphabet = "23456789" + "ABCDEFGHJKLMNPQRSTUVWXYZ" + "abcdefghijkmnpqrstuvwxyz" //gitleaks:allow

// NewPasskey mints a fresh passkey from the cryptographic RNG. It is returned
// to be held in memory and shown in the remote overlay; it is never written to
// disk, so a restart always means a new one.
func NewPasskey() (string, error) {
	buf := make([]byte, PasskeyLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint passkey: %w", err)
	}
	// Rejection-free modulo is fine here: len(alphabet) is 56, so the bias over
	// 256 values is under 2% per character and buys an attacker nothing behind a
	// rate limiter that stops them at a handful of tries.
	out := make([]byte, PasskeyLen)
	for i, b := range buf {
		out[i] = passkeyAlphabet[int(b)%len(passkeyAlphabet)]
	}
	return string(out), nil
}

// EqualPasskey compares a candidate against the current passkey in constant
// time. An empty current passkey never matches — "remote is not enabled" must
// not be reachable by sending an empty one.
func EqualPasskey(current, candidate string) bool {
	if current == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(current), []byte(candidate)) == 1
}

// DefaultPort is the port an address with no port of its own is dialled on. It
// is ssh's port because the transport IS ssh: baton binds nothing.
const DefaultPort = 22

// Address is a parsed remote target: the optional login, the host, and the port
// (DefaultPort unless the address named one).
type Address struct {
	User string
	Host string
	Port int
}

// ParseAddress reads the forms a person actually types — `host`, `user@host`,
// `host:port`, `user@host:port`, and the bracketed IPv6 spellings of the last
// two. A bare IPv6 literal is accepted unbracketed as well, since `::1` has no
// port to be confused with.
func ParseAddress(s string) (Address, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Address{}, fmt.Errorf("remote address is empty")
	}

	addr := Address{Port: DefaultPort}
	// Split the login off first: an ssh login may not contain '@', while an IPv6
	// host may not either, so the LAST '@' is unambiguous.
	if i := strings.LastIndex(s, "@"); i >= 0 {
		addr.User, s = s[:i], s[i+1:]
		if addr.User == "" {
			return Address{}, fmt.Errorf("remote address has an empty user")
		}
	}

	host, port, err := splitHostPort(s)
	if err != nil {
		return Address{}, err
	}
	if host == "" {
		return Address{}, fmt.Errorf("remote address has an empty host")
	}
	addr.Host = host
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return Address{}, fmt.Errorf("remote address has an invalid port %q", port)
		}
		addr.Port = n
	}
	return addr, nil
}

// splitHostPort separates an optional :port from a host, tolerating the bare
// IPv6 literal that net.SplitHostPort refuses and the bracketed one it wants.
func splitHostPort(s string) (host, port string, err error) {
	switch {
	case strings.HasPrefix(s, "["):
		// [::1] or [::1]:2222 — the bracket form always goes through net's parser,
		// which is the only thing that reads a zone id correctly.
		if strings.HasSuffix(s, "]") {
			return strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"), "", nil
		}
		h, p, err := net.SplitHostPort(s)
		if err != nil {
			return "", "", fmt.Errorf("remote address %q is not host:port", s)
		}
		return h, p, nil
	case strings.Count(s, ":") > 1:
		return s, "", nil // a bare IPv6 literal: every colon belongs to the address
	case strings.Contains(s, ":"):
		h, p, err := net.SplitHostPort(s)
		if err != nil {
			return "", "", fmt.Errorf("remote address %q is not host:port", s)
		}
		if p == "" {
			// "host:" parses cleanly but is a typo, not a request for the default —
			// quietly dialling 22 would hide the half-typed port rather than fix it.
			return "", "", fmt.Errorf("remote address %q has a trailing colon and no port", s)
		}
		return h, p, nil
	default:
		return s, "", nil
	}
}

// Target is the `[user@]host` ssh is given as its destination; the port travels
// separately as -p, which is the only spelling ssh accepts.
func (a Address) Target() string {
	if a.User == "" {
		return a.Host
	}
	return a.User + "@" + a.Host
}

// String is the address as a person would write it back — the port shown only
// when it is not the default, so an ordinary target reads as what was typed.
func (a Address) String() string {
	host := a.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	s := host
	if a.User != "" {
		s = a.User + "@" + host
	}
	if a.Port != DefaultPort && a.Port != 0 {
		s += ":" + strconv.Itoa(a.Port)
	}
	return s
}

// Attempt limiter defaults. Five wrong passkeys inside a minute is well past
// anything a person mistypes, and holding the door for that minute turns an
// online guess at 46 bits into something that never finishes.
const (
	DefaultMaxAttempts = 5
	DefaultWindow      = time.Minute
)

// Limiter slows a wrong passkey down: it counts failures inside a sliding
// window and blocks once too many sit in it. It is safe for concurrent use —
// one lives on the server and every accepted connection consults it.
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  []time.Time

	// now is time.Now in production; tests replace it to drive the window
	// without sleeping.
	now func() time.Time
}

// NewLimiter builds a limiter allowing max failures per window. Non-positive
// arguments fall back to the defaults, so a hand-edited config can never switch
// the limiter off by accident.
func NewLimiter(max int, window time.Duration) *Limiter {
	if max <= 0 {
		max = DefaultMaxAttempts
	}
	if window <= 0 {
		window = DefaultWindow
	}
	return &Limiter{max: max, window: window, now: time.Now}
}

// SetClock replaces the limiter's clock. It exists for tests.
func (l *Limiter) SetClock(now func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
}

// Allow reports whether another attempt may be made now.
func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	return len(l.fails) < l.max
}

// Fail records a rejected attempt and returns how many sit in the window,
// including this one — what the log line reports.
func (l *Limiter) Fail() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	l.fails = append(l.fails, l.now())
	return len(l.fails)
}

// Reset forgets every recorded failure. The server calls it when the passkey
// rotates: a new code deserves a clean slate, and the old failures were against
// a code that no longer opens anything.
func (l *Limiter) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails = nil
}

// prune drops failures that have aged out of the window. The caller holds l.mu.
func (l *Limiter) prune() {
	cutoff := l.now().Add(-l.window)
	keep := l.fails[:0]
	for _, t := range l.fails {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	l.fails = keep
}
