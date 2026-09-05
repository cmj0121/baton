package server

import "testing"

func TestSearchLinesStripsEscapesAndRewrites(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"plain", "one\ntwo", []string{"one", "two"}},
		{"crlf", "one\r\ntwo\r\n", []string{"one", "two", ""}},
		{"ansi stripped", "\x1b[31mred\x1b[0m line", []string{"red line"}},
		{"cr rewrite keeps final", "loading...\rdone", []string{"done"}},
		{"cursor move stripped", "a\x1b[2Kb", []string{"ab"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := searchLines([]byte(tc.raw))
			if len(got) != len(tc.want) {
				t.Fatalf("searchLines(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("searchLines(%q)[%d] = %q, want %q", tc.raw, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestCompileFleetSearchCaseInsensitive(t *testing.T) {
	re, err := compileFleetSearch("Baton")
	if err != nil {
		t.Fatalf("compileFleetSearch: %v", err)
	}
	if !re.MatchString("the BATON conducts") {
		t.Fatal("fleet search should be case-insensitive")
	}
}

func TestCompileFleetSearchLiteralFallback(t *testing.T) {
	// An invalid regexp must not panic — it falls back to a literal match, so the
	// raw term still finds itself (matching the cockpit's scrollback search rule).
	re, err := compileFleetSearch("cost[") // unterminated class: not a valid regexp
	if err != nil {
		t.Fatalf("compileFleetSearch: %v", err)
	}
	if !re.MatchString("the cost[ of it") {
		t.Fatal("an invalid regexp should fall back to a literal match")
	}
	if re.MatchString("no bracket here") {
		t.Fatal("the literal fallback should only match the raw term")
	}
}

// TestCompileFleetSearchRefusesInvalidUTF8 pins the one term the literal fallback
// cannot express. regexp.QuoteMeta escapes metacharacters but passes invalid UTF-8
// through, and regexp rejects that — so before this returned an error, a one-byte
// term panicked the command loop, and with it the daemon and every panel in it.
func TestCompileFleetSearchRefusesInvalidUTF8(t *testing.T) {
	if _, err := compileFleetSearch("\xff"); err == nil {
		t.Fatal("an invalid-UTF-8 term must be refused, not asserted away")
	}
}
