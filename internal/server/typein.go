package server

import (
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/agents"
)

// defaultSubmitGap is how long an agent panel is given between the text and the
// Enter that submits it, so a TUI does not read the pair as one paste. Best
// effort: the PTY write returns before the TUI reads, so the gap is a head start
// for the reader, not a confirmation that it consumed the text. Not a config
// key — a knob nobody can reason about would only be set wrong.
const defaultSubmitGap = 50 * time.Millisecond

// inputLanes serializes typed input per panel: one lane per panel id,
// refcounted, deleted when its last holder releases it (no leak, no lifecycle).
//
// The lane exists because a split write is two writes with a sleep between them.
// Two senders typing into one panel at once would otherwise interleave as
// text1 text2 submit1 submit2 — one submitted line holding both messages and a
// stray Enter after it. Holding the lane across text, gap and submit makes each
// line land whole. A lane is created on demand and dropped as soon as nobody
// holds or waits for it, so a panel that closes needs no teardown here.
type inputLanes struct {
	mu    sync.Mutex
	lanes map[string]*inputLane
}

// inputLane is one panel's lock plus the count of goroutines holding or waiting
// on it; refs is guarded by inputLanes.mu, not by the lane's own mu.
type inputLane struct {
	mu   sync.Mutex
	refs int
}

// acquire blocks until the caller holds panel id's lane and returns the func that
// releases it. The refcount is taken before blocking on the lane, so a waiter
// keeps the entry alive and the holder's release never deletes a lane somebody
// is still queued on.
func (l *inputLanes) acquire(id string) (release func()) {
	l.mu.Lock()
	if l.lanes == nil {
		l.lanes = make(map[string]*inputLane)
	}
	ln := l.lanes[id]
	if ln == nil {
		ln = &inputLane{}
		l.lanes[id] = ln
	}
	ln.refs++
	l.mu.Unlock()

	ln.mu.Lock()
	return func() {
		ln.mu.Unlock()
		l.mu.Lock()
		ln.refs--
		if ln.refs == 0 {
			delete(l.lanes, id)
		}
		l.mu.Unlock()
	}
}

// submitFor resolves the sequence that submits a line typed into panel id and
// whether it is an agent: explicit wins, then the panel's profile's configured
// submit (s.agentSubmit), then the built-in default for its command
// (agents.SubmitFor), then defaultSubmit. Takes s.mu itself; callers must NOT
// hold it. Profile and command are read from the spawn spec (s.specs), not the
// panel record: the server fills panel.Panel.Profile only on the wire snapshot,
// and the spec keeps the pre-isolation command, so a containerised grok, a
// profile-less `spawn --agent grok` and a restored panel all still submit right.
//
// An id that is not in the fleet resolves to defaultSubmit and not-an-agent, so
// a line sent to a panel that just closed is written the old way, in one piece —
// there is no process left for the gap to protect.
func (s *Server) submitFor(id, explicit string) (submit string, agent bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexLocked(id)
	if i < 0 {
		if explicit != "" {
			return explicit, false
		}
		return defaultSubmit, false
	}
	p := s.panels[i]
	spec := s.specs[id]
	switch {
	case explicit != "":
		submit = explicit
	case s.agentSubmit[spec.Profile] != "":
		submit = s.agentSubmit[spec.Profile]
	case agents.SubmitFor(spec.Command) != "":
		submit = agents.SubmitFor(spec.Command)
	default:
		submit = defaultSubmit
	}
	return submit, p.IsAgent()
}

// typeLine types text and then submit into panel id under its input lane.
// A non-agent gets ONE write, text+submit, byte-for-byte what it got before.
// An agent gets the text, then waits s.submitGap, then — only if the panel is
// still running the process it was running before the text — the submit as its
// own write. Returns false when the submit was dropped because the panel's
// process ended or changed during the gap.
//
// An agent whose program has turned on bracketed paste gets its text wrapped as
// one paste (see asPaste), and the gap and submit follow as before. That is what
// tells a TUI with paste-burst detection — codex reads an Enter that arrives just
// after a fast run of typed bytes as a newline inside the burst — that the text
// is over and the Enter is a key of its own; without the markers a long message
// sat in the input line, typed and never sent. An agent that turned the mode off,
// or has not been heard to set it, gets the raw text it always did, since a
// program that never asked for the markers would take them for input.
//
// The pid check guards against an Enter landing in the wrong process: a panel
// restarted during the gap has a fresh agent that never saw the text, and an
// Enter there submits whatever that agent had in its own input line. So the
// submit is written only when the pid read after the gap equals the one read
// before the text — any change drops it, including 0 to a live pid (a panel
// that had exited and was restarted inside the gap) and a live pid to 0 (the
// agent exited). A pid that is 0 on both reads is the one unchanged case that is
// not a live process: a panel the server cannot see (a test fake, or a panel the
// PTY manager no longer tracks, in which case the writes go nowhere anyway). It
// is treated as "cannot check" and the submit is written, because dropping it
// would turn every such panel into one that is typed into and never submitted —
// the very failure the split exists to end.
//
// It blocks for the gap; a caller that must not (the monitor tick) uses
// typeLineThen.
func (s *Server) typeLine(id string, agent bool, text []byte, submit string) bool {
	release := s.lanes.acquire(id)
	defer release()

	if !agent {
		data := make([]byte, 0, len(text)+len(submit))
		data = append(append(data, text...), submit...)
		s.writeInput(id, data)
		return true
	}

	paste, pid := s.pasteModeAndPid(id)
	if paste {
		text = asPaste(text)
	}
	s.writeInput(id, text)
	if s.submitGap > 0 {
		time.Sleep(s.submitGap)
	}
	if now := s.pidOf(id); now != pid {
		log.Warn().Str("panel", id).Int("pid", pid).Int("now", now).Msg("panel process changed while a line was typed; submit dropped")
		return false
	}
	s.writeInput(id, []byte(submit))
	return true
}

// typeLineThen runs typeLine and then done(ok), on its own goroutine tracked by
// s.typing when the line has a gap to wait out (agent and submitGap > 0); inline
// otherwise — a zero gap has nothing to hand off. Used by the monitor tick's
// deliver so the tick never sleeps, and by dispatchScored so a plugin's
// dispatch never holds the Lua worker for the gap. Shutdown waits on s.typing, so a line
// already handed off is finished, not cut between its text and its Enter.
//
// The hand-off is registered under s.mu, checked against shuttingDown: the
// tick runs concurrently with the signal goroutine's Shutdown, and a
// WaitGroup.Add racing its Wait is a runtime panic at worst and, at best, a
// line started after the wait that KillAll then cuts in half. Shutdown sets
// shuttingDown under the same lock before it waits, so every Add either lands
// before the Wait or is refused here, and a refused line reports done(false)
// without typing anything. Callers must NOT hold s.mu.
func (s *Server) typeLineThen(id string, agent bool, text []byte, submit string, done func(ok bool)) {
	if !agent || s.submitGap <= 0 {
		done(s.typeLine(id, agent, text, submit))
		return
	}
	s.mu.Lock()
	if s.shuttingDown {
		s.mu.Unlock()
		done(false)
		return
	}
	s.typing.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.typing.Done()
		done(s.typeLine(id, agent, text, submit))
	}()
}
