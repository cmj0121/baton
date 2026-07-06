package tui

import (
	"math/rand"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// The hidden screensaver Easter egg (modeScreensaver): a full-screen Matrix
// digital rain with the BATON wordmark and a big clock overlaid in the centre.
// It is summoned by C-t E — a direct key compare deliberately kept out of the
// bindings list, so it never shows in the key map / help, can't be rebound, and
// can't collide (the same off-book pattern the git menu's C-t g uses). It also
// auto-starts after saverIdle of no key / mouse-click input — a true "screen
// protector" for the idle cockpit — and any key or click dismisses it, restoring
// the view it took over. Frontend-only: nothing is sent to the server.

const (
	keyScreensaver = "E"             // hidden summon (C-t E)
	saverFPS       = 12              // rain animation frames per second
	saverIdle      = 3 * time.Minute // idle before the saver auto-starts
)

// saverInterval is the wall time between rain frames, derived from saverFPS.
var saverInterval = time.Second / saverFPS

// saverTickMsg drives the rain animation; it is distinct from the 1 s tickMsg
// (which keeps m.now, and so the overlaid clock, fresh) so the two cadences never
// interfere.
type saverTickMsg time.Time

// saverTick fires the next rain frame. It is only re-armed while the saver is the
// active mode, so the fast cadence costs nothing once the saver is dismissed.
func saverTick() tea.Cmd {
	return tea.Tick(saverInterval, func(t time.Time) tea.Msg { return saverTickMsg(t) })
}

// rainGlyphs is the character set the rain falls in — half-width katakana plus a
// few digits and symbols, the canonical Matrix look, all single-cell so the
// column maths stays a plain grid.
var rainGlyphs = []rune("ｱｲｳｴｵｶｷｸｹｺｻｼｽｾｿﾀﾁﾂﾃﾄﾅﾆﾇﾈﾉﾊﾋﾌﾍﾎﾏﾐﾑﾒﾓ0123456789:.=*+<>")

// rainShades are the 256-colour foreground codes from the drop's head (a bright
// white lead) fading down its trail into progressively dimmer greens.
var rainShades = []string{"231", "48", "42", "36", "29", "22"}

// drop is one column's falling trail: head is its lead row (may be < 0 while still
// above the top), trail its length, speed the frames between advances, and delay
// the frames left until the next advance.
type drop struct {
	head, trail, speed, delay int
}

// rain is the digital-rain state: one falling drop per screen column over a grid
// of the glyph each cell currently shows. The RNG is injected so a test can seed a
// deterministic stream.
type rain struct {
	w, h  int
	rng   *rand.Rand
	drops []drop   // one falling drop per column
	glyph [][]rune // glyph[row][col]: the character written when the head last passed that cell
}

// newRain builds a rain sized to w×h, seeded from rng (a nil rng gets a fixed
// fallback so a zero-value caller still renders rather than panicking).
func newRain(w, h int, rng *rand.Rand) *rain {
	if rng == nil {
		rng = rand.New(rand.NewSource(1))
	}
	r := &rain{rng: rng}
	r.resize(w, h)
	return r
}

// resize (re)allocates the grid to w×h and reseeds every column. Called on the
// first build and whenever the terminal changes size, so a resize can never index
// a stale column array.
func (r *rain) resize(w, h int) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	r.w, r.h = w, h
	r.drops = make([]drop, w)
	r.glyph = make([][]rune, h)
	for y := range r.glyph {
		r.glyph[y] = make([]rune, w)
	}
	for x := 0; x < w; x++ {
		r.spawn(x)
	}
}

// spawn reseeds one column with a fresh drop starting above the top of the screen
// (a negative head), so it falls into view rather than popping in mid-screen.
func (r *rain) spawn(x int) {
	speed := 1 + r.rng.Intn(3)
	r.drops[x] = drop{
		head:  -r.rng.Intn(r.h + 1),
		trail: 4 + r.rng.Intn(r.h/2+2),
		speed: speed,
		delay: speed,
	}
}

// step advances the rain one frame: each column falls at its own cadence, writing
// a fresh glyph at the new head cell, occasionally flickering a trailing cell, and
// respawning once its whole trail has fallen past the bottom.
func (r *rain) step() {
	for x := 0; x < r.w; x++ {
		d := &r.drops[x]
		if d.delay > 0 {
			d.delay--
			continue
		}
		d.delay = d.speed
		d.head++
		if y := d.head; y >= 0 && y < r.h {
			r.glyph[y][x] = rainGlyphs[r.rng.Intn(len(rainGlyphs))]
		}
		if r.rng.Intn(4) == 0 { // flicker a random trailing cell for the shimmer
			if ty := d.head - r.rng.Intn(d.trail+1); ty >= 0 && ty < r.h {
				r.glyph[ty][x] = rainGlyphs[r.rng.Intn(len(rainGlyphs))]
			}
		}
		if d.head-d.trail > r.h {
			r.spawn(x)
		}
	}
}

// render paints the rain into an h×w grid of styled single-cell strings (a space
// where no drop covers the cell), ready to be composited under the overlay.
func (r *rain) render() [][]string {
	grid := make([][]string, r.h)
	for y := 0; y < r.h; y++ {
		grid[y] = make([]string, r.w)
		for x := 0; x < r.w; x++ {
			grid[y][x] = " "
		}
	}
	for x := 0; x < r.w; x++ {
		d := r.drops[x]
		for i := 0; i <= d.trail; i++ {
			y := d.head - i
			if y < 0 || y >= r.h {
				continue
			}
			if g := r.glyph[y][x]; g != 0 {
				grid[y][x] = styleCell(g, rainShade(i, d.trail))
			}
		}
	}
	return grid
}

// rainShade maps a cell's distance d from its drop's head (0 = the bright head,
// trail = the dimmest tail) onto a foreground colour code.
func rainShade(d, trail int) string {
	if trail < 1 {
		trail = 1
	}
	idx := d * (len(rainShades) - 1) / trail
	if idx >= len(rainShades) {
		idx = len(rainShades) - 1
	}
	return rainShades[idx]
}

// styleCell wraps a single rune in a 256-colour SGR foreground and a reset.
func styleCell(r rune, code string) string {
	return "\x1b[38;5;" + code + "m" + string(r) + "\x1b[0m"
}

// clockFontBold is the primary 6-row clock font — bold, even-width block digits in
// the same shadowed-block idiom as the BATON banner, so the clock reads as the
// wordmark's kin rather than a thin afterthought.
var clockFontBold = map[rune][]string{
	'0': {"██████", "██  ██", "██  ██", "██  ██", "██  ██", "██████"},
	'1': {"  ██  ", "████  ", "  ██  ", "  ██  ", "  ██  ", "██████"},
	'2': {"██████", "    ██", "██████", "██    ", "██    ", "██████"},
	'3': {"██████", "    ██", " █████", "    ██", "    ██", "██████"},
	'4': {"██  ██", "██  ██", "██████", "    ██", "    ██", "    ██"},
	'5': {"██████", "██    ", "██████", "    ██", "    ██", "██████"},
	'6': {"██████", "██    ", "██████", "██  ██", "██  ██", "██████"},
	'7': {"██████", "    ██", "   ██ ", "  ██  ", "  ██  ", "  ██  "},
	'8': {"██████", "██  ██", "██████", "██  ██", "██  ██", "██████"},
	'9': {"██████", "██  ██", "██████", "    ██", "    ██", "██████"},
	':': {"    ", " ██ ", "    ", "    ", " ██ ", "    "},
}

// clockFontSmall is the compact 5-row fallback, used when the bold clock would
// overflow a narrow terminal.
var clockFontSmall = map[rune][]string{
	'0': {"███", "█ █", "█ █", "█ █", "███"},
	'1': {"  █", "  █", "  █", "  █", "  █"},
	'2': {"███", "  █", "███", "█  ", "███"},
	'3': {"███", "  █", "███", "  █", "███"},
	'4': {"█ █", "█ █", "███", "  █", "  █"},
	'5': {"███", "█  ", "███", "  █", "███"},
	'6': {"███", "█  ", "███", "█ █", "███"},
	'7': {"███", "  █", "  █", "  █", "  █"},
	'8': {"███", "█ █", "███", "█ █", "███"},
	'9': {"███", "█ █", "███", "  █", "███"},
	':': {"   ", " █ ", "   ", " █ ", "   "},
}

// renderClock lays t out as HH:MM:SS in font, one string per glyph row, each glyph
// separated by a blank column. Unknown runes fall back to the colon cell.
func renderClock(t time.Time, font map[rune][]string) []string {
	rows := make([]string, len(font['0']))
	for i := range rows {
		var b strings.Builder
		for j, ch := range t.Format("15:04:05") {
			if j > 0 {
				b.WriteByte(' ')
			}
			g, ok := font[ch]
			if !ok {
				g = font[':']
			}
			b.WriteString(g[i])
		}
		rows[i] = b.String()
	}
	return rows
}

// bigClock renders the clock in the bold font, falling back to the compact font
// when the bold form would be wider than maxW cells.
func bigClock(t time.Time, maxW int) []string {
	rows := renderClock(t, clockFontBold)
	if utf8.RuneCountInString(rows[0]) <= maxW {
		return rows
	}
	return renderClock(t, clockFontSmall)
}

// saverBlock is the centred overlay: the BATON wordmark above the big clock, each
// with its own colour. text carries the plain runes (spaces let the rain show
// through between glyphs); code is the SGR colour for that line's non-space cells.
type saverBlock struct {
	text string
	code string
}

// saverWordmark is the BATON banner as overlay lines (bright white), built once —
// it never changes, so only the clock is rebuilt per frame.
var saverWordmark = func() []saverBlock {
	var b []saverBlock
	for _, ln := range strings.Split(banner, "\n") {
		b = append(b, saverBlock{ln, "231"})
	}
	return b
}()

// saverWordmarkW is the wordmark's cell width, computed once.
var saverWordmarkW = func() int {
	w := 0
	for _, b := range saverWordmark {
		if l := utf8.RuneCountInString(b.text); l > w {
			w = l
		}
	}
	return w
}()

// enterScreensaver takes over the screen with the rain, remembering the mode to
// restore on dismiss. The rain is seeded from m.now so production gets variety
// while a test with a fixed clock stays deterministic.
func (m model) enterScreensaver() model {
	m.saverReturn = m.mode
	m.mode = modeScreensaver
	m.prefix = false // never carry a half-armed leader into the takeover
	m.saver = newRain(m.width, m.height, rand.New(rand.NewSource(m.now.UnixNano())))
	return m
}

// exitScreensaver dismisses the saver, restoring the view it covered and resetting
// the idle clock so it does not immediately re-arm on the same frame.
func (m model) exitScreensaver() model {
	m.mode = m.saverReturn
	m.saver = nil
	m.lastInput = m.now
	return m
}

// canAutoSaver reports whether the idle auto-start may fire from the current view.
// It refuses whenever keystrokes are (or should be) flowing to a program or an
// overlay — a zoom, the group split, the scratch pane, a text input, scroll mode,
// a half-typed leader or rebind, a pending confirm — and while the backend is
// down, so the saver never buries a live view, a prompt, or an outage alert.
func (m model) canAutoSaver() bool {
	switch m.mode {
	case modeScreensaver, modeZoom, modeGroupZoom:
		return false
	}
	if m.scratchOpen || m.input != inputNone || m.scrolling || m.editing || m.prefix {
		return false
	}
	if m.backendDown || m.pendingClose || m.pendingRestart {
		return false
	}
	return true
}

// maybeAutoSaver enters the saver when the cockpit has sat idle past saverIdle in a
// view that permits it, returning the animation tick to start the rain (nil when
// it does not fire). Called from the 1 s tick, where m.now has just advanced.
func (m *model) maybeAutoSaver() tea.Cmd {
	if m.lastInput.IsZero() {
		m.lastInput = m.now // seed on first observation; never read "unset" as idle-since-epoch
		return nil
	}
	if !m.canAutoSaver() || m.now.Sub(m.lastInput) < saverIdle {
		return nil
	}
	*m = m.enterScreensaver()
	return saverTick()
}

// saverPadX is the horizontal breathing room the cleared panel adds on each side
// of the overlay content.
const saverPadX = 3

// screensaverView renders the full-screen rain with the clock as the centrepiece —
// seated on the screen's true centre, with the BATON wordmark floating a couple of
// rows above it. It owns every cell of the terminal (no footer), so it is a clean
// takeover the next real render fully replaces.
func (m model) screensaverView() string {
	w, h := m.width, m.height
	if w < 1 || h < 1 || m.saver == nil {
		return strings.Repeat("\n", max(0, h-1))
	}
	grid := m.saver.render()

	clock := bigClock(m.now, w-2*saverPadX)
	clockTop := (h - len(clock)) / 2 // the clock straddles the vertical centre

	// The wordmark floats a gap above the clock — but only when there is room to seat
	// it whole; on a short terminal it is dropped so the clock never fights a
	// half-clipped banner for the centre.
	const gap = 2
	wmTop := clockTop - gap - len(saverWordmark)
	showWordmark := wmTop >= 1

	// Clear a centred, padded panel behind the overlay so it reads crisply while the
	// rain keeps falling all around it. The band is as wide as the widest element.
	bandW := utf8.RuneCountInString(clock[0])
	panelTop := clockTop - 1
	if showWordmark {
		panelTop = wmTop - 1
		if saverWordmarkW > bandW {
			bandW = saverWordmarkW
		}
	}
	clearBand(grid, panelTop, clockTop+len(clock)+1, (w-bandW)/2-saverPadX, bandW+2*saverPadX)

	if showWordmark {
		for i, ln := range saverWordmark {
			stampCentred(grid, wmTop+i, ln.text, ln.code, w)
		}
	}
	for i, ln := range clock {
		stampCentred(grid, clockTop+i, ln, "87", w) // clock in light cyan
	}

	rows := make([]string, h)
	for y := 0; y < h; y++ {
		rows[y] = strings.Join(grid[y], "")
	}
	return strings.Join(rows, "\n")
}

// clearBand blanks the rain across rows [top, bottom) and columns [left, left+width),
// clipped to the grid, so an overlay drawn there reads against clean cells.
func clearBand(grid [][]string, top, bottom, left, width int) {
	for y := top; y < bottom; y++ {
		if y < 0 || y >= len(grid) {
			continue
		}
		for x := left; x < left+width; x++ {
			if x >= 0 && x < len(grid[y]) {
				grid[y][x] = " "
			}
		}
	}
}

// stampCentred draws text horizontally centred on row y of the grid, stamping only
// its non-space glyphs (so the rain shows through the gaps) in the given colour.
func stampCentred(grid [][]string, y int, text, code string, screenW int) {
	if y < 0 || y >= len(grid) {
		return
	}
	x := (screenW - utf8.RuneCountInString(text)) / 2
	for _, r := range text {
		if r != ' ' && x >= 0 && x < screenW {
			grid[y][x] = styleCell(r, code)
		}
		x++
	}
}
