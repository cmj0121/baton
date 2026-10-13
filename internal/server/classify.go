package server

import (
	"context"
	"hash/fnv"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/classifier"
	"github.com/cmj0121/baton/internal/panel"
)

// classQueueCap bounds the ask queue. The Monitor enqueues at most one request
// per quiet panel per settling tick, so sixty-four is a fleet's worth of
// questions in flight; past that a request is dropped and the regex decides,
// which is the whole of D5 — the tick never waits on the socket, and a slow
// classifier degrades to today's behaviour rather than to a stalled fleet.
const classQueueCap = 64

// classReq is one question for the classifier: which panel asked, the hash of
// the tail it was asked about, and that tail's raw bytes (a private copy, as
// ptymgr.Tail returns them). The hash is what makes the answer safe to apply a
// tick later (R3): a panel that has printed anything since no longer holds the
// tail the answer is about.
type classReq struct {
	id   string
	hash uint64
	tail []byte
}

// newClassifier turns a resolved config block into a live client, or nil when
// there is none. The client logs its one line per failure kind through the
// daemon's logger at warn, the level a missing optional dependency deserves.
func newClassifier(cfg *classifier.Config) *classifier.Client {
	if cfg == nil {
		return nil
	}
	return classifier.New(*cfg, func(format string, args ...any) { log.Warn().Msgf(format, args...) })
}

// classifyLoop is the one worker behind the ask queue: it drains classQ until
// stop closes, asking the client of the moment (a reload swaps it), and records
// a confident attention as a hit for the Monitor to apply on its next tick. No
// deadline is set here: the client's own exchange bounds dial, write and read
// with the configured timeout. It logs nothing — a failure is the client's to
// say once, and a "no" is not news.
//
// The tail is shaped here, off the lock, by rowText — the same function that
// writes the rows a bundle is trained from, so the classifier is asked in the
// form it learned.
func (s *Server) classifyLoop(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case req := <-s.classQ:
			s.mu.Lock()
			c := s.classifier
			s.mu.Unlock()
			if c == nil {
				continue
			}
			ans, err := c.Ask(context.Background(), rowText(req.tail))
			if err != nil {
				// Nobody answered, so the tail was not asked: forget it was, or
				// a panel that settled while mold was down would never be asked
				// again once it is back — and a screen sitting on `Password:`
				// is exactly a tail that does not change.
				s.mu.Lock()
				if s.classAsked[req.id] == req.hash {
					delete(s.classAsked, req.id)
				}
				s.mu.Unlock()
				continue
			}
			if !ans.Attention {
				continue
			}
			s.mu.Lock()
			if s.classHit == nil {
				s.classHit = make(map[string]uint64)
			}
			s.classHit[req.id] = req.hash
			s.mu.Unlock()
		}
	}
}

// enqueueClassLocked hands one quiet panel's tail to the worker, once per
// distinct tail: the same screen asked twice would get the same answer and cost
// a dial for it. A full queue drops the request and does NOT record the tail
// as asked — the regex has already decided this tick, and the next settle on
// the same screen gets another turn at the queue. Only a request that was
// handed over is remembered; the worker forgets it again if nobody answered.
// Caller holds s.mu.
func (s *Server) enqueueClassLocked(id string, hash uint64, tail []byte) {
	if s.classAsked[id] == hash {
		return
	}
	select {
	case s.classQ <- classReq{id: id, hash: hash, tail: tail}:
		if s.classAsked == nil {
			s.classAsked = make(map[string]uint64)
		}
		s.classAsked[id] = hash
	default:
	}
}

// classLiveLocked reports whether there is a classifier worth asking: one is
// configured and it has not been found absent. Caller holds s.mu.
func (s *Server) classLiveLocked() bool { return s.classifier != nil && !s.classifier.Absent() }

// classAskedLocked reports whether the classifier has been asked about SOME
// screen of this panel since it last woke — any hash, not a particular one;
// the per-screen check is enqueueClassLocked's. Caller holds s.mu.
func (s *Server) classAskedLocked(id string) bool {
	_, ok := s.classAsked[id]
	return ok
}

// forgetAskedLocked drops every "already asked" memory, so each quiet panel is
// asked once more about the screen it holds. It is what a re-armed classifier
// needs — a fresh client on reload, or a status probe that found mold alive —
// since the asks that failed while it was absent were forgotten one by one,
// but the ones dropped at a full queue or answered "no" by a stale bundle were
// not. Caller holds s.mu.
func (s *Server) forgetAskedLocked() { s.classAsked = nil }

// takeClassHitLocked consumes a pending classifier hit for a panel and reports
// whether it still applies: the tail is read once more and the answer counts
// only when the bytes it was asked about are the bytes on screen now (R3). The
// hit is dropped either way — a stale one is wrong, and a fresh one is spent by
// the transition it raises. Caller holds s.mu.
func (s *Server) takeClassHitLocked(id string) bool {
	h, ok := s.classHit[id]
	if !ok {
		return false
	}
	delete(s.classHit, id)
	return fnv64(s.pty.Tail(id, attnTailBytes)) == h
}

// noteRaisedLocked records who raised a panel's hand when the ladder moves it
// into attention without a declaration — the classifier, or the tail regex —
// and forgets that the moment it moves anywhere else. A declaration supersedes
// a guess, so a move into attention the agent declared forgets any guess too.
// The inbox prints the source (D10) so an operator dismissing a wrong guess
// knows which guess it was, and the dismiss's negative row reads the same
// sourceLocked, so "who raised it" has one definition. Caller holds s.mu.
func (s *Server) noteRaisedLocked(id string, ns panel.State, sig stateSignals) {
	if ns != panel.Attention || sig.declared {
		delete(s.raisedBy, id)
		return
	}
	if s.raisedBy == nil {
		s.raisedBy = make(map[string]string)
	}
	if sig.classAtt {
		s.raisedBy[id] = "classifier"
	} else {
		s.raisedBy[id] = "tail"
	}
}

// forgetClassLocked drops every classifier bookkeeping entry for a panel: the
// tail it was last asked about, any answer waiting to be applied, and who
// raised it. Called wherever the panel's declaration is dropped. Caller holds
// s.mu.
func (s *Server) forgetClassLocked(id string) {
	delete(s.classAsked, id)
	delete(s.classHit, id)
	delete(s.raisedBy, id)
}

// sourceLocked is what the wire says about why a panel is in attention:
// "declared" when the agent's own declaration stands, else who raised it, else
// nothing. Only an attention panel has a source — the entry is gone the tick
// the panel leaves. Caller holds s.mu.
func (s *Server) sourceLocked(p panel.Panel) string {
	if p.State != panel.Attention {
		return ""
	}
	if s.declaredLocked(p.ID) {
		return "declared"
	}
	return s.raisedBy[p.ID]
}

// fnv64 hashes a tail for the equality the classifier path needs — "is this
// the same screen I asked about?" — and nothing more. FNV-1a, for the reason
// panelSig gives: nothing here is a secret, and a collision costs one answer
// applied to a screen that changed, once.
func fnv64(b []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return h.Sum64()
}
