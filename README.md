# Baton

> A next-gen, extensible, agent-friendly terminal multiplexer.

Baton is to AI agents what tmux is to shells. Instead of juggling windows and scattered CLI sessions, you run one
keyboard-driven cockpit: a live dashboard of every agent, grouped into work items, any one a keystroke away.

You hold the baton. The agents play. You conduct.

## Concept

- **Agents, not shells.** The unit of work is a running agent, not a window to babysit.
- **Dashboard, not windows.** A live overview of everything at once, not a pile of tabs.
- **Headless core, replaceable frontends.** The brain is a background daemon; the face that renders it is swappable.

| Concept       | What it is                                                              |
| ------------- | ----------------------------------------------------------------------- |
| **Panel**     | One live terminal — an _agent_ panel (an agent CLI) or a _shell_ panel. |
| **Work item** | A named group of panels that belong to one task.                        |

## Views

You drive Baton through three views, moving between them with a keystroke:

- **Dashboard** — mission control. A live grid (a tree once it gets crowded) of every panel with its status and a
  preview. Here you navigate, spawn and close panels, and group them into work items.
- **Group** — a work item's live split: its panels tiled side by side, all streaming at once. The first few stream as
  live tiles; the rest fold into a single **summary tile** you can zoom into. Pin a few to keep them always-on, drive the
  focused one in place with **`i`**, or **`enter`** to drop into it.
- **Zoom** — one panel as your only terminal. Keystrokes go straight to the program; the leader **`C-t`** is how you act
  or step back out.

## Keys

Keys are modal: on the **dashboard** and in a **group** each action is a single key; in a **zoom** or **interact**
keystrokes drive the program, so a Baton action is the leader **`C-t`** then the key. Press **`?`** for the full,
rebindable list of the current view.

| Where                  | Key                         | Does                                      |
| ---------------------- | --------------------------- | ----------------------------------------- |
| Anywhere (after `C-t`) | `C-t d`                     | go to the dashboard                       |
|                        | `C-t g`                     | go to the group view                      |
|                        | `C-t [`                     | enter scroll mode                         |
|                        | `C-t k`                     | edit the key map                          |
|                        | `C-t P`                     | panel config (default shell, workdir, …)  |
|                        | `C-t R`                     | reload config (backend + cockpit)         |
|                        | `C-t D`                     | diff the selected agent panel             |
|                        | `C-t q`                     | detach (server keeps running)             |
| Dashboard              | `hjkl` / arrows             | move the cursor                           |
|                        | `enter`                     | open / zoom the selection                 |
|                        | `p`                         | new shell panel                           |
|                        | `A`                         | new agent panel                           |
|                        | `c`                         | new panel (pick the command)              |
|                        | `w`                         | close the selection                       |
|                        | `r`                         | re-run the selected exited panel          |
|                        | `x`                         | purge exited panels                       |
|                        | `s`                         | send a signal to the selection            |
|                        | `f`                         | find — filter panels by title / group     |
|                        | `S-←` / `S-→`               | reorder the selected item                 |
|                        | `g`                         | mark / unmark a panel                     |
|                        | `G`                         | group the marked panels                   |
|                        | `a`                         | add marked panels to the selected group   |
|                        | `u`                         | ungroup the selected work item            |
|                        | `e`                         | rename the panel or group                 |
|                        | `D`                         | diff the selected agent panel             |
| Group view             | `tab`                       | focus the next panel                      |
|                        | `+` / `-`                   | show more / fewer live tiles              |
|                        | `p`                         | pin / unpin the focused panel             |
|                        | `s`                         | send a signal to the focused panel        |
|                        | `S`                         | send a signal to every panel in the group |
|                        | `i`                         | interact (type into the focused tile)     |
|                        | `x`                         | remove the focused panel from the group   |
|                        | `S-←` / `S-→`               | reorder the focused panel                 |
|                        | `D`                         | diff the focused agent panel              |
|                        | `enter`                     | zoom the focused panel                    |
| Zoom / interact        | type                        | drive the program directly                |
|                        | `C-t C-t`                   | send a literal `C-t`                      |
|                        | `C-t s`                     | send a signal to this panel               |
|                        | `C-t f`                     | search the scrollback                     |
| Scroll mode (`C-t [`)  | `↑` / `↓` (`k`/`j`)         | scroll a line                             |
|                        | `b` / `Spc` (`PgUp`/`PgDn`) | scroll a page                             |
|                        | `g` / `G`                   | jump to top / bottom                      |
|                        | `v` / `y`                   | start a selection / copy to the clipboard |
|                        | `n` / `N`                   | next / previous search match              |
|                        | `esc` / `q`                 | exit scroll mode                          |

Names stay unique unless you set `allow-name-conflict`.

Closing with **`w`** asks `y/n` first: a single panel honours the **confirm on
close** toggle (on by default, in the key map's settings block), while closing a
**group** always confirms and names how many panels it will retire — a whole work
item never goes in one keystroke.

`C-t R` reloads the config without a restart: the daemon re-reads its settings
(name policy, default workdir, replay buffer) while the fleet keeps running, and
the cockpit refreshes its own (key map, toggles, panel defaults). Sending the
daemon `SIGHUP` (e.g. `kill -HUP $(cat ~/.baton/*.pid)`) does the backend half.

`s` opens the signal picker — one keycap per signal (`SIGINT`, `SIGTERM`,
`SIGKILL`, `SIGHUP`, `SIGQUIT`, `SIGUSR1`, `SIGUSR2`), `↑↓`+`enter` to choose, or
`o` for **other…** to type any name or number (`WINCH`, `TSTP`, `28`). Target by
view: the selection on the dashboard (a panel, or every live member of a group
card), this panel in a zoom (`C-t s`), the focused panel in the split (`s`) or
every member (`S`). Exited panels are skipped, so the count is what's delivered.

The signal goes to the panel's whole **process group**, so it reaches the
foreground job, not just the shell — but a child that daemonizes into its own
group escapes it. Note this is the panel's `SIGHUP`, unrelated to baton's own
`C-t R` config reload. Delivery is fire-and-forget: a process that traps or
ignores a signal still shows as sent.

**Find, search, copy.** On the dashboard, **`f`** filters the fleet — type to keep
only panels whose title or group (or a group member's title) matches; the heading
shows the match count, `enter` keeps the filter and `esc` clears it. In a zoom (or
over a focused group tile), **`C-t f`** searches the scrollback with a
case-insensitive regular expression: the view jumps to the newest hit and holds
in scroll mode with the match highlighted, and `n` / `N` walk older / newer
matches. A term that is not a valid regexp is matched literally. In scroll mode, **`v`** marks a selection and **`y`**
copies the selected lines — or, with no selection, the visible page — to the
system clipboard via OSC52, so it works over SSH with no helper binary.

**Diff.** **`D`** on the dashboard or in a group split (`C-t D` from a zoom)
pops up the working-tree diff of the focused agent panel — the binding name is
`diff`, rebindable in the key map (`C-t k`). Only an agent panel is a valid
target; a shell, a group card, or an empty selection just hints `diff: select an
agent panel`. The server checks the agent's workdir is inside a git work tree
first — if not, the status line reads `not a git repository: <dir>`, and a clean
tree reads `no uncommitted changes`. The diff is the working tree against `HEAD`,
**untracked files included** (a brand-new file a tracked-only check would miss is
the most common agent output), computed without touching the index, worktree, or
refs. It opens as a **transient, auto-zoomed panel**: it never lands on the
dashboard and is never persisted, and you dismiss it with the normal zoom exit
**`C-t d`** (or `C-t q` to detach) — that closes it. Inside it you get the usual
scroll mode (`C-t [`), scrollback search (`C-t f`), and copy. The tool is
`panel.diff-command` in the config, resolved in order: an explicit
`panel.diff-command` (run via `sh -c`, so a full shell line or pipe works, e.g.
`git diff HEAD | delta`); else the repo's own `git config diff.tool` (run via
`git difftool -d --no-prompt`, which keeps git's tracked-only difftool
semantics); else a built-in `git diff` that includes untracked files and pages
naturally. If `diff.tool` is a GUI tool and the daemon runs headless or over
SSH with no display, `git difftool` errors or hangs — contained to that panel,
not fatal; on headless/remote hosts prefer a terminal diff (leave `diff.tool`
unset for the built-in, or set `panel.diff-command` to a terminal tool like
`git diff | delta`). The diff pages through git's pager and is navigable, but
if the repo disables the pager (`core.pager=cat`) or your `panel.diff-command`
doesn't page, a very large diff dumps in and only the last `panel.replay-kb` is
kept in scrollback — the top truncates. A connection holds at most 8 open diff
pop-ups at once; past that the key reports `too many open diffs — close one
first`.

**Mouse.** Off by default, so your terminal's own selection and copy stay
available. Toggle it in the key map (`C-t k`, the settings block); once on, the
wheel scrolls the scrollback in a zoom or tile and moves the dashboard selection.

**Split & summary.** In a group, **`+`** / **`-`** dial how many members stream
as live tiles — server-owned, clamped to `1`–`16`, and remembered across a
restart. Pinned members are always tiles. Everyone past that count folds into one
**summary tile**: a rollup of the hidden members' count, their per-state
breakdown, and the most urgent activity line. Focus it and press **`enter`** to
zoom into a sub-grid of just those collapsed members; **`esc`** returns to the
parent group, not the dashboard.

**Layout, restart, respawn.** Baton remembers its fleet. The daemon writes the
layout — each panel's spawn spec (command, args, workdir), group membership,
pins, order, and every group's visible-tile count — to a per-session state file
on each change, and rebuilds it on the next start. Restore is inert: panels come
back as **exited dead slots**, never auto-respawned (shells included). Press
**`r`** on the dashboard to re-run the selected exited panel from its retained
spec; closing or purging a panel drops its spec for good. The state file lives
beside the socket and pid file, so one daemon-per-session owns one layout; an
unreadable or newer-schema file is renamed aside rather than wedging the daemon.

## Architecture

A headless **baton server** (a background daemon) owns all state and every terminal. Pluggable frontends attach over a
single Unix domain socket — commands up, events down — so you detach and reattach without losing a thing.

See [docs/SPEC.md](docs/SPEC.md) for the full diagram and interaction model.

## DDD (Dream-Driven Development)

This project follows DDD (dream-driven development): every feature is built from what I dream of and need.
