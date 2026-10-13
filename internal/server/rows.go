package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/attn"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/scrub"
)

// The row files: what the fleet hands MOLD to learn from (D6, D7).
//
// A classifier is only as good as the tails a human certified, and the two
// places a human (or the agent itself) certifies one are an agent's own
// `panel.attention` and an operator's `attention.mark` — both positives, to
// attention-rows.jsonl. A dismiss of a guessed attention is the one honest
// negative there is, and it goes to attention-holdout.jsonl as a `keys: []`
// row: `mold eval` reads those, `mold train` refuses them, so they are two
// files rather than one with a flag. Nothing here runs unless
// panel.classifier.record is on (D8): scrollback on disk is a decision of its
// own.

// rowCap is the size past which a row file stops growing. At the cap the write
// is skipped and said once rather than rotated (D13): truncating oldest-first
// would silently forget the earliest positives of a training set, while
// stopping loses nothing and is visible in `classifier status`. The operator
// trains, then rotates by hand.
const rowCap = 1 << 20

// rowTextRunes caps one row's text. A tail is attnTailBytes of screen, and a
// row is for a model to read one line of, not a transcript.
const rowTextRunes = 2000

// rowFile is one of the two files: its path, the hashes of every text already
// in it (loaded on first use — nil until then — so the same permission overlay
// coming back turn after turn is written once), how many lines it holds, and
// whether the cap has been reported.
type rowFile struct {
	path   string
	seen   map[uint64]bool
	lines  int
	capped bool
}

// row is a line of either file, in the shape mold reads and nothing more, so
// mold never meets a field it does not know.
type row struct {
	Text string   `json:"text"`
	Keys []string `json:"keys"`
}

// append writes text as one row — a positive (`["attention"]`) or a negative
// (`[]`) — and returns the line number it wrote, the file's new line count; 0
// means nothing was written. Nothing is written for an empty text, a text
// already in the file, or a file at its cap.
func (f *rowFile) append(text string, positive bool) int {
	if text == "" {
		return 0
	}
	f.load()
	h := fnv64([]byte(text))
	if f.seen[h] {
		return 0
	}
	if st, err := os.Stat(f.path); err == nil && st.Size() >= rowCap {
		if !f.capped {
			f.capped = true
			log.Warn().Str("file", f.path).Int("cap", rowCap).Msg("attention rows: file at its cap, no longer recording")
		}
		return 0
	}
	f.capped = false

	keys := []string{}
	if positive {
		keys = []string{"attention"}
	}
	line, err := json.Marshal(row{Text: text, Keys: keys})
	if err != nil {
		return 0
	}
	if err := paths.EnsureDir(f.path); err != nil {
		log.Warn().Err(err).Str("file", f.path).Msg("attention rows: cannot create the directory")
		return 0
	}
	fh, err := os.OpenFile(f.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		log.Warn().Err(err).Str("file", f.path).Msg("attention rows: cannot open the file")
		return 0
	}
	_, err = fh.Write(append(line, '\n'))
	if cerr := fh.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		log.Warn().Err(err).Str("file", f.path).Msg("attention rows: write failed")
		return 0
	}
	f.seen[h] = true
	f.lines++
	return f.lines
}

// load fills the dedup set and the line count from what is already on disk,
// once. A missing or unreadable file is an empty one; a line that is not a row
// still counts as a line, since that is what `wc -l` and mold would say.
func (f *rowFile) load() {
	if f.seen != nil {
		return
	}
	f.seen = map[uint64]bool{}
	f.lines, _ = scanRows(f.path, func(line []byte) {
		var r row
		if json.Unmarshal(line, &r) == nil && r.Text != "" {
			f.seen[fnv64([]byte(r.Text))] = true
		}
	})
}

// scanRows opens path once, reports its size against rowCap from the open
// handle, and counts its lines, handing each to each when it is non-nil. The
// scanner's buffer is the cap itself, so no line of a file under the cap is
// too long to read.
func scanRows(path string, each func(line []byte)) (lines int, capped bool) {
	fh, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = fh.Close() }()
	if st, err := fh.Stat(); err == nil {
		capped = st.Size() >= rowCap
	}
	sc := bufio.NewScanner(fh)
	sc.Buffer(nil, rowCap)
	for sc.Scan() {
		lines++
		if each != nil {
			each(sc.Bytes())
		}
	}
	return lines, capped
}

// rowText is what a tail becomes on disk: its content lines (no CSI, no
// border-only lines), scrubbed to one line and capped. Empty means no row.
func rowText(tail []byte) string {
	return scrub.Capped(strings.Join(attn.ContentLines(string(tail)), "\n"), rowTextRunes)
}

// rowFileLocked returns the rows file for a positive or the holdout for a
// negative, making it on first use. Caller holds s.mu.
func (s *Server) rowFileLocked(positive bool) *rowFile {
	if positive {
		if s.rows == nil {
			s.rows = &rowFile{path: paths.AttentionRows()}
		}
		return s.rows
	}
	if s.holdout == nil {
		s.holdout = &rowFile{path: paths.AttentionHoldout()}
	}
	return s.holdout
}

// recordRowLocked writes the panel's tail at this instant as a row — a
// positive to the rows file or a negative to the holdout — when recording is
// on, and returns the line it landed on (0 when no row was written). Caller
// holds s.mu.
func (s *Server) recordRowLocked(id string, positive bool) int {
	if !s.classifierRecord {
		return 0
	}
	return s.rowFileLocked(positive).append(rowText(s.pty.Tail(id, attnTailBytes)), positive)
}

// markAttention handles attention.mark: an operator saying "this panel is
// waiting on a person, whatever the fleet thinks" — from the inbox, a zoom, or
// `baton ctl attention-mark`. It writes the panel's tail as a positive row and
// changes nothing else: the mark is a label for the classifier to learn from,
// not a verdict on the panel's state, which the ladder keeps deciding on its
// own terms.
//
// An unknown id is a silent no-op, as panel.ack's is: the row can be reaped
// between the keystroke and the command. An exited panel is refused out loud:
// a dead screen is not a wait, whatever it says, and the cockpit's row may be
// stale, so the notice is what tells the operator nothing was recorded. The
// reply is otherwise the plain fleet broadcast plus a notice saying whether a
// row landed — "row N" is the line the file now ends on — so the status line
// can say more than "sent".
func (s *Server) markAttention(cc *clientConn, cmd proto.Command) {
	s.mu.Lock()
	i := s.indexLocked(cmd.ID)
	if i < 0 {
		s.mu.Unlock()
		return
	}
	if s.panels[i].State == panel.Exited {
		s.mu.Unlock()
		send(cc, proto.ServerMsg{Type: "notice", ID: cmd.ID, Notice: "not marked: the panel has exited"})
		return
	}
	n := s.recordRowLocked(cmd.ID, true)
	s.mu.Unlock()

	notice := "marked as a wait (not recorded)"
	if n > 0 {
		notice = fmt.Sprintf("marked as a wait (row %d)", n)
	}
	send(cc, proto.ServerMsg{Type: "notice", ID: cmd.ID, Notice: notice})
	s.broadcast(s.panelsMsg())
}

// rowCounts is what `classifier status` reports about the files: the lines in
// each (0 when absent) and whether either has reached its cap. It reads the
// files rather than the Server's own bookkeeping so a ctl connection can ask
// without s.mu, and so a file the operator rotated by hand reads as what it is.
func rowCounts() (rows, holdout int, capped bool) {
	rows, rc := scanRows(paths.AttentionRows(), nil)
	holdout, hc := scanRows(paths.AttentionHoldout(), nil)
	return rows, holdout, rc || hc
}
