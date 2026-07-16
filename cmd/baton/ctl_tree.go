package main

import (
	"encoding/json"
	"fmt"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/proctree"
)

// ctlTree prints the daemon's process tree: the baton daemon at the root, the
// fleet's nested work-item groups as scaffolding, each panel filed under its group
// with its process-group-leader pid, and every panel's live OS descendant
// processes hanging off it. It answers "what is this daemon actually running?" —
// the panels baton knows about, joined to the real OS processes they spawned. The
// tree is built in internal/proctree, shared with the cockpit overlay. --json
// emits the same tree as structured JSON.
type ctlTree struct {
	JSON bool `help:"Emit the tree as JSON instead of drawing it."`
}

func (t ctlTree) Run(c *control.Client) error {
	panels, err := c.List()
	if err != nil {
		return err
	}
	children, comm, err := proctree.OSProcessTable()
	if err != nil {
		return fmt.Errorf("read OS process table: %w", err)
	}
	root := proctree.Build(proctree.DaemonPid(), panels, children, comm)

	if t.JSON {
		out, err := json.MarshalIndent(root, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}
	fmt.Print(proctree.Render(root))
	return nil
}
