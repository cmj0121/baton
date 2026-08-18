package vtquery

import "testing"

// TestOSC7Path: the report is read in both its terminations, the host comes back
// beside the path so the caller can decide whether it means anything locally, and
// a percent-encoded directory arrives usable.
func TestOSC7Path(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		path, host string
		ok         bool
	}{
		{"BEL terminated", "\x1b]7;file://box.local/tmp/x\x07", "/tmp/x", "box.local", true},
		{"ST terminated", "\x1b]7;file://box.local/tmp/x\x1b\\", "/tmp/x", "box.local", true},
		{"no host", "\x1b]7;file:///srv/app\x07", "/srv/app", "", true},
		{"percent-encoded", "\x1b]7;file://h/tmp/a%20b\x07", "/tmp/a b", "h", true},
		{"amid other output", "ready$ \x1b]7;file://h/w\x07\x1b[0m", "/w", "h", true},
		{"not a report", "\x1b]0;a title\x07", "", "", false},
		{"no sequence at all", "plain output", "", "", false},
		{"wrong scheme", "\x1b]7;http://h/w\x07", "", "", false},
		{"empty payload", "\x1b]7;\x07", "", "", false},
		{"unterminated", "\x1b]7;file://h/w", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path, host, ok := OSC7Path([]byte(c.in))
			if path != c.path || host != c.host || ok != c.ok {
				t.Fatalf("OSC7Path(%q) = %q/%q/%v, want %q/%q/%v", c.in, path, host, ok, c.path, c.host, c.ok)
			}
		})
	}
}

// TestOSC7PathTakesTheLast: a chunk can carry several prompts' worth of reports,
// and only the newest says where the shell is now.
func TestOSC7PathTakesTheLast(t *testing.T) {
	in := "\x1b]7;file://h/first\x07out\x1b]7;file://h/second\x07"
	if path, _, ok := OSC7Path([]byte(in)); !ok || path != "/second" {
		t.Fatalf("got %q/%v, want /second", path, ok)
	}
}

// TestHasOSC7Gate: the cheap check must agree with the parse on whether there is
// anything to find, or the hot path would skip real reports.
func TestHasOSC7Gate(t *testing.T) {
	with := []byte("x\x1b]7;file://h/w\x07")
	without := []byte("\x1b]0;title\x07plain")
	if !HasOSC7(with) {
		t.Error("the gate missed a real report")
	}
	if HasOSC7(without) {
		t.Error("the gate fired on output with no report")
	}
}

// TestOSC7SurvivesStrip: the stripper removes sequences the emulator would answer.
// A working-directory report answers nothing, so it must reach the reader intact.
func TestOSC7SurvivesStrip(t *testing.T) {
	in := "a\x1b]7;file://h/tmp/x\x07b"
	if got := string(Strip([]byte(in))); got != in {
		t.Fatalf("Strip altered the report: %q", got)
	}
}
