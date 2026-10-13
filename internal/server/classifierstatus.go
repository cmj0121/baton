package server

import (
	"context"

	"github.com/cmj0121/baton/internal/proto"
)

// classifierStatus answers classifier.status: it reads the client and the
// record flag under the lock, lets go of it, and only then probes the socket.
// The probe dials with the client's own timeout, and a dial under s.mu would
// hold every tick and every command behind a socket that may be two seconds
// from answering — so the snapshot and the probe are two steps on purpose.
//
// Absent is read before the probe. A successful Status clears the latch, so
// reading it after would always say "no" on the very call that re-armed the
// client, and the operator would never learn their fleet had been running on
// the regex alone until that moment.
func (s *Server) classifierStatus(cc *clientConn) {
	s.mu.Lock()
	client, record := s.classifier, s.classifierRecord
	s.mu.Unlock()

	st := proto.ClassifierStatus{Record: record}
	st.Rows, st.Holdout, st.Capped = rowCounts()
	if client == nil {
		send(cc, proto.ServerMsg{Type: "classifier", Classifier: &st})
		return
	}

	cfg := client.Config()
	st.Enabled = true
	st.Socket, st.Scope, st.MinConfidence = cfg.Socket, cfg.Scope, cfg.MinConfidence
	st.Absent = client.Absent()

	mold, err := client.Status(context.Background())
	if err != nil {
		st.Error = err.Error()
	} else {
		// The conversion is the compile-time check that the two types stay in step.
		m := proto.MoldStatus(mold)
		st.Mold = &m
		// The probe is also the re-arm (D9): the quiet screens that went
		// unasked while mold was away get asked on their next tick.
		s.mu.Lock()
		s.forgetAskedLocked()
		s.mu.Unlock()
	}
	send(cc, proto.ServerMsg{Type: "classifier", Classifier: &st})
}
