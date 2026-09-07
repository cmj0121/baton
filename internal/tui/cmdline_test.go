package tui

import (
	"strings"
	"testing"
)

// TestSplitCommandLine is the table for every rule splitCommandLine's doc
// comment claims, because the rules are the whole of it: the function is what
// stands between a line an operator typed and the argv a panel is spawned with,
// and each row here is a line somebody will type.
func TestSplitCommandLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"only spaces", "   \t ", nil},
		{"one word", "make", []string{"make"}},
		{"program and argument", "make test", []string{"make", "test"}},
		{"go test", "go test ./...", []string{"go", "test", "./..."}},
		{"runs of space collapse", "go  test\t./...", []string{"go", "test", "./..."}},
		{"leading and trailing space", "  make test  ", []string{"make", "test"}},

		// The reason this is not strings.Fields. Both are ordinary on macOS.
		{"quoted path with a space", `"/Applications/Some App/bin/tool" --now`,
			[]string{"/Applications/Some App/bin/tool", "--now"}},
		{"single-quoted argument", `go test -run 'Foo Bar'`,
			[]string{"go", "test", "-run", "Foo Bar"}},

		{"quote opens mid-token", `tar --dir='My Notes' -c`, []string{"tar", "--dir=My Notes", "-c"}},
		{"quotes rejoin one word", `'go'"test"`, []string{"gotest"}},
		{"the other quote is literal inside", `echo "it's here"`, []string{"echo", "it's here"}},
		{"double quote inside single", `echo 'say "hi"'`, []string{"echo", `say "hi"`}},

		// An empty argument is a thing a program can be given; dropping it would
		// shift every argument after it onto the wrong position.
		{"empty quoted argument", `grep '' file`, []string{"grep", "", "file"}},

		// No escape character: a backslash is a literal backslash, so a regexp
		// arrives as it was typed.
		{"backslash is literal", `grep -E '\d+' .`, []string{"grep", "-E", `\d+`, "."}},
		{"backslash does not escape a space", `a\ b`, []string{`a\`, "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitCommandLine(tc.in)
			if err != nil {
				t.Fatalf("splitCommandLine(%q) = error %v, want %q", tc.in, err, tc.want)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("splitCommandLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("splitCommandLine(%q) = %q, want %q", tc.in, got, tc.want)
				}
			}
		})
	}
}

// An unterminated quote is refused rather than read to end of line. The choice is
// the point of the test: a line the operator has not finished quoting is a line
// they have not finished typing, and running it spawns a panel whose argv nobody
// asked for. The refusal has to say WHICH quote is open, or it sends the operator
// hunting through their own line for it.
func TestSplitCommandLineRefusesAnUnterminatedQuote(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    string
		quote string
	}{
		{"single", `go test -run 'Foo`, "'"},
		{"double", `tool "/Applications/Some App`, `"`},
		{"a lone quote", `'`, "'"},
		{"closed then reopened", `echo 'one' 'two`, "'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitCommandLine(tc.in)
			if err == nil {
				t.Fatalf("splitCommandLine(%q) = %q, want a refusal", tc.in, got)
			}
			if got != nil {
				t.Errorf("a refused line still yielded %q; it must spawn nothing", got)
			}
			if !strings.Contains(err.Error(), tc.quote) {
				t.Errorf("refusal %q never names the %s quote that is open", err, tc.quote)
			}
		})
	}
}
