package tui

import (
	"fmt"
	"strings"
)

// splitCommandLine splits one typed command line into the program and the
// arguments to run it with. It exists because the new-panel form takes a line
// and proto.Command takes a Path and an Args, and strings.Fields is not a safe
// bridge between them: /Applications/Some App/bin/tool is an ordinary macOS path
// and whitespace splitting turns it into three arguments that name nothing.
//
// The rules are the smallest set that covers what an operator types into that
// box, and deliberately no more:
//
//   - spaces and tabs separate tokens; runs of them, leading and trailing, are
//     dropped, so a double space is not an empty argument.
//   - between a ' or a " and its partner every character is literal — including
//     the other quote character — and the quotes themselves are removed.
//   - a quote may open and close mid-token, so --dir='My Notes' is one argument,
//     and a quoted half joined to an unquoted one is still a single word.
//   - an empty pair of quotes produces an EMPTY argument. An empty argument is a
//     thing a program can be given, and dropping it would silently shift every
//     argument after it onto the wrong position.
//   - there is no escape character. A backslash is a literal backslash, so a
//     regexp typed here arrives as written; a space is quoted, never escaped.
//
// It is not a shell. No variable expansion, no globbing, no operators: the panel
// runs the program directly, so a line that means something to sh and nothing
// here would be a lie either way. `sh -c '…'` is how you ask for a shell, and it
// tokenises correctly.
//
// An unterminated quote is an ERROR, not a token that runs to end of line. Both
// readings are guesses at a half-typed line, but they differ in what they cost:
// running it spawns a panel with an argument the operator never closed and leaves
// them to work out why it did the wrong thing, where refusing costs one keystroke
// and can say which quote is still open.
func splitCommandLine(s string) ([]string, error) {
	var (
		out     []string
		tok     strings.Builder
		quote   rune // the quote character we are inside, or 0
		started bool // a token has begun — possibly still empty, via '' or ""
	)
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			tok.WriteRune(r)
		case r == '\'' || r == '"':
			quote, started = r, true
		case r == ' ' || r == '\t':
			if started {
				out = append(out, tok.String())
				tok.Reset()
				started = false
			}
		default:
			tok.WriteRune(r)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote", quote)
	}
	if started {
		out = append(out, tok.String())
	}
	return out, nil
}
