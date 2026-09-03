# Baton — Git

**English** · [繁體中文](GIT.zh-TW.md)

> Do common git work without leaving the agent you are watching. The **git menu**
> is a keyed pop-up, opened with the leader **`C-t G`** while zoomed into an agent
> panel, that runs git against that agent's working directory.

It is **zoom-only** by design — you act on the one agent you are looking at — and
**agent-only**: a shell, a non-repo, or a transient (diff/git) view never opens it.
It builds on the [diff](./SPEC.md) feature's machinery: most ops capture their output
into a **scrollable pop-up** over the cockpit, the text sibling of the diff pop-up.

## The menu

`C-t G` in a zoom opens the menu for the zoomed agent. Pick an op by its keycap, or
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

Underneath the menu there is **one server path**, and it takes three things: a
**repo**, a **branch**, and an **agent spec** (command, args, profile).

```text
repo + branch + spec → git worktree add -b → spawn an agent in the tree → group it under the branch
```

`C-t G` `w` is **one caller** of that path, not the path itself. It resolves the
repo and the spec from the agent you are zoomed into and hands both over; nothing
in the sequence after that needs a live agent sitting in the repo. A path that is
not a git repository is refused with `not a git repository: …`, and there is no
fallback onto a plain spawn.

There is a **second caller**, and it is the reason the path takes no panel id.
`n w` on the **dashboard** starts the same sequence from a directory instead of
from an agent — see [two ways in](#two-ways-in) below.

- **`w` (worktree + agent)** asks for a branch name, then runs that path with the
  zoomed agent's repo and its command, args and profile — so the new tree gets the
  same kind of agent under the same resource caps, **grouped under the branch** so
  it lands as a work item at once. This is how you fan an agent out onto an
  isolated branch without it stepping on the tree you are in. The tree goes under
  **`panel.worktree-dir`** when set, else a sibling `"<repo>-worktrees/<branch>"`
  (the branch's slashes become dashes).
- **`W` (worktrees)** lists the repo's worktrees in a text pop-up.
- **`x` (rm worktree)** asks for a path, confirms, then `git worktree remove` it. It
  runs **without `--force`**, so git refuses a worktree with uncommitted changes or
  a lock — the safe default, surfaced as the error. It targets a typed path, never
  the live agent's own workdir, so you cannot pull a tree out from under a running
  agent by accident.

### Two ways in

The same tree, agent and group are reached by two verbs, and which one you want is
decided by whether you are already watching an agent.

| Verb        | Where          | Repo comes from      | Agent profile         |
| ----------- | -------------- | -------------------- | --------------------- |
| `n w`       | the dashboard  | a directory you pick | the **fleet default** |
| `C-t G` `w` | a zoomed agent | that agent's workdir | **that agent's** spec |

**`n w`** is how you start isolated when there is nothing to fan out from. It asks
for the repository first — a typed path with `tab` completion, `C-b` to delete a
segment, and `C-o` for the directory picker, exactly the prompt `A` uses — and then
for the branch, in the same field `C-t G` `w` opens. It has no source panel, so the
new agent is the **fleet default** profile (what `A` spawns when you pick nothing),
never a copy of whatever the dashboard cursor happened to be on.

A directory that is not a git repository is **refused**: nothing spawns, no
directory is created, and there is no fallback onto a plain `A`. `A` in that same
directory still does what it always did — it lands an agent **in** the repo and
grows no tree.

**`C-t G` `w`** is the other end: you are watching an agent, and you want another
one like it on a branch of its own. It copies that agent's command, args and
profile, so the new tree gets the same kind of agent under the same resource caps.

Closing the new panel leaves the tree standing, whichever verb opened it. Neither
verb removes anything; `x` in the git menu is the only way a tree goes.

### Trees baton opened, and trees you did

If the tree is created but the **agent fails to start**, the tree is **left in
place** — retire it with `x` rather than have baton guess.

So that baton can tell such a tree from one you made yourself, every tree this path
opens is recorded in a file beside the fleet snapshot: `<socket>.worktrees.json`,
derived from the control socket, machine-written and never hand-edited, exactly as
`<socket>.state.json` is. **Nothing is written into the worktree itself** — a marker
file there would sit in front of the agent working in that tree, which might well
commit it. A tree you made with plain `git worktree add` is never in the record.

It is a separate file from the fleet snapshot because the lifetimes differ: closing
the agent, purging, and restarting the daemon all leave the tree standing, so the
record outlives the fleet the snapshot describes. It is written only when the
snapshot is. A tree goes only through `x` here or through
[`baton ctl worktree sweep`](#seeing-the-residue-and-clearing-it) — both still
without `--force`.

Retiring a tree with `x` also takes it back **out** of the record, so the file names
the trees baton owns now rather than every tree it ever opened. That is housekeeping,
not a guarantee: removing a tree with `git worktree remove` in your own terminal, or
deleting the directory outright, never reaches baton, so a recorded path can still
name a tree that is gone.

### Seeing the residue, and clearing it

Closing panels leaves trees standing, which is the right default and also an
accumulating one. Two commands read that record back:

```sh
baton ctl worktree list          # every tree baton opened, and what became of it
baton ctl worktree sweep         # remove the orphans among them; asks first
```

`list` classifies each stamped path against the fleet as it stands:

| Status      | Means                                                      |
| ----------- | ---------------------------------------------------------- |
| `live`      | a running panel is working in it — leave it alone          |
| `dead-slot` | only an **exited** panel names it; the slot is still there |
| `orphan`    | nothing in the fleet names it — this is the residue        |

A **dead slot is not an orphan.** The panel is gone but its card is not: it still
carries the agent's transcript and a respawn still points at the tree. Purge the
slot (`x x` in the cockpit, or `panel.purge`) and the tree becomes an orphan. Note
that `close` skips that stage — closing a panel drops its spawn spec outright, so
the tree is unclaimed immediately.

`sweep` acts on **orphans only**, through the same `git worktree remove` the `x` key
uses and with the same **no `--force`**: a tree holding uncommitted work, or a locked
one, is **skipped and named** rather than failing the sweep around it, and stays in
the record so a later sweep can finish once you have dealt with it. An orphan whose
directory is already gone has nothing left to remove, so only its record is dropped —
git's own stale entry is what `git worktree prune` is for.

It confirms on a terminal and needs an explicit `--yes` from a script, so a command
discovered by accident cannot empty a disk. It is **not** offered over MCP, and the
daemon refuses it to a conductor connection either way: opening worktrees is an
agent's to do, retiring them is yours.

**Nothing outside the record is ever touched.** A tree you made with plain
`git worktree add` is in no record, so it is in no listing, and a sweep that reads
that listing cannot reach it — not even one sitting under `panel.worktree-dir` or in
`<repo>-worktrees` beside baton's own. And with persistence off there is no record at
all, which means **sweep nothing**: no state file is read as "baton opened none of
these", never as "none of these are accounted for".

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
- **worktree-add** resolves the repo and the spec, then calls the shared
  repo + branch + spec path, which creates the tree, records it, spawns + groups the
  agent, and broadcasts the fleet — neither verb keeps a private copy of that
  sequence. **How** it resolves is the only difference between the two: an `id`
  names a panel and both come from it, while an **empty `id`** is the dashboard's
  form, where `dir` names the repo and `path`/`args`/`profile` carry the spec the
  cockpit resolved from the fleet default. Both verbs send the same command, so no
  second wire action was added and the protocol version did not move; an older
  daemon, which knows only the first form, answers the second with `no panel with
id ""` — a refusal, not a misread. The targetless form is refused for a
  **conductor** connection: naming its own command would be `panel.create`'s power
  without `panel.create`'s fleet ceiling and rate cap, so a conductor keeps the
  form that copies an existing agent;
- **worktree-remove** runs synchronously and confirms with a notice.

The agent-only and git-work-tree gates are enforced server-side — the cockpit gates
too, but the daemon is the source of truth.
