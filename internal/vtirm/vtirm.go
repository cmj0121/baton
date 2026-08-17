// Package vtirm teaches baton's client-side emulator insert mode.
//
// A shell editing a line in place has two ways to open a gap for the character you just
// typed. It can ask for the gap outright — ICH, CSI n @ — or it can flip the terminal
// into IRM (Insert/Replace Mode, CSI 4 h, terminfo smir/rmir) and let every printed cell
// push the rest of the line right until it flips back with CSI 4 l.
//
// github.com/charmbracelet/x/vt, the emulator baton renders panels with, implements the
// first and not the second: mode 4 is absent from its recognised-mode table and a printed
// cell always overwrites the one under the cursor. ptymgr gives every panel
// TERM=xterm-256color, whose terminfo advertises mir/smir/rmir, so readline takes the IRM
// path to insert a single character — and the character lands on top of its neighbour
// instead of beside it. Typing "a" at the front of "ls a/b/c/d" shows "as a/b/c/d" rather
// than "als a/b/c/d" (github.com/cmj0121/baton#10).
//
// Rather than fork the emulator, a Filter rewrites the stream on its way in. It tracks
// mode 4 and, while it is set, prefixes each run of printable cells with the ICH the
// emulator already gets right. The two are equivalent: ICH n opens n blank cells at the
// cursor and shifts the rest of the line right, which is precisely what printing n cells
// under IRM does — so the reconstructed screen is the one the shell intended.
//
// Mode 4 itself is taken out of the stream, so the emulator never sees it. That keeps the
// rewrite honest in one direction that matters: should a future x/vt learn IRM natively,
// it cannot double up on the ICH this package already emits.
package vtirm

import (
	"bytes"
	"strconv"

	"github.com/charmbracelet/x/ansi"
)

const (
	// irmParam is the ANSI mode number for Insert/Replace Mode.
	irmParam = int(ansi.ModeInsertReplace)

	// heldMax caps how many bytes of a half-read CSI sequence a Filter will hold back
	// waiting for the rest of it. Every mode set worth recognising is a handful of
	// bytes; anything longer is some other sequence entirely and is better let through
	// than held, so a program that writes a long CSI and then pauses cannot stall the
	// panel it is drawing to.
	heldMax = 64
)

// Filter carries the insert-mode rewrite state for one emulator-bound stream: whether IRM
// is currently set, how far into an escape sequence the last chunk ended, and the bytes of
// a CSI sequence that a chunk boundary cut in half. All three have to survive that
// boundary, because the bytes arrive in whatever sizes the PTY read and the socket
// happened to produce — and missing a CSI 4 l because it was split would leave the filter
// inserting text that should have overwritten, which is worse than the bug it fixes.
//
// The zero value is ready to use. A nil *Filter passes every chunk through untouched, so
// a caller that feeds an emulator it does not own — a test, a one-shot render — needs no
// filter at all.
type Filter struct {
	insert bool   // IRM is set: printed cells push the rest of the line right
	state  byte   // ansi decoder state, carried because a chunk may split a sequence
	held   []byte // a CSI sequence split across chunks, held until it completes
}

// Rewrite returns b with insert-mode printing expressed as ICH, and mode 4 removed.
//
// The fast path returns the caller's slice unchanged — no parse, no allocation — when
// insert mode is off, no sequence is half-read, and the chunk holds no ESC: nothing there
// can turn insert mode on, and nothing needs an ICH. That is the overwhelmingly common
// chunk, so the filter costs the stream nothing until a program actually asks for IRM.
//
// Only the 7-bit CSI form is recognised. Terminfo's smir/rmir are ESC [ 4 h / ESC [ 4 l,
// and a UTF-8 stream — which is what a panel carries — has no room for the 8-bit C1
// introducer, whose byte is a continuation byte there.
//
// A grapheme split across two chunks is measured from the bytes of the first chunk alone,
// so a multi-byte character straddling a boundary *while insert mode is set* could get an
// ICH a cell short. Insert-mode bursts are emitted whole by the line editors that use
// them, so this stays theoretical; it is called out because the alternative — withholding
// the tail until the next chunk — trades a rare miscount for a rare missing character.
func (f *Filter) Rewrite(b []byte) []byte {
	if f == nil || len(b) == 0 {
		return b
	}
	if !f.insert && f.state == ansi.NormalState && len(f.held) == 0 && bytes.IndexByte(b, ansi.ESC) < 0 {
		return b
	}

	out := make([]byte, 0, len(b)+8) // room for the one ICH a typing burst adds
	var run []byte                   // printable bytes awaiting the ICH that opens their gap
	cells := 0                       // how many cells run occupies

	flush := func() {
		if len(run) == 0 {
			return
		}
		out = append(out, ansi.ESC, '[')
		out = strconv.AppendInt(out, int64(cells), 10)
		out = append(out, '@')
		out = append(out, run...)
		run, cells = run[:0], 0
	}

	for rest := b; len(rest) > 0; {
		_, width, n, state := ansi.DecodeSequence(rest, f.state, nil)
		f.state = state
		if n <= 0 {
			// The decoder always advances; belt and braces so a change upstream can
			// never spin this loop on a chunk it cannot make sense of.
			flush()
			out = append(out, f.release()...)
			return append(out, rest...)
		}
		seq := rest[:n]
		rest = rest[n:]

		switch {
		case len(f.held) > 0:
			f.held = append(f.held, seq...)
			if state != ansi.NormalState { // still incomplete
				if f.holdable() {
					continue
				}
				flush()
				out = append(out, f.release()...)
				continue
			}
			seq, width = f.release(), 0 // reassembled, and a sequence prints nothing
		case state != ansi.NormalState:
			f.held = append(f.held, seq...)
			if f.holdable() {
				continue
			}
			flush()
			out = append(out, f.release()...)
			continue
		}

		if f.insert && width > 0 {
			run = append(run, seq...)
			cells += width
			continue
		}

		flush()
		if residual, set, ok := splitIRM(seq); ok {
			f.insert = set
			out = append(out, residual...)
			continue
		}
		out = append(out, seq...)
	}
	flush()
	return out
}

// holdable reports whether the half-read bytes are still worth waiting for: they can only
// become a CSI sequence, and they have not outgrown the cap.
func (f *Filter) holdable() bool {
	if len(f.held) == 0 || len(f.held) > heldMax || f.held[0] != ansi.ESC {
		return false
	}
	return len(f.held) == 1 || f.held[1] == '['
}

// release hands back the held bytes and clears the hold. The returned slice is only valid
// until the next byte is held, which is all any caller here needs.
func (f *Filter) release() []byte {
	held := f.held
	f.held = f.held[:0]
	return held
}

// splitIRM decides what a CSI mode set/reset sequence means for insert mode, and what
// should reach the emulator in its place. When seq carries mode 4 it returns the same
// sequence with that parameter taken out — nothing at all in the usual case where mode 4
// was the only parameter — and whether the mode was set (CSI … h) or reset (CSI … l).
//
// Only a bare numeric parameter list counts. A private marker makes it a different mode
// entirely (CSI ? 4 h is DECSCLM, smooth scroll), as does an intermediate byte or a
// sub-parameter, and those are passed through untouched.
func splitIRM(seq []byte) (residual []byte, set, ok bool) {
	if len(seq) < 3 || seq[0] != ansi.ESC || seq[1] != '[' {
		return nil, false, false
	}
	final := seq[len(seq)-1]
	if final != 'h' && final != 'l' {
		return nil, false, false
	}
	params := seq[2 : len(seq)-1]
	for _, c := range params {
		if (c < '0' || c > '9') && c != ';' {
			return nil, false, false
		}
	}

	kept := make([][]byte, 0, 4)
	found := false
	for _, p := range bytes.Split(params, []byte{';'}) {
		if !found && param(p) == irmParam {
			found = true
			continue
		}
		kept = append(kept, p)
	}
	if !found {
		return nil, false, false
	}
	if len(kept) == 0 {
		return nil, final == 'h', true // mode 4 was the whole sequence: drop it
	}

	residual = append(residual, ansi.ESC, '[')
	residual = append(residual, bytes.Join(kept, []byte{';'})...)
	return append(residual, final), final == 'h', true
}

// param reads one CSI parameter. An omitted parameter defaults to 0, and a run of digits
// long enough to overflow is not a mode number anyone means, so both answer -1 — never
// the mode this package is looking for.
func param(b []byte) int {
	if len(b) == 0 || len(b) > 4 {
		return -1
	}
	n := 0
	for _, c := range b {
		n = n*10 + int(c-'0')
	}
	return n
}
