package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/worktree"
)

// ctlStateServer is ctlTestServer with PERSISTENCE ON, which is what gives the
// daemon a worktree record at all. ctlTestServer deliberately runs without one,
// and a sweep against that server can only ever be a no-op.
func ctlStateServer(t *testing.T) string {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	// A short temp root rather than t.TempDir(): macOS caps a unix socket path
	// near 104 bytes, and a directory named after the test overruns it.
	home, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	sock := filepath.Join(home, "b.sock")
	t.Setenv("BATON_SOCK", sock)
	ln, lnErr := net.Listen("unix", sock)
	if lnErr != nil {
		t.Fatalf("listen: %v", lnErr)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := server.New(ln, server.WithStateFile(filepath.Join(home, "b.state.json")))
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { srv.Shutdown() })
	return sock
}

// ctlDial opens a control client on sock and closes it with the test.
func ctlDial(t *testing.T, sock string) *control.Client {
	t.Helper()
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// withTTY makes the sweep believe (or disbelieve) it is talking to a person, and
// supplies the answer it would type. Both are restored when the test ends.
func withTTY(t *testing.T, tty bool, answer string) {
	t.Helper()
	oldTTY, oldIn := stdinIsTTY, confirmIn
	t.Cleanup(func() { stdinIsTTY, confirmIn = oldTTY, oldIn })
	stdinIsTTY = func() bool { return tty }
	confirmIn = strings.NewReader(answer)
}

// ctlOrphan opens a worktree through `ctl spawn --worktree`, lets its agent exit,
// then purges the slot — the sequence that turns a tree baton opened into an
// orphan. It returns the tree path as the record spells it.
func ctlOrphan(t *testing.T, c *control.Client, repo, branch string) string {
	t.Helper()
	cmd := ctlSpawn{Agent: "/bin/sh", Arg: []string{"-c", "exit 0"}, Dir: repo, Worktree: true, Branch: branch}
	captureStdout(t, func() {
		if err := cmd.Run(c); err != nil {
			t.Fatalf("ctl spawn --worktree: %v", err)
		}
	})

	tree := filepath.Join(repo+"-worktrees", strings.ReplaceAll(branch, "/", "-"))
	if _, err := os.Stat(filepath.Join(tree, ".git")); err != nil {
		t.Fatalf("the worktree should exist at %s: %v", tree, err)
	}

	// Wait for the agent to exit, then purge the slot it leaves. Only then does
	// nothing in the fleet name the tree.
	deadline := time.Now().Add(15 * time.Second)
	for {
		panels, err := c.List()
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		done := len(panels) > 0
		for _, p := range panels {
			if p.State != "exited" {
				done = false
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worktree agent never exited, panels %+v", panels)
		}
	}
	if err := c.Do(proto.Command{Action: "panel.purge"}); err != nil {
		t.Fatalf("purge: %v", err)
	}

	resolvedTree, err := filepath.EvalSymlinks(tree)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return resolvedTree
}

// ctlTrees runs `ctl worktree list` and decodes what it printed, so the assertion
// is on what an operator actually sees rather than on the client call behind it.
func ctlTrees(t *testing.T, c *control.Client) []worktree.Entry {
	t.Helper()
	out := captureStdout(t, func() {
		if err := (ctlWorktreeList{}).Run(c); err != nil {
			t.Fatalf("ctl worktree list: %v", err)
		}
	})
	var entries []worktree.Entry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("ctl worktree list should print JSON, got %q: %v", out, err)
	}
	return entries
}

// TestCtlWorktreeSweepNeedsYesFromAScript is the fence that keeps this command
// from emptying a disk by being discovered.
//
// Without --yes and without a terminal there is nobody to ask, so the command
// REFUSES rather than prompting a stdin that will answer EOF. The orphan standing
// afterwards is what makes this an assertion about effect rather than about a
// message: a version that prompted, read EOF and declined would also print
// something, and would also be wrong.
func TestCtlWorktreeSweepNeedsYesFromAScript(t *testing.T) {
	repo := ctlWTRepo(t)
	c := ctlDial(t, ctlStateServer(t))
	tree := ctlOrphan(t, c, repo, "feat/scripted")
	withTTY(t, false, "y\n") // a script: no terminal, and it typed yes anyway

	err := (ctlWorktreeSweep{}).Run(c)
	if err == nil {
		t.Fatal("a sweep with no terminal and no --yes must be refused")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("the refusal should say how to mean it, got %v", err)
	}
	if _, statErr := os.Stat(tree); statErr != nil {
		t.Fatalf("the refused sweep must have removed nothing: %v", statErr)
	}
}

// TestCtlWorktreeSweepDeclinedAtThePrompt is the other half of the guard: on a
// terminal it asks, and anything but yes leaves every orphan standing. "n" and a
// bare newline are both tried, since the bare newline is what an operator who
// hits return without reading produces — the case a [y/N] prompt exists for.
func TestCtlWorktreeSweepDeclinedAtThePrompt(t *testing.T) {
	for name, answer := range map[string]string{"no": "n\n", "just return": "\n", "eof": ""} {
		t.Run(name, func(t *testing.T) {
			repo := ctlWTRepo(t)
			c := ctlDial(t, ctlStateServer(t))
			tree := ctlOrphan(t, c, repo, "feat/declined")
			withTTY(t, true, answer)

			if err := (ctlWorktreeSweep{}).Run(c); err != nil {
				t.Fatalf("declining is not an error: %v", err)
			}
			if _, err := os.Stat(tree); err != nil {
				t.Fatalf("a declined sweep must remove nothing: %v", err)
			}
			if s := ctlStatus(t, ctlTrees(t, c), tree); s != worktree.StatusOrphan {
				t.Fatalf("the orphan should still be recorded, got %q", s)
			}
		})
	}
}

// TestCtlWorktreeListAndSweepYes is the acceptance through the CLI an operator
// actually types: list names the orphan, `sweep --yes` removes it, and the record
// is emptied with it.
//
// The confirmation is armed to say NO for this run. --yes must not consult it at
// all, so a --yes that still fell through to the prompt would fail here rather
// than pass by accident.
func TestCtlWorktreeListAndSweepYes(t *testing.T) {
	repo := ctlWTRepo(t)
	c := ctlDial(t, ctlStateServer(t))
	tree := ctlOrphan(t, c, repo, "feat/swept")
	withTTY(t, false, "n\n")

	if s := ctlStatus(t, ctlTrees(t, c), tree); s != worktree.StatusOrphan {
		t.Fatalf("a purged worktree panel leaves an orphan, got %q", s)
	}

	out := captureStdout(t, func() {
		if err := (ctlWorktreeSweep{Yes: true}).Run(c); err != nil {
			t.Fatalf("ctl worktree sweep --yes: %v", err)
		}
	})
	if !strings.Contains(out, tree) {
		t.Fatalf("the sweep should report the path it removed, got %q", out)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatalf("the orphan should be gone, stat err = %v", err)
	}
	if got := ctlTrees(t, c); len(got) != 0 {
		t.Fatalf("the record should be empty after the sweep, got %+v", got)
	}
}

// TestCtlWorktreeSweepAcceptsAtThePrompt closes the loop: the guard is a guard,
// not a wall. A person who answers yes gets the sweep, with no --yes needed.
func TestCtlWorktreeSweepAcceptsAtThePrompt(t *testing.T) {
	repo := ctlWTRepo(t)
	c := ctlDial(t, ctlStateServer(t))
	tree := ctlOrphan(t, c, repo, "feat/agreed")
	withTTY(t, true, "y\n")

	captureStdout(t, func() {
		if err := (ctlWorktreeSweep{}).Run(c); err != nil {
			t.Fatalf("ctl worktree sweep: %v", err)
		}
	})
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatalf("an accepted sweep should have removed the orphan, stat err = %v", err)
	}
}

// TestCtlWorktreeListLeavesAHandMadeTreeOut is the stamp doing its job at the
// surface an operator reads. A worktree the test made itself, in the same
// repository, is in no record — so it is in no listing, and the sweep that reads
// that listing can never reach it.
func TestCtlWorktreeListLeavesAHandMadeTreeOut(t *testing.T) {
	repo := ctlWTRepo(t)
	c := ctlDial(t, ctlStateServer(t))

	byHand := filepath.Join(repo+"-byhand", "theirs")
	ctlGit(t, repo, "worktree", "add", "-q", "-b", "hand/made", byHand)
	tree := ctlOrphan(t, c, repo, "feat/mine")

	entries := ctlTrees(t, c)
	if len(entries) != 1 || entries[0].Path != tree {
		t.Fatalf("only baton's own tree should be listed, got %+v", entries)
	}

	withTTY(t, false, "")
	captureStdout(t, func() {
		if err := (ctlWorktreeSweep{Yes: true}).Run(c); err != nil {
			t.Fatalf("sweep: %v", err)
		}
	})
	if _, err := os.Stat(byHand); err != nil {
		t.Fatalf("a hand-made worktree must survive the sweep: %v", err)
	}
}

// ctlGit runs one git command in dir under the same neutralised environment
// ctlWTRepo set on the process, failing the test on a non-zero exit.
func ctlGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=baton", "GIT_AUTHOR_EMAIL=baton@example.com",
		"GIT_COMMITTER_NAME=baton", "GIT_COMMITTER_EMAIL=baton@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// ctlStatus finds one path in a listing.
func ctlStatus(t *testing.T, entries []worktree.Entry, path string) worktree.Status {
	t.Helper()
	for _, e := range entries {
		if e.Path == path {
			return e.Status
		}
	}
	t.Fatalf("no entry for %q in %+v", path, entries)
	return ""
}

// TestStdinIsTTYRejectsADevNullStdin checks the REAL tty probe, not the injected
// one every other test in this file uses. It is here because the obvious stdlib
// spelling of this check is wrong in the dangerous direction.
//
// os.Stdin.Stat() reports a character device for /dev/null, so a guard written as
// `mode & os.ModeCharDevice != 0` calls `baton ctl worktree sweep < /dev/null`
// interactive and prompts where it was asked to refuse. `go test` runs with
// exactly that stdin, which is what lets this test hold the ioctl in place: swap
// isatty.IsTerminal for the mode check and this fails.
func TestStdinIsTTYRejectsADevNullStdin(t *testing.T) {
	if stdinIsTTY() {
		t.Fatal("go test runs with /dev/null on stdin, which is not a terminal")
	}
	// …and the mode check the guard must NOT use would have said otherwise here,
	// so the two really do disagree on this stdin rather than agreeing by luck.
	fi, err := os.Stdin.Stat()
	if err != nil {
		t.Skipf("cannot stat stdin: %v", err)
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		t.Skip("stdin is not a character device here, so the two checks cannot be told apart")
	}
}
