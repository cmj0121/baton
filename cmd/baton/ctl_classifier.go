package main

import (
	"fmt"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/proto"
)

// ctlClassifier groups the MOLD attention-classifier verbs under
// `baton ctl classifier …`. There is one: status, which is also the way to
// re-arm a classifier the daemon gave up on, since the probe it runs clears the
// absence latch when the socket answers.
type ctlClassifier struct {
	Status ctlClassifierStatus `cmd:"" help:"Print whether the attention classifier runs, what its socket says, and how many training rows it has recorded. Probing re-arms a classifier the daemon had written off."`
}

type ctlClassifierStatus struct{}

// Run prints one line: the situation, and the facts that bear on it.
func (ctlClassifierStatus) Run(c *control.Client) error {
	st, err := c.ClassifierStatus()
	if err != nil {
		return err
	}
	fmt.Println(classifierLine(st))
	return nil
}

// classifierLine renders a status as the one line the operator reads. Three
// situations, three shapes: off (nothing configured), unreachable (configured,
// the probe failed — the regex is deciding alone), and on (mold answered, so
// here is what it said beside what baton asks of it).
func classifierLine(st *proto.ClassifierStatus) string {
	if !st.Enabled {
		return "classifier: off"
	}
	if st.Mold == nil {
		return fmt.Sprintf("classifier: unreachable — %s (regex decides)", st.Error)
	}
	line := fmt.Sprintf("classifier: on — mold %s pid %d, bundle %s, %d keys, %s; scope %s ≥ %.2f; rows %d, holdout %d",
		st.Mold.Version, st.Mold.PID, st.Mold.Bundle, st.Mold.Keys, st.Mold.Device,
		st.Scope, st.MinConfidence, st.Rows, st.Holdout)
	if st.Capped {
		line += " [capped]"
	}
	if !st.Record {
		line += " record off"
	}
	return line
}
