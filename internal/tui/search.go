package tui

import (
	"fmt"
	"regexp"
	"strings"

	vt "github.com/charmbracelet/x/vt"
)

// Scrollback search. C-t f in a zoom (or over the focused group tile) opens a
// find prompt in the footer; on enter the term is matched against the panel's
// scrollback and live screen, the view jumps to the most recent match and holds
// in scroll mode, and n / N walk older / newer hits. The matched term is drawn in
// reverse video on the current line and underlined on the others.
//
// The term is a case-insensitive regular expression. A pattern that does not
// compile falls back to a literal match of the typed text (the footer says so),
// so a plain term with regex metacharacters (e.g. "foo[") still searches for
// itself rather than erroring.

// searchActive reports whether a search has live hits to highlight and navigate.
func (m model) searchActive() bool {
	return m.searchQuery != "" && len(m.searchHits) > 0
}

// openSearch opens the find prompt for whatever scroll mode would target — the
// zoom's emulator, or the focused tile's. A no-op where there is nothing to
// search (e.g. the focus rests on a tree row).
func (m model) openSearch() model {
	if emu, _ := m.scrollTarget(); emu == nil {
		m.status = "nothing to search here"
		return m
	}
	m.input = inputSearch
	m.inputBuf = m.searchQuery // seed with the last term so repeating a search is one keypress
	m.status = "search · type a regexp · enter finds · esc cancels"
	return m
}

// runSearch matches query against the target's scrollback and screen, lands on
// the newest hit, and holds the view in scroll mode so n / N can walk the rest.
// An empty term clears any active search; no hit leaves the view where it was.
//
// Hits are absolute line indices captured here. Lines already in the scrollback
// keep their index, so a search over history stays accurate; only matches on the
// live screen can drift if a noisy program keeps emitting while the search is
// open (the screen rows reindex as they roll into the scrollback). Every consumer
// bounds-checks, so the worst case is a stale highlight, never a crash — search is
// meant for an idle or exited panel, which is the common case.
func (m model) runSearch(query string) model {
	emu, _ := m.scrollTarget()
	if emu == nil {
		m.status = "nothing to search here"
		return m
	}
	if query == "" {
		m = m.clearSearch()
		m.status = "search cleared"
		return m
	}
	re, literal := compileSearch(query)
	lines, _ := combinedPlain(emu)
	hits := make([]int, 0, 8)
	for i, ln := range lines {
		if re.MatchString(ln) {
			hits = append(hits, i)
		}
	}
	if len(hits) == 0 {
		m = m.clearSearch() // drop any prior hits; a failed search leaves nothing active
		m.status = fmt.Sprintf("no match for %q", query)
		return m
	}
	m.searchQuery = query
	m.searchRe = re
	m.searchHits = hits
	m.searchAt = len(hits) - 1 // start on the newest match, nearest the live bottom
	m.scrolling = true         // hold the view so the match stays put and n / N work
	m.positionToHit(emu)
	m.status = m.searchStatus()
	if literal { // the term was not a valid regexp; say so rather than silently downgrading
		m.status = "bad regexp · matched literally · " + m.status
	}
	return m
}

// gotoMatch steps to the next (dir +1, newer) or previous (dir -1, older) hit,
// wrapping at the ends, and re-positions the viewport onto it.
func (m model) gotoMatch(dir int) model {
	if !m.searchActive() {
		return m
	}
	emu, _ := m.scrollTarget()
	if emu == nil {
		return m
	}
	m.searchAt = wrapIndex(m.searchAt, dir, len(m.searchHits))
	m.positionToHit(emu)
	m.status = m.searchStatus()
	return m
}

// clearSearch drops the active search state.
func (m model) clearSearch() model {
	m.searchQuery, m.searchRe, m.searchHits, m.searchAt = "", nil, nil, 0
	return m
}

// compileSearch turns a typed term into a case-insensitive matcher. A pattern
// that fails to compile falls back to a literal match of the raw text (literal
// true), so a search term with regex metacharacters still finds itself instead
// of erroring; the caller surfaces the fallback rather than hiding it. The
// literal form is always valid, so the regexp is never nil.
func compileSearch(query string) (re *regexp.Regexp, literal bool) {
	if re, err := regexp.Compile("(?i)" + query); err == nil {
		return re, false
	}
	return regexp.MustCompile("(?i)" + regexp.QuoteMeta(query)), true
}

// searchContextRows is how many lines of context the viewport keeps above the
// matched line, so a hit lands a little below the top rather than flush against it.
const searchContextRows = 2

// positionToHit scrolls the viewport so the current hit sits near the top, a
// couple of context lines down. A hit already on the live screen rests at the
// bottom (offset clamps to zero).
func (m *model) positionToHit(emu *vt.SafeEmulator) {
	if emu == nil || m.searchAt < 0 || m.searchAt >= len(m.searchHits) {
		return
	}
	sbLen := emu.ScrollbackLen()
	off := sbLen - m.searchHits[m.searchAt] + searchContextRows
	if off < 0 {
		off = 0
	}
	if off > sbLen {
		off = sbLen
	}
	m.scrollOff = off
}

// searchStatus is the footer line shown while walking matches.
func (m model) searchStatus() string {
	return fmt.Sprintf("search %q · %d/%d · n older · N newer · esc clears", m.searchQuery, m.searchAt+1, len(m.searchHits))
}

// searchSeg labels the footer mode cap while a search is active or being typed.
func (m model) searchSeg() string {
	return seg("⌕ SEARCH", colDark, colCyan)
}

// searchPromptFooter is the footer shown while the find prompt is open: the brand
// and search caps with the term being typed, so the screen behind stays visible
// rather than being hidden under a centred popup.
func (m model) searchPromptFooter() string {
	left := seg("◈ BATON", colDark, colBrand) + m.searchSeg() + barBold.Render(" "+m.inputBuf+"▌ ")
	return m.statusBar(left, "")
}

// combinedPlain returns the panel's full text — every scrollback line followed by
// the live screen rows — as plain strings (no styling), plus the scrollback
// length so a hit index can be mapped back to the scroll offset. This is the
// space search matches against and highlights within.
func combinedPlain(emu *vt.SafeEmulator) ([]string, int) {
	if emu == nil {
		return nil, 0
	}
	sbLen := emu.ScrollbackLen()
	lines := make([]string, 0, sbLen+24)
	if sbLen > 0 {
		sb := emu.Scrollback()
		for i := 0; i < sbLen; i++ {
			if ln := sb.Line(i); ln != nil {
				lines = append(lines, ln.String())
			} else {
				lines = append(lines, "")
			}
		}
	}
	for _, sl := range strings.Split(emu.Render(), "\n") {
		lines = append(lines, stripANSI(sl))
	}
	return lines, sbLen
}

// searchWindow renders a scroll window like emuWindow, but with the search term
// highlighted on any matched line: reverse video on the current hit, underline on
// the rest. When no search is active it is exactly emuWindow.
func (m model) searchWindow(emu *vt.SafeEmulator, cols, rows, off int) []string {
	lines := emuWindow(emu, cols, rows, off)
	if !m.searchActive() || emu == nil {
		return lines
	}
	start := windowStart(emu.ScrollbackLen(), off) // top of the window in the combined index space
	plain, _ := combinedPlain(emu)

	hit := make(map[int]bool, len(m.searchHits))
	for _, h := range m.searchHits {
		hit[h] = true
	}
	cur := -1
	if m.searchAt >= 0 && m.searchAt < len(m.searchHits) {
		cur = m.searchHits[m.searchAt]
	}
	for i := range lines {
		idx := start + i
		if idx < 0 || idx >= len(plain) || !hit[idx] {
			continue
		}
		lines[i] = highlightLine(plain[idx], m.searchRe, cols, idx == cur)
	}
	return lines
}

// highlightLine clips a plain line to cols cells and wraps each match of re in
// reverse video (the current hit) or underline (the rest). Highlighting works on
// the plain text — no program styling to interleave with. Empty (zero-width)
// matches are skipped so a pattern like "a*" can't emit stray styling.
func highlightLine(s string, re *regexp.Regexp, cols int, current bool) string {
	s = clipCells(s, cols)
	if re == nil {
		return s
	}
	on, off := "\x1b[4m", "\x1b[24m" // underline for non-current matches
	if current {
		on, off = "\x1b[7m", "\x1b[27m" // reverse video for the current match
	}
	var b strings.Builder
	i := 0
	for _, loc := range re.FindAllStringIndex(s, -1) {
		if loc[0] == loc[1] { // zero-width match — nothing to wrap
			continue
		}
		b.WriteString(s[i:loc[0]])
		b.WriteString(on)
		b.WriteString(s[loc[0]:loc[1]])
		b.WriteString(off)
		i = loc[1]
	}
	b.WriteString(s[i:])
	return b.String()
}

// stripANSI drops escape sequences from s, leaving the visible text — used to turn
// a rendered screen line into the plain text search matches against.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i += escLen(s[i:])
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// clipCells returns the prefix of s holding at most width display cells, counting
// a wide (CJK) glyph as two. Unlike clipVisible it assumes plain text, so it adds
// no reset sequence.
func clipCells(s string, width int) string {
	if width < 1 {
		return ""
	}
	vis := 0
	var b strings.Builder
	for _, r := range s {
		w := cellWidth(r)
		if vis+w > width {
			break
		}
		b.WriteRune(r)
		vis += w
	}
	return b.String()
}
