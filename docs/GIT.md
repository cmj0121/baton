# Baton — Git

> Do common git work without leaving the agent you are watching. The **git menu**
> is a keyed pop-up, opened with the leader **`C-t g`** while zoomed into an agent
> panel, that runs git against that agent's working directory.

It is **zoom-only** by design — you act on the one agent you are looking at — and
**agent-only**: a shell, a non-repo, or a transient (diff/git) view never opens it.
It builds on the [diff](./SPEC.md) feature's machinery: most ops capture their output
into a **scrollable pop-up** over the cockpit, the text sibling of the diff pop-up.

## The menu

`C-t g` in a zoom opens the menu for the zoomed agent. Pick an op by its keycap, or
`↑↓` (`j`/`k`) and `enter`; `esc` cancels. `push` and `remove` ask `y/n` first.

| Key | Op          | Runs                                              | Result                           |
| --- | ----------- | ------------------------------------------------- | -------------------------------- |
| `d` | diff        | working tree vs `HEAD`, untracked-included        | master-detail pop-up (the diff)  |
| `l` | log         | `git log --oneline --graph --decorate -n 200`     | text pop-up                      |
| `s` | status      | `git status`                                      | text pop-up                      |
| `a` | stage all   | `git add -A`                                      | text pop-up                      |
| `c` | commit      | `git add -A && git commit` (opens `$EDITOR`)      | transient PTY panel              |
| `p` | push        | `git push` — **confirms first**                   | text pop-up                      |
| `b` | branch      | `git checkout -b <name>`                          | text pop-up                      |
| `w` | worktree    | `git worktree add -b <branch> <path>` + an agent  | new grouped agent (a fleet item) |
| `W` | worktrees   | `git worktree list`                               | text pop-up                      |
| `x` | rm worktree | `git worktree remove <path>` — **confirms first** | a status notice                  |

A **text pop-up** shows the op's captured output over the current view: the server
runs the command in the agent's workdir one-shot, reaps it, and replies with the
text — nothing spawns on the dashboard and nothing is persisted. `j`/`k` and the
page keys scroll; `esc` closes and restores the view you came from. A non-zero exit
(a rejected push, a failed branch) still opens the pop-up, header tinted, so you see
git's own message. The captures run with `GIT_TERMINAL_PROMPT=0` and a 30s cap, so a
push that would prompt for credentials fails fast rather than hanging.

**`commit`** is the one exception: it opens `$EDITOR`, which needs a real terminal,
so it keeps the **transient PTY panel** — the server spawns it as an ephemeral PTY,
never on the dashboard and never persisted, and the cockpit drops straight into it
as an auto-zoom. Dismiss it with the normal zoom exit (`C-t b` back, `C-t d`
dashboard, `C-t q` detach) — that tears it down. A connection holds at most 8
transient panels (diff's explicit `diff-command` and commit share the cap); past
that the op reports `too many open panels (max 8) — close one first`.

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
- **`W` (worktrees)** lists the repo's worktrees in a text pop-up.
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
duplicate branch) surface verbatim in the pop-up or the status line.

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

- a **non-interactive output op** (log/status/add/push/branch/worktrees) is captured
  by `gitops.Capture` and replied as a `gitout` message the cockpit shows in the text
  pop-up — no PTY, nothing persisted;
- **commit** keeps the transient PTY panel (it drives `$EDITOR`), replying so the
  cockpit auto-zooms it (the `openEphemeral` engine the explicit `diff-command` uses);
- **worktree-add** creates the tree, spawns + groups the agent, and broadcasts the
  fleet;
- **worktree-remove** runs synchronously and confirms with a notice.

The agent-only and git-work-tree gates are enforced server-side — the cockpit gates
too, but the daemon is the source of truth.
