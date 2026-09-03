package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/worktree"
)

// ctlWorktree groups the verbs for the trees baton opened under
// `baton ctl worktree …`.
type ctlWorktree struct {
	List  ctlWorktreeList  `cmd:"" help:"Print the worktrees baton opened as JSON, each classified live / dead-slot / orphan."`
	Sweep ctlWorktreeSweep `cmd:"" help:"Remove the orphaned worktrees baton opened. Confirms first; from a script pass --yes."`
}

// stdinIsTTY reports whether this command is talking to a person. It is a
// variable so a test can say which, because the two answers are the whole of
// what the sweep's guard decides.
//
// It is an isatty ioctl rather than a check of os.ModeCharDevice, and that is not
// a style preference: /dev/null is a character device, so the mode test calls a
// script run with `< /dev/null` interactive — including every `go test`, where
// stdin is exactly that. The guard would then prompt where it was asked to
// refuse.
var stdinIsTTY = func() bool { return isatty.IsTerminal(os.Stdin.Fd()) }

// confirmIn is where the answer to a confirmation is read from. A variable for
// the same reason: the path that says yes is the path that deletes things, so it
// is the one that most needs a test.
var confirmIn io.Reader = os.Stdin

type ctlWorktreeList struct{}

// Run prints every tree baton opened with what became of it. It stays one blob
// of JSON, like `ctl list` and `ctl queue list`, because the reader is as often
// a jq filter as a person.
func (ctlWorktreeList) Run(c *control.Client) error {
	out, err := c.WorktreesJSON()
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

type ctlWorktreeSweep struct {
	Yes bool `help:"Remove the orphans without asking. Required when stdin is not a terminal."`
}

// Run removes the orphaned worktrees baton opened, and nothing else.
//
// It is guarded twice over, because it is the one verb here that deletes work
// from disk. Without --yes it names the orphans and asks; and it will only ask a
// PERSON, so a script that reaches this command without meaning to is refused
// rather than prompted at a stdin that will answer EOF. That refusal is the
// point: a conductor that discovered this command must not be able to empty the
// disk by running it, and on MCP it is not reachable at all.
//
// The prompt goes to stderr so `sweep --yes | jq` still gets clean JSON on
// stdout.
func (x ctlWorktreeSweep) Run(c *control.Client) error {
	if !x.Yes {
		ok, err := x.confirm(c)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "nothing swept")
			return nil
		}
	}

	out, err := c.SweepWorktrees()
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

// confirm names the orphans and asks whether to remove them, returning whether
// the sweep should go ahead. A run with no orphans stops here rather than asking
// about nothing.
//
// The orphans it lists are the ones standing when it asked. The server
// re-classifies at sweep time and acts only on what is an orphan THEN, so the
// listing is what the operator is being shown, never a promise about what will
// go.
func (x ctlWorktreeSweep) confirm(c *control.Client) (bool, error) {
	if !stdinIsTTY() {
		return false, fmt.Errorf("sweep removes worktrees from disk: run it from a terminal, or pass --yes to mean it from a script")
	}

	trees, err := c.Worktrees()
	if err != nil {
		return false, err
	}
	var orphans []worktree.Entry
	for _, e := range trees {
		if e.Status == worktree.StatusOrphan {
			orphans = append(orphans, e)
		}
	}
	if len(orphans) == 0 {
		fmt.Fprintln(os.Stderr, "no orphaned worktrees: every tree baton opened is still a panel's workdir")
		return false, nil
	}

	fmt.Fprintf(os.Stderr, "%d orphaned worktree(s) baton opened:\n", len(orphans))
	for _, e := range orphans {
		note := ""
		if !e.Exists {
			note = "  (already gone; only the record is dropped)"
		}
		fmt.Fprintf(os.Stderr, "  %s%s\n", e.Path, note)
	}
	fmt.Fprint(os.Stderr, "remove them? [y/N] ")

	line, err := bufio.NewReader(confirmIn).ReadString('\n')
	if err != nil && line == "" {
		return false, nil // EOF with nothing typed is not a yes
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}
