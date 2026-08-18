package panellog

import (
	"strings"
	"testing"
)

// TestStripSequences covers the sequence families the machine knows, each fed as
// one chunk: what a terminal would have consumed is dropped, what a human would
// have read survives.
func TestStripSequences(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"csi-colour", "\x1b[31mred\x1b[0m", "red"},
		{"csi-erase", "\x1b[2Kline", "line"},
		{"csi-private", "\x1b[?1049hvim\x1b[?1049l", "vim"},
		{"osc-bel", "\x1b]0;a title\x07text", "text"},
		{"osc-st", "\x1b]7;file:///tmp\x1b\\text", "text"},
		{"dcs", "\x1bPsome dcs\x1b\\after", "after"},
		{"charset", "\x1b(Bplain", "plain"},
		{"two-byte", "\x1bcreset", "reset"},
		{"c0-dropped", "a\x07b\x08c", "abc"},
		{"tab-kept", "a\tb", "a\tb"},
		{"crlf", "one\r\ntwo\r\n", "one\ntwo\n"},
		{"lone-cr", "draft\rfinal\n", "draft\nfinal\n"},
		{"lf-after-other", "a\rb\n", "a\nb\n"},
		{"cr-then-esc-then-lf", "a\r\x1b[K\n", "a\n\n"},
	}
	for _, tt := range tests {
		var s Stripper
		got := string(s.Strip([]byte(tt.in))) + string(s.Flush())
		if got != tt.want {
			t.Errorf("%s: Strip(%q) = %q; want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

// TestStripAcrossChunks is the reason this is a state machine and not a regexp:
// a sequence split at every possible byte boundary must still be removed whole.
func TestStripAcrossChunks(t *testing.T) {
	in := "a\x1b[1;31mb\x1b]0;t\x07c\x1b(Bd"
	const want = "abcd"
	for cut := 0; cut <= len(in); cut++ {
		var s Stripper
		got := string(s.Strip([]byte(in[:cut])))
		got += string(s.Strip([]byte(in[cut:])))
		got += string(s.Flush())
		if got != want {
			t.Errorf("split at %d: got %q; want %q", cut, got, want)
		}
	}
}

// TestStripByteAtATime feeds the whole stream one byte per call — the worst case
// a slow PTY read can produce.
func TestStripByteAtATime(t *testing.T) {
	in := "\x1b[32mgreen\x1b[0m\r\ndone\n"
	var s Stripper
	var b strings.Builder
	for i := 0; i < len(in); i++ {
		b.Write(s.Strip([]byte{in[i]}))
	}
	b.Write(s.Flush())
	if got, want := b.String(), "green\ndone\n"; got != want {
		t.Errorf("byte-at-a-time = %q; want %q", got, want)
	}
}

// TestStripUnterminatedGivesUp checks the bound on a sequence that never ends: a
// program that opens an OSC and forgets to close it must not swallow the log.
func TestStripUnterminatedGivesUp(t *testing.T) {
	var s Stripper
	junk := "\x1b]0;" + strings.Repeat("x", pendingCap+16)
	got := string(s.Strip([]byte(junk)))
	if len(got) == 0 {
		t.Fatalf("an unterminated sequence past the cap should be emitted as text, got nothing")
	}
	if !strings.Contains(got, "xxxx") {
		t.Errorf("gave up but kept nothing readable: %q", got[:min(40, len(got))])
	}
}

// TestStripFlushHoldsPartial checks that a sequence cut off at the end of the
// stream is emitted as literal text rather than silently dropped.
func TestStripFlushHoldsPartial(t *testing.T) {
	var s Stripper
	if got := string(s.Strip([]byte("ok\x1b[3"))); got != "ok" {
		t.Fatalf("Strip = %q; want %q", got, "ok")
	}
	if got := string(s.Flush()); got != "\x1b[3" {
		t.Errorf("Flush = %q; want the held partial sequence", got)
	}
	if got := string(s.Flush()); got != "" {
		t.Errorf("second Flush = %q; want nothing left", got)
	}
}

// TestStripStrayEscInString covers an ESC inside an OSC that is not the ST: the
// machine keeps consuming rather than ending the sequence early.
func TestStripStrayEscInString(t *testing.T) {
	var s Stripper
	got := string(s.Strip([]byte("\x1b]0;a\x1bXb\x07tail")))
	if got != "tail" {
		t.Errorf("Strip = %q; want %q", got, "tail")
	}
}
