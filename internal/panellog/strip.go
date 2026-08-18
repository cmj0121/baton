package panellog

// The escape stripper.
//
// A log is meant to be grepped, pasted into an issue, and re-read by another
// agent, and none of those want a screenful of \x1b[2K. So what lands on disk is
// the text a human would have read, with the sequences that positioned and
// coloured it removed.
//
// It is a state machine rather than a regexp because the input arrives in
// arbitrary PTY chunks: a sequence is routinely split across two reads, and a
// regexp applied per chunk would emit the front half as literal garbage. The
// machine holds a partial sequence over the boundary and resumes on the next
// chunk — which is also why the caller must keep one Stripper per panel for the
// life of the file, not build one per write.

// pendingCap bounds how much of an unterminated sequence is held over a chunk
// boundary. A program that emits a bare ESC ] and never terminates it would
// otherwise swallow its own output forever; past the cap the machine gives up on
// the sequence, emits what it held as literal text, and resumes reading text. The
// cap is far above any real sequence (an OSC 52 clipboard write is the longest
// baton sees, and it is bounded by the selection).
const pendingCap = 4096

// state is where the machine is inside a sequence.
type state int

const (
	stText    state = iota // ordinary text
	stEsc                  // saw ESC, waiting for what kind of sequence it opens
	stCSI                  // inside ESC [ …, ends at a byte in @-~
	stString               // inside OSC / DCS / PM / APC, ends at BEL or ST
	stStringE              // inside a string sequence, saw ESC, expecting the \ of ST
	stCharset              // inside ESC ( ) * + …, consumes exactly one more byte
)

// Stripper turns a stream of raw PTY bytes into the plain text underneath, one
// chunk at a time. The zero value is ready to use. It is NOT safe for concurrent
// use — the Sink that owns one serialises writes around it.
type Stripper struct {
	st      state
	pending []byte // the incomplete sequence held over a chunk boundary
	lastCR  bool   // the previous emitted byte was a carriage return, so a following LF collapses into it
}

// Strip returns the plain text in chunk. Bytes belonging to an escape sequence
// are dropped; a sequence left incomplete at the end of chunk is held and
// resumed on the next call.
//
// Carriage returns become newlines rather than vanishing: a program that redraws
// a line in place (a spinner, a progress bar) then leaves its intermediate states
// in the file as repeated LINES, which greps and reads sanely, instead of as one
// unbounded line nothing can read. CRLF collapses to a single newline. The other
// C0 controls are dropped — they position a cursor, and there is no cursor here.
func (s *Stripper) Strip(chunk []byte) []byte {
	out := make([]byte, 0, len(chunk))
	for _, b := range chunk {
		switch s.st {
		case stText:
			out = s.text(out, b)
		case stEsc:
			s.esc(b)
		case stCSI:
			// A CSI ends at its final byte; the parameter and intermediate bytes
			// before it are 0x20-0x3f.
			s.hold(b)
			if b >= '@' && b <= '~' {
				s.reset()
			}
		case stString:
			s.hold(b)
			switch b {
			case 0x07: // BEL terminates an OSC
				s.reset()
			case 0x1b: // maybe the ESC of an ST
				s.st = stStringE
			}
		case stStringE:
			s.hold(b)
			if b == '\\' { // ST
				s.reset()
			} else {
				s.st = stString // a stray ESC inside the string; keep consuming
			}
		case stCharset:
			s.hold(b)
			s.reset()
		}
		// A sequence that never ends must not eat the stream. Give up on it, emit
		// what was held as literal text, and read the rest as text.
		if s.st != stText && len(s.pending) >= pendingCap {
			out = append(out, s.pending...)
			s.reset()
		}
	}
	return out
}

// Flush returns any bytes still held as an incomplete sequence, as literal text,
// and resets the machine. It is what closing a file calls, so a partial sequence
// at the end of a stream is not silently dropped.
func (s *Stripper) Flush() []byte {
	if len(s.pending) == 0 {
		s.reset()
		return nil
	}
	out := append([]byte(nil), s.pending...)
	s.reset()
	return out
}

// text handles one byte read in the ordinary text state.
func (s *Stripper) text(out []byte, b byte) []byte {
	switch {
	case b == 0x1b:
		s.st = stEsc
		s.lastCR = false
		s.pending = append(s.pending, b)
		return out
	case b == '\r':
		// A lone CR ends a line, so a redraw leaves its drafts as separate lines.
		s.lastCR = true
		return append(out, '\n')
	case b == '\n':
		// Only an LF IMMEDIATELY after a CR collapses into it: that pair is one line
		// break the terminal wrote, while a CR with anything between it and the LF is
		// two things that happened.
		if s.lastCR {
			s.lastCR = false
			return out
		}
		return append(out, b)
	case b == '\t':
		s.lastCR = false
		return append(out, b)
	case b < 0x20 || b == 0x7f:
		s.lastCR = false
		return out // the other C0 controls position a cursor; there is none here
	default:
		s.lastCR = false
		return append(out, b)
	}
}

// esc dispatches on the byte after an ESC.
func (s *Stripper) esc(b byte) {
	s.hold(b)
	switch b {
	case '[':
		s.st = stCSI
	case ']', 'P', '^', '_': // OSC, DCS, PM, APC — all terminated by BEL or ST
		s.st = stString
	case '(', ')', '*', '+', '-', '.', '/', '%', '#': // charset / designation, one more byte
		s.st = stCharset
	default:
		s.reset() // a two-byte escape (ESC c, ESC =, ESC 7 …) is complete
	}
}

// hold appends a byte to the sequence being consumed, so it can be emitted as
// literal text if the sequence turns out never to end.
func (s *Stripper) hold(b byte) { s.pending = append(s.pending, b) }

// reset returns the machine to reading text and drops whatever it held.
func (s *Stripper) reset() {
	s.st = stText
	s.pending = s.pending[:0]
}
