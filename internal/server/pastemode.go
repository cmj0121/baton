package server

import (
	"bytes"

	"github.com/cmj0121/baton/internal/vtquery"
)

// pasteCarry is how many trailing bytes of a panel's output are kept to be
// re-read against the start of the next chunk, so a bracketed-paste toggle the
// PTY handed over in two reads is still seen. It only has to span one DEC
// private-mode sequence; a combined list long enough to outgrow it would be
// toggling more modes at once than any program sends.
const pasteCarry = 64

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

// notePasteModeLocked reads a panel's bracketed-paste toggle out of a chunk of
// its output and keeps the state it leaves behind. It is deliberately not part
// of the working-directory tracker: that one returns at once unless track-cwd
// reads reports, and whether a typed line must arrive as a paste has nothing to
// do with whether anybody wanted directories tracked. Caller holds s.mu.
//
// It keeps its own carry for the same reason. The chunk is scanned in two parts:
// the seam — the previous chunk's tail joined to this chunk's head, where a
// sequence split across the reads sits whole — and then the chunk itself. A
// toggle wholly inside the chunk comes after anything at the seam, so the
// chunk's answer, when it has one, is the last word; the seam's is used only
// when the chunk alone says nothing. A toggle the seam finds wholly inside the
// old tail was already applied by the previous call and only repeats the state
// it set, so re-reading it is harmless.
//
// This runs on every chunk of every panel, so nothing in it allocates once a
// panel's tail exists: the seam is built only when the chunk alone says nothing,
// into one scratch buffer the server reuses under s.mu, and the tail is rewritten
// in the slice it already has.
//
// A chunk for a panel no longer in the fleet is ignored. The PTY can hand over a
// last read after the panel was closed or pruned, and recording it would bring
// back the entries the close just forgot, for a panel nothing will ever forget
// again.
func (s *Server) notePasteModeLocked(id string, data []byte) {
	if s.indexLocked(id) < 0 {
		return
	}
	tail := s.pasteTail[id]

	on, seen := vtquery.BracketedPaste(data)
	if !seen && len(tail) > 0 {
		head := data[:min(len(data), pasteCarry)]
		s.pasteSeam = append(append(s.pasteSeam[:0], tail...), head...)
		on, seen = vtquery.BracketedPaste(s.pasteSeam)
	}
	if seen {
		if s.pasteMode == nil {
			s.pasteMode = make(map[string]bool)
		}
		s.pasteMode[id] = on
	}

	// The next seam needs the last pasteCarry bytes of the stream. A chunk at
	// least that long supplies them alone; a shorter one keeps the end of the old
	// tail in front of it. copy moves overlapping bytes correctly, so the old tail
	// can be shifted down inside its own slice.
	if cap(tail) < pasteCarry {
		grown := make([]byte, len(tail), pasteCarry)
		copy(grown, tail)
		tail = grown
	}
	if len(data) >= pasteCarry {
		tail = append(tail[:0], data[len(data)-pasteCarry:]...)
	} else {
		keep := tail[max(0, len(tail)-(pasteCarry-len(data))):]
		n := copy(tail[:len(keep)], keep)
		tail = append(tail[:n], data...)
	}
	if s.pasteTail == nil {
		s.pasteTail = make(map[string][]byte)
	}
	s.pasteTail[id] = tail
}

// forgetPasteModeLocked drops what is known of a panel's paste mode, so it reads
// as unknown — today's raw typing — until the next process says otherwise. A
// mode is the running program's request, not the panel's, and a fresh process
// that never asks must not inherit the old one's. Caller holds s.mu.
func (s *Server) forgetPasteModeLocked(id string) {
	delete(s.pasteMode, id)
	delete(s.pasteTail, id)
}

// pasteModeAndPid reports whether panel id's program last asked for bracketed
// paste (unknown reads as off) together with the pid of the process running in
// it, read under one hold of s.mu. Read apart, a re-run between the two would
// pair the old process's mode with the new process's pid, and typeLine would
// wrap text for a program that never asked for the markers — the pid check after
// the gap cannot catch it, since the pid it compares against is already the new
// one. The re-run forgets the mode under s.mu, so under one hold the pair always
// describes the same process. Takes s.mu itself; callers must NOT hold it.
func (s *Server) pasteModeAndPid(id string) (paste bool, pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pasteMode[id], s.pidOf(id)
}

// asPaste wraps text as one bracketed paste. The markers are stripped from the
// text first: an ESC[201~ inside a message would end the paste early and leave
// the rest — including anything the sender placed after it — read as typed keys,
// and a stray ESC[200~ would open a paste the closing marker no longer matches.
// Removal repeats until neither is left, because taking one out can join its
// neighbours into the other: ESC[20 ESC[201~ 0~ is an ESC[200~ once the inner
// marker is gone.
func asPaste(text []byte) []byte {
	for bytes.Contains(text, pasteStart) || bytes.Contains(text, pasteEnd) {
		text = bytes.ReplaceAll(text, pasteStart, nil)
		text = bytes.ReplaceAll(text, pasteEnd, nil)
	}
	out := make([]byte, 0, len(pasteStart)+len(text)+len(pasteEnd))
	return append(append(append(out, pasteStart...), text...), pasteEnd...)
}
