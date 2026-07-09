# Baton — Specification

Design detail behind the concept sketched in the [README](../README.md). Start there for the pitch and vocabulary;
this document covers how the pieces fit together.

## Three views, one cockpit

Baton is keyboard-driven and has three ways to look at your agents. The dashboard and the zoom are the two poles — all at
once, or one fully — and the group split sits between them, showing one work item's members live side by side.

- **Dashboard** — see everything at once. Navigate panels, spawn new ones, group them into work items, reorder them
  (`shift`+arrows), retire the dead ones.
- **Group** — see one work item live. Zooming a work item's card opens a split of its members, each in its own tile, all
  streaming at once (see [Work items](#work-items)). It is an overview you navigate, not one you type into until you
  press `i`.
- **Zoom** — see one thing fully. Drive a single panel as if it were your only terminal; `C-t [` opens a tmux-style
  scroll mode (`↑`/`↓` a line, `b`/`space` or `PgUp`/`PgDn` a page, `g`/`G` top/bottom, `esc` exits) to read back through
  its history. Every bare key drives the program (vim, a BBS), never baton, so the leader works on any terminal — then
  pop back out to the dashboard. How much history is
  kept and replayed on attach is `panel.replay-kb` in the config (a larger value pages back further; full-screen programs
  keep no scrollback).

You never juggle windows or tabs. You conduct from the dashboard, drop into a group when one task needs a closer look,
and zoom in only when a single player needs you.

## Panels

A panel is one PTY (pseudo-terminal) the server owns. There are two kinds:

- **Agent panel** — runs an agent CLI directly as the panel's process. There is no shell and no shell prompt in between;
  the agent CLI _is_ the program the PTY runs.
- **Shell panel** — runs a plain host shell, for ad-hoc commands on the machine.

Both are ordinary PTYs and share the lifecycle below; they differ only in what process they launch and in how loudly the
Monitor flags them for your attention.

**Agent profiles.** An agent panel is spawned from a named **profile** — a command and its arguments — run in a **working
directory** you choose, the directory the agent operates on. **Claude** is the built-in profile (`claude`); more are
defined under `panel.agents` in the config, with `panel.default-agent` naming the one the new-agent action spawns. The
client resolves the profile and sends `panel.create` with the command, args, and workdir; the server starts the process
there, and the panel's title reads `<command> · <workdir>` (e.g. `claude · baton`) so its task and place are visible at
a glance.

### Lifecycle

The Monitor moves a panel through a small set of states, from the moment you spawn it to the moment you close it:

```txt
                  ● spawn
                  │
                  ▼
             ┌──────────┐
             │ spawning │
             └────┬─────┘
                  │ process started
                  ▼
     ┌──────────┐                           ┌──────────┐
     │ running  │  ──── output quiet ────>  │   idle   │
     │          │  <─── output resumes ───  │          │
     └──┬────▲──┘                           └────┬─────┘
        │    │                                   │
  needs │    │ replies                           │
  input ▼    │                      needs input  │
     ┌──────────┐                                │
     │ attention│ <──────────────────────────────┘
     └────┬─────┘
          │ process exits      (also from running / idle)
          ▼
     ┌──────────┐
     │  exited  │
     └────┬─────┘
          │ user dismisses
          ▼
     ┌──────────┐
     │  closed  │ <──── kill panel (from any state)
     └──────────┘
```

- **spawning** — the PTY and process are being set up.
- **running** — the process is actively producing output.
- **idle** — no output for a while; an agent is waiting, or a shell sits at its prompt.
- **attention** — you are needed: the agent asked a question, finished its task, or printed something notable.
- **exited** — the process ended on its own; its exit code is kept until you dismiss it.
- **closed** — the panel is retired and leaves the dashboard.

`running`, `idle`, and `attention` are the live states the Monitor shuttles a panel between as output ebbs and flows. A
panel can be **killed from any state**, jumping straight to `closed`, and it reaches `exited` whenever its process stops
on its own — from `running`, `idle`, or `attention` alike.

## Work items

A **work item** is a named group of panels that belong to one task. Group membership is just a name carried on each
panel (`group`), so a work item _is_ its name: filing panels under `"api"` makes them one item, renaming the group
rewrites that name on every member, and removing one panel clears only its own name. The server owns this — grouping,
removing, and renaming are core actions (`panel.group`, `panel.ungroup`, `panel.rename`) reached over the socket — so
every frontend and every reattach sees the same groups. `panel.ungroup` takes either a group name (dissolve the whole
item) or a set of panel ids (drop just those members).

**Names are unique by default.** The server rejects a rename or a new group whose name already belongs to another panel
title or group, so a work item is never ambiguous. Adding panels to an _existing_ group reuses its name and is allowed;
the policy can be lifted with the `allow-name-conflict` setting, which the daemon reads at startup.

**Nested groups.** A group name is a **slash-path**, so a group can nest inside another: a panel filed under
`"backend/api"` sits in the group `api`, nested under `backend`. Membership stays derived — `backend` "exists" while any
panel carries `backend` or a path beneath it. Nest by using a path when you group (`backend/api`), by **renaming a group
to a path** — renaming `db` to `backend/db` re-parents the whole `db` subtree in one move (the server rewrites the path
prefix across every descendant) — or by **grouping/adding a whole group into another**: mark a group and group or add it
into a target, and it nests as `target/<its name>` (keeping its own sub-structure) rather than flattening.
Group-wide actions **recurse over the subtree**: dispatching to, closing, or
signalling `backend` reaches every descendant panel, nested groups included; **dissolving** `backend` promotes its
subtree one level (its direct panels go lone, its sub-groups become top-level) rather than deleting the work. The
dashboard shows only the **top level** — a group card folds its whole subtree — and you walk the hierarchy by descending
in the split.

On the dashboard a work item collapses into a single card: a member count and a state that **rolls up to its most urgent
member** (attention beats running beats spawning beats idle beats exited), so one card speaks for the whole task.

**Favourites.** `*` **favourites** the selected dashboard item — a lone panel or a whole group card — and favourited cards
sort to the **front** of the dashboard, in both the grid and the tree, each marked with a `⊙`. It is **server-owned state**
(`panel.favourite` / `panel.unfavourite` for a panel, `group.favourite` / `group.unfavourite` for a group), carried on the
snapshot — so it survives a restart, is shared across clients, and follows a group through a rename or ungroup. It is
**separate from the split's pin**: favouriting only reorders the dashboard, never which tiles stream live or the single-pin
descend.

**The group split.** Zooming a work item opens a split scoped to that path's **direct children** — each direct panel
rendered live in its own tile, plus one tile per immediate **sub-group** (a rollup box marked `▣`). By default the split
is an _overview you navigate_, not a surface you type into: `tab` moves the focus between tiles, `+`/`-` dials how many
panels stream live (see below), `shift`+`←`/`→` reorders the focused member within the group, `C-t [` opens scroll mode on
the focused tile, `x` removes the focused member from the group, `D` diffs the focused agent member, and `L`/`z` cycle the
layout / resize a tile (view-local, see [TUI.md](./TUI.md#resize)). `enter` **descends**: on a panel it drops into that
panel's own single zoom; on a sub-group tile it re-scopes the split into that sub-group (its header shows the path as a
breadcrumb, `backend › api`).
`esc` / `b` pop **back one level** — a sub-group to its parent, the summary sub-view to its group, the top-level group to
the dashboard — while `d` jumps straight out to the dashboard from any depth.
From a zoomed member, **back** (`C-t b`) pops back to the split it was launched from. You enter a group with `enter` on
its card and walk out with back.

**Visible count and the summary tile.** A group streams its first **N** members as live tiles; `+` / `-` dial N, which
is **server-owned state** (`group.show`, carried on the snapshot as the group's `Shown`), clamped to `[1, maxGroupTiles]`
and persisted, so the split reopens the way you left it. Rather than stranding the rest, every member past N folds into a
single **summary tile** — the last cell of the even grid — that rolls up the hidden members: their count, a per-state
breakdown, and the most urgent activity line. `tab` walks the live tiles and then the summary slot as one ring. Focus the
summary and `enter` zooms it into a **sub-grid of just the collapsed members**; `esc` returns to the parent group, not the
dashboard, and the pin/interact/signal/remove keys no-op on the summary with a hint.

**Pinning, for crowded groups.** `p` **pins** the focused member: a pinned panel always holds a live tile (a `⊙` marks
it), promoting it ahead of the auto-filled tiles. So you curate which of a busy group's panels stream live and which fold
into the summary. Pins are **server-owned state** (`panel.pin` / `panel.unpin`), carried on the panel the server
broadcasts — so they survive a frontend restart and are shared across clients. The pin set is re-derived from the parent
group's full membership on every snapshot, so a refresh arriving while the summary sub-view is open does not wipe your
curation. Reopening a group brings back the tiles you pinned; and a group with exactly **one** pinned member treats it as
the default — entering the group drops straight into that panel's zoom rather than a one-tile split (**back**, `C-t b`,
pops back to the split).

**Signals.** `s` opens a picker of the common signals (or `o` to type any name or number); the chosen one is sent over
the socket (`panel.signal`) and delivered to the panel's whole process group, so it reaches the foreground job, not just
the shell — though a child that daemonizes into its own group escapes it, and delivery is fire-and-forget (a trapped
signal still reads as sent). The target follows the view: the selection on the dashboard, the focused member in the split
(`s`) or every member (`S`), this panel in a zoom (`C-t s`). Exited panels are skipped on both sides, so the reported
count is the count delivered. The name→signal table lives in one place (`internal/signals`), shared by the picker and the
server, so the menu and the accepted set cannot drift. **Reload.** `C-t R` (or a `SIGHUP` to the daemon) re-reads the
config in place — the name policy, default workdir, and replay buffer change under a running fleet, no restart, no panel
lost.

**Diff.** `D` (the `diff` binding, `C-t D` in a zoom) shows the working-tree diff of the focused **agent** panel. It is
agent-only by design — a shell, a group card, or an empty selection never resolves a target and the client just hints
`diff: select an agent panel`, so the action never reaches the server without a panel. The server gates on the panel's
workdir: it must be inside a git work tree, or the request fails with `not a git repository: <dir>`; a tree with nothing
to show fails with `no uncommitted changes`. The change check (`internal/gitdiff`) reads `git status --porcelain`, whose
output lists **untracked files too**, so a brand-new file an agent just wrote counts — and the diff itself is the working
tree against `HEAD` computed **without mutating the repo**: no write to the index, the worktree, or any ref — though
staging into the throwaway index does write loose blobs to `.git/objects`, which are harmless and reclaimed by a later
`git gc`. The diff **command** is resolved in priority order: an explicit `panel.diff-command` wins, run via `sh -c` so
the user can write a full shell line or pipe; else, if the repo configures a `diff.tool`, that per-repo choice is honoured
with `git difftool
-d --no-prompt` (`--no-prompt` stops difftool stalling on a `[Y/n]` inside the PTY, and this branch keeps git's
tracked-only difftool semantics — untracked inclusion is a property of the explicit and built-in paths); else a built-in,
non-mutating, untracked-inclusive `git diff` that pages naturally. A caveat for the difftool branch: if the repo's
`diff.tool` is a GUI tool and the daemon runs headless or over SSH with no display, `git difftool` errors or hangs — the
failure is contained to the diff panel, not fatal, but on headless/remote hosts prefer a terminal diff (leave `diff.tool`
unset for the built-in, or point `panel.diff-command` at a terminal tool such as `git diff | delta`). The diff pages
through git's pager and is navigable; but if the repo disables the pager (`core.pager=cat`) or the explicit
`panel.diff-command` does not page, a very large diff dumps into the panel and only the last `panel.replay-kb` survives in
scrollback — the top truncates.

The diff renders in a **transient, ephemeral panel**. The server spawns it as a PTY running the resolved command in the
agent's workdir, but it is never inserted into `s.panels` — so it is invisible to the dashboard grid and to the state
file, never broadcast as fleet membership and never restored on a restart. It is **auto-zoomed**: the client drops
straight into it as a single zoom, reusing every read affordance — scroll mode (`C-t [`), scrollback search (`C-t f`),
and copy. Its lifecycle is tied to that zoom: the normal exit keys close it. `C-t d` (back to the dashboard) and `C-t q`
(detach) dismiss the pop-up and the server tears down the transient panel — there is no separate "close diff" verb, and
nothing lingers once you step out. A connection holds at most a small number of open diff pop-ups at once (currently 8);
past that the diff key reports `too many open diffs (max 8) — close one first`.

**Git menu.** `C-t g` in a zoom opens the **git menu** for the zoomed agent — a keyed pop-up (the signal picker's shape)
of git operations run against that agent's workdir. It is **zoom-only** (you act on the one agent you are looking at) and
agent-only. The **non-interactive output** ops (log, status, stage, push, branch, worktree-list) are run one-shot by
`gitops.Capture` and replied as a `gitout` message the cockpit shows in a **scrollable text pop-up** (`modeGitOut`) — the
text sibling of the diff pop-up: no PTY, nothing in `s.panels` or the state file, `esc` closes it. A non-zero exit still
opens the pop-up (header tinted) so git's own message shows; the captures run with `GIT_TERMINAL_PROMPT=0` under a 30s cap
so a credential-prompting push fails fast instead of hanging. Only **commit** keeps the transient, auto-zoomed PTY panel
(via the `openEphemeral` engine the explicit `diff-command` shares, 8-panel cap): it injects the configured editor as
`GIT_EDITOR` (via `ptymgr.Spec.Env`) so it opens in the panel's PTY. Two more ops are neither: **worktree-add** creates a
tree on a new branch and spawns an agent rooted
in it — grouped under the branch, broadcast as a real fleet change — the **isolation bridge** the panel-model roadmap
named; **worktree-remove** runs synchronously and confirms with a notice. The op set is additive (no reset / clean /
discard / `--force`); `push` and `worktree-remove` confirm first. The wire is one command, `panel.git`, carrying the op,
the target id, and a branch or worktree path; the agent-only and work-tree gates are authoritative on the server. The
commit editor and the worktree base directory are `panel.editor` / `panel.worktree-dir`, hot-reloaded like the rest. See
[GIT.md](./GIT.md) for the full op table and the config.

**Persistence and respawn.** The daemon survives its own restart. On every structural change it writes the fleet to a
per-session **state file** (`internal/state`, derived from the socket path like the pid file, one daemon-per-session) —
each panel's immutable spawn spec (command, args, workdir), group membership, pins, order, the id counter, and each
group's visible-tile count. Writes are coalesced through a one-deep dirty channel and flushed off the hot lock by a saver
goroutine; the `SIGINT`/`SIGTERM` handler calls `SaveNow` before exit, so the last action survives a clean shutdown even
though `os.Exit` skips the saver. Saves are atomic and durable (temp file, fsync, rename, fsync the parent dir); a load
never hard-fails boot — a missing file is a clean first run, and an unparsable or newer-schema file is renamed aside
(`.corrupt-<ts>`) rather than wedging the daemon. Restore is deliberately **inert**: every panel comes back as an exited
dead slot, no process auto-respawned (shells or agents alike), and the id counter resumes past the highest restored id so
a new panel can never collide. The `panel.respawn` action (the dashboard `r` key) re-runs an exited slot on demand from
its retained spec — one command per dead slot, so `r` on a focused group restarts every exited member at once and `r` in
the group split re-runs the focused tile; closing or purging a panel drops its spec for good.

**Interact mode.** Pressing `i` hands the keyboard to the focused tile so you can drive its program _in place_, without
the full-screen zoom — the tile glows green and wears a keyboard badge, and every keystroke is forwarded to that panel.
Like a zoom, the prefix is the only way out: `C-t i` returns to navigation, `C-t d` leaves for the
dashboard, `C-t q` detaches, and `C-t C-t` sends a literal prefix. Only the focused tile receives input; the others stay
passive, so the navigation keys are never ambiguous with what a panel might want until you opt in. If the panel being
typed into leaves the group, interact ends rather than silently retargeting the tile the focus falls onto.

Under the hood a single client attaches to every member at once; the server tags each output message with its panel id
and the client demuxes it into the matching tile, while each tile's input side is forwarded so interact can reach the
PTY. The split reconciles on every snapshot — members added or removed elsewhere appear and disappear in place, an
emptied group exits to the dashboard, and live tiles are capped so a very large group cannot spawn unbounded terminals.

## Tasks and the queue

A **panel** is a terminal; a **task** is a unit of work you hand one. Typing into a panel is raw keystrokes — the server
sees bytes. A **dispatch** is different: you give the server the _objective_, and it records it on the panel and delivers
it to the process as a unit. The brief then shows on the panel's card (a `▸` line) and in the snapshot, so every frontend
and every reattach knows what each agent is working on.

**Dispatch** is the direct path. `T` on the dashboard (`C-t T` in a zoom) opens a one-line prompt; what you type is
delivered to the focused agent — or, on a work-item card, **fanned to every member** of the group at once, the mechanic
behind racing N agents on the same prompt. The conductor cannot dispatch to its own panel (see
[CONTROL.md](./CONTROL.md)). Delivery **waits for readiness**: a brief sent to a busy agent is held and written the
moment the panel settles to `idle` or `attention`, so it lands at a prompt rather than interleaving with a running
command.

**A task is a tracked entity.** Each dispatch becomes a task with a forward-only status — `queued → dispatched → running
→ done` (or `failed` from any non-terminal state) — an attempt count, and the panel and group it belongs to. The Monitor
advances it as output flows: delivery moves it to `dispatched`, the first output to `running`, a settle to `done`, a
process exit or a closed panel to `failed`. A failed task keeps a short **reason** (`panel exited (code N)`, `panel
closed`); finished tasks linger as history, bounded to the most recent 50 so a long session never grows unboundedly.

**The queue** is for work with nowhere to run yet. Instead of dispatching to a named panel, you **enqueue** a brief (with
an optional group) — from the cockpit with `t` (the everyday sibling of `T` dispatch), or over `baton ctl` / MCP — and a
server-owned scheduler drains it onto a free idle agent on a later tick, honouring a per-group concurrency cap. Tasks
drain **highest priority first, then oldest**; a waiting task can be reordered to the head or tail of the backlog. This is
the flagship **you → conductor → fleet** flow: you hand the conductor a batch of briefs, it enqueues them, and they flow
out to whichever workers come free.

**Spawn-on-demand.** A queued task may carry a spawn spec — a command, args, workdir, and a close-on-done flag. When the
scheduler finds no free agent, it **provisions a fresh agent** for such a task (below the 64-panel fleet ceiling),
dispatches the task there, and reaps that agent once the task settles if asked. The backlog then becomes a true work
queue that grows its own ephemeral workers, not just a buffer over the standing fleet. A task with no spawn spec still
only rides the agents already in the fleet.

The backlog is **persistent**. Each task is one JSON file under `$HOME/.baton/<session>.queue/`, written atomically and
removed by the scheduler when the task leaves the backlog — so a daemon restart restores the queue. A task already
assigned to a panel that did not survive the restart is re-queued (its id and attempt count kept) rather than lost. The
queue is **opt-in**: with no `queue` config block the daemon takes dispatches but runs no scheduler, so the auto-drain
never surprises you. `queue.max` caps the unassigned backlog; `queue.concurrency` caps how many of a group's tasks run
at once.

**Managing the backlog.** `Q` (`C-t Q` in a zoom) opens the queue manager — one row per task with its status, id, group,
and brief (a spawn-on-demand task is badged ⚡, a finished one shows its reason). `K` / `J` promote / demote the
highlighted **queued** task to the head / tail of the backlog, `d` cancels it (a task already in flight on a panel is
left to finish; cancel it by closing or signalling its panel), and `D` drains the whole unassigned backlog. The popup
owns no state of its own: every mutation is a server action, and the fresh snapshot it replies with — the live backlog in
run order, finished history below — is what redraws the list.

A plugin can shape tasks as they flow — react to every status change, or rewrite/veto a brief before it is delivered. See
the [task hooks](./PLUGIN.md#tasks-and-the-queue) in PLUGIN.md.

## Find, search, copy

**Find** (`f`, on the dashboard) filters the fleet: type and only panels whose title or group — or a group member's title
— match stay, the heading shows the match count, `enter` keeps the filter and `esc` clears it. **Search** (`C-t f`, in a
zoom or over a focused group tile) runs a case-insensitive regular expression over the scrollback: the view jumps to the
newest hit and holds in scroll mode with the match highlighted, `n` / `N` walk older / newer matches, and a term that is
not a valid regexp is matched literally. **Fleet search** (`/`, on the dashboard; `C-t /` in a zoom) greps _every_ panel
at once: the server scans each panel's retained output for the term and returns the matching lines, which the popup lists
grouped by panel with the term highlighted. `j` / `k` (or `n` / `N`) walk the hits and `enter` zooms the hit's panel,
re-running the term there as a scrollback search so the zoom opens on the match — the fleet grep handing straight off to
the exact per-panel search. It reads the same bounded replay buffer the scrollback shows, so it searches recent output,
not all history, and the same literal-fallback rule applies. **Copy** lives in scroll mode (`C-t [`): `v` starts a
selection and `y` copies
the selected lines — or, with no selection, the visible page — to the system clipboard via OSC52, so it works over SSH
with no helper binary. `V` instead starts a **block** (rectangular) selection — the same rows, but only the columns `[0,
n]`, with `h`/`l` pulling the right edge in and out — so you can lift a narrow left column out of aligned output.

## Mouse

The mouse is **off by default**, so your terminal's own selection and copy stay available. Toggle it in the key map
(`C-t k`, the settings block); once on, the wheel scrolls the scrollback in a zoom or tile and moves the dashboard
selection, and a **left click in the group split focuses the tile under the pointer** (so you can jump to a member instead
of tabbing to it). The toggle persists in the config (`settings.mouse`).

## Settings

A few behaviours are persisted cockpit toggles, edited in the key map's settings block (`C-t k`) and written to the
config:

- **confirm-on-close** (on by default) — closing a single panel with `w` asks `y/n` first. Closing a whole **group**
  always confirms and names how many panels it will retire, regardless of the toggle — a work item never goes in one
  keystroke.
- **allow-name-conflict** (off by default) — lifts the unique-name policy so two work items may share a name.
- **bell** (on by default) — rings the terminal when a panel enters `attention`.
- **mouse** (off by default) — see above.

## Keys

Keys are modal. On the **dashboard** and in a **group** each action fires on a single key; in a **zoom** or **interact**
the keys reach the live program, so a baton action is the leader **`C-t`** then the key. Two escapes — the dashboard jump
and the key-map editor — are reached after the prefix in every mode. Everything here is rebindable in the key map
(`C-t k`); press `?` for the live list of the current view.

| Where                  | Key                         | Does                                            |
| ---------------------- | --------------------------- | ----------------------------------------------- |
| Anywhere (after `C-t`) | `C-t d`                     | go to the dashboard                             |
|                        | `C-t b`                     | back one level (zoom → group → dashboard)       |
|                        | `C-t ~`                     | toggle the floating scratch pane                |
|                        | `C-t [`                     | enter scroll mode                               |
|                        | `C-t k`                     | edit the key map                                |
|                        | `C-t c`                     | open the plugin command picker                  |
|                        | `C-t P`                     | panel config (default shell, workdir, …)        |
|                        | `C-t R`                     | reload config (backend + cockpit)               |
|                        | `C-t S`                     | force-restart the server (kills the fleet)      |
|                        | `C-t D`                     | diff the selected agent panel                   |
|                        | `C-t T`                     | dispatch a task to the zoomed agent             |
|                        | `C-t Q`                     | manage the task queue                           |
|                        | `C-t q`                     | detach (server keeps running)                   |
| Dashboard              | `hjkl` / arrows             | move the cursor                                 |
|                        | `enter`                     | open / zoom the selection                       |
|                        | `p`                         | new shell panel                                 |
|                        | `A`                         | new agent panel                                 |
|                        | `C`                         | open the conductor (find-or-create)             |
|                        | `c`                         | new panel (pick the command)                    |
|                        | `w`                         | close the selection                             |
|                        | `r`                         | re-run exited panel(s) in the selection         |
|                        | `x`                         | purge exited panels                             |
|                        | `s`                         | send a signal to the selection                  |
|                        | `f`                         | find — filter panels by title / group           |
|                        | `/`                         | fleet search — grep every panel's output        |
|                        | `S-←` / `S-→`               | reorder the selected item                       |
|                        | `g`                         | mark / unmark a panel                           |
|                        | `G`                         | group the marked panels                         |
|                        | `a`                         | add marked panels to the selected group         |
|                        | `u`                         | ungroup the selected work item                  |
|                        | `e`                         | rename the panel or group                       |
|                        | `*`                         | favourite the panel / group (sorts to front)    |
|                        | `D`                         | diff the selected agent panel                   |
|                        | `T`                         | dispatch a task to the agent / work item        |
|                        | `Q`                         | manage the task queue (list · cancel · drain)   |
| Group view             | `tab`                       | focus the next panel                            |
|                        | `+` / `-`                   | show more / fewer live tiles                    |
|                        | `L`                         | cycle the tile layout (see [TUI.md](./TUI.md))  |
|                        | `z`                         | resize mode — arrows grow / shrink the tile     |
|                        | `p`                         | pin / unpin the focused panel                   |
|                        | `s`                         | send a signal to the focused panel              |
|                        | `S`                         | send a signal to every panel in the group       |
|                        | `i`                         | interact (type into the focused tile)           |
|                        | `x`                         | remove the focused panel from the group         |
|                        | `S-←` / `S-→`               | reorder the focused panel                       |
|                        | `D`                         | diff the focused agent panel                    |
|                        | `b` / `esc`                 | back one level (sub-group → parent → dashboard) |
|                        | `enter`                     | zoom a panel, or descend into a sub-group       |
| Zoom / interact        | type                        | drive the program directly                      |
|                        | `C-t b`                     | back to the group / dashboard                   |
|                        | `C-t g`                     | git menu (agent panel)                          |
|                        | `C-t C-t`                   | send a literal `C-t`                            |
|                        | `C-t s`                     | send a signal to this panel                     |
|                        | `C-t f`                     | search the scrollback                           |
|                        | `C-t /`                     | fleet search — grep every panel                 |
| Scroll mode (`C-t [`)  | `↑` / `↓` (`k`/`j`)         | scroll a line                                   |
|                        | `b` / `Spc` (`PgUp`/`PgDn`) | scroll a page                                   |
|                        | `g` / `G`                   | jump to top / bottom                            |
|                        | `v` / `y`                   | start a selection / copy to the clipboard       |
|                        | `V` (then `h`/`l`)          | start a block selection / set its columns       |
|                        | `n` / `N`                   | next / previous search match                    |
|                        | `esc` / `q`                 | exit scroll mode                                |

## Architecture

```txt
╔═════════════════════════════════════════════════════════════════════════╗
║                     FRONTENDS (pluggable frontends)                     ║
║                                                                         ║
║   ┌──────────────┐   ┌────────────────┐   ┌──────────────────────────┐  ║
║   │ TUI client   │   │ browser        │   │ Others                   │  ║
║   └───────┬──────┘   └───────┬────────┘   └────────────┬─────────────┘  ║
╚═══════════╪══════════════════╪═════════════════════════╪════════════════╝
            │                  │                         │
            │   Unix domain socket (semantic, versioned, negotiated)
            │   ↑ commands       ↓ events (broadcast, by subscription)
            │                  │                         │
╔═══════════╪══════════════════╪═════════════════════════╪════════════════╗
║           ▼                  ▼                         ▼                ║
║                                                                         ║
║               baton server (daemon, background resident)                ║
║  ┌─────────────────────────────────────────────────────────────┐        ║
║  │  CONNECTION LAYER                                           │        ║
║  │  · multi-client attach / detach / broadcast                 │        ║
║  │  · incoming command  ->  core Action                        │        ║
║  └─────────────────────────────┬───────────────────────────────┘        ║
║                                │                                        ║
║                                ▼                                        ║
║  ┌─────────────────────────────────────────────────────────────┐        ║
║  │  baton.* API  <-- controlled gate (only formal entry) -->   │        ║
║  └────┬─────────────────────┬──────────────────────┬───────────┘        ║
║       │ (socket cmd map)    │ (Lua call map)       │ (event reg)        ║
║       │                     │                      │                    ║
║       │              ┌──────┴──────────┐           │  <- (1) config     ║
║       │              │  LUA RUNTIME    │           │  <- (2) hook       ║
║       │              └──────┬──────────┘           │  <- (3) command    ║
║       ▼                     ▼                      ▼                    ║
║  ┌─────────────────────────────────────────────────────────────┐        ║
║  │  CORE ACTIONS  (single source of truth / single impl)       │        ║
║  │  socket commands and Lua calls all land on this layer       │        ║
║  └─────┬──────────────┬─────────────────┬──────────────────┬───┘        ║
║        │              │                 │                  │            ║
║        ▼              ▼                 ▼                  ▼            ║
║   ┌─────────┐  ┌──────────────┐   ┌───────────┐  ┌───────────────────┐  ║
║   │ STATE   │  │ PTY MANAGER  │   │ MONITOR   │  │ EVENT DISPATCHER  │  ║
║   └─────────┘  └──────┬───────┘   └───────────┘  └───────────────────┘  ║
║                       │                                                 ║
║               ┌───────┴───────┬───────────────┐                         ║
║               ▼               ▼               ▼                         ║
║          ┌─────────┐     ┌─────────┐      ┌─────────┐                   ║
║          │ Panel A │     │ Panel B │      │ Panel C │                   ║
║          └─────────┘     └─────────┘      └─────────┘                   ║
╚═════════════════════════════════════════════════════════════════════════╝
```

The picture, read from top to bottom:

| Block                | Role                                                                           |
| -------------------- | ------------------------------------------------------------------------------ |
| **Frontends**        | Stateless clients — render events, send commands. TUI, browser, …              |
| **The socket**       | The one wire — semantic, versioned, negotiated. Commands up, events down.      |
| **baton server**     | Background daemon owning all state and terminals; outlives any frontend.       |
| **Connection layer** | Multi-client attach / detach / broadcast; commands → core actions.             |
| **baton.\* API**     | The only gate in — socket, Lua, and events all pass through it.                |
| **Lua runtime**      | Config, hooks, and commands as Lua, all calling `baton.*`.                     |
| **Core actions**     | Single source of truth; every request lands here, and only here.               |
| **State**            | Holds panels, work items, tasks, and layout; the queue is snapshotted to disk. |
| **PTY manager**      | Spawns and feeds the real processes behind each panel.                         |
| **Monitor**          | Watches panels for liveness, idleness, and notable output.                     |
| **Event dispatcher** | Broadcasts every change to subscribers and hooks.                              |
| **Panels**           | The live terminals themselves — each an agent or a shell.                      |
