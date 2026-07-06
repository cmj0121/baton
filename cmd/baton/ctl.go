package main

import (
	"fmt"
	"os"

	"github.com/alecthomas/kong"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/proto"
)

// ctlCLI is the `baton ctl` control surface: a thin client over the session's
// control socket that drives the same fleet the cockpit shows. Run from a plain
// shell it acts with the full-power cockpit role; run inside a conductor panel
// (where baton injects BATON_ROLE/BATON_PANEL_ID) the server fences it.
type ctlCLI struct {
	List          ctlList          `cmd:"" help:"Print the fleet as JSON."`
	Spawn         ctlSpawn         `cmd:"" help:"Spawn a panel and print its id."`
	Close         ctlClose         `cmd:"" help:"Close panels by id."`
	Group         ctlGroup         `cmd:"" help:"Group panels under a work item."`
	Rename        ctlRename        `cmd:"" help:"Rename a panel or a group."`
	Pin           ctlPin           `cmd:"" help:"Pin panels to live tiles in their group split."`
	Unpin         ctlUnpin         `cmd:"" help:"Unpin panels."`
	Signal        ctlSignal        `cmd:"" help:"Send a signal to panels."`
	Send          ctlSend          `cmd:"" help:"Send text (a prompt) to a panel."`
	Dispatch      ctlDispatch      `cmd:"" help:"Assign a task brief to a panel and deliver it as a unit."`
	DispatchGroup ctlDispatchGroup `cmd:"" name:"dispatch-group" help:"Dispatch one task to every member of a work item."`
	Queue         ctlQueue         `cmd:"" help:"Manage the task backlog (add/list/cancel/drain)."`
}

// ctlMain parses and runs `baton ctl <command>`. It is kept separate from the
// cockpit entry so the default `baton` (attach) path is untouched. It returns a
// process exit code.
func ctlMain(args []string) int {
	var cli ctlCLI
	parser, err := kong.New(&cli,
		kong.Name("baton ctl"),
		kong.Description("Drive the baton fleet over the control socket."),
		kong.UsageOnError(),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "baton ctl:", err)
		return 2
	}
	kctx, err := parser.Parse(args)
	if err != nil {
		parser.FatalIfErrorf(err) // prints usage and exits non-zero
	}

	c, err := control.Dial()
	if err != nil {
		fmt.Fprintln(os.Stderr, "baton ctl:", err)
		return 1
	}
	defer func() { _ = c.Close() }()

	if err := kctx.Run(c); err != nil {
		fmt.Fprintln(os.Stderr, "baton ctl:", err)
		return 1
	}
	return 0
}

type ctlList struct{}

func (ctlList) Run(c *control.Client) error {
	out, err := c.ListJSON()
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

type ctlSpawn struct {
	Agent string   `help:"Agent profile command to run, e.g. claude. Omit for a shell panel."`
	Arg   []string `help:"Argument passed to the agent command (repeatable)."`
	Dir   string   `help:"Working directory the panel runs in."`
}

func (s ctlSpawn) Run(c *control.Client) error {
	id, err := c.SpawnPanel(s.Agent, s.Arg, s.Dir)
	if err != nil {
		return err
	}
	fmt.Println(id)
	return nil
}

type ctlClose struct {
	IDs []string `arg:"" name:"id" help:"Panel ids to close."`
}

func (x ctlClose) Run(c *control.Client) error {
	return c.Do(proto.Command{Action: "panel.close", IDs: x.IDs})
}

type ctlGroup struct {
	Name string   `arg:"" help:"Work item name to file the panels under."`
	IDs  []string `arg:"" name:"id" help:"Panel ids to group."`
}

func (x ctlGroup) Run(c *control.Client) error {
	return c.Do(proto.Command{Action: "panel.group", Group: x.Name, IDs: x.IDs})
}

type ctlRename struct {
	ID    string `help:"Panel id to rename (mutually exclusive with --group)." xor:"target"`
	Group string `help:"Existing group name to rename (mutually exclusive with --id)." xor:"target"`
	Name  string `arg:"" help:"The new name."`
}

func (x ctlRename) Run(c *control.Client) error {
	return c.Do(proto.Command{Action: "panel.rename", ID: x.ID, Group: x.Group, Name: x.Name})
}

type ctlPin struct {
	IDs []string `arg:"" name:"id" help:"Panel ids to pin."`
}

func (x ctlPin) Run(c *control.Client) error {
	return c.Do(proto.Command{Action: "panel.pin", IDs: x.IDs})
}

type ctlUnpin struct {
	IDs []string `arg:"" name:"id" help:"Panel ids to unpin."`
}

func (x ctlUnpin) Run(c *control.Client) error {
	return c.Do(proto.Command{Action: "panel.unpin", IDs: x.IDs})
}

type ctlSignal struct {
	Signal string   `arg:"" help:"Signal name or number, e.g. SIGINT or 2."`
	IDs    []string `arg:"" name:"id" help:"Panel ids to signal."`
}

func (x ctlSignal) Run(c *control.Client) error {
	return c.Do(proto.Command{Action: "panel.signal", Signal: x.Signal, IDs: x.IDs})
}

type ctlSend struct {
	ID      string `arg:"" help:"Target panel id."`
	Text    string `arg:"" help:"Text to type into the panel."`
	NoEnter bool   `help:"Do not append a newline, so the text is typed but not submitted."`
}

func (x ctlSend) Run(c *control.Client) error {
	return c.SendText(x.ID, x.Text, !x.NoEnter)
}

type ctlDispatch struct {
	ID     string `arg:"" help:"Target panel id."`
	Prompt string `arg:"" help:"The task brief to assign and deliver."`
}

func (x ctlDispatch) Run(c *control.Client) error {
	return c.Dispatch(x.ID, x.Prompt)
}

type ctlDispatchGroup struct {
	Group  string `arg:"" help:"Work-item name whose members receive the task."`
	Prompt string `arg:"" help:"The task brief to dispatch to every member."`
}

func (x ctlDispatchGroup) Run(c *control.Client) error {
	return c.DispatchGroup(x.Group, x.Prompt)
}

// ctlQueue groups the backlog verbs under `baton ctl queue …`.
type ctlQueue struct {
	Add     ctlQueueAdd     `cmd:"" help:"Enqueue a task for the scheduler to drain onto a free agent."`
	List    ctlQueueList    `cmd:"" help:"Print the backlog as JSON."`
	Cancel  ctlQueueCancel  `cmd:"" help:"Cancel a queued task by id."`
	Promote ctlQueuePromote `cmd:"" help:"Bump a queued task to the head of the backlog."`
	Demote  ctlQueueDemote  `cmd:"" help:"Drop a queued task to the tail of the backlog."`
	Drain   ctlQueueDrain   `cmd:"" help:"Clear every queued task."`
}

type ctlQueueAdd struct {
	Prompt string `arg:"" help:"The task brief to enqueue."`
	Group  string `help:"Restrict the task to agents in this work item."`
}

func (x ctlQueueAdd) Run(c *control.Client) error {
	return c.Enqueue(x.Prompt, x.Group)
}

type ctlQueueList struct{}

func (ctlQueueList) Run(c *control.Client) error {
	out, err := c.TasksJSON()
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

type ctlQueueCancel struct {
	ID string `arg:"" help:"Queued task id to cancel."`
}

func (x ctlQueueCancel) Run(c *control.Client) error {
	return c.CancelTask(x.ID)
}

type ctlQueuePromote struct {
	ID string `arg:"" help:"Queued task id to bump to the head."`
}

func (x ctlQueuePromote) Run(c *control.Client) error {
	return c.PromoteTask(x.ID)
}

type ctlQueueDemote struct {
	ID string `arg:"" help:"Queued task id to drop to the tail."`
}

func (x ctlQueueDemote) Run(c *control.Client) error {
	return c.DemoteTask(x.ID)
}

type ctlQueueDrain struct{}

func (ctlQueueDrain) Run(c *control.Client) error {
	return c.DrainQueue()
}
