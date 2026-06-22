# Baton — Git

> Do common git work without leaving the agent you are watching. The **git menu**
> is a keyed pop-up, opened with the leader **`C-t g`** while zoomed into an agent
> panel, that runs git against that agent's working directory.

It is **zoom-only** by design — you act on the one agent you are looking at — and
**agent-only**: a shell, a non-repo, or a transient (diff/git) view never opens it.
It builds on the [diff](./SPEC.md) feature's machinery, so every output op opens the
same transient, auto-zoomed panel you already know.

## The menu

`C-t g` in a zoom opens the menu for the zoomed agent. Pick an op by its keycap, or
`↑↓` (`j`/`k`) and `enter`; `esc` cancels. `push` and `remove` ask `y/n` first.

| Key | Op          | Runs                                              | Result                           |
| --- | ----------- | ------------------------------------------------- | -------------------------------- |
| `d` | diff        | working tree vs `HEAD`, untracked-included        | transient panel (the diff path)  |
| `l` | log         | `git log --oneline --graph --decorate -n 200`     | transient panel                  |
| `s` | status      | `git status`                                      | transient panel                  |
| `a` | stage all   | `git add -A`                                      | transient panel                  |
| `c` | commit      | `git add -A && git commit` (opens `$EDITOR`)      | transient panel                  |
| `p` | push        | `git push` — **confirms first**                   | transient panel                  |
| `b` | branch      | `git checkout -b <name>`                          | transient panel                  |
| `w` | worktree    | `git worktree add -b <branch> <path>` + an agent  | new grouped agent (a fleet item) |
| `W` | worktrees   | `git worktree list`                               | transient panel                  |
| `x` | rm worktree | `git worktree remove <path>` — **confirms first** | a status notice                  |

A **transient panel** is the diff pop-up's vehicle: the server spawns the command
in the agent's workdir as an ephemeral PTY, never on the dashboard and never
persisted, and the cockpit drops straight into it as an auto-zoom. Dismiss it with
the normal zoom exit (`C-t b` back, `C-t d` dashboard, `C-t q` detach) — that tears
it down. A connection holds at most 8 transient panels (diff and git share the cap);
past that the op reports `too many open panels (max 8) — close one first`.

## Commit — your editor, in the panel

`commit` stages everything and runs `git commit`, which opens your editor **inside
the transient panel's PTY** — vim, nano, whatever you use, behaving exactly as in a
terminal. Write the message, save, quit; the commit completes and the panel shows
the result. A clean tree refuses with `nothing to commit`.

The editor is resolved in order: the **`panel.editor`** config, else git's own
chain (`$GIT_EDITOR` → `git config core.editor` → `$EDITOR` → `vi`). So if git
already opens the editor you want at the command line, baton needs no extra config.

## Worktrees — isolation for parallel agents

- **`w` (worktree + agent)** asks for a branch name, then `git worktree add -b
<branch>` a fresh tree and **spawns an agent rooted in it**, reusing the source
  agent's command, **grouped under the branch** so it lands as a work item at once.
  This is how you fan an agent out onto an isolated branch without it stepping on
  the tree you are in. The tree goes under **`panel.worktree-dir`** when set, else a
  sibling `"<repo>-worktrees/<branch>"` (the branch's slashes become dashes).
- **`W` (worktrees)** lists the repo's worktrees in a transient panel.
- **`x` (rm worktree)** asks for a path, confirms, then `git worktree remove` it. It
  runs **without `--force`**, so git refuses a worktree with uncommitted changes or
  a lock — the safe default, surfaced as the error. It targets a typed path, never
  the live agent's own workdir, so you cannot pull a tree out from under a running
  agent by accident.

## Safety

The op set is **additive**: read (diff/log/status/worktrees), stage, commit,
branch, push, worktree-add. There is **no `reset`, no `clean`, no
`checkout`-discard, and no `--force` anywhere**, so a misfire never destroys work.
The two ops that reach outward or remove something — **push** and **worktree-remove**
— each ask `y/n` first. git's own refusals (no upstream, a dirty worktree, a
duplicate branch) surface verbatim in the panel or the status line.

## Config

All three settings live under `panel:` in `$HOME/.baton/config` and **hot-reload**
with `C-t R` (or a `SIGHUP` to the daemon) — no restart, no panel lost:

```yaml
panel:
  editor: nvim # commit editor (GIT_EDITOR); empty = git's own chain
  worktree-dir: ~/src/.worktrees # base for new worktrees; empty = a sibling of the repo
  diff-command: git diff HEAD | delta # the diff op's command; empty = git diff.tool then built-in
```

## Under the hood

The menu sends one command, `panel.git`, carrying the op (`git`), the target agent
(`id`), and — where one applies — a branch (`name`) or a worktree path (`dir`). The
server resolves the op to a concrete command in [`internal/gitops`](../internal/gitops)
(a sibling of `gitdiff`), then:

- an **output op** spawns the transient panel and replies so the cockpit auto-zooms
  it (the same `openEphemeral` engine the diff uses);
- **worktree-add** creates the tree, spawns + groups the agent, and broadcasts the
  fleet;
- **worktree-remove** runs synchronously and confirms with a notice.

The agent-only and git-work-tree gates are enforced server-side — the cockpit gates
too, but the daemon is the source of truth.
