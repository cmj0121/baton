package scrub

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// This suite is written against a specific failure mode rather than for
// coverage. The filter it guards was copied three times precisely because the
// packages could not share it (#47), and what makes that dangerous is not a
// filter that stops working — that shows up immediately — but one that stops
// dropping ONE class while still dropping the others. A single round-trip test
// ("an ESC does not survive") passes against a filter doing a third of its job.
//
// So the classes are enumerated, one case each, and every case names a marker
// that must survive alongside the rune that must not. A widening that swallowed
// a kept class fails on the marker; a narrowing that stopped taking one dropped
// class fails on exactly that class's row and no other.
//
// Every rune the filter is meant to remove is written as an escape rather than
// as itself. They are by definition invisible or destructive in a source file,
// and a table whose rows an editor can silently eat is not a table.

// dropCases is the table of what the filter MUST remove: one row per rune class,
// plus the specific sequences that motivated the class. Each input carries the
// marker "ok" so a case cannot pass by scrubbing everything away.
var dropCases = []struct{ name, in, want string }{
	// Cc / C0 — the class the whole filter exists for.
	{"C0 NUL", "ok\x00ok", "okok"},
	{"C0 BEL terminates an OSC sequence", "ok\aok", "okok"},
	{"C0 ESC introduces every escape sequence", "ok\x1bok", "okok"},
	{"C0 DEL", "ok\x7fok", "okok"},
	{"an escape loses its ESC and stays visible", "\x1b[1;31mred", "[1;31mred"},
	{"an OSC 52 clipboard write cannot reach the terminal", "\x1b]52;c;aGk=\adone", "]52;c;aGk=done"},
	{"an OSC 9 payload cannot be closed early", "ok\a\x1b]52;c;aGk=\aok", "ok]52;c;aGk=ok"},

	// Cc / C1 — the single-byte introducers some terminals still honour. These
	// are NOT reachable through the C0 rows: unicode.IsControl covers both
	// halves, and a change that narrowed it to C0 would leave every row above
	// green while handing those terminals an introducer.
	{"C1 CSI", "ok\u009bok", "okok"},
	{"C1 OSC", "ok\u009dok", "okok"},
	{"C1 DCS", "ok\u0090ok", "okok"},

	// Cf — invisible to unicode.IsControl, and the reason Drop cannot be a
	// single IsControl call. Each of these tells a lie rather than executing.
	{"Cf RLO cannot reverse the line", "ok\u202egnp.exe", "okgnp.exe"},
	{"Cf LRI/PDI isolates go with it", "\u2066ok\u2069 text", "ok text"},
	{"Cf ZWSP cannot hide a word break", "ok\u200bok", "okok"},
	{"Cf BOM mid-string", "ok\ufeffok", "okok"},
	{"Cf soft hyphen", "ok\u00adok", "okok"},

	// The replacement character — what an invalid UTF-8 byte decodes to. It is
	// in neither category the two calls above test, so it needs its own arm of
	// Drop and its own row here.
	{"the replacement character is dropped", "ok\ufffdok", "okok"},
	{"invalid UTF-8 bytes decode to it and go", "ok" + string([]byte{0xff, 0xfe}) + "ok", "okok"},

	// Whitespace is FOLDED, not dropped: a filter that removed it would run
	// words together, and one that kept a newline would break the one-line
	// contract every caller's surface depends on.
	{"a newline folds to one space", "line one\nline two", "line one line two"},
	{"a carriage return folds too", "ok\r\nok", "ok ok"},
	{"a tab folds", "ok\tok", "ok ok"},
	{"a run of mixed whitespace folds to one space", "a \t\n  b", "a b"},
	{"a non-breaking space folds", "ok\u00a0ok", "ok ok"},
	{"an ideographic space folds", "ok\u3000ok", "ok ok"},
	{"NEL is whitespace before it is C1, and folds", "ok\u0085ok", "ok ok"},
	{"leading and trailing space is dropped", "  hemmed in  ", "hemmed in"},
	{"whitespace around a dropped rune does not double", "ok \u200b ok", "ok ok"},
	{"nothing but noise scrubs to nothing", "\x1b\a\n", ""},
	{"the empty string survives", "", ""},
}

// keepCases is the other half of the same guard: text the filter must NOT touch.
// Without it, "drop everything but ASCII letters" passes every row above.
var keepCases = []struct{ name, in string }{
	{"ordinary ASCII", "which migration do I run first?"},
	{"CJK", "要先跑哪一個 migration？"},
	{"an emoji is a symbol, not a control", "shipped \U0001f680 today"},
	{"a combining mark is a mark, not a format rune", "e\u0301clair"},
	{"punctuation and brackets", "run `make ci` — then [check] {this}"},
	{"a private-use rune is not a control character", "ok\ue000ok"},
}

// TestTextDropsEveryClass walks the table above. A failure names the class, so a
// filter that stopped taking one of the three fails on that class's rows alone.
func TestTextDropsEveryClass(t *testing.T) {
	for _, tc := range dropCases {
		if got := Text(tc.in); got != tc.want {
			t.Errorf("%s: Text(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// TestTextKeepsWhatItMustKeep pins the other direction. The filter guards against
// what a terminal EXECUTES, not against a taste in alphabets, so a widening that
// took a whole script or symbol class with it is a bug and not a stricter fix.
func TestTextKeepsWhatItMustKeep(t *testing.T) {
	for _, tc := range keepCases {
		if got := Text(tc.in); got != tc.in {
			t.Errorf("%s: Text(%q) = %q, want it untouched", tc.name, tc.in, got)
		}
	}
}

// TestTextOutputCarriesNoDroppedRune is the invariant behind the table: whatever
// goes in, nothing Drop names comes out and the result is one line. It is the
// claim the per-class rows make falsifiable one class at a time — this one would
// still pass against a filter that dropped far too much, which is exactly why it
// is not the only test here.
func TestTextOutputCarriesNoDroppedRune(t *testing.T) {
	for _, in := range everyInput() {
		out := Text(in)
		for _, r := range out {
			if Drop(r) {
				t.Errorf("Text(%q) = %q kept the dropped rune %U", in, out, r)
			}
			if unicode.IsSpace(r) && r != ' ' {
				t.Errorf("Text(%q) = %q kept the non-space whitespace %U", in, out, r)
			}
		}
		if strings.Contains(out, "  ") {
			t.Errorf("Text(%q) = %q left a doubled space", in, out)
		}
		if out != strings.TrimSpace(out) {
			t.Errorf("Text(%q) = %q is not trimmed", in, out)
		}
		if !utf8.ValidString(out) {
			t.Errorf("Text(%q) = %q is not valid UTF-8", in, out)
		}
	}
}

// TestDropNamesExactlyThreeClasses states the classes as a predicate rather than
// through the strings above, so a fourth class arriving — or one of the three
// leaving — fails here with a name on it. Representative runes per class: a test
// that asked unicode.Is the same questions Drop asks would be a re-implementation
// of Drop, and could not fail.
func TestDropNamesExactlyThreeClasses(t *testing.T) {
	drop := []struct {
		name string
		r    rune
	}{
		{"C0 NUL", 0x00},
		{"C0 BEL", 0x07},
		{"C0 ESC", 0x1b},
		{"C0 DEL", 0x7f},
		{"C1 DCS", 0x90},
		{"C1 CSI", 0x9b},
		{"Cf ZWSP", 0x200b},
		{"Cf RLO", 0x202e},
		{"Cf BOM", 0xfeff},
		{"the replacement character", unicode.ReplacementChar},
	}
	keep := []struct {
		name string
		r    rune
	}{
		{"a space", ' '},
		{"a non-breaking space", 0x00a0},
		{"an ideographic space", 0x3000},
		{"a letter", 'a'},
		{"a Han ideograph", '要'},
		{"an emoji", '\U0001f680'},
		{"a combining acute", 0x0301},
		{"a private-use rune", 0xe000},
		{"the highest valid rune", 0x10ffff},
	}
	for _, tc := range drop {
		if !Drop(tc.r) {
			t.Errorf("%s (%U) must be dropped", tc.name, tc.r)
		}
	}
	for _, tc := range keep {
		if Drop(tc.r) {
			t.Errorf("%s (%U) must not be dropped", tc.name, tc.r)
		}
	}
}

// TestWhitespaceIsFoldedBeforeItIsDropped pins the one piece of ORDER in Text
// that is load-bearing, and it is not obvious: a tab and a newline are Cc, so
// Drop takes both. Only the switch consulting unicode.IsSpace FIRST is what
// makes "a\tb" fold to "a b" instead of collapsing to "ab".
//
// Reordering those two arms compiles, keeps every dropped-class row above green,
// and silently runs words together in every reason, title and score entry that
// arrived with a newline in it. This test is the thing that fails instead.
func TestWhitespaceIsFoldedBeforeItIsDropped(t *testing.T) {
	for _, r := range []rune{'\t', '\n', '\r', '\v', '\f', 0x0085} {
		if !Drop(r) {
			t.Errorf("%U is a control rune and Drop should say so", r)
		}
		if !unicode.IsSpace(r) {
			t.Errorf("%U should be whitespace, or this test is guarding nothing", r)
		}
		if got := Text("a" + string(r) + "b"); got != "a b" {
			t.Errorf("Text(a%Ub) = %q, want %q — the fold must see whitespace before Drop does", r, got, "a b")
		}
	}
}

// TestCappedCountsRunes: the cap is in runes and not bytes, so non-ASCII text is
// not silently held to a third of the length ASCII gets, and a cut never lands
// inside a rune. The three inputs differ only in rune WIDTH, so a byte cap passes
// the first and fails the other two.
func TestCappedCountsRunes(t *testing.T) {
	const limit = 16
	for _, tc := range []struct{ name, in string }{
		{"one-byte runes", strings.Repeat("a", 500)},
		{"three-byte runes", strings.Repeat("要", 500)},
		{"four-byte runes", strings.Repeat("\U0001f680", 500)},
	} {
		got := Capped(tc.in, limit)
		if n := len([]rune(got)); n != limit {
			t.Errorf("%s: Capped kept %d runes, want %d", tc.name, n, limit)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: the cap cut a rune in half: %q", tc.name, got)
		}
	}
	if got := Capped(strings.Repeat("a", limit), limit); len(got) != limit {
		t.Errorf("text exactly at the cap should survive whole, got %d runes", len(got))
	}
	if got := Capped("short", limit); got != "short" {
		t.Errorf("text under the cap should pass through, got %q", got)
	}
}

// TestCappedTrimsTheSpaceTheCutLeaves: the fold never emits a trailing space, but
// a cut can land just after one, and a line ending in a space nobody wrote is a
// line the cap invented.
func TestCappedTrimsTheSpaceTheCutLeaves(t *testing.T) {
	// "abc def ghi" cut at 4 runes is "abc ", which must come back as "abc".
	if got := Capped("abc def ghi", 4); got != "abc" {
		t.Errorf("a cut landing on a space should trim it, got %q", got)
	}
	// The fold turns the run into one space first, so the cut sees the same
	// string whatever whitespace was written.
	if got := Capped("abc \t\n def", 4); got != "abc" {
		t.Errorf("a folded run should cut the same way, got %q", got)
	}
	if got := Capped("abcdef", 4); got != "abcd" {
		t.Errorf("a cut inside a word keeps its runes, got %q", got)
	}
}

// TestCappedScrubsBeforeItCounts pins the order of the two operations. Counting
// first would let text arrive under the cap only because control bytes had spent
// it — the cap would be enforced against a payload the scrub was about to remove,
// and the visible text a caller budgeted for would be cut to nothing.
func TestCappedScrubsBeforeItCounts(t *testing.T) {
	noise := strings.Repeat("\x1b", 100) + "hello"
	if got := Capped(noise, 8); got != "hello" {
		t.Errorf("Capped should scrub before counting, got %q", got)
	}
	if got := Capped("\x1b\a\n", 8); got != "" {
		t.Errorf("pure noise should cap to nothing, got %q", got)
	}
}

// TestNeedsScrubAgreesWithText is the fast path's whole contract: it may only say
// "no" where Text would have returned its input unchanged. A false negative here
// is a silently unfiltered string, which is the one bug in this package that
// reaches a terminal.
func TestNeedsScrubAgreesWithText(t *testing.T) {
	ins := append(everyInput(), "plain ascii", "a", "", " ", "  ", "a  b", " a", "a ", "a\u00a0b")
	for _, in := range ins {
		if needsScrub(in) {
			continue
		}
		// slowText bypasses the fast path, so this compares the two answers
		// rather than comparing Text against itself.
		if got := slowText(in); got != in {
			t.Errorf("needsScrub(%q) said no, but the filter would have made it %q", in, got)
		}
	}
}

// TestNeedsScrubStillCatchesEveryDroppedCase is the same contract read the other
// way: no input the table above says must change may take the fast path.
func TestNeedsScrubStillCatchesEveryDroppedCase(t *testing.T) {
	for _, tc := range dropCases {
		if tc.in == tc.want {
			continue
		}
		if !needsScrub(tc.in) {
			t.Errorf("%s: needsScrub(%q) said no, but the filter must change it to %q", tc.name, tc.in, tc.want)
		}
	}
}

// everyInput is every string either table names, so the invariant tests run over
// the same corpus the per-class tests do.
func everyInput() []string {
	ins := make([]string, 0, len(dropCases)+len(keepCases))
	for _, tc := range dropCases {
		ins = append(ins, tc.in)
	}
	for _, tc := range keepCases {
		ins = append(ins, tc.in)
	}
	return ins
}

// slowText is Text without the fast path, so TestNeedsScrubAgreesWithText has
// something independent to compare against.
func slowText(s string) string {
	var b strings.Builder
	pendingSpace := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
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
