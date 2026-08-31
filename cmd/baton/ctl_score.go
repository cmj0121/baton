package main

import (
	"fmt"

	"github.com/cmj0121/baton/internal/control"
)

// ctlScore groups the fleet-memory verbs (#39) under `baton ctl score …`. The
// memory records how this fleet behaves — submitted freely, weighed by
// recurrence, ranked for injection — and these are its surfaces: submit a note,
// see every entry with the arithmetic that ranks it, and see whether the
// subsystem runs at all.
type ctlScore struct {
	Submit ctlScoreSubmit `cmd:"" help:"Record one short observation about how this fleet behaves."`
	List   ctlScoreList   `cmd:"" help:"Print every entry as JSON — tier, rank, factor breakdown, whether the brief carries it and why not, and the context they were ranked against."`
	Status ctlScoreStatus `cmd:"" help:"Print whether the memory runs, how many entries it holds, its tuning, and where."`
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

type ctlScoreList struct {
	Panel string `arg:"" optional:"" help:"Rank for this panel: its directory, profile and group are what the context dimensions match. Omitted, nothing is matched against and every context factor is 1.0."`
}

// Run prints the whole store, not the few entries a brief carries: every entry
// in rank order, each with its tier, its rank, the five factors that multiply
// out to that rank, and whether the working set took it — or, when it did not,
// which of the three caps left it out. One cap, even where two of them apply:
// score.Standing names the one the operator can act on, so an entry past the
// count budget reads below-budget whether or not the rune backstop had also
// closed.
//
// It stays one line of JSON, because the reader is as often a `jq` filter as a
// person — `jq '.entries[] | select(.active)'` is the working set,
// `jq '.entries[] | select(.standing == "block-full")'` is what the injected
// block had no room for, and `jq '.entries[] | select(.tier > 1)'` is what the
// fleet has learned to take care over. The answer used to stop at the seventh
// entry, which meant a store past seven had entries whose tier appeared in NO
// surface at all (#42); the breakdown is what turns "why is this in my brief"
// from a number into a reason, and the standing does the same for "why is this
// one not", which has three answers and had none.
//
// Name a panel and the ranking runs against ITS directory, profile and group,
// which is the question an operator actually has: not "what does the store
// think" but "why is this in the brief that panel gets". Name none — the
// cockpit's own view — and nothing is matched against, so every context factor
// reads 1.0 and the order is tier and recency alone.
//
// Either way the reply's `context` says which of the two happened, because
// `active` reads exactly like "this is in the brief" and a contextless `active`
// set is one no real panel dispatch produces. A column of 1.0s alone would say
// "nothing matched" when the truth is "nothing could match". See the server's
// scoreList.
func (x ctlScoreList) Run(c *control.Client) error {
	out, err := c.ScoreList(x.Panel)
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
