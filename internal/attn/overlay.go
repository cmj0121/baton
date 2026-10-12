package attn

import (
	"regexp"
	"strings"
	"unicode"
)

// OverlayLines is how many trailing content lines a permission overlay may
// occupy. A distinctive phrase further up the tail is ordinary log, not a live
// chooser. The monitor and the inbox share this window so a key that answers
// the overlay is answering the same screen the state machine read.
const OverlayLines = 12

// overlayPhrases are distinctive permission-TUI tokens. A bare "allow" or a
// "?" is not one of them, and this list is not a catalogue of every CLI.
var overlayPhrases = []string{
	"don't ask again",
	"do not ask again",
	"do you want to proceed",
	"allow this",
	"allow command",
	"allow tool",
}

// optionYes / optionNo match a permission overlay's numbered choices. The pair
// together is the signal; either token alone (or a bare "No") is ordinary prose.
var (
	optionYes = regexp.MustCompile(`(?:^|[^0-9])(?:❯\s*)?1\.\s+yes\b`)
	optionNo  = regexp.MustCompile(`(?:^|[^0-9])(?:❯\s*)?[23]\.\s+no\b`)
)

// ansiSeq matches a CSI escape sequence. It is the same pattern the monitor
// strips before any other read of a panel's tail, kept here so the inbox can
// apply it without importing the daemon.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// PermissionOverlay reports whether tail is a live permission TUI: a distinctive
// phrase, or a numbered Yes paired with a numbered No, inside the last
// OverlayLines content lines. A trailing "?" is not enough.
func PermissionOverlay(tail []byte) bool {
	return Overlay(ContentLines(string(tail)))
}

// Overlay is PermissionOverlay for a tail that has already been split into
// content lines. Callers that also apply a last-line guess share one split.
func Overlay(lines []string) bool {
	n := len(lines)
	if n == 0 {
		return false
	}
	start := 0
	if n > OverlayLines {
		start = n - OverlayLines
	}
	return overlayMatch(lines[start:])
}

// ContentLines strips CSI, splits on newlines and carriage returns, and drops a
// line only when every visible rune is whitespace or Unicode Box Drawing
// (U+2500–U+257F). A permission TUI's last physical line is often only a border;
// a line like "│ Allow this? │" is content.
func ContentLines(text string) []string {
	raw := strings.FieldsFunc(ansiSeq.ReplaceAllString(text, ""), func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if boxBorderLine(line) {
			continue
		}
		out = append(out, line)
	}
	return out
}

func boxBorderLine(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		if r < '\u2500' || r > '\u257F' {
			return false
		}
	}
	return true
}

func overlayMatch(lines []string) bool {
	var sawYes, sawNo bool
	for _, line := range lines {
		lower := strings.ToLower(line)
		for _, p := range overlayPhrases {
			if strings.Contains(lower, p) {
				return true
			}
		}
		if optionYes.MatchString(lower) {
			sawYes = true
		}
		if optionNo.MatchString(lower) {
			sawNo = true
		}
	}
	return sawYes && sawNo
}
