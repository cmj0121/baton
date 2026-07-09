package server

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/proto"
)

// Fleet-wide content search. fleet.search scans every panel's retained output
// ring server-side — the frontend only holds emulators for the panels it has
// zoomed, so it cannot do this itself — and replies "search" with the matching
// lines as structured hits. The cockpit renders them grouped by panel; selecting
// one zooms that panel and re-runs the term as a scrollback search there.
//
// The scan reads the same replay ring the Monitor's attention tail and a client's
// attach-replay read: bounded to the per-panel replay buffer, so it searches
// recent output, not all history. It is a best-effort text grep — the ring holds
// raw PTY bytes, so escape sequences are stripped and a line rewritten in place
// (via a carriage return) is read at its final form — good enough to answer "which
// agent mentioned this", never a substitute for the exact per-panel search.

// Search result caps. A pathological match (e.g. searching "." against a busy
// fleet) must not build an unbounded reply, so hits are capped per panel and
// overall; when a cap trims the result the server logs the shortfall rather than
// silently reporting a partial set as complete.
const (
	maxHitsPerPanel = 100
	maxHitsTotal    = 1000
)

// sendSearch scans every panel's retained output for query and replies "search"
// with the matching lines. An empty query is an error (nothing to match); a query
// that is not a valid regexp falls back to a literal match of the raw text, mirroring
// the cockpit's own scrollback search, so a term with metacharacters still finds
// itself. A search that matches nothing still replies (with no hits) so the cockpit
// can say so.
func (s *Server) sendSearch(cc *clientConn, query string) error {
	query = strings.TrimSpace(query)
	if query == "" {
		return fmt.Errorf("empty search term")
	}
	re := compileFleetSearch(query)

	// Collect the panel identity under the lock, then read the rings without it:
	// pty.Snapshot takes the ptymgr's own lock, so holding s.mu across the whole
	// scan would needlessly serialise it against live output.
	type meta struct{ id, title, group string }
	s.mu.Lock()
	metas := make([]meta, len(s.panels))
	for i, p := range s.panels {
		title := p.Title
		if p.DisplayTitle != "" {
			title = p.DisplayTitle
		}
		metas[i] = meta{id: p.ID, title: title, group: p.Group}
	}
	s.mu.Unlock()

	hits := make([]proto.SearchHit, 0, 64)
	truncated := false
scan:
	for _, mt := range metas {
		raw := s.pty.Snapshot(mt.id)
		if len(raw) == 0 {
			continue
		}
		n := 0
		for _, ln := range searchLines(raw) {
			if strings.TrimSpace(ln) == "" || !re.MatchString(ln) {
				continue
			}
			hits = append(hits, proto.SearchHit{
				Panel: mt.id,
				Title: mt.title,
				Group: mt.group,
				Text:  strings.TrimRight(ln, " \t"),
			})
			if n++; n >= maxHitsPerPanel {
				truncated = true
				break
			}
			if len(hits) >= maxHitsTotal {
				truncated = true
				break scan
			}
		}
	}
	if truncated {
		log.Info().Str("query", query).Int("hits", len(hits)).Msg("fleet search truncated at a cap")
	} else {
		log.Info().Str("query", query).Int("hits", len(hits)).Msg("fleet search")
	}
	send(cc, proto.ServerMsg{Type: "search", Hits: hits})
	return nil
}

// compileFleetSearch turns a typed term into a case-insensitive matcher, falling
// back to a literal match when the term is not a valid regexp — the same rule the
// cockpit's scrollback search uses, so the two searches accept identical input and
// a fallback-literal fleet search lines up with the per-panel one it hands off to.
func compileFleetSearch(query string) *regexp.Regexp {
	if re, err := regexp.Compile("(?i)" + query); err == nil {
		return re
	}
	return regexp.MustCompile("(?i)" + regexp.QuoteMeta(query))
}

// searchLines turns a panel's raw output ring into plain text lines to match
// against: escape sequences are stripped (ansiSeq, shared with the Monitor's
// attention sniff), CRLF is normalised, and a bare carriage return collapses to
// the text after it so a line the program rewrote in place is read at its final
// form rather than matching a stale draft.
func searchLines(raw []byte) []string {
	text := ansiSeq.ReplaceAllString(string(raw), "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		if cr := strings.LastIndexByte(ln, '\r'); cr >= 0 {
			lines[i] = ln[cr+1:]
		}
	}
	return lines
}
