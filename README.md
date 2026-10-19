# Baton

> An extensible, agent-friendly terminal multiplexer.

[![Release](https://img.shields.io/github/v/release/cmj0121/baton)](https://github.com/cmj0121/baton/releases/latest)
[![License](https://img.shields.io/github/license/cmj0121/baton)](LICENSE)
[![CI](https://github.com/cmj0121/baton/actions/workflows/ci.yml/badge.svg)](https://github.com/cmj0121/baton/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/cmj0121/baton/branch/main/graph/badge.svg)](https://codecov.io/gh/cmj0121/baton)

**English** · [繁體中文](docs/README.zh-TW.md) · [日本語](docs/README.ja.md) · [한국어](docs/README.ko.md) ·
[Français](docs/README.fr.md) · [Deutsch](docs/README.de.md) · [Español](docs/README.es.md)

Running a handful of AI coding agents at once gets messy fast — windows to juggle, sessions scattered across tabs, and
no single place to see who's working, who's stuck, and who's waiting on you. Baton is to AI agents what tmux is to
shells: **one keyboard-driven cockpit**, a live dashboard of every agent grouped into the tasks they belong to, any one
a keystroke away. You hold the baton. The agents play. You conduct. 🎼

![Baton cockpit demo — the key list, then panels spawned, the conductor opened, two panels grouped into a work item, and the same ? pressed again in the split and in the zoom](docs/assets/baton-demo.png)

_`?` lists the keys for wherever you are: panels spawn, `n C` calls the conductor, `g g` then `g c` groups two into a
work item — and `?` again in the split and `C-t ?` in the zoom show three different tables._

## Get started

Baton is a single static binary. On macOS, `brew install cmj0121/tap/baton`; on Linux, one line does it:

```sh
curl -fsSL https://raw.githubusercontent.com/cmj0121/baton/main/scripts/install.sh | sh
```

…or `go install github.com/cmj0121/baton/cmd/baton@latest` with [Go](https://go.dev) 1.26+, or `make install` from a
clone. Then run `baton`: it starts its background server and drops you on the **dashboard**. Your first minute:

1. Press **`A`** to spawn an agent (pick a working directory; `enter` lands here, `w` isolates on a branch).
2. Press **`enter`** to zoom in and watch it work; **`C-t d`** pops you back to the dashboard.
3. Press **`q`** to detach and walk away — everything keeps running. Come back any time with `baton`.

Lost? **`?`** always shows the keys for wherever you are. A fleet is **`n C`** plus standing orders in
`$HOME/.baton/CONDUCTOR.md` — the conductor spawns the workers and dispatches the work. See
**[docs/CONTROL.md](docs/CONTROL.md)**.

## Concepts

| Concept          | What it is                                                                                                             |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------- |
| **Panel**        | One live terminal — an _agent_ panel (an agent CLI) or a _shell_ panel.                                                |
| **Work item**    | A named group of panels that belong to one task.                                                                       |
| **Task**         | A brief you dispatch to an agent — tracked through its lifecycle, queued and scheduled if it must wait.                |
| **Conductor**    | An agent that drives the fleet for you — spawns, groups, and prompts the other panels over the socket.                 |
| **Global shell** | A singleton plain host shell the server holds in `$HOME`, always one keystroke away — a home base, not a fleet driver. |

You move between three views. The **dashboard** is mission control: a grid of cards for a small fleet, a live tree of
work items and their panels from six things up (`v l` switches by hand, `v p` adds a detail pane). A **group** is a work
item's live split, its panels streaming side by side. A **zoom** is one panel as your only terminal; the leader `C-t` is
how you act or step back out.

A headless **baton server** owns all state and every terminal; frontends attach over a single Unix domain socket, so you
detach and reattach without losing a thing. See **[docs/SPEC.md](docs/SPEC.md)** for the views and the architecture.

## Why Baton

tmux has no idea what is in the pane: you find out an agent has been waiting on you by cycling through windows until you
see it. herdr, Claude Squad and cmux already assume the pane holds an agent, but they are not the same product:

|                      | tmux, by hand                   | Baton                                          | herdr              | Claude Squad     | cmux                 |
| -------------------- | ------------------------------- | ---------------------------------------------- | ------------------ | ---------------- | -------------------- |
| Runtime              | own server                      | own PTY daemon                                 | own PTY daemon     | tmux wrap        | GPU terminal         |
| Who needs you        | cycle the panes and read        | a live state per panel and a `C-t a` inbox     | blocked sidebar    | session list     | notification rings   |
| Isolation            | nothing                         | offer on `A`; first-class `n w`                | workspaces         | spawn = worktree | workspaces           |
| Handing work out     | type it into each pane yourself | work items, dispatch to them, or a conductor   | —                  | —                | —                    |
| Runaway build        | nothing                         | CPU, memory and process caps on the whole tree | —                  | —                | —                    |
| What the fleet costs | nothing                         | tokens, cost and quota bars, traced to a panel | —                  | —                | —                    |
| Fleet memory (Score) | nothing                         | what agents learn, ranked into every brief     | —                  | —                | —                    |
| License              | —                               | `MIT`                                          | `Apache 2.0`       | `AGPL-3.0`       | `GPL-3.0-or-later`   |
| vs tmux              | —                               | not a replacement                              | replacement-shaped | wraps it         | a terminal, not tmux |

Baton does not want your shells — run it inside tmux if that is where you live.

## Features

- **Attention, not polling** — one quiet clock ranks every panel (`running`, `idle`, `done`, `stuck`) and an agent can
  raise its own hand. `C-t a` opens the inbox from any view; `settings.notify` sends a desktop notification when nobody
  is looking. See **[docs/ATTENTION.md](docs/ATTENTION.md)**.
- **A conductor** — `n C` opens an agent that spawns, groups, dispatches and enqueues the fleet over the socket, through
  `baton ctl` or the `baton mcp` tools, fenced so it cannot wreck its own host. See **[docs/CONTROL.md](docs/CONTROL.md)**.
- **Tasks and a backlog** — `T` dispatches a brief to an agent or a whole work item, delivered when it is ready; `Q`
  manages a persistent backlog a server-owned scheduler drains onto free agents.
- **Score, the fleet's memory** — agents submit short observations; what keeps coming back climbs a ladder and the
  highest-ranked few are prepended to every brief, kept in `score.md`. On by default. See **[docs/SCORE.md](docs/SCORE.md)**.
- **Caps over the whole process tree** — CPU, memory and process caps held over a panel's whole tree, a fleet-wide floor
  with per-agent overrides, enforced with cgroup v2 on Linux. See **[docs/LIMITS.md](docs/LIMITS.md)**.
- **Usage traced to a panel** — `v u` cycles a footer of the billing window's tokens and cost, the focused panel's
  share, or your rate-limit bars; `v U` shows which panels are eating them. See **[docs/USAGE.md](docs/USAGE.md)**.
- **Container isolation** — `isolate: docker` on an agent profile runs its panels in a container with the worktree
  mounted; off by default, and not a boundary against a hostile agent. See **[docs/ISOLATION.md](docs/ISOLATION.md)**.
- **Grep the whole fleet** — `/` searches every panel's output, grouped by panel; `C-t f` searches one scrollback, and
  scroll mode (`C-t [`) copies over OSC 52, so it works over SSH.
- **Agent backends** — `claude`, `codex`, `gemini`, `aider`, `opencode` and `grok` are detected on the fleet's machine;
  `C-t P` sets the default, and `panel.agents` adds your own.
- **Remote access** — `baton --remote` attaches the cockpit to a fleet on another machine over the ssh you already use;
  `C-t @` turns it on and lists every connection. See **[docs/REMOTE.md](docs/REMOTE.md)**.
- **Plugins** — one Lua file, `$HOME/.baton/plug-in.lua`, reacts to lifecycle events, drives the fleet, adds commands
  and sets config through one `baton` object. See **[docs/PLUGIN.md](docs/PLUGIN.md)**.

And the cockpit you would expect of a multiplexer:

| Feature              | Key             | What it does                                                      |
| -------------------- | --------------- | ----------------------------------------------------------------- |
| Diff                 | `D`             | the agent panel's work-tree diff, untracked included              |
| Git                  | `C-t G`         | diff, log, status, stage, commit, push, branch and worktrees      |
| Signals              | `s`             | any signal to the selection, the focused tile or the whole group  |
| Group layouts        | `+` `-` `L`     | how many members stream as live tiles, and the shape of the split |
| Global shell         | `n h`           | one plain host shell the server holds in `$HOME`                  |
| Remembered directory | `n .`           | a new shell in the focused panel's live directory                 |
| Panel logging        | `C-t l` `C-t L` | pipe a panel's output to a file, and read it back                 |
| Serial port          | `n c`           | `baton serial /dev/… 115200` — a port in a panel, reconnecting    |
| Persistence          | `r`             | the fleet survives a restart as exited slots you re-run           |
| Restart policy       | —               | `panel.restart: on-failure` brings a panel back with a backoff    |
| Hot reload           | `C-t R`         | config without restarting the fleet — or a `SIGHUP` to the daemon |
| Appearance           | —               | theme, split grids and language in `$HOME/.baton/TUI.yaml`        |

## Essential keys

On the dashboard and in a group each action is one key; in a zoom your keystrokes drive the program, so an action is
the leader `C-t` then the key. `?` lists every key of the current view, and `C-t k` rebinds them.

| Where       | Key               | Does                                                  |
| ----------- | ----------------- | ----------------------------------------------------- |
| Dashboard   | `jk` / `↑↓`       | move the cursor                                       |
|             | `enter`           | open / zoom the selection                             |
|             | `A`               | new agent — workdir, then enter here or `w` isolate   |
|             | `n w`             | isolate first: repo, then branch, fleet-default agent |
|             | `n C`             | open the conductor (an agent that drives the fleet)   |
|             | `g g` / `g c`     | mark / group the marked panels                        |
|             | `T` / `Q` / `I`   | dispatch a task / manage the queue / GitHub issues    |
|             | `/`               | search every panel's output (grep the fleet)          |
|             | `v u` / `v U`     | cycle the usage footer / account usage                |
|             | `w` / `x x`       | close the selection / purge exited                    |
| After `C-t` | `d` / `b`         | jump to the dashboard / back one level                |
|             | `a`               | the attention inbox — clear what needs a human        |
|             | `[`               | enter scroll mode                                     |
|             | `R` / `S`         | reload config / force-restart the server              |
|             | `q`               | detach (server keeps running); bare on the dashboard  |
| Zoom        | `C-t f` / `C-t G` | search the scrollback / git menu (agent)              |

Also: `C-t P` picks the fleet's default agent and `C-t @` manages remote access. The full list is
**[docs/KEYS.md](docs/KEYS.md)**.

## Documentation

- **Using the cockpit** — [SPEC](docs/SPEC.md) (views, panel lifecycle, architecture) · [KEYS](docs/KEYS.md) (every
  key) · [ATTENTION](docs/ATTENTION.md) (the ladder, inbox, notifications) · [TUI](docs/TUI.md) (theme, layouts,
  language) · [GIT](docs/GIT.md) (the git menu) · [LOGGING](docs/LOGGING.md) (panel logs)
- **Running a fleet** — [CONTROL](docs/CONTROL.md) (conductor, `baton ctl`, `baton mcp`) · [SCORE](docs/SCORE.md)
  (fleet memory) · [USAGE](docs/USAGE.md) (footer and quota bars) · [LIMITS](docs/LIMITS.md) (resource caps) ·
  [ISOLATION](docs/ISOLATION.md) (containers) · [RESTART](docs/RESTART.md) (restart policy)
- **Extending and connecting** — [PLUGIN](docs/PLUGIN.md) (the Lua API) · [REMOTE](docs/REMOTE.md) (over SSH) ·
  [SERIAL](docs/SERIAL.md) (serial ports)
- **Internals** — [DAEMON](docs/DAEMON.md) (boot order, readiness, when the server does not come up)

## DDD (Dream-Driven Development)

This project follows DDD (dream-driven development): every feature is built from what I dream of and need.
