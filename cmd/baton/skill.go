package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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

// dir is the skills directory with every symlink in its existing part
// resolved, so what is printed and acted on is where the files really go.
//
// The two scopes treat a symlink differently, on purpose. A --project dir sits
// in a repository that may have been cloned from anyone, and that repository
// can commit .claude or .claude/skills as a symlink to anywhere — install,
// --force and uninstall would then write and delete there. So a --project dir
// must resolve inside the resolved current directory or it is refused. A
// --user dir is the operator's own home, where a ~/.claude linked into a
// dotfiles repository is their layout and the place they mean, so it is
// followed wherever it goes.
func (s skillScope) dir() (string, error) {
	if s.User {
		return resolveExisting(paths.Expand("~/.claude/skills"))
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", err
	}
	dir, err := resolveExisting(filepath.Join(cwd, ".claude", "skills"))
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(root, dir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing %s: it resolves to %s, outside the current directory %s (a symlinked .claude?)",
			filepath.Join(cwd, ".claude", "skills"), dir, root)
	}
	return dir, nil
}

// resolveExisting is p with its deepest existing ancestor's symlinks resolved
// and the missing rest appended. An ancestor that is there but will not
// resolve — a dangling link — is an error: MkdirAll would follow it later, to
// wherever it points.
func resolveExisting(p string) (string, error) {
	var rest []string
	for cur := p; ; cur = filepath.Dir(cur) {
		_, err := os.Lstat(cur)
		if err == nil {
			r, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", fmt.Errorf("resolve %s: %w", cur, err)
			}
			for i := len(rest) - 1; i >= 0; i-- {
				r = filepath.Join(r, rest[i])
			}
			return r, nil
		}
		if !errors.Is(err, fs.ErrNotExist) || filepath.Dir(cur) == cur {
			return "", err
		}
		rest = append(rest, filepath.Base(cur))
	}
}

type skillInstall struct {
	skillScope
	Force bool `help:"Replace a same-name skill baton did not install."`
}

func (c skillInstall) Run(out io.Writer) error {
	dir, err := c.dir()
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "skills dir: "+dir)
	written, err := skills.Install(dir, skills.Outside, c.Force)
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
	dir, err := c.dir()
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "skills dir: "+dir)
	removed, err := skills.Uninstall(dir, skills.Outside)
	if err != nil {
		return err
	}
	if len(removed) == 0 {
		_, _ = fmt.Fprintln(out, "nothing to remove: no skills installed by baton in "+dir)
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
