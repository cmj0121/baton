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
	return s.scoreLook(s.scoreState.Store.View(ctx))
}

// scoreExplain is scoreView's operator-facing twin: the same reconcile, the
// same logging, the same failure handling, over Store.Explain — which adds
// every entry the store holds, ranked, with the factors that placed it.
//
// It is a second call rather than a flag on the first because the two reads
// cost differently: a factor breakdown per entry is an allocation the size of
// the store, and scoreView runs once per brief. Only score.list asks for it.
func (s *Server) scoreExplain(ctx score.Context) score.View {
	return s.scoreLook(s.scoreState.Store.Explain(ctx))
}

// scoreLook is what both reads do with the answer: log the folds, latch the
// failure, and report what the pass changed. It takes the store call's two
// results directly, so each read is a single expression and neither can end up
// with a copy of the logging that drifts from the other's — a fold is the one
// mutation an operator cannot see by looking at their own file, and #38 asks for
// exactly one log line per fold whichever read drained it.
func (s *Server) scoreLook(v score.View, err error) score.View {
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
//
// A DISPATCHED BRIEF is the third door, and it gets its own wording because the
// other two cannot describe it: the record reads exactly like the one the
// operator's own `ctl score submit` leaves — same source, no line touched — and
// yet they submitted nothing. #38 §4 accepts that a brief may coincidentally
// repeat an unrelated entry, and that cost is only bearable while it is visible,
// so the line has to say a dispatch was what counted rather than leave it to be
// worked out.
func logScoreFolds(folds []score.Fold) {
	for _, f := range folds {
		// `at` is the fold's OWN moment, not this line's. Every record but a
		// signal's waits for the next read to drain it, so zerolog's timestamp is
		// when the daemon got round to saying it and can be minutes out; see
		// score.Fold.At.
		e := log.Info().Str("id", f.Id).Str("entry", f.Text).Str("duplicate", f.Repeat).
			Int("duplicates", f.Duplicates).Str("source", f.Prov.Source).
			Int("reinforcements", f.Reinforcements).Int("user_signals", f.UserSignals).
			Int("tier", f.Tier).Bool("counted", f.Counted).Time("at", f.At)
		if f.Prov.SourcePanel != "" {
			e = e.Str("panel", f.Prov.SourcePanel)
		}
		msg := "score folded a repeat into an existing entry"
		switch {
		case f.FromFile:
			e = e.Bool("removed", f.Removed)
			msg = "score folded a duplicate line out of score.md"
			if !f.Removed {
				msg = "score folded a duplicate line but could not remove it from score.md"
			}
		case f.FromSignal:
			msg = "score counted a brief the user dispatched as a repeat of an existing entry"
		}
		e.Msg(msg)
	}
}

// ScoreFolds writes the fold lines for records a caller drained itself, and is
// the daemon's way of emptying the buffer on the way out (cmd/baton's cleanup).
// It exists for the reason ScoreCounters does: internal/score is stdlib-only and
// never logs, so the shape of these lines lives here, and a shutdown that
// hand-rolled its own would be the second producer logScoreFolds was written to
// prevent.
func ScoreFolds(folds []score.Fold) {
	logScoreFolds(folds)
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
//
// It only READS the memory. A brief the user wrote is one of #38 §4's two
// sources of the user signal, but that is recorded by scoreSignal after the
// dispatch has actually landed — a brief a task.pre hook vetoed, or one that
// failed on an unknown panel id, is not the user telling the fleet anything.
func (s *Server) dispatchBrief(id, prompt string) TaskBrief {
	ctx, _ := s.panelContext(id)
	b := TaskBrief{Prompt: prompt, Panel: id, Group: ctx.Group, Cwd: ctx.Cwd, Profile: ctx.Profile}
	// panelContext has let go of s.mu and the store takes its own lock, so this
	// runs off both; a nil (disabled) store yields the zero view and nothing is
	// injected.
	v := s.scoreView(ctx)
	b.Score = v.Block
	logScoreInjection(ctx, v)
	return b
}

// scoreSignal records a brief the USER dispatched as a reinforcement of whatever
// entry it repeats — #38 §4's second source, the one that needs no protocol
// beyond the connection it arrived on.
//
// It runs AFTER the dispatch has landed, not while the brief is being built. A
// task.pre hook can veto a dispatch and an unknown panel id can fail one, and
// either way nothing reached an agent; counting those would make the signal a
// record of what the user ASKED for rather than of what the fleet was told, and
// the entry would climb on briefs that never happened.
//
// It counts the user's OWN text rather than the brief the hook chain produced.
// A task.pre hook may rewrite a prompt freely, so ranking its output as the
// user's voice would let a plugin reach the one tier #37 reserves for the
// operator — the same self-report #38 §4 rules out, arriving through the
// customisation point instead of through an agent.
//
// The discrimination is connProvenance's and nothing else's: an agent panel can
// dispatch too (a conductor driving a worker), and an agent's brief is not the
// user asking for anything.
//
// A brief that matches no entry does nothing at all — Store.Signal admits
// nothing, so a dispatch is never an observation being recorded. It is not free
// even then, and this is the cost R4 actually adds to a dispatch: like every
// other mutation path the store has, Signal re-reads score.md unconditionally
// rather than through the render's fingerprint gate. On a large store that is
// about a millisecond for a miss and rather more for a hit, which dwarfs
// anything the entry's own size costs the ranking — anyone tuning this should
// start here. It is a path a person drives one dispatch at a time, and it is
// never on an agent's.
//
// The fold is logged HERE, on the dispatch that caused it, through the same
// logScoreFolds every other fold goes through. Store.Signal hands the record
// back rather than buffering it for the next read, because #38 §4's accepted
// cost is only bearable while it is visible and a line that arrives on the next
// dispatch — or not at all, if the daemon stops first — is not that.
func (s *Server) scoreSignal(cc *clientConn, prompt string) {
	if !s.scoreState.available() {
		return
	}
	prov := s.connProvenance(cc)
	if prov.Source != score.SourceUser {
		return
	}
	rec, hit, err := s.scoreState.Store.Signal(prompt, prov)
	switch {
	case err != nil && !s.scoreState.failing.Load():
		// Only when the dispatch's own view has not already said it. The two calls
		// share a reconcile pass and therefore share their failure, and scoreLook's
		// latch exists precisely so an unreadable score.md is reported on the
		// dispatch it started on rather than on every dispatch after it.
		log.Warn().Err(err).Str("dir", s.scoreState.Store.Dir()).
			Msg("score could not count the user's brief as a reinforcement")
	case hit:
		logScoreFolds([]score.Fold{rec})
	}
}

// connProvenance is #38 §4's discrimination and the ONE place it is made: a
// connection that declared a self on hello is an agent inside a panel, and one
// that did not is the TUI or `baton ctl` from the operator's own shell.
//
// BE CLEAR ABOUT WHAT THIS IS. cc.self is assigned straight from the hello
// frame's Self field and validated against nothing (see the hello case in
// onCommand). It is what an HONEST client declares, not something the daemon
// verified. What makes it work is only that the daemon injects BATON_PANEL_ID
// into every agent panel it spawns, so an ordinary agent is stamped correctly
// without doing anything, while `env -u BATON_PANEL_ID baton ctl score submit`
// from inside that same panel is stamped as the user.
//
// That is not a hole to be closed here. #38's Trust and exposure section says
// outright that an agent which unsets its own environment can pose as a cockpit,
// and that one with filesystem access can write score.md directly — Score does
// not claim to be a boundary against a hostile agent, and invariant I6 protects
// against a fleet reinforcing its own conclusions, not against an adversary. A
// check here would buy nothing while both of those stand.
//
// Two consequences worth knowing before trusting a stamp. A SHELL panel gets no
// identity environment at all, so an agent CLI a user starts by hand inside one
// is stamped as the user for as long as it runs — no forging required. And the
// ROLE fence cannot substitute: BATON_ROLE reaches the conductor panel alone, so
// an ordinary worker panel's connection is unfenced and would look exactly like
// a cockpit.
//
// The agent branch also carries the three ranking dimensions, read through
// panelContext because that is where a dispatch reads them: an entry's recorded
// cwd and a dispatch's cwd have to come from one reader or they can never be
// equal. A cockpit has no panel row, so it records none of the three and its
// entries rank on tier and recency alone.
func (s *Server) connProvenance(cc *clientConn) score.Provenance {
	if cc.self == "" {
		return score.Provenance{Source: score.SourceUser}
	}
	prov := score.Provenance{Source: score.SourceAgent, SourcePanel: cc.self}
	if ctx, ok := s.panelContext(cc.self); ok {
		prov.SourceCwd, prov.SourceProfile, prov.SourceGroup = ctx.Cwd, ctx.Profile, ctx.Group
	}
	return prov
}

// logScoreInjection names the entries this brief is carrying, and the context
// that put them there.
//
// Nothing else can answer "why did panel 7 get this entry" after the fact. The
// factor breakdown answers it for a listing taken NOW, against the store as it
// stands now — but a brief is delivered once, against the panel's directory and
// the log's positions at that moment, and by the time anyone asks, an entry may
// have been reworded, retired, or outranked. It is the same invariant I8
// obligation the breakdown is: the fleet must not be told something an operator
// cannot afterwards account for.
//
// Silent when nothing was injected, which is the ordinary case for an empty or
// disabled store and is already said elsewhere.
func logScoreInjection(ctx score.Context, v score.View) {
	if len(v.Entries) == 0 {
		return
	}
	ids := zerolog.Arr()
	for _, e := range v.Entries {
		ids = ids.Str(e.Id)
	}
	e := log.Info().Str("panel", ctx.Panel).Array("entries", ids).
		Int("injected", len(v.Entries)).Int("of", v.Total)
	// The context, only where there is one: an entry matches a dimension by
	// equalling it, so a field the panel does not have is a field nothing was
	// ranked on, and printing it empty would read as one that was.
	if ctx.Cwd != "" {
		e = e.Str("cwd", ctx.Cwd)
	}
	if ctx.Profile != "" {
		e = e.Str("profile", ctx.Profile)
	}
	if ctx.Group != "" {
		e = e.Str("group", ctx.Group)
	}
	e.Msg("score injected into a direct dispatch")
}

// panelContext is the ranking context of the panel id: the three properties an
// entry's provenance is matched against, plus the id itself. found is false when
// the fleet has no such panel, and the context is then the zero one — every
// dimension unmatched, which is what an unknown id has to mean.
//
// It is the ONE place a context is built from a panel, and that is what makes
// the cwd dimension able to match at all. Both ends of the comparison come from
// here: score.submit stamps an entry's provenance with it, and dispatchBrief and
// score.list rank against it. Two readers would not merely be untidy — an entry
// whose cwd was recorded one way can never equal a dispatch's read another way,
// so the weight would silently apply to some entries and not others.
//
// The directory therefore goes through panelCwd rather than the panels row's own
// Cwd field. That field is learned lazily: a panel spawned with --dir has not
// reported one yet while it is still starting, so an entry submitted in that
// window recorded NO source_cwd — and provenance is written once, so that entry
// could never match a cwd for the rest of its life. panelCwd samples the process
// table on demand, which is exactly the moment something is about to use the
// path. It takes s.mu itself, so it is called after this one has let go.
func (s *Server) panelContext(id string) (ctx score.Context, found bool) {
	ctx.Panel = id
	s.mu.Lock()
	idx := s.indexLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return ctx, false
	}
	ctx.Group = s.panels[idx].Group
	ctx.Profile = s.specs[id].Profile
	s.mu.Unlock()
	ctx.Cwd = s.panelCwd(id)
	return ctx, true
}

// scoreSubmit handles score.submit: record cmd.Prompt as a new entry, stamped
// with the provenance of the connection it arrived on — see connProvenance,
// which is where #38 §4's rule lives and which a dispatched brief now asks the
// same question of. The store refuses plainly when disabled (nil), and that
// refusal is the whole disabled story: no flag here.
func (s *Server) scoreSubmit(cc *clientConn, cmd proto.Command) {
	prov := s.connProvenance(cc)
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

// scoreList answers score.list: EVERY entry the store holds, in rank order, each
// with its tier, its rank, the five factors that multiply out to that rank, and
// whether the working set took it — beside the context they were ranked
// against.
//
// Uncapped, which is the point. It used to answer with the view's rendered set
// — seven entries — so a store past seven hid the tier of everything after
// them from score.list, score.status and score.md alike, and only the raw
// event log had it. R2 made tiers real, so an operator could be told an entry
// was important with no way to see which ones those were (#42). Invariant I8
// is the rule that forbids that: the operator must not have to read the log to
// understand what the fleet is being told.
//
// The BREAKDOWN is the other half of the same obligation. A multiplicative rank
// reported alone answers "why is this entry in my brief" with a number; the
// factors answer it with a reason, and an operator who multiplies the five gets
// the rank beside them exactly (see score.Factors).
//
// The reply ECHOES the context it ranked against, and that is not decoration.
// cmd.ID may name a panel, in which case the ranking runs against that panel's
// directory, profile and group — the question an operator actually has, which
// is "why is this in the brief THAT panel gets". Named nothing, the context is
// empty: a cockpit is not a panel and has nothing of its own to match, so every
// context factor reads 1.0 and the order is tier and recency alone.
//
// Without the echo those two answers are indistinguishable in the payload.
// `active` reads exactly like "this is in the brief", and every real direct
// dispatch fills Cwd from the panel row and usually Profile — so a contextless
// `active` set is one no real panel dispatch will produce. The breakdown leaves
// a trail, since every context factor sits at 1.0 while score.status reports
// weights of 2.0, but that resolves to "nothing matched" rather than "nothing
// COULD match", and telling those apart is the whole of invariant I8 here.
//
// An id no panel answers to is REFUSED rather than quietly ranked against
// nothing, for the same reason: silently answering a question about panel 7 with
// the contextless listing is the exact ambiguity the echo exists to remove.
func (s *Server) scoreList(cc *clientConn, cmd proto.Command) {
	ctx := score.Context{}
	if cmd.ID != "" {
		var found bool
		if ctx, found = s.panelContext(cmd.ID); !found {
			send(cc, proto.ServerMsg{Type: "error", Error: "no panel " + cmd.ID})
			return
		}
	}
	ranked := s.scoreExplain(ctx).Ranked
	if ranked == nil {
		ranked = []score.Ranked{} // an empty list, never JSON null
	}
	send(cc, proto.ServerMsg{Type: "score", Score: scoreJSON(struct {
		Context score.Context  `json:"context"`
		Entries []score.Ranked `json:"entries"`
	}{Context: ctx, Entries: ranked})})
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
// Both counts are reported because they legitimately disagree: rendered is the
// WORKING SET, which is capped at the working-set budget and withholds anything
// over the entry weight cap, so a store past either would carry fewer entries
// in its briefs than it holds with no way to tell whether the gap was a cap or a
// bug. Naming the rendered count lets status explain its own gap — and
// working_set below names the cap it was measured against.
//
// oversized and block_full then say WHICH cap. Three of them can make rendered
// fall short of the budget — an entry too long to inject at all, the block's
// rune backstop, and the budget itself — and from the two counts alone every
// one of them looks like the same ordinary truncation. oversized is the count of
// lines withheld for their own weight, which without this do nothing anywhere
// except sit in the file the operator typed them into. block_full says the
// working set stopped because the injected block ran out of runes rather than
// out of budget, which is the only one of the three that is invisible from both
// the entry and the policy. Whichever it was, score.list carries the same answer
// per entry as a Standing.
//
// promote_at, user_signals_at, working_set and rank are the tuning actually in
// force, which is not the same as what the config file says: all of them reload
// on SIGHUP, all of them are clamped, and a config that failed to parse leaves
// the running values alone. An operator retuning them has no other way to confirm the
// daemon took the change, and a knob whose effect cannot be observed is one
// they cannot trust (invariant I8). rank matters most of the three, because a
// weight of 1.0 is indistinguishable in a score.list breakdown from a dimension
// that simply did not match — the factor reads 1.0 either way, and only the
// policy says which happened.
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
	// The weights are OMITTED rather than reported as zeros when no store is
	// running. A view of a store that is not there is the zero view, and zero is
	// a value the clamp can never produce — every weight in force is at least
	// one — so printing it would be the reply inventing a policy. available and
	// reason already carry the truth on that branch. promote_at and working_set
	// elide themselves through omitempty for the same reason.
	var rank *score.Rank
	if s.scoreState.available() {
		r := v.Policy.Rank
		rank = &r
	}
	return scoreJSON(struct {
		Enabled       bool        `json:"enabled"`
		Available     bool        `json:"available"`
		Unlocked      bool        `json:"unlocked,omitempty"`
		Reason        string      `json:"reason,omitempty"`
		Entries       int         `json:"entries"`
		Rendered      int         `json:"rendered"`
		Oversized     int         `json:"oversized"`
		BlockFull     bool        `json:"block_full,omitempty"`
		PromoteAt     int         `json:"promote_at,omitempty"`
		UserSignalsAt int         `json:"user_signals_at,omitempty"`
		WorkingSet    int         `json:"working_set,omitempty"`
		Rank          *score.Rank `json:"rank,omitempty"`
		Dir           string      `json:"dir,omitempty"`
	}{
		Enabled:       s.scoreState.Enabled,
		Available:     s.scoreState.available(),
		Unlocked:      v.Unlocked,
		Reason:        s.scoreState.reason(),
		Entries:       v.Total,
		Rendered:      len(v.Entries),
		Oversized:     v.Health.Oversized,
		BlockFull:     v.BlockFull,
		PromoteAt:     v.Policy.PromoteAt,
		UserSignalsAt: v.Policy.UserSignalsAt,
		WorkingSet:    v.Policy.WorkingSet,
		Rank:          rank,
		Dir:           s.scoreState.Store.Dir(),
	})
}

// scoreJSON marshals a reply payload built above from in-memory maps, structs,
// and string slices — shapes that cannot fail to encode.
func scoreJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
