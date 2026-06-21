package signals

import (
	"syscall"
	"testing"
)

func TestLookup(t *testing.T) {
	cases := []struct {
		in   string
		want syscall.Signal
		ok   bool
	}{
		{"SIGINT", syscall.SIGINT, true},
		{"int", syscall.SIGINT, true},   // prefix optional, case-insensitive
		{"Term", syscall.SIGTERM, true}, // mixed case
		{"WINCH", syscall.SIGWINCH, true},
		{"9", syscall.Signal(9), true}, // by number
		{"  hup  ", syscall.SIGHUP, true},
		{"", 0, false},
		{"nope", 0, false},
		{"SIGNOPE", 0, false},
		{"0", 0, false},   // out of range
		{"999", 0, false}, // out of range
	}
	for _, c := range cases {
		got, ok := Lookup(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("Lookup(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestChoicesResolve makes sure every picker shortcut is a name Lookup accepts —
// the cockpit can never offer a signal the server would reject.
func TestChoicesResolve(t *testing.T) {
	for _, c := range Choices {
		if sig, ok := Lookup(c.Name); !ok || sig != c.Sig {
			t.Errorf("choice %s does not resolve through Lookup: (%v, %v)", c.Name, sig, ok)
		}
	}
}
