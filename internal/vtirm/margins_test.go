package vtirm

import (
	"bytes"
	"testing"
)

// TestClampMargins tables what a DECSTBM becomes on a 24-row emulator.
func TestClampMargins(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"\x1b[1;24r", "\x1b[1;24r"},   // fits: untouched
		{"\x1b[r", "\x1b[r"},           // the whole screen: untouched
		{"\x1b[1;40r", "\x1b[1;24r"},   // too tall: bottom pinned to the last row
		{"\x1b[10;99r", "\x1b[10;24r"}, // top kept, bottom pinned
		{"\x1b[30;40r", ""},            // wholly off screen: dropped, as xterm ignores it
		{"\x1b[24;40r", ""},            // one row after the clamp: dropped
		{"\x1b[0;40r", "\x1b[1;24r"},   // zero means default
		{"\x1b[?1;40r", "\x1b[?1;40r"}, // a private sequence is not DECSTBM
		{"\x1b[1;2;3r", "\x1b[1;2;3r"}, // too many parameters: not ours to judge
		{"\x1b[1;40m", "\x1b[1;40m"},   // not a margin at all
	}
	for _, c := range cases {
		f := &Filter{Rows: 24}
		if got := f.Rewrite([]byte(c.in)); string(got) != c.want {
			t.Errorf("Rewrite(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestClampMarginsNeedsRows proves a Filter that was never told a height leaves
// margins alone rather than clamping to nothing.
func TestClampMarginsNeedsRows(t *testing.T) {
	f := &Filter{}
	in := []byte("\x1b[1;40r")
	if got := f.Rewrite(in); !bytes.Equal(got, in) {
		t.Fatalf("Rewrite with Rows=0 = %q, want %q untouched", got, in)
	}
}

// TestClampMarginsAcrossChunks proves a DECSTBM cut by a chunk boundary is still
// clamped once its second half arrives, the same hold that keeps IRM honest.
func TestClampMarginsAcrossChunks(t *testing.T) {
	f := &Filter{Rows: 24}
	var out []byte
	out = append(out, f.Rewrite([]byte("\x1b[1;4"))...)
	out = append(out, f.Rewrite([]byte("0r$ "))...)
	if want := "\x1b[1;24r$ "; string(out) != want {
		t.Fatalf("split DECSTBM = %q, want %q", out, want)
	}
}
