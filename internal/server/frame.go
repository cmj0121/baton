package server

import (
	"errors"
	"io"
)

// maxCommandBytes caps how many bytes ONE proto.Command frame may occupy on the
// control socket.
//
// The cap exists because encoding/json's streaming decoder buffers a whole value
// before it returns one: an unbounded json.NewDecoder(conn) turns a single
// enormous command line into a single enormous allocation inside the daemon that
// supervises the entire fleet. Driven against a live daemon, 256 MiB in one frame
// peaked its heap at 449 MiB in 0.43 s — the raw buffer plus the string the
// decoder unquotes out of it — and nothing refused it. A handful of connections
// doing that concurrently is the whole fleet gone, which is the harm this round
// is about: one panel's agent taking every other panel down with it.
//
// One MiB, and the argument is the size of the largest frame a legitimate client
// has to send:
//
//   - The cockpit's biggest frame is a panel.input chunk, and zoomReader's buffer
//     fixes that at 4 KiB. This cap is 256 times it.
//   - `baton ctl` and the MCP tools carry text that arrived as an argv string —
//     a prompt to dispatch, a line to send — so the operating system has already
//     bounded it well below a megabyte (128 KiB per argument on Linux, a 1 MiB
//     total on darwin).
//   - A score.submit note is capped at 300 runes by internal/score long before it
//     could matter here.
//
// So a megabyte is out of reach of every honest sender while still being memory a
// daemon can hold per connection. It is deliberately a BYTE cap and not a rune
// one: this is a bound on allocation, and the allocation is bytes.
const maxCommandBytes = 1 << 20

// errFrameTooLarge is what a reader hits when one frame runs past the cap. It
// ends the connection rather than skipping the frame: the decoder has no way to
// resynchronise mid-value, so continuing would feed it the tail of a command as
// if it were the head of the next one.
var errFrameTooLarge = errors.New("command frame exceeds the size limit")

// frameLimiter bounds the bytes one json.Decoder.Decode may consume. The budget
// is restored by reset, which the command loop calls after each decoded value, so
// the cap is per FRAME rather than per connection — a client may stay attached
// for hours and send thousands of commands, and only a single oversized one is
// refused.
//
// Bytes the decoder read ahead into its own buffer are charged to the frame that
// was being decoded when the read happened, so a client that pipelines several
// commands into one write spends its budget slightly early. That direction is
// harmless: the budget is restored the moment a value comes out, and the reads
// that follow are served from the decoder's buffer without touching this reader
// at all. Only a single value that cannot be completed within the budget fails.
type frameLimiter struct {
	r     io.Reader
	left  int64
	limit int64
}

func newFrameLimiter(r io.Reader, limit int64) *frameLimiter {
	return &frameLimiter{r: r, left: limit, limit: limit}
}

// Read is io.Reader, refusing to hand the decoder more than the frame's remaining
// budget.
func (f *frameLimiter) Read(p []byte) (int, error) {
	if f.left <= 0 {
		return 0, errFrameTooLarge
	}
	if int64(len(p)) > f.left {
		p = p[:f.left]
	}
	n, err := f.r.Read(p)
	f.left -= int64(n)
	return n, err
}

// reset restores the budget for the next frame.
func (f *frameLimiter) reset() { f.left = f.limit }
