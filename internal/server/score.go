package server

import (
	"encoding/json"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
)

// This file is the server's side of the fleet memory (internal/score, #39): the
// score.* verb handlers and the brief builder that renders the score block into
// a direct dispatch. The store itself never logs and never sees the wire — the
// server owns both ends here, exactly as it does for the plugin subsystem.

// scoreView takes one consistent look at the fleet memory for the given
// dispatch context, reconciling the operator's score.md edits into the store
// first — #38's invariant I2, "no render, submit, or reinforce acts on a stale
// view". score.md is a file a person edits in their own editor while the fleet
// runs: without this an edit would stay inert until a restart, and a restart
// returns every panel as Exited.
//
// Store.View is what makes both halves true rather than remembered. The
// reconcile is folded into the read, so a read path added later cannot forget
// it, and everything the reply reports comes from a single hold of the store's
// lock, so a concurrent submit cannot land between two of the numbers.
//
// It is deliberately cheap enough for the dispatch path: the store stats
// score.md off its own lock and parses nothing unless the file moved, so the
// steady state costs one stat per dispatch.
//
// A failed reconcile is a warning, not a refused dispatch — the view returned
// is the last one the store did read, which is a brief built on slightly old
// memory rather than no brief at all.
func (s *Server) scoreView(ctx score.Context) score.View {
	v, err := s.scoreState.Store.View(ctx)

	// One line per fold, FIRST — before the error branch below, because a failing
	// pass is exactly when a fold most needs saying. A fold can be durable and
	// counted and still leave the pass in error (the rewrite that should have
	// removed the line failed), and score.md is then the one place that cannot
	// show it: the entry's counter moved, no line was added, and the operator
	// would be left with a tier they cannot account for. Returning early dropped
	// these records on the floor, since View drains them either way.
	logScoreFolds(v.Folds)

	if err != nil {
		// Latch the failure. A score.md the store cannot read stays unreadable,
		// and the gate is not armed by a failed pass, so the naive shape logs an
		// identical line on every dispatch for as long as the condition lasts.
		// Report the transition instead — the operator needs to know it started
		// and that it stopped, not how many dispatches happened in between. The
		// latch also reaches score.status, so the answer to "why is my edit inert"
		// does not live only in the log (invariant I8).
		if !s.scoreState.failing.Swap(true) {
			log.Warn().Err(err).Str("dir", s.scoreState.Store.Dir()).
				Msg("score reconcile failing; serving the last view read")
		}
		return v
	}
	if s.scoreState.failing.Swap(false) {
		log.Info().Str("dir", s.scoreState.Store.Dir()).Msg("score reconcile recovered")
	}
	// One line per reconcile change (#38 lifecycle), so "why did this entry
	// appear" is answerable without reading the event log by hand. The pass
	// reports what IT did, so there is no before/after subtraction to keep in
	// step — and no window for a concurrent submit's work to be attributed here.
	// Oversized is a gauge rather than a counter, and is the operator's only
	// warning that a line they wrote is too long to inject; see
	// score.maxEntryRunes.
	if v.Delta != (score.Delta{}) {
		ScoreCounters(log.Info(), v.Delta, v.Health).Msg("score reconciled the operator's edits")
	}
	return v
}

// logScoreFolds writes the one line per fold #38's lifecycle asks for. It is the
// SINGLE producer of that line, for both ways a repeat arrives: the store buffers
// a record wherever it folds — a submission or a duplicate score.md line — and
// every View drains them here. Two hand-rolled log sites with two messages and
// two field sets meant an operator grepping for folds found two shapes for one
// concept, and #43 would have added a third.
//
// A count alone cannot answer "which line did it take" once the line is gone, so
// each record names the surviving wording beside the repeat. The message then
// follows what actually happened rather than what the pass set out to do:
// `counted: false` is a repeat the store already owed the removal of, taken out
// now, and `removed: false` is the other half — the fold is durable but the line
// is still in the file. Announcing a deletion there would be #38's one log line
// per fold, saying something untrue. A submission fold removed nothing and never
// claims to; only a file fold reports the field at all.
func logScoreFolds(folds []score.Fold) {
	for _, f := range folds {
		e := log.Info().Str("id", f.Id).Str("entry", f.Text).Str("duplicate", f.Repeat).
			Int("duplicates", f.Duplicates).Str("source", f.Prov.Source).
			Int("reinforcements", f.Reinforcements).Int("tier", f.Tier).
			Bool("counted", f.Counted)
		if f.Prov.SourcePanel != "" {
			e = e.Str("panel", f.Prov.SourcePanel)
		}
		msg := "score folded a repeat into an existing entry"
		if f.FromFile {
			e = e.Bool("removed", f.Removed)
			msg = "score folded a duplicate line out of score.md"
			if !f.Removed {
				msg = "score folded a duplicate line but could not remove it from score.md"
			}
		}
		e.Msg(msg)
	}
}

// ScoreCounters stamps a log event with everything one pass of the store did and
// everything the store currently stands at. It is the ONE enumeration of those
// two field lists: the boot pass (cmd/baton) and every reconcile pass (above)
// both report them, and when each site kept its own list they drifted — the boot
// line omitted `folded` and `raised`, so a tier the recovery pass granted was
// logged nowhere at all, which is exactly the mutation an operator cannot see by
// looking at their own file.
//
// It lives here rather than on score.Health because this package already imports
// zerolog and score, and internal/score is stdlib-only on purpose.
//
// Delta counts what THIS pass did; Health is the store's standing — a gauge
// (oversized) plus the four counters that each say the store chose to remember
// less. None of the latter is an error, and none is visible any other way.
func ScoreCounters(e *zerolog.Event, d score.Delta, h score.Health) *zerolog.Event {
	return e.Int("admitted", d.Admitted).
		Int("reattributed", d.Reattributed).
		Int("adopted", d.Adopted).
		Int("superseded", d.Superseded).
		Int("folded", d.Folded).
		Int("raised", d.Raised).
		Int("retired", d.Retired).
		Int("reprojected", d.Reprojected).
		Int("oversized", h.Oversized).
		Int("torn_events", h.TornEvents).
		Int("cache_write_failures", h.CacheWriteFailures).
		Int("swallowed_repeats", h.SwallowedRepeats).
		Int("unreported_folds", h.UnreportedFolds).
		Int("alias_evictions", h.AliasEvictions).
		Int("rejected_raises", h.RejectedRaises)
}

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
	// The store takes its own lock, so this runs off s.mu; a nil (disabled) store
	// yields the zero view and nothing is injected.
	b.Score = s.scoreView(score.Context{Panel: b.Panel, Profile: b.Profile, Cwd: b.Cwd, Group: b.Group}).Block
	return b
}

// scoreSubmit handles score.submit: record cmd.Prompt as a new entry, stamped
// with provenance derived from the connection (#38 §4). A connection that
// declared a self on hello is an agent panel, so the entry carries that panel's
// id — plus its profile, cwd and group when the row is still in the fleet, which
// are the three dimensions the ranking matches a dispatch against — while one
// that did not is the operator's cockpit, which has no panel row and therefore
// records none of the three. The store refuses plainly when
// disabled (nil), and that refusal is the whole disabled story: no flag here.
func (s *Server) scoreSubmit(cc *clientConn, cmd proto.Command) {
	prov := score.Provenance{Source: "user"}
	if cc.self != "" {
		prov = score.Provenance{Source: "agent", SourcePanel: cc.self}
		s.mu.Lock()
		if idx := s.indexLocked(cc.self); idx >= 0 {
			prov.SourceCwd = s.panels[idx].Cwd
			prov.SourceGroup = s.panels[idx].Group
			prov.SourceProfile = s.specs[cc.self].Profile
		}
		s.mu.Unlock()
	}
	// A nil store is not always "switched off": it is also a directory another
	// daemon holds and a set of files that would not open. Say which, so the
	// answer is actionable rather than merely accurate.
	if !s.scoreState.available() {
		send(cc, proto.ServerMsg{Type: "error", Error: s.scoreState.reason()})
		return
	}
	e, folded, err := s.scoreState.Store.Submit(cmd.Prompt, prov)
	if err != nil {
		send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
		return
	}
	// The fold is not logged here. The store recorded it on the same buffer a
	// folded score.md line lands on, and logScoreFolds — reached by the next
	// view, which every dispatch, list and status takes — is the one place either
	// becomes a log line. A plain submission needs none: it is already visible as
	// a new line in score.md, while a fold leaves the file untouched, which is
	// why it is the mutation worth saying out loud.
	//
	// The reply says WHICH of the two happened, per #38's "get back new or folded
	// into id". Without it four identical submissions answer with one id and
	// nothing to tell the first from the rest, and an agent cannot learn that the
	// fleet already knew what it just said — which is the one thing worth knowing
	// when a submission is free.
	send(cc, proto.ServerMsg{Type: "score", Score: scoreJSON(struct {
		Id     string `json:"id"`
		Folded bool   `json:"folded,omitempty"`
	}{Id: e.Id, Folded: folded})})
}

// scoreList is the score.list payload: the store's entries. S0 has no richer
// read than the view's rendered set — an empty context returns the first N
// entries in file order, which is the dummy the walking skeleton promises (#39);
// R3 gives the list a real view of its own.
func (s *Server) scoreList() json.RawMessage {
	entries := s.scoreView(score.Context{}).Entries
	if entries == nil {
		entries = []score.Entry{} // an empty list, never JSON null
	}
	return scoreJSON(entries)
}

// scoreStatus is the score.status payload: whether the subsystem is switched
// on, whether it is actually running and why not when it is not, how many
// entries it holds, how many of those a dispatch would carry, and where its
// files live.
//
// enabled and available are separate because they answer different questions.
// enabled is the config knob. available is whether a store opened — which it
// does not when another daemon holds the score directory, the ordinary outcome
// of the second fleet BATON_SOCK exists to run. Collapsing the two would leave
// the operator of that second fleet with an empty memory, a "disabled" status,
// and nothing anywhere to explain it.
//
// reason then covers the third case the pair still cannot express: a store that
// opened normally and whose score.md has since become unreadable is available,
// enabled, and inert.
//
// Both counts are reported because they legitimately disagree: the listed
// entries ride the render, which caps at the render limit and withholds
// anything over the entry weight cap, so a store past either would show a
// shorter list than its entry count with no way to tell whether the gap was a
// cap or a bug. Naming the rendered count lets status explain its own gap.
//
// oversized then says WHICH cap. A line too long to inject is withheld from
// score.list as well as from every brief, so without this the operator's own
// entry is invisible everywhere except the file they typed it into, and the
// entries/rendered gap looks identical to an ordinary render-limit truncation.
//
// promote_at is the recurrence threshold actually in force, which is not the
// same as the one in the config file: it reloads on SIGHUP, it has a floor, and
// a config that failed to parse leaves the running value alone. An operator
// retuning it has no other way to confirm the daemon took the change, and a knob
// whose effect cannot be observed is one they cannot trust (invariant I8).
//
// unlocked reports a store running without its single-writer claim, which
// happens where the filesystem cannot lock — an NFS $HOME being exactly where
// the default score directory lands. The boot warning is one line in a log
// nobody reads until something is wrong, and "something is wrong" here looks
// like two daemons quietly diverging.
//
// Every field follows invariant I8: the operator must not have to read the
// daemon log to find out why their memory is behaving as it is. One view backs
// all of them, so the reply can never mix two readings of the store.
func (s *Server) scoreStatus() json.RawMessage {
	v := s.scoreView(score.Context{})
	return scoreJSON(struct {
		Enabled   bool   `json:"enabled"`
		Available bool   `json:"available"`
		Unlocked  bool   `json:"unlocked,omitempty"`
		Reason    string `json:"reason,omitempty"`
		Entries   int    `json:"entries"`
		Rendered  int    `json:"rendered"`
		Oversized int    `json:"oversized"`
		PromoteAt int    `json:"promote_at,omitempty"`
		Dir       string `json:"dir,omitempty"`
	}{
		Enabled:   s.scoreState.Enabled,
		Available: s.scoreState.available(),
		Unlocked:  v.Unlocked,
		Reason:    s.scoreState.reason(),
		Entries:   v.Total,
		Rendered:  len(v.Entries),
		Oversized: v.Health.Oversized,
		PromoteAt: v.Policy.PromoteAt,
		Dir:       s.scoreState.Store.Dir(),
	})
}

// scoreJSON marshals a reply payload built above from in-memory maps, structs,
// and string slices — shapes that cannot fail to encode.
func scoreJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
