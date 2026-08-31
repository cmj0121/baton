package server

import (
	"encoding/json"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
)

// This file is the server's side of the fleet memory (internal/score, #39): the
// score.* verb handlers and the brief builder that renders the score block into
// a direct dispatch. The store itself never logs and never sees the wire — the
// server owns both ends here, exactly as it does for the plugin subsystem.

// dispatchBrief builds the task.pre brief for a DIRECT dispatch to panel id: the
// panel's own context (group, cwd, profile) read from its row, and the score
// block rendered against that context. Only this path fills the context fields —
// task.enqueue and panel.dispatch-group ride empty briefs, because injecting at
// a queued or fanned-out delivery is R5's problem (#39). An unknown id yields a
// bare brief and is left for dispatchScored to report.
func (s *Server) dispatchBrief(id, prompt string) TaskBrief {
	b := TaskBrief{Prompt: prompt, Panel: id}
	s.mu.Lock()
	if idx := s.indexLocked(id); idx >= 0 {
		b.Group, b.Cwd = s.panels[idx].Group, s.panels[idx].Cwd
		b.Profile = s.specs[id].Profile
	}
	s.mu.Unlock()
	// RenderBlock takes the store's own lock, so it runs off s.mu; a nil
	// (disabled) store renders the empty string and nothing is injected.
	b.Score = s.score.RenderBlock(score.Context{Panel: b.Panel, Profile: b.Profile, Cwd: b.Cwd, Group: b.Group})
	return b
}

// scoreSubmit handles score.submit: record cmd.Prompt as a new entry, stamped
// with provenance derived from the connection (#38 §4). A connection that
// declared a self on hello is an agent panel, so the entry carries that panel's
// id — plus its profile and cwd when the row is still in the fleet — while one
// that did not is the operator's cockpit. The store refuses plainly when
// disabled (nil), and that refusal is the whole disabled story: no flag here.
func (s *Server) scoreSubmit(cc *clientConn, cmd proto.Command) {
	prov := score.Provenance{Source: "user"}
	if cc.self != "" {
		prov = score.Provenance{Source: "agent", SourcePanel: cc.self}
		s.mu.Lock()
		if idx := s.indexLocked(cc.self); idx >= 0 {
			prov.SourceCwd = s.panels[idx].Cwd
			prov.SourceProfile = s.specs[cc.self].Profile
		}
		s.mu.Unlock()
	}
	e, err := s.score.Submit(cmd.Prompt, prov)
	if err != nil {
		send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
		return
	}
	send(cc, proto.ServerMsg{Type: "score", Score: scoreJSON(map[string]string{"id": e.Id})})
}

// scoreList is the score.list payload: the store's entries. S0 has no richer
// read than Render — an empty context returns the first N entries in file order,
// which is the dummy the walking skeleton promises (#39); R3 gives the list a
// real view of its own.
func (s *Server) scoreList() json.RawMessage {
	entries := s.score.Render(score.Context{})
	if entries == nil {
		entries = []score.Entry{} // an empty list, never JSON null
	}
	return scoreJSON(entries)
}

// scoreStatus is the score.status payload: whether the subsystem runs, how many
// entries it holds, how many of those a dispatch would actually carry, and where
// its files live. Deliberately minimal — it answers "is the memory on, is
// anything in it, and is the fleet being told all of it", nothing more.
//
// Both counts are reported because they legitimately disagree: score.list rides
// Render, which caps at the render limit, so a store past that cap would show a
// shorter list than its entry count with no way to tell whether the gap was a
// cap or a bug. Naming the rendered count lets status explain its own gap.
func (s *Server) scoreStatus() json.RawMessage {
	return scoreJSON(struct {
		Enabled  bool   `json:"enabled"`
		Entries  int    `json:"entries"`
		Rendered int    `json:"rendered"`
		Dir      string `json:"dir,omitempty"`
	}{
		Enabled:  s.score != nil,
		Entries:  s.score.Len(),
		Rendered: len(s.score.Render(score.Context{})),
		Dir:      s.score.Dir(),
	})
}

// scoreJSON marshals a reply payload built above from in-memory maps, structs,
// and string slices — shapes that cannot fail to encode.
func scoreJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
