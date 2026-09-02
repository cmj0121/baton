package server

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/score"
)

// This file is the server's side of the fleet memory (internal/score, #39): the
// score.* verb handlers and the brief builder that renders the score block into
// a brief on its way to a panel. The store itself never logs and never sees the
// wire — the server owns both ends here, exactly as it does for the plugin
// subsystem.

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
	// A RUNTIME compaction is announced from here, on the read that is about to
	// USE the re-spaced memory. #56 gave the store a compactor of its own, so the
	// re-spacing the boot line has always warned about now happens with no restart
	// — and said nothing anywhere, because internal/score does not log and nothing
	// outside it was watching. The store cannot announce it and the compactor is
	// not on any path the daemon logs from, so it is said by whoever is holding
	// the result: here that is every brief and every score.list / score.status,
	// each holding the very view the new spacing produced.
	//
	// It is NOT the only door, and saying so here was wrong for the one shape that
	// matters most: a daemon whose traffic is submissions and nothing else takes
	// neither of these, and submissions are what grow the log the compactor is
	// woken by. scoreSubmit says it too; see noteScoreCompaction, which holds the
	// latch that keeps the three doors to one line per rewrite.
	//
	// The view is the store's own single hold of its lock, so the numbers on the
	// line describe one reading and not three.
	s.noteScoreCompaction(v.Health, v.Total)
	return v
}

// noteScoreCompaction writes the compaction line ONCE for each rewrite that has
// landed since the last time anything looked, and nothing at all otherwise.
//
// THREE DOORS REACH IT, and they are not interchangeable: scoreView and
// scoreExplain, which is every brief and every score.list / score.status, and
// scoreSubmit. The last is not a read at all and is there because the compactor
// is woken by growth: the only verb that grows the log is the one that must be
// able to say so, or a daemon that submits and never reads compacts in silence.
//
// Once per COMPACTION rather than once per changed number, which is why it
// watches score.Health.Compactions and not Compacted. Compacted is a description
// of the last rewrite, so watching it for a change asks the wrong question twice
// over: two rewrites that left the store the same shape carry the same count and
// the second would go unannounced, and a store that never compacts again holds a
// number that stopped changing rather than one that says it is spent.
//
// The latch starts at whatever the store had already done when the server was
// handed it (WithScore), which is what keeps a BOOT compaction to exactly one
// line: cmd/baton has already written it from the same producer by the time any
// connection exists, and the first read must not write it again.
//
// It is an atomic because every connection's command loop reaches this, and the
// swap is a compare so two callers racing across one rewrite produce one line
// rather than two — which is what lets the write path share the latch with the
// two reads instead of needing one of its own. Two rewrites landing between two
// calls produce one line as well, and that is honest rather than a gap: the line
// describes the log that is on disk, which is the later of them, and there is
// nothing left to say about the earlier one.
func (s *Server) noteScoreCompaction(h score.Health, entries int) {
	landed := int64(h.Compactions)
	for {
		seen := s.scoreState.compactions.Load()
		if landed <= seen {
			return
		}
		if s.scoreState.compactions.CompareAndSwap(seen, landed) {
			break
		}
	}
	ScoreCompaction(s.scoreState.Store.Dir(), entries, h)
}

// ScoreCompaction writes the one line that says a compaction happened, for both
// the boots that run one (cmd/baton) and the running daemon that now does too.
//
// It is the SINGLE producer, for the reason ScoreCounters and ScoreFolds are:
// the boot had this sentence hand-rolled inside logScoreBoot, and #56's runtime
// rewrite needed the same one said at a second site. Two copies of a warning
// this specific drift, and the operator then meets one concept under two
// wordings — the failure logScoreFolds was written to stop.
//
// IT IS A WARNING, which is the odd part, because what it announces is not a
// failure but a change nobody asked for. Compaction re-spaces recency: the ORDER
// of the live entries survives the rewrite and the SPACING does not, so a
// panel's working set can come back ordered differently with nothing submitted
// and no config touched — measured at boot against a non-compacting twin that
// restarted byte-identical, and the same re-spacing is what the store's own
// compactor now does with no restart at all. `compacted=310` alone connects none
// of that to what the agents then see, and an operator watching their fleet
// change its mind deserves the one line that explains it. log_before and
// log_after ride along because nothing else the
// daemon says names the growth compaction exists to bound; see
// score.Health.LogBefore for why the record count beside them is not that
// number.
//
// The wording says "than before" rather than "than before this restart" because
// there may not have been one. The re-spacing is the same event either way, and
// a boot's line has the boot's own counters beside it to place it.
func ScoreCompaction(dir string, entries int, h score.Health) {
	log.Warn().Str("dir", dir).Int("compacted", h.Compacted).Int("entries", entries).
		Int64("log_before", h.LogBefore).Int64("log_after", h.LogAfter).
		Msg("score compaction rewrote the log; entry order is preserved but recency spacing is not, " +
			"so a panel's working set may be ordered differently than before")
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
// (oversized) plus the counters that each say the store hit something it had to
// tolerate: a torn log line, a log rewrite it could not make, a repeat it
// declined to count, a fold record it dropped, an alias it evicted, a tier
// record it refused. None of them is an error, and none is visible any other
// way.
//
// `compacted` is the odd one and belongs on the boot line above all: it is how
// many records the LAST compaction rewrote the event log to, and 0 when none has
// run. Said that way rather than "the boot's", because Health is a package-level
// type and compaction being boot-only is a restriction it is meant to outlive,
// which under #56 it now has. `compactions` beside it is how many rewrites there
// have BEEN, which is the half that says whether the one described here is the
// boot's or something the daemon has done since. A log that shrank by two
// orders of magnitude between two boots is a thing an operator should read about
// rather than discover, and the daemon is the only party that can say it
// happened.
//
// It is not the only reporter of the eviction counter any more. A conductor's
// correction produces no Delta at all, so it never reaches this line; scoreRefine
// reports its own evictions, on the action that caused them.
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
		Int("compacted", h.Compacted).
		Int("compactions", h.Compactions).
		Int("compaction_failures", h.CompactionFailures).
		Int("swallowed_repeats", h.SwallowedRepeats).
		Int("unreported_folds", h.UnreportedFolds).
		Int("alias_evictions", h.AliasEvictions).
		Int("rejected_tiers", h.RejectedTiers)
}

// dispatchBrief binds a brief to the panel it is about to be delivered to: the
// panel's own context (group, cwd, profile) read from its row, and the score
// block rendered against that context.
//
// It is the ONE brief builder, and every delivery the WIRE makes comes through
// it: a direct panel.dispatch, each member of a fan-out, a queued task at the
// moment the scheduler drains it onto a panel, and a spawn-on-demand task at the
// held delivery its provisioned panel settles into. That is what makes #44's
// rule true rather than repeated — a queued task carries the same block a direct
// dispatch to that same panel would have carried, because it is the same call.
//
// A plugin-originated dispatch is the exception it has always been: baton.dispatch,
// baton.dispatch_group and a task baton.enqueue queued all deliver the bare prompt
// and never come here.
//
// An id the fleet does not have gets NO BLOCK — #38 §2's standing rule that
// t.score is never rendered for an unknown panel. Every context factor would
// read 1.0 against a panel that is not there, so the block would be the
// contextless ranking dressed as that panel's, and there are now two ways to ask
// for one: a dispatch to an id that never existed, and a delivery whose panel
// left the fleet between the assignment and the write. The caller reports the
// unknown id; this only declines to invent a working set for it.
//
// KNOWN AND NOT COVERED BY THAT: an EXITED panel is still a row in the fleet
// until it is pruned, so it is found here and its stale context is ranked
// against. The write that follows lands on a closed PTY and onPanelExit has
// already failed the task, so nothing reaches an agent — but the block was
// rendered and logged for a dead panel, and the log line is the part an operator
// can see. Narrowing the check to a live panel would be a change to what
// dispatchScored accepts, which is not this issue's to make.
//
// It only READS the memory. A brief the user wrote is one of #38 §4's two
// sources of the user signal, but that is recorded by scoreSignal after the
// dispatch has actually landed — a brief a task.pre hook vetoed, or one that
// failed on an unknown panel id, is not the user telling the fleet anything.
func (s *Server) dispatchBrief(id, prompt string) TaskBrief {
	ctx, found := s.panelContext(id)
	b := TaskBrief{Prompt: prompt, Panel: id, Group: ctx.Group, Cwd: ctx.Cwd, Profile: ctx.Profile}
	if !found {
		return b
	}
	// panelContext has let go of s.mu and the store takes its own lock, so this
	// runs off both; a nil (disabled) store yields the zero view and nothing is
	// injected.
	v := s.scoreView(ctx)
	b.Score = v.Block
	logScoreInjection(ctx, v)
	return b
}

// bindBrief binds prompt to a panel: it builds that panel's brief and runs the
// task.pre chain over it, returning the (possibly rewritten) brief, whether to
// proceed, and how long the whole bind took. It must run with s.mu RELEASED —
// both halves take the lock themselves and the chain blocks on the Lua worker.
//
// It exists because the ORDER is the contract and nothing else states it. The
// brief has to be built first and filtered second: filter-then-bind hands the
// hook a brief with no score and no panel context, which is exactly what #44
// stopped doing, and bind-filter-rebind reads the store twice and hands the
// panel a block the hook has already edited away. Both compile, and both are one
// plausible line each at a site that only wants "bind this prompt to that
// panel". Three sites wanted it, so it is one call.
//
// The elapsed time is returned rather than measured by each caller because it is
// what the two budgets charge, and they were charging different things: a
// monitor tick timed the whole bind while a fan-out timed only the chain. It is
// read off the monitor's clock, so a test advances time inside the hook instead
// of sleeping against it.
func (s *Server) bindBrief(panelID, prompt string) (TaskBrief, bool, time.Duration) {
	started := s.mon.now()
	b, ok := s.filterBrief(s.dispatchBrief(panelID, prompt))
	return b, ok, s.mon.now().Sub(started)
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
// that put them there. It runs wherever a brief is bound, which is now every
// delivery the wire makes rather than a direct dispatch alone: a fan-out member
// and a queued task drained onto a panel leave the same line.
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
	e.Msg("score injected into a delivered brief")
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
//
// The row's own Cwd is therefore read HERE, in the take this already holds, and
// panelCwd is asked only for the case it exists for — a row with no directory
// yet, in a mode that reads the process table. That is the same answer panelCwd
// gives (it returns a known directory unchanged), one lock take and one fleet
// scan cheaper, on a path #44 put on every delivery.
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
	ctx.Cwd = s.panels[idx].Cwd
	track := s.trackCwd
	s.mu.Unlock()
	if ctx.Cwd == "" && track.ReadsProcess() {
		ctx.Cwd = s.panelCwd(id)
	}
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
		// Only the DURABLE WRITE is said out loud, and score.ErrSubmissionText is
		// what tells the two apart.
		//
		// A write that did not land — a full disk, a read-only mount, a directory
		// that went away under a running daemon — is the operator's problem, and
		// the agent that hit it is the only party that was ever told, in a reply
		// it has no reason to keep and every reason to retry past. A fleet can go
		// on submitting into a broken store for as long as it likes with nothing
		// in the daemon log to show for it, and invariant I8 says the operator
		// must not have to read the event log to learn their memory is not
		// working — which is doubly true when the event log is the thing that
		// could not be written.
		//
		// The store's own refusal of the TEXT is not that. It is the submitter's
		// to fix, it needs nobody else told, and it is reachable by any agent that
		// sends spaces — so logging it here would put the operator's broken-disk
		// line under the control of every panel on the fleet. Two hundred blank
		// submissions once produced two hundred warnings about a store that was
		// perfectly healthy.
		//
		// Warn rather than Error for the reason the refine refusals are: the
		// daemon is doing exactly what it should, and the fleet is still running.
		// It is not throttled, and the refine cap does not cover this door: a
		// submitting loop against a broken store is noise, and it is noise that
		// says something true, which is the trade the alternative loses.
		if !errors.Is(err, score.ErrSubmissionText) {
			log.Warn().Err(err).Str("dir", s.scoreState.Store.Dir()).
				Str("source", prov.Source).Str("panel", prov.SourcePanel).
				Msg("score could not record a submission")
		}
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
	// A RUNTIME compaction is announced from here as well as from the read path,
	// and this is the case the read path cannot cover. The compactor is woken by
	// GROWTH, and what grows the log is this verb — so a daemon whose traffic is
	// submissions and nothing else is exactly the daemon that compacts, and it
	// takes neither of scoreLook's two doors. Announced from the reads alone, the
	// rewrite that the submit-only shape drives is the one that says nothing.
	//
	// AFTER the reply, because the reply is what the agent is waiting on and the
	// line is for an operator reading the log afterwards. The rewrite this
	// announces is not the one this submission caused: the compactor runs on its
	// own goroutine, so what is being reported is a rewrite that has already
	// landed, and once per rewrite is what the latch in noteScoreCompaction keeps
	// true across both doors.
	//
	// The two numbers come from two holds of the store's lock here, where
	// scoreLook's come from one. What that costs is that entries may count a
	// submission that landed between them; the line is about the rewrite, and the
	// count beside it is a gauge rather than part of the claim.
	s.noteScoreCompaction(s.scoreState.Store.Health(), s.scoreState.Store.Len())
}

// minRefineGap throttles the conductor's score corrections, the way
// minConductorSpawnGap throttles its panel.create — and for the same reason,
// which is that the thing holding these tools is a loop.
//
// The measured shape: sixty-one merges in nine tenths of a second collapsed a
// sixty-two entry store to a single entry, and there is no undo for that beyond
// reading score-events.jsonl by hand, which is precisely what invariant I8 says
// an operator must never need. It has a second cost too. A merge is NOT cheap,
// and what it costs is written down where it is paid rather than copied here to
// go stale: see score.mergeLocked, which holds the store's mutex for two durable
// appends and a whole-file rewrite. Store.View shares that mutex, so one merge
// can stall a brief being assembled for another panel for as long as one takes.
// Unthrottled, sustained refining pushed dispatch p50 from 2.8 ms to 36 ms for
// the whole fleet; paced by this cap it sits at 2.8 ms.
//
// A quarter second is the same figure the spawn cap uses, chosen the same way:
// far below anything a person doing this deliberately would notice, far above
// what a loop needs to do damage at machine speed. It is a separate constant
// rather than a shared one because the two caps answer to different pressures
// and a later tuning of one must not silently retune the other.
//
// It is a guardrail against agent accidents over a uid-private socket, not a
// budget: it bounds the RATE, never the total. An operator who wants a hundred
// merges gets them, over half a minute, with a log line each. What the rate
// alone cannot see is the patient version of the same accident, which is what
// noteMergeDrop is for.
const minRefineGap = 250 * time.Millisecond

// The slow-collapse alarm: a Warn when merges have taken more than
// mergeAlarmDropPercent of the entries the store held at the start of a
// mergeAlarmWindow. See mergeAlarm.
//
// It is an ALARM and not a budget, deliberately. A cap on the total would refuse
// a legitimate large tidy-up — an operator who has just noticed forty
// near-duplicates is exactly the person this verb exists for — and refusing them
// to catch a loop is the wrong trade. So nothing is blocked; the operator is
// told, which is all invariant I8 asks and all that is needed once the rate cap
// has already made the collapse take minutes rather than seconds.
//
// Half, within a minute, above a floor the store's own tuning sets. Each figure
// earns its place:
//
//   - HALF, because a proportion is what makes this a statement about the memory
//     rather than about a number of merges. A tenth would fire on any ordinary
//     tidy-up of a small store; losing half of what the fleet remembers is not a
//     tidy-up under any reading.
//   - A MINUTE, because it has to be long enough that a conductor working
//     honestly through a list of duplicates at the paced rate does not trip on a
//     handful, and short enough that the operator hears about a collapse while
//     it is happening rather than afterwards.
//
// THE FLOOR IS NOT A CONSTANT HERE, and that is the third figure's whole story.
// It is the store's own working set plus one — the smallest store that does NOT
// fit entirely into a single brief — read off Policy where the alarm is asked
// (scoreRefine). Below it, everything the fleet remembers is already in front of
// every agent on every dispatch, and "half of it" is three entries: merging the
// only pair you have is ordinary, and an alarm that fires on it is one people
// learn to ignore.
//
// It was the literal 8, hand-mirroring internal/score's defaultWorkingSet with a
// cross-package test to keep the mirror honest. The mirror is only right for a
// fleet that never touches score.working-set: set it to twelve — it is operator
// config, and #46 makes it reloadable while the daemon runs — and every store of
// eight to twelve alarmed while fitting whole into every brief, which is the
// "alarm people learn to ignore" the paragraph above argues against. A constant
// that needs a breadcrumb test in another package to stay true is a constant on
// the wrong side of the boundary. Policy is clamped, so the store answers 7
// unset and the operator's own figure once they tune it.
//
// EVERY ONE OF THESE IS PINNED IN BOTH DIRECTIONS, and that sentence is the
// point of this comment rather than a footnote to it. A tuned number is the
// artefact most worth defending, and a test that only proves the mechanism FIRES
// leaves every loosening free: each figure here was argued at length and
// asserted only from the tightening side, so raising the window to an hour,
// dropping the share to a tenth and lowering the floor all passed the suite
// while contradicting the paragraphs above. An argument for a number is not an
// argument until it is an assertion, in the direction the prose is about. See
// TestTheAlarmsFiguresAreWhatTheyClaim for the arithmetic and
// TestTheAlarmFloorIsTheWorkingSet for the floor — which is an assertion about
// what a tuned fleet is told, rather than a mirror checked against a default.
const (
	mergeAlarmWindow      = time.Minute
	mergeAlarmDropPercent = 50
)

// mergeAlarm watches for the patient version of the accident minRefineGap stops:
// not sixty merges in a second, but a conductor quietly merging its way through
// the whole memory at the paced rate, where every individual step looks
// reasonable. Four a second is what the cap allows, so the few hundred entries
// #38 sizes this store at are gone in a minute or two — slow enough that no
// single call looks wrong, fast enough that nobody is going to be reading the
// daemon log while it happens.
//
// It has the shape the throttle has, and for the reason the throttle earned it:
// a small type with a pure, clock-injectable method, so the figures it turns on
// are asserted as arithmetic rather than reconstructed from a live server, a log
// capture and a pacing helper.
//
// at and from are the current window's start and the entry count it opened on;
// fired says that window has already alarmed. The zero value opens its first
// window on the first merge. Guarded by Server.mu.
type mergeAlarm struct {
	at    time.Time
	from  int
	fired bool
}

// note takes one merge — the entry count before it and the count after — and
// answers whether this is the merge that crossed the line, and the count the
// current window opened on, for the log line. floor is the smallest store worth
// alarming about; the caller reads it off the store's policy, and the constants
// above say why it is not one of them.
//
// It fires ONCE per window. The point is to tell an operator that their memory
// is collapsing, and a line per merge after that adds nothing they can act on —
// the per-merge Info lines are already there for anyone reconstructing it. The
// window then rolls and can alarm again, so a collapse that outlasts a minute is
// not reported once and forgotten.
//
// Nothing is refused here. See the constants for why an alarm rather than a
// budget. The caller holds Server.mu.
func (a *mergeAlarm) note(before, after, floor int, now time.Time) (from int, alarm bool) {
	if a.at.IsZero() || now.Sub(a.at) > mergeAlarmWindow {
		// The window opens on the count BEFORE this merge, so the very first merge
		// of a window is measured against a store that still had its entry.
		a.at, a.from, a.fired = now, before, false
	}
	if a.fired || a.from < floor || (a.from-after)*100 < a.from*mergeAlarmDropPercent {
		return a.from, false
	}
	a.fired = true
	return a.from, true
}

// scoreRefine handles the conductor's three corrections — score.merge,
// score.reword and score.lower — behind the gate below. Everything about WHAT
// they do is internal/score's; this is the wire, the gate, the rate cap, and the
// log lines.
//
// Every exit logs, and that symmetry is the point rather than a courtesy. A
// refusal is the interesting event here: fifteen impersonation attempts from a
// worker panel used to leave no trace at all while the successes logged at Info,
// so the daemon was quietest about exactly the thing an operator would want to
// know. Refusals go to Warn — someone is asking for a write surface they do not
// hold, or a loop is running — and successes to Info.
//
// The reply names the entry that was corrected and nothing else. What the
// correction did to it is a question score.list answers with the whole
// breakdown, and inventing a second, thinner shape for it here would be a
// second thing to keep in step with the store.
func (s *Server) scoreRefine(cc *clientConn, cmd proto.Command) {
	if !s.isConductor(cc) {
		// Warn, with what the connection claimed: a panel that declared the
		// conductor role and is not the conductor is either a broken client or the
		// thing the gate exists for, and an operator can act on neither if the
		// refusal is silent. The same reasoning the monotone-hello guard uses.
		log.Warn().Str("action", cmd.Action).Str("entry", cmd.ID).
			Str("self", cc.self).Str("declared_role", cc.role).
			Msg("refused a score correction: this connection is not the conductor panel")
		send(cc, proto.ServerMsg{Type: "error", Error: "score refine: only the conductor may correct the fleet's memory"})
		return
	}
	// A nil store is not always "switched off" — see scoreSubmit, which says the
	// same thing for the same reason.
	if !s.scoreState.available() {
		why := s.scoreState.reason()
		log.Warn().Str("action", cmd.Action).Str("entry", cmd.ID).Str("reason", why).
			Msg("refused a score correction: the store is not available")
		send(cc, proto.ServerMsg{Type: "error", Error: why})
		return
	}
	// Throttled on the PANEL the gate just identified, not on this connection —
	// see Server.refine. The check stamps as it admits, so a caller refused
	// here does not push the next allowed correction further away: a loop gets
	// exactly one through per gap.
	if since, tooSoon := s.tooSoon(&s.refine, cc.self, time.Now()); tooSoon {
		log.Warn().Str("action", cmd.Action).Str("entry", cmd.ID).Str("conductor", cc.self).
			Dur("since_last", since).Msg("refused a score correction: correcting too fast")
		send(cc, proto.ServerMsg{Type: "error",
			Error: "score refine: correcting the memory too fast, slow down"})
		return
	}
	// The operator's own file is folded in FIRST, through the ordinary read path,
	// and the baseline is then read off THAT view. A correction reconciles on the
	// way in like every other mutation, so without this pass the operator's
	// pending edits would be reconciled INSIDE the call and their alias evictions
	// would be counted against the conductor's correction — the line below would
	// name the wrong actor. Running the pass here instead puts those evictions on
	// scoreLook's own reconcile line, beside the Delta that explains them.
	//
	// The baseline comes off the view rather than from two further calls, because
	// it is already there: View.Health is the struct Store.Health returns and
	// View.Total the len(entries) Store.Len counts, both captured in the single
	// hold of the store's lock that produced this view. Re-reading them took that
	// lock twice more on the one path whose own comments measure what holding it
	// costs, and took the baseline from a moment LATER than the reconcile it is
	// supposed to be the baseline for.
	//
	// It costs a second score.md pass on a verb the cap already limits to four a
	// second. What it does not close is a save landing in the microseconds
	// between this pass and the correction's own; that one still lands here, and
	// is the same window every read-reconcile-write path in the store carries.
	//
	// The eviction gauge is read either side of the call, because this is one of
	// the two paths that generate the most alias evictions and the only pair that
	// could not report any: ScoreCounters rides a reconcile Delta, and a
	// conductor's correction produces none. An evicted alias is a wording that
	// will no longer fold, so the entry it would have joined gains a twin
	// instead — the operator is entitled to see the store choosing to remember
	// less, on the action that chose it.
	//
	// The entry count is taken from the same view for the same kind of reason:
	// only a merge can take an entry out, and the alarm needs the count from
	// before it did.
	v := s.scoreView(score.Context{})
	evictions, entries := v.Health.AliasEvictions, v.Total
	// One switch, and it is the wire's: it maps the three actions onto the three
	// store methods and names each for the log and the reply. The store takes
	// three different arguments for the three verbs — a second entry, a wording,
	// nothing — so there is no shared call to hoist out of here, which is exactly
	// why internal/score stopped offering one.
	var op, arg string
	var err error
	switch cmd.Action {
	case "score.merge":
		op, arg = "merge", cmd.From
		err = s.scoreState.Store.Merge(cmd.ID, cmd.From)
	case "score.reword":
		op, arg = "reword", cmd.Prompt
		err = s.scoreState.Store.Reword(cmd.ID, cmd.Prompt)
	case "score.lower":
		op = "lower"
		err = s.scoreState.Store.Lower(cmd.ID)
	}
	if err != nil {
		log.Warn().Err(err).Str("op", op).Str("entry", cmd.ID).Str("conductor", cc.self).
			Msg("the conductor's correction was refused")
		send(cc, proto.ServerMsg{Type: "error", Error: err.Error()})
		return
	}
	// Said out loud, because a refine changes what the operator already has on
	// nobody's initiative but an agent's: a line in their own file now reads
	// differently, or is gone, or an entry sits a rung lower with nothing in the
	// file to show for it. Every other mutation announces itself — a submission
	// is a new line, and a fold gets its own line through logScoreFolds — and
	// #38's invariant I8 is that they must not have to read the event log to find
	// out why theirs changed under them.
	log.Info().Str("op", op).Str("entry", cmd.ID).Str("conductor", cc.self).
		Str("arg", arg).Int("alias_evictions", s.scoreState.Store.Health().AliasEvictions-evictions).
		Msg("the conductor corrected the fleet's memory")
	if op == "merge" {
		// The floor is the store's OWN working set plus one — the smallest store
		// that does not fit entirely into one brief — taken from the policy in
		// force on the view above rather than from a constant mirroring the
		// package default, so a fleet that tuned score.working-set gets the alarm
		// its own briefs justify. See mergeAlarmWindow's comment.
		after := s.scoreState.Store.Len()
		if from, alarm := s.noteMergeDrop(entries, after, v.Policy.WorkingSet+1, time.Now()); alarm {
			log.Warn().Str("conductor", cc.self).Int("entries", after).
				Int("was", from).Dur("within", mergeAlarmWindow).
				Msg("merging has taken more than half the fleet's memory; check score.md is still what you meant")
		}
	}
	send(cc, proto.ServerMsg{Type: "score", Score: scoreJSON(struct {
		Id string `json:"id"`
		Op string `json:"op"`
	}{Id: cmd.ID, Op: op})})
}

// noteMergeDrop takes Server.mu around one alarm check, the way tooSoon does
// around one throttle check and for the same reason: both run off the command
// loop, where the lock is not already held. See mergeAlarm.note, which is where
// the rule lives.
func (s *Server) noteMergeDrop(before, after, floor int, now time.Time) (from int, alarm bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.merges.note(before, after, floor, now)
}

// isConductor reports whether cc is the connection of the panel THIS SERVER
// marked Conductor. It is the gate on the refine verbs and the daemon's first
// conductor-only surface.
//
// It compares cc.self against the fleet rather than reading cc.role, and the
// difference is the whole of the check. #38 §4's rule — identified by the
// connection, never by a claim — was written about the user's signal, and this
// is the same rule applied to the second question the daemon now has to answer.
//
// guardConductor cannot serve here, and not merely because it is elsewhere: it
// is a DENY list. It returns "allowed" for every connection that did not declare
// the role and only refuses specific actions TO one that did, so declaring
// `role: conductor` has until now only ever ADDED refusals. That is exactly why
// R4's monotone hello lets a connection declare it from empty. Reserve something
// FOR the role on top of that reasoning and it inverts: any agent panel could
// declare itself the conductor and take merge, reword and lower over the whole
// fleet's memory. So the role is not consulted at all.
//
// The server already knows the answer without asking. A conductor panel is
// created with the Conductor flag and the singleton is maintained
// (hasConductorLocked), so the question is whether the panel this connection
// declared as its own is that panel. A connection with no self — the TUI, or
// `baton ctl` from an ordinary shell — is not a panel and is refused with
// everything else; the operator's surface on the score is their own editor
// (#38 §3), not a verb.
//
// It also asks whether that panel is still ALIVE. A conductor whose process has
// exited leaves its row in the fleet until the operator purges it — that is what
// makes a respawn keep the id — so a flag check alone would leave the write
// surface open for as long as the dead slot sits there, on a connection whose
// agent is gone. Nothing in the threat model turns on it, and it is not what a
// reader of the line above would expect either. panel.Exited is the one state
// that means the process is not there; every other state is a panel that is
// starting, working, or waiting, and all of them are the conductor.
//
// It is worth being exact about what this is and is not, in the same voice
// connProvenance is. cc.self is still self-declared and validated against
// nothing: an agent panel that learns the conductor's id and greets with it
// passes this gate. What the check removes is the case that needs no knowledge
// at all — declaring a ROLE, which every panel can do from empty and which no
// id is required for — and that is the difference between a fence with a latch
// and a fence with a hole in it. #38's Trust and exposure section already says
// Score does not claim to be a boundary against a hostile agent with the
// operator's own uid.
func (s *Server) isConductor(cc *clientConn) bool {
	if cc.self == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.conductorLocked()
	return ok && s.panels[i].ID == cc.self && s.panels[i].State != panel.Exited
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
