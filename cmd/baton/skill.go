package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/alecthomas/kong"

	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/scrub"
	"github.com/cmj0121/baton/internal/skills"
)

// `baton skill` puts baton's outside-facing skills (baton, conduct, drive) where
// an agent baton did not launch will find them: a repository's .claude/skills,
// or the operator's own ~/.claude/skills. It is the same set the marketplace
// plugin serves, for an operator who would rather not add a marketplace.
//
// It ships skills only, no .mcp.json: wiring an MCP server into someone's
// repository or home is a bigger step than a skills install should take
// silently, and the drive skill already says how to reach baton without one.

// skillCLI is the `baton skill` command line.
type skillCLI struct {
	Install   skillInstall   `cmd:"" help:"Write baton's skills into a .claude/skills directory."`
	Uninstall skillUninstall `cmd:"" help:"Remove the skills baton installed, and nothing else."`
}

// skillScope is where the skills go. --project is the default because it is the
// one that cannot surprise anyone outside this repository: --user reaches every
// Claude Code session the operator runs, the baton conductor's included.
type skillScope struct {
	Project bool `xor:"scope" help:"Use ./.claude/skills under the current directory (the default)."`
	User    bool `xor:"scope" help:"Use ~/.claude/skills, read by every Claude Code session you run."`
}

func (s skillScope) dir() string {
	if s.User {
		return paths.Expand("~/.claude/skills")
	}
	return paths.Expand(filepath.Join(".claude", "skills"))
}

type skillInstall struct {
	skillScope
	Force bool `help:"Replace a same-name skill baton did not install."`
}

func (c skillInstall) Run(out io.Writer) error {
	written, err := skills.Install(c.dir(), skills.Outside, c.Force)
	if err != nil {
		return err
	}
	for _, p := range written {
		_, _ = fmt.Fprintln(out, p)
	}
	// A personal skill outranks a project one of the same name, so these copies
	// are the ones the baton conductor reads in place of the workspace's own —
	// and they are frozen at this binary's text until install runs again.
	if c.User {
		_, _ = fmt.Fprintln(out, "warning: skills in ~/.claude/skills shadow the copies the baton conductor is given;"+
			" re-run `baton skill install --user` after upgrading baton, or they go stale.")
	}
	return nil
}

type skillUninstall struct {
	skillScope
}

func (c skillUninstall) Run(out io.Writer) error {
	removed, err := skills.Uninstall(c.dir())
	if err != nil {
		return err
	}
	if len(removed) == 0 {
		_, _ = fmt.Fprintln(out, "nothing to remove: no skills installed by baton in "+c.dir())
	}
	for _, p := range removed {
		_, _ = fmt.Fprintln(out, p)
	}
	return nil
}

// skillMain runs `baton skill …` and returns a process exit code: 0 on success,
// 1 when the command ran and failed, 2 when the command line was wrong. ctl gets
// its usage-error exit from kong's FatalIfErrorf, which prints usage and calls
// os.Exit itself with kong's usage code, 80; this prints the same usage and
// returns 2, so a test can read the code without the process ending.
func skillMain(args []string, stdout, stderr io.Writer) int {
	var cli skillCLI
	parser, err := kong.New(&cli,
		kong.Name("baton skill"),
		kong.Description("Install baton's agent skills for agents baton did not launch."),
		kong.Writers(stdout, stderr),
		kong.BindTo(stdout, (*io.Writer)(nil)),
	)
	if err != nil {
		return skillFail(stderr, err, 2)
	}
	kctx, err := parser.Parse(args)
	if err != nil {
		// Parse hands back no context on an error; the one that failed rides
		// in the ParseError, and its usage is what ctl prints via FatalIfErrorf.
		var pe *kong.ParseError
		if errors.As(err, &pe) && pe.Context != nil {
			_ = pe.Context.PrintUsage(true)
		}
		return skillFail(stderr, err, 2)
	}
	if err := kctx.Run(); err != nil {
		return skillFail(stderr, err, 1)
	}
	return 0
}

func skillFail(stderr io.Writer, err error, code int) int {
	_, _ = fmt.Fprintln(stderr, "baton skill:", scrub.Text(err.Error()))
	return code
}
