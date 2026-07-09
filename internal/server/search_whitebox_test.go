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
	re := compileFleetSearch("Baton")
	if !re.MatchString("the BATON conducts") {
		t.Fatal("fleet search should be case-insensitive")
	}
}

func TestCompileFleetSearchLiteralFallback(t *testing.T) {
	// An invalid regexp must not panic — it falls back to a literal match, so the
	// raw term still finds itself (matching the cockpit's scrollback search rule).
	re := compileFleetSearch("cost[") // unterminated class: not a valid regexp
	if !re.MatchString("the cost[ of it") {
		t.Fatal("an invalid regexp should fall back to a literal match")
	}
	if re.MatchString("no bracket here") {
		t.Fatal("the literal fallback should only match the raw term")
	}
}
