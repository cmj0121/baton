package attn

import "testing"

func TestPermissionOverlayIgnoresAQuestionMark(t *testing.T) {
	if PermissionOverlay([]byte("Apply this refactor?")) {
		t.Fatal("a trailing ? is the guessing tail, not a permission overlay")
	}
	if PermissionOverlay([]byte("Proceed? (y/n)")) {
		t.Fatal("a (y/n) last line is not an overlay phrase")
	}
}

func TestPermissionOverlayMatchesTheSharedPhrases(t *testing.T) {
	cases := []struct {
		name string
		tail string
		want bool
	}{
		{"allow this above a border", "Allow this\n└────────────────┘", true},
		{"don't ask again", "2. Yes, and don't ask again\n3. No", true},
		{"numbered yes and no", "❯ 1. Yes\n  2. No", true},
		{"numbered yes without no", "1. Yes, tests pass\n2. Coverage holds", false},
		{"csi around the phrase", "\x1b[32mAllow command\x1b[0m\n╰──╯", true},
		{"phrase older than the window", "don't ask again\n" +
			"still compiling\nstill compiling\nstill compiling\nstill compiling\n" +
			"still compiling\nstill compiling\nstill compiling\nstill compiling\n" +
			"still compiling\nstill compiling\nstill compiling\nstill compiling\nok", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		if got := PermissionOverlay([]byte(tc.tail)); got != tc.want {
			t.Errorf("%s: PermissionOverlay() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
