# Baton

> An extensible, agent-friendly terminal multiplexer.

[![Release](https://img.shields.io/github/v/release/cmj0121/baton)](https://github.com/cmj0121/baton/releases/latest)
[![License](https://img.shields.io/github/license/cmj0121/baton)](LICENSE)
[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

**English** · [繁體中文](docs/README.zh-TW.md) · [日本語](docs/README.ja.md) · [한국어](docs/README.ko.md) ·
[Français](docs/README.fr.md) · [Deutsch](docs/README.de.md) · [Español](docs/README.es.md)

Running a handful of AI coding agents at once? It gets messy fast — windows to juggle, sessions scattered across tabs, and
no single place to see who's working, who's stuck, and who's waiting on you.

Baton is to AI agents what tmux is to shells. It gives you **one keyboard-driven cockpit**: a live dashboard of every
agent, grouped into the tasks they belong to, any one a keystroke away.

You hold the baton. The agents play. You conduct. 🎼

![Baton cockpit demo — the key list, then panels spawned, the conductor opened, two panels grouped into a work item, and the same ? pressed again in the split and in the zoom](docs/assets/baton-demo.png)

_One key does the whole tour: `?` lists the keys for wherever you are. Panels spawn, `n C` calls the conductor, `g g`
then `g c` groups two into a work item — and `?` again in the split, `C-t ?` in the zoom, are three different tables._

_Recorded from [`baton-demo.tape`](docs/assets/baton-demo.tape); the conductor's agent CLI is a stand-in
([`demo-agent.sh`](docs/assets/demo-agent.sh)) so the clip records the same on any machine, and the fleet it drives over
the socket is real._

## Get started

Baton is a single static binary. On macOS, tap it with [Homebrew](https://brew.sh):

```sh
brew install cmj0121/tap/baton
```

On Linux, one line does it:

```sh
curl -fsSL https://raw.githubusercontent.com/cmj0121/baton/main/scripts/install.sh | sh
```

…or, on any platform, grab it with [Go](https://go.dev) 1.26+:

```sh
go install github.com/cmj0121/baton/cmd/baton@latest
```

…or build from a clone with `make install`. Then just run:

```sh
baton
```

Baton starts its background server and drops you on the **dashboard** — your home base. Your first minute:

1. Press **`A`** to spawn an agent (you'll pick a working directory for it).
2. Press **`enter`** to zoom in and watch it work; **`C-t d`** pops you back to the dashboard.
3. Press **`q`** to detach and walk away — everything keeps running. Come back any time with `baton`.

Lost? **`?`** always shows the keys for wherever you are.

## Why not just tmux?

Because tmux has no idea what is in the pane. It hands you windows; you supply the memory of which one is which, and you
find out an agent has been waiting on you by cycling through them until you see it. Baton assumes the pane holds an
agent, and the rest follows from that:

| What you are doing            | tmux, by hand                         | Baton                                                                             |
| ----------------------------- | ------------------------------------- | --------------------------------------------------------------------------------- |
| Finding who needs you         | cycle the panes and read              | a live state per panel, and a `C-t a` inbox of the ones that stopped for a human  |
| Keeping related work together | name the windows, remember the scheme | work items — a named group of panels, made with two keys                          |
| Handing work out              | type it into each pane yourself       | dispatch a task to one or a whole group, or let a conductor agent drive the fleet |
| Stopping a runaway build      | nothing                               | CPU, memory and process caps, held over the panel's whole process tree            |
| Knowing what the fleet costs  | nothing                               | the billing window's tokens and cost, and your quota bars, traced to a panel      |

Baton is not a tmux replacement and does not want your shells — run it inside tmux if that is where you live.

## Concept

- **Agents, not shells.** The unit of work is a running agent, not a window to babysit.
- **Dashboard, not windows.** A live overview of everything at once, not a pile of tabs.
- **Headless core, replaceable frontends.** The brain is a background daemon; the face that renders it is swappable.

| Concept          | What it is                                                                                                             |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------- |
| **Panel**        | One live terminal — an _agent_ panel (an agent CLI) or a _shell_ panel.                                                |
| **Work item**    | A named group of panels that belong to one task.                                                                       |
| **Task**         | A brief you dispatch to an agent — tracked through its lifecycle, queued and scheduled if it must wait.                |
| **Conductor**    | An agent that drives the fleet for you — spawns, groups, and prompts the other panels over the socket.                 |
| **Global shell** | A singleton plain host shell the server holds in `$HOME`, always one keystroke away — a home base, not a fleet driver. |

## Views

You drive Baton through three views, moving between them with a keystroke:

- **Dashboard** — mission control. A small fleet is a grid of **cards**, one per panel and one per work item; from six
  things at the top level up it becomes a live **tree** of every panel: work items as rows, their sub-groups indented under them,
  their panels under those. `space` shows or hides what is nested under a row at any depth, `→` opens a work item and
  steps inside it, `←` shuts it and steps back out — on the cards that pair moves between them instead. `v l` switches the
  two layouts by hand. The tree row carries
  the state, the working directory, the output sparkline and the dispatched task as the terminal gets wide enough for
  each; `v p` adds a detail pane beside it. Here you navigate, spawn and close panels, and group them into work items.
- **Group** — a work item's live split: its panels tiled side by side, all streaming at once. The first few stream as
  live tiles; the rest fold into a single **summary tile** you can zoom into. Pin a few to keep them always-on, drive the
  focused one in place with **`i`**, or **`enter`** to drop into it.
- **Zoom** — one panel as your only terminal. Keystrokes go straight to the program; the leader **`C-t`** is how you act
  or step back out.

## Keys

Keys are **modal**: on the dashboard and in a group each action is a single key; in a zoom or interact your keystrokes
drive the program, so a Baton action is the leader **`C-t`** then the key. Press **`?`** for the full, rebindable list of
the current view, and **`C-t k`** to edit the key map. Four keys are **landings** that open a family rather than acting
alone — `n` spawns, `v` draws, `g` groups, `x` is the double tap that confirms itself — and the status bar names what
each can take next.

| Where       | Key               | Does                                                         |
| ----------- | ----------------- | ------------------------------------------------------------ |
| After `C-t` | `d` / `b`         | jump to the dashboard / back one level                       |
|             | `a`               | the attention inbox — clear what needs a human               |
|             | `[`               | enter scroll mode                                            |
|             | `l` / `L`         | log the panel to a file / read that log back                 |
|             | `R` / `S`         | reload config / force-restart the server                     |
|             | `q`               | detach (server keeps running)                                |
| Dashboard   | `jk` / `↑↓`       | move the cursor                                              |
|             | `hl` / `←→`       | move a card · in the tree: collapse / expand a work item     |
|             | `space`           | show / hide what is nested under the row                     |
|             | `v p` / `v g`     | detail pane / cycle group-by: work item, dir, profile, state |
|             | `v l`             | the dashboard layout: cards or tree                          |
|             | `m`               | pick a row up — arrows carry it, `enter` drops it            |
|             | `enter`           | open / zoom the selection                                    |
|             | `p` / `A` / `n c` | new shell / agent / pick-command panel                       |
|             | `n .`             | new shell panel in the focused panel's directory             |
|             | `n C`             | open the conductor (an agent that drives the fleet)          |
|             | `n h`             | open the global shell (a host shell in `$HOME`)              |
|             | `w` / `x x`       | close the selection / purge exited                           |
|             | `r`               | re-run the exited panel(s) under the focus                   |
|             | `g g` / `g c`     | mark / group the marked panels                               |
|             | `g a` / `g u`     | add to the selected work item / ungroup                      |
|             | `s` / `f` / `D`   | signal / find / diff the selection                           |
|             | `/`               | search every panel's output (grep the fleet)                 |
|             | `T` / `Q`         | dispatch a task / manage the task queue                      |
|             | `v u`             | cycle the usage footer: off / window / focused panel / quota |
|             | `v U`             | account usage — quota bars, and who is spending them         |
|             | `v k`             | toggle the key-press readout in the footer                   |
| Group       | `tab`             | focus the next panel                                         |
|             | `+` / `-`         | show more / fewer live tiles                                 |
|             | `L`               | cycle the tile layout                                        |
|             | `p` / `i`         | pin / interact with the focused panel                        |
|             | `enter`           | zoom the focused panel                                       |
| Zoom        | type              | drive the program directly                                   |
|             | `C-t f` / `C-t G` | search the scrollback / git menu (agent)                     |

See **[docs/KEYS.md](docs/KEYS.md)** for the complete key reference, and **[docs/SPEC.md](docs/SPEC.md)** for the design
behind every view.

## Features

Five things Baton does that a terminal multiplexer does not:

- **Attention, not polling** — a fleet is mostly fine; you are looking at the screen because of the few panels that are
  not. One quiet clock ranks every one of them — `running`, `idle` at ten seconds, `done` for an agent that finished its
  turn, `stuck` when it has gone on too long — and an agent can raise its own hand above the lot. `C-t a` opens the
  inbox from any view and the queue is cleared from there; `settings.notify` sends an OSC 9 desktop notification when
  nobody is looking, coalesced, and never for `done`. See **[docs/ATTENTION.md](docs/ATTENTION.md)**.
- **A conductor** — `n C` opens an agent that drives the fleet for you: it spawns, groups, signals and prompts the other
  panels over the socket, through `baton ctl` or the `baton mcp` tools, fenced so it cannot wreck its own host. Set its
  goal in `$HOME/.baton/CONDUCTOR.md`. See **[docs/CONTROL.md](docs/CONTROL.md)**.
- **Tasks and a backlog** — `T` dispatches a brief to an agent, or fans it across a whole work item; it is recorded on
  the card and delivered when the agent is ready. `Q` manages a persistent backlog that a server-owned scheduler drains
  onto free agents. A `task.pre` Lua hook can rewrite or veto a brief; `task.change` watches it.
- **Caps over the whole process tree** — cap what a panel may use, CPU, memory and processes, and hold its **whole
  process tree** to it, so a runaway build cannot take the machine with it. A fleet-wide floor with per-agent overrides,
  applied to the running fleet by `C-t R`, enforced with cgroup v2 on Linux — and the panel says plainly when a host
  cannot enforce them. See **[docs/LIMITS.md](docs/LIMITS.md)**.
- **Usage traced to a panel** — `v u` cycles a footer readout: the billing window's tokens and cost with a countdown
  (`⊙ 1.2M tok · ≈$12.34 API · ⏳ 2:14:31`), the focused panel's share of it, or your account's rate-limit bars
  (`⊙ 5h ▓▓▓▓▓░░░ 2:14:31 · 7d ▓▓▓░░░░░ 3d4h`). `v U` opens the lot — every quota window, the extra-usage credit, and
  which panels are eating them. See **[docs/USAGE.md](docs/USAGE.md)**.

Four more that most of them do not have either:

- **Container isolation** — opt in per agent profile with `isolate: docker`, and that profile's panels run inside a
  container with your worktree mounted. You name the image (Baton ships none); `mount`, `network`, `env-allow` and
  `user` decide what else crosses, and nothing from your environment does unless you name it. Off by default, and not a
  boundary against a hostile agent. See **[docs/ISOLATION.md](docs/ISOLATION.md)**.
- **Grep the whole fleet** — `/` searches every panel's output at once and lists the hits grouped by panel; `enter`
  zooms the one you pick, landed on the match. `C-t f` regex-searches a single scrollback, and scroll mode (`C-t [`)
  selects and copies over OSC 52, so it works over SSH with no helper binary.
- **Agent backends** — Baton knows a catalogue of agent CLIs (`claude`, `codex`, `gemini`, `aider`, `opencode`, `grok`)
  and detects which of them the machine the fleet runs on actually has. `A` spawns the one you pick; `C-t P` sets the
  fleet default and names the ones this machine has not got with where to get each; `C-t R` re-detects after an install.
  Add your own — command, arguments, caps or container — under `panel.agents`.
- **Remote access** — `baton --remote` attaches the same cockpit to a fleet on **another machine**, over the ssh you
  already use to reach it: no listening port, no TLS, no key exchange of Baton's own. Off by default; `C-t @` turns it
  on, mints a passkey that is never written to disk, and lists every live connection to kick, rotate or shut down. See
  **[docs/REMOTE.md](docs/REMOTE.md)**.

And the cockpit you would expect of a multiplexer, each a keystroke away:

| Feature              | Key             | What it does                                                                                  |
| -------------------- | --------------- | --------------------------------------------------------------------------------------------- |
| Diff                 | `D`             | the agent panel's work-tree diff — staged and unstaged at once, untracked included            |
| Git                  | `C-t G`         | diff, log, status, stage, commit, push, branch and worktrees — **[docs/GIT.md](docs/GIT.md)** |
| Signals              | `s`             | any signal to the selection, the focused tile or the whole group                              |
| Find                 | `f`             | filter the fleet by title or group                                                            |
| Group layouts        | `+` `-` `L`     | how many members stream as live tiles, and the shape of the split                             |
| Global shell         | `n h`           | one plain host shell the server holds in `$HOME`, always one keystroke away                   |
| Remembered directory | `n .`           | panels track their live directory from OSC 7 — **[docs/RESTART.md](docs/RESTART.md)**         |
| Panel logging        | `C-t l` `C-t L` | pipe a panel's output to a file, and read it back — **[docs/LOGGING.md](docs/LOGGING.md)**    |
| Persistence          | `r`             | the fleet survives a restart as exited slots you re-run from their retained spec              |
| Restart policy       | —               | `panel.restart: on-failure` brings a panel back with a backoff and a limit                    |
| Hot reload           | `C-t R`         | config without restarting the fleet — or a `SIGHUP` to the daemon                             |
| Appearance           | —               | theme and custom split grids in `$HOME/.baton/TUI.yaml` — **[docs/TUI.md](docs/TUI.md)**      |
| Mouse                | —               | off by default, so your terminal's own selection stays available                              |
| Language             | —               | the key list reads in English or 繁體中文 — **[docs/TUI.md](docs/TUI.md#language)**           |
| Screen protector     | —               | a full-screen digital rain when the cockpit rests — **[docs/TUI.md](docs/TUI.md)**            |

## Architecture

A headless **baton server** (a background daemon) owns all state and every terminal. Pluggable frontends attach over a
single Unix domain socket — commands up, events down — so you detach and reattach without losing a thing.

See **[docs/SPEC.md](docs/SPEC.md)** for the full diagram and interaction model.

## Plugins

A single Lua file (`$HOME/.baton/plug-in.lua`) reshapes Baton to your workflow: react to lifecycle events (ping you when
an agent needs you, chain the next step when one finishes), drive the fleet, add your own commands, and set config — all
through one `baton` object. See **[docs/PLUGIN.md](docs/PLUGIN.md)**.

## Documentation

- **[docs/SPEC.md](docs/SPEC.md)** — the full specification: views, the panel lifecycle, work items, signals, diff,
  persistence, the per-view key reference, and the architecture diagram.
- **[docs/ATTENTION.md](docs/ATTENTION.md)** — attention at scale: the quiet ladder (`done`, `stuck`, failed), the
  `C-t a` inbox, the dashboard folds, desktop notifications, and every knob they take.
- **[docs/TUI.md](docs/TUI.md)** — the cockpit appearance file (`$HOME/.baton/TUI.yaml`): the colour theme and the
  group-split layouts (presets and custom grids).
- **[docs/LIMITS.md](docs/LIMITS.md)** — resource limits: the config, the two layers, hot reload, and where they are
  actually enforced.
- **[docs/ISOLATION.md](docs/ISOLATION.md)** — container isolation: the per-profile config, what the agent keeps, how
  the caps are enforced inside a container, and what it is not a boundary for.
- **[docs/RESTART.md](docs/RESTART.md)** — the restart policy: what is and is not a failure, the backoff and the limit,
  and why there is no `always`.
- **[docs/GIT.md](docs/GIT.md)** — the git menu: every op, the commit-editor flow, worktrees, and the config.
- **[docs/LOGGING.md](docs/LOGGING.md)** — panel logging: what is written, where it lands, the session markers, the
  roll, and what it is not a boundary for.
- **[docs/REMOTE.md](docs/REMOTE.md)** — remote access over SSH: the `--stdio` bridge, the passkey and what it is and
  is not, the `C-t @` connection list, and the failures it reports.
- **[docs/USAGE.md](docs/USAGE.md)** — the account usage footer and quota bars: every source, the config, and caveats.
- **[docs/PLUGIN.md](docs/PLUGIN.md)** — the Lua plugin API: the `baton` object, events, commands, and config.
- **[docs/CONTROL.md](docs/CONTROL.md)** — driving the fleet by agent: the conductor, the `baton ctl` CLI, the
  `baton mcp` tools, and the guardrails.
- **[docs/SCORE.md](docs/SCORE.md)** — Score, the fleet's memory: the `score.md` file and its one undo, the tier
  ladder, the ranking weights, compaction, and what it is not a boundary for.

## DDD (Dream-Driven Development)

This project follows DDD (dream-driven development): every feature is built from what I dream of and need.
