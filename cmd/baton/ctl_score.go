package main

import (
	"fmt"

	"github.com/cmj0121/baton/internal/control"
)

// ctlScore groups the fleet-memory verbs (#39) under `baton ctl score …`. The
// memory records how this fleet behaves — submitted freely, weighed by
// recurrence — and these are its walking-skeleton surfaces: submit a note, list
// what stands, and see whether the subsystem runs at all.
type ctlScore struct {
	Submit ctlScoreSubmit `cmd:"" help:"Record one short observation about how this fleet behaves."`
	List   ctlScoreList   `cmd:"" help:"Print every recorded entry as JSON."`
	Status ctlScoreStatus `cmd:"" help:"Print whether the memory runs, how many entries it holds, and where."`
}

type ctlScoreSubmit struct {
	Text string `arg:"" help:"The observation to record — one short sentence."`
}

// Run prints the id the note landed in, and says so when the store recognised
// the text as a repeat of one it already holds. The id stays the first token, so
// a script reading it with `cut` or `read` is unaffected, but an operator
// submitting the same observation four times can now see that three of them were
// counted rather than recorded — which is the whole of what the memory did.
func (x ctlScoreSubmit) Run(c *control.Client) error {
	id, folded, err := c.ScoreSubmit(x.Text)
	if err != nil {
		return err
	}
	if folded {
		fmt.Println(id, "(folded into an entry the fleet already remembers)")
		return nil
	}
	fmt.Println(id)
	return nil
}

type ctlScoreList struct{}

func (ctlScoreList) Run(c *control.Client) error {
	out, err := c.ScoreList()
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

type ctlScoreStatus struct{}

func (ctlScoreStatus) Run(c *control.Client) error {
	out, err := c.ScoreStatus()
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}
