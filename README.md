# Baton

> An extensible, agent-friendly terminal multiplexer.

[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

**English** · [繁體中文](docs/README.zh-TW.md) · [日本語](docs/README.ja.md) · [한국어](docs/README.ko.md) ·
[Français](docs/README.fr.md) · [Deutsch](docs/README.de.md) · [Español](docs/README.es.md)

Running a handful of AI coding agents at once? It gets messy fast — windows to juggle, sessions scattered across tabs, and
no single place to see who's working, who's stuck, and who's waiting on you.

Baton is to AI agents what tmux is to shells. It gives you **one keyboard-driven cockpit**: a live dashboard of every
agent, grouped into the tasks they belong to, any one a keystroke away.

You hold the baton. The agents play. You conduct. 🎼

![Baton cockpit demo — panels on a dashboard, zoom to drive one, group into a work item, open the conductor and the global shell](docs/assets/baton-demo.png)

_Spawn panels, zoom into one to drive it, group two into a work item, call the conductor with `C` and the global shell with
`H` — and `?` is always there for the keys._

_Clip generated from [`baton-demo.tape`](docs/assets/baton-demo.tape) — regeneration steps are in the tape header. The
conductor's agent CLI is a stand-in ([`demo-agent.sh`](docs/assets/demo-agent.sh)) so the clip records the same on any
machine; the fleet it drives over the socket is real._

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

- **Dashboard** — mission control. A live **tree** of every panel: work items as rows, their sub-groups indented under
  them, their panels under those. `→` opens a work item and steps inside it, `←` shuts it and steps back out. The row
  carries the state, the working directory, the output sparkline and the dispatched task as the terminal gets wide
  enough for each; `v` adds a detail pane beside it. Here you navigate, spawn and close panels, and group them into work
  items.
- **Group** — a work item's live split: its panels tiled side by side, all streaming at once. The first few stream as
  live tiles; the rest fold into a single **summary tile** you can zoom into. Pin a few to keep them always-on, drive the
  focused one in place with **`i`**, or **`enter`** to drop into it.
- **Zoom** — one panel as your only terminal. Keystrokes go straight to the program; the leader **`C-t`** is how you act
  or step back out.

## Keys

Keys are **modal**: on the dashboard and in a group each action is a single key; in a zoom or interact your keystrokes
drive the program, so a Baton action is the leader **`C-t`** then the key. Press **`?`** for the full, rebindable list of
the current view, and **`C-t k`** to edit the key map.

| Where       | Key               | Does                                                         |
| ----------- | ----------------- | ------------------------------------------------------------ |
| After `C-t` | `d` / `b`         | jump to the dashboard / back one level                       |
|             | `a`               | the attention inbox — clear what needs a human               |
|             | `[`               | enter scroll mode                                            |
|             | `l` / `L`         | log the panel to a file / read that log back                 |
|             | `R` / `S`         | reload config / force-restart the server                     |
|             | `q`               | detach (server keeps running)                                |
| Dashboard   | `jk` / `↑↓`       | move the cursor                                              |
|             | `hl` / `←→`       | collapse / expand a work item — and step out / in            |
|             | `v` / `z`         | detail pane / cycle group-by: work item, dir, profile, state |
|             | `space`           | pick a row up — arrows carry it, `enter` drops it            |
|             | `enter`           | open / zoom the selection                                    |
|             | `p` / `A` / `c`   | new shell / agent / pick-command panel                       |
|             | `.`               | new shell panel in the focused panel's directory             |
|             | `C`               | open the conductor (an agent that drives the fleet)          |
|             | `H`               | open the global shell (a host shell in `$HOME`)              |
|             | `w` / `x`         | close the selection / purge exited                           |
|             | `r`               | re-run the exited panel(s) under the focus                   |
|             | `g` / `G` / `u`   | mark / group marked panels / ungroup                         |
|             | `s` / `f` / `D`   | signal / find / diff the selection                           |
|             | `/`               | search every panel's output (grep the fleet)                 |
|             | `T` / `Q`         | dispatch a task / manage the task queue                      |
|             | `U`               | cycle the usage footer: off / window / focused panel         |
|             | `K`               | toggle the key-press readout in the footer                   |
| Group       | `tab`             | focus the next panel                                         |
|             | `+` / `-`         | show more / fewer live tiles                                 |
|             | `L`               | cycle the tile layout                                        |
|             | `p` / `i`         | pin / interact with the focused panel                        |
|             | `enter`           | zoom the focused panel                                       |
| Zoom        | type              | drive the program directly                                   |
|             | `C-t f` / `C-t g` | search the scrollback / git menu (agent)                     |

See **[docs/SPEC.md](docs/SPEC.md)** for the complete, per-view key reference and the design behind every view.

## Features

Everything you'd reach for while shepherding a fleet, a keystroke away:

- **Agent backends** — baton knows a catalogue of agent CLIs (`claude`, `codex`, `gemini`, `aider`, `opencode`) and
  detects which of them the machine the fleet runs on actually has. `A` lists the ones you can run and spawns the one you
  pick; `C-t P` sets the fleet default from the same list; `C-t R` re-detects after an install. Add your own — or change
  a preset's command, arguments, caps or container — under `panel.agents`. No new key for any of it.
- **Signals** — `s` sends any signal to the selection, the focused tile, or the whole group; the picker lists the common
  ones, `o` types any name or number.
- **Find, search, copy** — `f` filters the fleet by title or group; `/` greps every panel's output at once and lists the
  hits grouped by panel — `enter` zooms the one you pick, landed on the match; `C-t f` regex-searches a panel's scrollback; scroll
  mode (`C-t [`) selects and copies over OSC52, so it works over SSH with no helper binary.
- **Diff** — `D` (or `C-t D` in a zoom) pops up the agent panel's work-tree diff — staged and unstaged at once,
  untracked included — in a master-detail overlay.
- **Git** — `C-t g` opens a git menu against the zoomed agent: diff, log, status, stage, commit, push, branch, and
  worktrees. See **[docs/GIT.md](docs/GIT.md)**.
- **Conductor & control** — `C` opens a conductor: an agent that drives the fleet for you. It spawns, groups, signals,
  and prompts the other panels over the socket — through `baton ctl` or the `baton mcp` tools — fenced so it can't wreck
  its own host. Set its goal in `$HOME/.baton/CONDUCTOR.md`. See **[docs/CONTROL.md](docs/CONTROL.md)**.
- **Global shell** — `H` opens the global shell: a single plain host shell the server holds in `$HOME`, always one
  keystroke away. Like the conductor it is a mark in the FLEET heading rather than a card, and the server keeps just one —
  it survives a restart as a dead slot you re-run with `r`. Unlike the conductor it drives nothing: no scoped role, no
  managed workspace. (Distinct from the floating **scratch** shell `C-t ~`, which is transient and dies on detach.)
- **Tasks & the queue** — `T` dispatches a brief to an agent (or fans it to a whole work item), recorded on the card and
  delivered when the agent is ready. `Q` manages a persistent backlog a server-owned scheduler drains onto free agents —
  the **you → conductor → fleet** flow. A `task.pre` Lua hook can rewrite or veto a brief; `task.change` watches it.
- **Groups & summary** — `+` / `-` dial how many members stream as live tiles; the rest fold into one summary tile.
  Pinned members always stream. `L` cycles the split's **layout** — the even grid, `main-vertical`, `main-horizontal`,
  `stack`, or your own grids from `TUI.yaml`.
- **Resource limits** — cap what a panel may use — CPU, memory, processes — and hold its **whole process tree** to it, so
  a runaway build cannot take the machine with it. Set a fleet-wide floor and per-agent overrides in the config or under
  `C-t P`; `C-t R` applies them to the running fleet. Enforced with cgroup v2 on Linux, and the panel says plainly when a
  host cannot enforce them. See **[docs/LIMITS.md](docs/LIMITS.md)**.
- **Container isolation** — opt-in per agent profile: `isolate: docker` runs that profile's panels inside a container
  with your worktree mounted, so an agent that goes wrong is confined to a workspace. You name the image (Baton ships
  none); `mount`, `network`, `env-allow` and `user` decide what else crosses, and nothing from your environment does
  unless you name it. The caps still apply, enforced by the runtime. Off by default, and not a boundary against a
  hostile agent. See **[docs/ISOLATION.md](docs/ISOLATION.md)**.
- **Restart policy** — off by default; set `panel.restart: on-failure` and a panel whose process dies abnormally comes
  back, with an exponential backoff and a limit that settles it loudly rather than looping. A clean exit, a panel you
  closed, and one you signalled are all left alone. Overridable per agent profile.
- **Remembered directory** — a panel's live working directory is tracked from the shell's own OSC 7 report, or the
  process table when it makes none. A re-run lands where you were, `.` opens a shell in the focused panel's directory,
  the path identifies the card, and the git menus follow an agent into a worktree. See
  **[docs/RESTART.md](docs/RESTART.md)**.
- **Appearance** — `$HOME/.baton/TUI.yaml` reshapes the cockpit: a colour **theme** and the group-split **layouts**,
  hot-reloaded with `C-t R`. See **[docs/TUI.md](docs/TUI.md)**.
- **Usage footer** — `U` cycles a footer readout of the billing window: the account's token usage and cost with a
  countdown to the reset (`⊙ 1.2M tok · ≈$12.34 API · ⏳ 2:14:31`), or the focused panel's share of that window. It
  reads Claude Code's own transcripts by default (works on a Pro/Max subscription) or the Anthropic Admin API with a key.
  The cost is API-equivalent, not a subscription charge. See **[docs/USAGE.md](docs/USAGE.md)**.
- **Panel logging** — `C-t l` pipes a panel's output to a file on the machine the fleet runs on, flushing the replay
  buffer in first so you keep what made you press it; `C-t L` reads it back in a temporary panel that follows the file.
  Plain text, escape sequences stripped, rolled at `log-max-mb`. Off until you set `panel.log-dir`; a profile can log
  from spawn. See **[docs/LOGGING.md](docs/LOGGING.md)**.
- **Remote access** — `baton --remote` attaches the same cockpit to a fleet on **another machine**, over the ssh you
  already use to reach it: no listening port, no TLS, no key exchange of baton's own. Off by default; `settings.remote`
  or `C-t @` turns it on and mints an 8-character passkey that is never written to disk. `C-t @` also lists every live
  connection with its source, role and duration — `k` kicks one, `n` rotates the passkey, `x` shuts remote down. See
  **[docs/REMOTE.md](docs/REMOTE.md)**.
- **Persistence & respawn** — Baton remembers its fleet across a restart; panels come back as inert exited slots and
  `r` re-runs them from their retained spec.
- **Reload** — `C-t R` (or a `SIGHUP` to the daemon) hot-reloads config without restarting the fleet.
- **Mouse** — off by default so your terminal's own selection stays available; toggle it in the key map to scroll and
  select with the wheel.
- **Language** — the `?` key list and the `C-t k` key map read in English or 繁體中文. Set `settings.language`, cycle it
  live from the key map, or just let your `$LANG` decide. See **[docs/TUI.md](docs/TUI.md#language)**.

## Screensaver

Walk away and let it sit. After a few idle minutes — or on the hidden `C-t E` — the cockpit drops into a full-screen
Matrix rain with the **BATON** wordmark and a big clock floating in the middle. It's a frontend-only takeover: nothing is
sent to the server, and any key or click brings your view straight back.

![Baton screensaver — a Matrix digital rain with the BATON wordmark and a big clock](docs/assets/baton-screensaver.png)

_Clip generated from [`baton-screensaver.tape`](docs/assets/baton-screensaver.tape) — regeneration steps are in the tape header._

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
- **[docs/USAGE.md](docs/USAGE.md)** — the account usage footer: the local and Admin-API sources, config, and caveats.
- **[docs/PLUGIN.md](docs/PLUGIN.md)** — the Lua plugin API: the `baton` object, events, commands, and config.
- **[docs/CONTROL.md](docs/CONTROL.md)** — driving the fleet by agent: the conductor, the `baton ctl` CLI, the
  `baton mcp` tools, and the guardrails.

## DDD (Dream-Driven Development)

This project follows DDD (dream-driven development): every feature is built from what I dream of and need.
