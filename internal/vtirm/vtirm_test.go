package vtirm

import (
	"strings"
	"testing"
)

// TestRewrite pins the translation itself: what a chunk looks like on the way into the
// emulator, insert mode on and off.
func TestRewrite(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// The bug from the issue, byte for byte: readline's way of inserting one
			// character at the front of a line.
			"single character insert becomes ICH",
			"\x1b[4ha\x1b[4l",
			"\x1b[1@a",
		},
		{"a run of characters gets one ICH", "\x1b[4habc\x1b[4l", "\x1b[3@abc"},
		{"a wide character opens two cells", "\x1b[4h世\x1b[4l", "\x1b[2@世"},
		{"a combining mark stays with its cluster", "\x1b[4hé\x1b[4l", "\x1b[1@é"},
		{
			// A control code is not a printed cell, so it closes the run and the cells
			// after it open a gap of their own.
			"a control code splits the run",
			"\x1b[4hab\rcd\x1b[4l",
			"\x1b[2@ab\r\x1b[2@cd",
		},
		{
			// Cursor moves inside a burst are common — readline walks back over what it
			// redrew — and each stretch of printing needs its own gap.
			"a cursor move splits the run",
			"\x1b[4hab\x1b[3Dcd\x1b[4l",
			"\x1b[2@ab\x1b[3D\x1b[2@cd",
		},
		{"insert mode ends at the reset", "\x1b[4ha\x1b[4lb", "\x1b[1@ab"},
		{"a reset with no set is still consumed", "\x1b[4lxy", "xy"},
		{"mode 4 is dropped from a multi-mode set", "\x1b[4;20ha\x1b[20;4l", "\x1b[20h\x1b[1@a\x1b[20l"},

		// Everything below must come out byte for byte as it went in.
		{"plain text is untouched", "ls a/b/c/d", "ls a/b/c/d"},
		{"colours are untouched", "\x1b[31mred\x1b[0m", "\x1b[31mred\x1b[0m"},
		{"underline is not mode 4", "\x1b[4munder\x1b[24m", "\x1b[4munder\x1b[24m"},
		{"DECSCLM is not IRM", "\x1b[?4hsmooth\x1b[?4l", "\x1b[?4hsmooth\x1b[?4l"},
		{"a mode set without 4 is untouched", "\x1b[20ha\x1b[20l", "\x1b[20ha\x1b[20l"},
		{"ICH from a shell that asks for it directly", "\x1b[1@a", "\x1b[1@a"},
		{"the 8-bit CSI form is left alone", "\x9b4ha\x9b4l", "\x9b4ha\x9b4l"},
		{"an alt-screen switch is untouched", "\x1b[?1049h\x1b[2Jhi", "\x1b[?1049h\x1b[2Jhi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var f Filter
			if got := string(f.Rewrite([]byte(c.in))); got != c.want {
				t.Errorf("Rewrite(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestRewriteAcrossChunks checks the state a Filter has to carry: the bytes arrive in
// whatever sizes the PTY read and the socket produced, and a burst can be split anywhere
// — including in the middle of the escape sequence that turns insert mode on.
func TestRewriteAcrossChunks(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		want   string
	}{
		{"the burst is split at the mode set", []string{"\x1b[4h", "ab", "\x1b[4l"}, "\x1b[2@ab"},
		{"the mode set itself is split", []string{"\x1b[", "4ha", "\x1b[4l"}, "\x1b[1@a"},
		{"insert mode survives a quiet chunk", []string{"\x1b[4h", "", "z", "\x1b[4l"}, "\x1b[1@z"},
		{"the mode reset is split", []string{"\x1b[4hq\x1b", "[4l", "r"}, "\x1b[1@qr"},
		{"a plain stream stays plain", []string{"ls ", "a/b", "/c/d"}, "ls a/b/c/d"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var f Filter
			var got string
			for _, chunk := range c.chunks {
				got += string(f.Rewrite([]byte(chunk)))
			}
			if got != c.want {
				t.Errorf("Rewrite over %q = %q, want %q", c.chunks, got, c.want)
			}
		})
	}
}

// TestRewriteReleasesWhatItCannotUse covers the other half of holding a half-read
// sequence: bytes that can never turn out to be a mode set have to go straight through, so
// a program drawing with long or non-CSI sequences is never held up by this filter.
func TestRewriteReleasesWhatItCannotUse(t *testing.T) {
	long := "\x1b[" + strings.Repeat("1;", heldMax) + "1m" // a CSI longer than the hold cap
	cases := []struct {
		name   string
		chunks []string
		want   string
	}{
		{"a split OSC is not worth holding", []string{"\x1b]0;a title", "\x07done"}, "\x1b]0;a title\x07done"},
		{"a split ESC sequence is not worth holding", []string{"\x1b", "(Btext"}, "\x1b(Btext"},
		{"a CSI past the hold cap is let go", []string{long[:20], long[20:]}, long},
		{"a chunk ending on a bare ESC", []string{"hi\x1b", "[31mred"}, "hi\x1b[31mred"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var f Filter
			var got string
			for _, chunk := range c.chunks {
				got += string(f.Rewrite([]byte(chunk)))
			}
			if got != c.want {
				t.Errorf("Rewrite over %q = %q, want %q", c.chunks, got, c.want)
			}
		})
	}
}

// TestParam pins the parameter reader on the values that must never be mistaken for mode
// 4: the omitted parameter (which defaults to 0) and a run of digits too long to be a mode
// anyone means.
func TestParam(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"4", 4},
		{"04", 4},
		{"0004", 4},
		{"", -1},
		{"00004", -1},
		{"40", 40},
	}
	for _, c := range cases {
		if got := param([]byte(c.in)); got != c.want {
			t.Errorf("param(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestRewriteFastPath pins the allocation-free path: a chunk that cannot possibly need
// the rewrite comes back as the identical backing array, not a copy. Every emulator-bound
// byte in baton passes through here, so the common chunk has to stay free.
func TestRewriteFastPath(t *testing.T) {
	var f Filter
	in := []byte("a shell prompt and some plain output")
	got := f.Rewrite(in)
	if len(got) == 0 || &got[0] != &in[0] {
		t.Errorf("Rewrite allocated on a chunk with no escape; want the same backing slice")
	}
}

// TestRewriteNilFilter covers the caller that feeds an emulator it does not own: no
// filter, no rewrite, no panic.
func TestRewriteNilFilter(t *testing.T) {
	var f *Filter
	in := []byte("\x1b[4ha\x1b[4l")
	if got := string(f.Rewrite(in)); got != string(in) {
		t.Errorf("nil Filter Rewrite = %q, want the input unchanged", got)
	}
}

// TestRewriteStaysInInsertMode is the guard against the rewrite leaking past its burst: a
// stream that never resets mode 4 keeps inserting, and one that resets it stops.
func TestRewriteStaysInInsertMode(t *testing.T) {
	var f Filter
	if got := string(f.Rewrite([]byte("\x1b[4hx"))); got != "\x1b[1@x" {
		t.Fatalf("first chunk = %q, want %q", got, "\x1b[1@x")
	}
	if got := string(f.Rewrite([]byte("y"))); got != "\x1b[1@y" {
		t.Fatalf("second chunk = %q, want %q; insert mode should still be set", got, "\x1b[1@y")
	}
	if got := string(f.Rewrite([]byte("\x1b[4lz"))); got != "z" {
		t.Fatalf("third chunk = %q, want %q; insert mode should be clear", got, "z")
	}
}
