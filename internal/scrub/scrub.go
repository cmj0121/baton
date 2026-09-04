// Package scrub holds baton's filter for untrusted text that will reach a real
// terminal: it drops the rune classes a terminal would ACT on, folds every run
// of whitespace into a single space, and optionally caps the result in runes.
//
// It exists because that filter used to exist three times, verbatim, and the
// copies were the point rather than an accident (#47). Each guards a different
// boundary — an agent's attention reason entering the daemon, a panel title
// going into an OSC 9 payload, a score entry on its way to a panel's pty — and
// none of the three packages can import another: internal/score is deliberately
// stdlib-only, and internal/server and internal/tui are both far too heavy for
// it to depend on. A copied security filter is a filter that diverges, and the
// divergence is invisible until an escape sequence reaches somebody's terminal.
//
// So this package is stdlib-only and imports nothing of baton's, which is what
// lets all three depend on it. Keep it that way: the moment it grows an import,
// internal/score has to copy the filter back.
//
// What it does NOT do is escape or render. A caller that needs a placeholder for
// text that scrubbed away to nothing, or a cap of its own size, keeps that at
// its own boundary — those differ per call site and always have.
package scrub

import (
	"strings"
	"unicode"
)

// Drop reports whether r is a rune the filter removes. Three classes, and each
// is here for its own reason rather than as part of a general "printable" test:
//
//   - Control characters (Cc, which is both C0 and C1). This is the class the
//     filter exists for: ESC introduces every escape sequence, BEL terminates an
//     OSC one, and the single-byte C1 introducers some terminals still honour do
//     the same job in one byte. Dropping the introducer and keeping its
//     parameters is deliberate — text that tried to carry an escape reads as
//     "[1;31m" rather than quietly becoming clean prose, so whoever sees it can
//     tell something tried.
//   - Format characters (Cf), which unicode.IsControl does not see. U+202E
//     RIGHT-TO-LEFT OVERRIDE and the bidi isolates render a line backwards,
//     U+200B is invisible. They execute nothing, but they lie about what the
//     text says, and every consumer downstream has been told this text is
//     already safe.
//   - The replacement character, which is what an invalid UTF-8 byte decodes
//     to. Nothing legitimate arrives carrying one.
//
// Whitespace is NOT dropped here. Callers fold it (see Text); a filter that
// removed it would run words together.
func Drop(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar
}

// Text scrubs s: every rune Drop names is removed, and every run of whitespace
// becomes a single space, so the result is one line by construction with no
// leading or trailing space. Everything else — non-ASCII, punctuation, symbols,
// emoji — is kept, because this is a filter against what a terminal EXECUTES and
// not a taste in alphabets.
//
// One line is not incidental. Each caller's surface has exactly one to give the
// text: an inbox row, a card, a notification, a line of score.md.
//
// The ORDER of the switch below is load-bearing and is easy to reorder without
// noticing. A tab and a newline are Cc, so Drop takes both; asking IsSpace first
// is the only reason "a\tb" folds to "a b" rather than collapsing to "ab". Swap
// the two arms and every test about a dropped class still passes while words run
// together in every reason, title and entry that arrived with a newline in it.
func Text(s string) string {
	if !needsScrub(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r): // before Drop: a tab and a newline are Cc too
			pendingSpace = true
		case Drop(r):
			continue
		default:
			if pendingSpace && b.Len() > 0 {
				b.WriteRune(' ')
			}
			pendingSpace = false
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Capped is Text with the result cut to maxRunes, which must be positive.
//
// The unit is RUNES and not bytes so that a cap does not silently hold non-ASCII
// text to a third of the length ASCII gets, and so a cut can never land inside a
// rune. A cut can land after a space the fold left mid-string, so the tail is
// trimmed — a line that ends in a space ends in a space nobody wrote.
func Capped(s string, maxRunes int) string {
	out := Text(s)
	if len(out) > maxRunes { // bytes >= runes, so this skips the common case
		if rs := []rune(out); len(rs) > maxRunes {
			out = strings.TrimRight(string(rs[:maxRunes]), " ")
		}
	}
	return out
}

// needsScrub reports whether s needs the builder in Text at all. It says no only
// for plain, single-spaced, printable ASCII, which is what nearly every string
// reaching this package is — and internal/score scrubs every line of score.md
// twice on every dispatch while the file is being edited, so the common case
// must not rebuild the string.
//
// The two rejected classes mirror Text's own work: a byte outside printable
// ASCII (which is every control byte, and every rune Drop might take as Cf, C1,
// or the replacement character), and a space that is leading, trailing, or
// repeated (which is the whitespace folding). Anything it rejects falls through
// and Text itself decides.
func needsScrub(s string) bool {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c < 0x20, c >= 0x7f:
			return true
		case c == ' ' && (i == 0 || i == len(s)-1 || s[i-1] == ' '):
			return true
		}
	}
	return false
}
