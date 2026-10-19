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

Baton is a single static binary: `brew install cmj0121/tap/baton` on macOS, or on Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/cmj0121/baton/main/scripts/install.sh | sh
```

…or `go install github.com/cmj0121/baton/cmd/baton@latest` with [Go](https://go.dev) 1.26+. Then run `baton`, and:

1. **`A`** spawns an agent (pick a working directory; `enter` lands here, `w` isolates on a branch).
2. **`enter`** zooms in to watch it work; **`C-t d`** pops back to the dashboard.
3. **`q`** detaches — everything keeps running. Come back any time with `baton`.

Lost? **`?`** shows the keys for wherever you are. For a whole fleet, **`n C`** opens a conductor that follows your
standing orders in `$HOME/.baton/CONDUCTOR.md` — see **[docs/CONTROL.md](docs/CONTROL.md)**.

## Concepts

A **panel** is one live terminal running an agent CLI or a shell. A **work item** groups the panels of one task. A
**task** is a brief you dispatch to an agent, queued until it is free. The **conductor** is an agent that drives the
rest of the fleet for you.

You see them through three views: the **dashboard** (every panel as cards or a live tree), a **group** (one work
item's panels streaming side by side) and a **zoom** (one panel as your only terminal, the leader `C-t` to step out).
Behind them, a headless server owns every terminal over a Unix socket, so you detach and reattach without losing a
thing. See **[docs/SPEC.md](docs/SPEC.md)**.

## Why Baton

tmux hands you windows and has no idea what is in them. Baton assumes every pane holds an agent, and builds from there:

- **You see who needs you.** Each panel carries a live state, and the ones waiting on a human queue up in one inbox
  instead of being found by cycling through windows.
- **Work stays grouped.** Panels that serve one task form a work item, made with two keys and driven as one.
- **You hand work out, not keystrokes.** Dispatch a brief to one agent or a whole group, or let a conductor run the
  fleet.
- **The fleet is bounded.** Caps hold a runaway build's whole process tree, and usage is traced back to the panel that
  spent it.
- **The fleet remembers.** What agents keep learning is ranked and handed to the next brief.

Baton does not want your shells — run it inside tmux if that is where you live.

## Features

- **Attention** — panels ranked `running`, `idle`, `done` or `stuck`; `C-t a` is the inbox. [ATTENTION](docs/ATTENTION.md)
- **Conductor** — `n C` drives the fleet through `baton ctl` or `baton mcp`, fenced from its host. [CONTROL](docs/CONTROL.md)
- **Tasks** — `T` dispatches a brief to an agent or a work item; `Q` is a backlog a scheduler drains onto free agents.
- **Score** — what agents keep learning climbs a ladder and leads every brief, kept in `score.md`. [SCORE](docs/SCORE.md)
- **Caps** — CPU, memory and process limits held over a panel's whole process tree. [LIMITS](docs/LIMITS.md)
- **Usage** — `v u` shows tokens, cost and quota bars; `v U` shows which panels spend them. [USAGE](docs/USAGE.md)
- **Isolation** — `isolate: docker` runs a profile's panels in a container on your worktree. [ISOLATION](docs/ISOLATION.md)
- **Fleet grep** — `/` searches every panel's output; `C-t [` scrolls and copies over OSC 52, even over SSH.
- **Backends** — `claude`, `codex`, `gemini`, `aider`, `opencode` and `grok` detected; add your own under `panel.agents`.
- **Remote** — `baton --remote` attaches to a fleet on another machine over your ssh. [REMOTE](docs/REMOTE.md)
- **Plugins** — one Lua file, `$HOME/.baton/plug-in.lua`, hooks events and adds commands. [PLUGIN](docs/PLUGIN.md)
- **The usual** — diff (`D`), a git menu (`C-t G`), signals (`s`), panel logs, serial ports, a restart policy, hot
  reload (`C-t R`), themes and split layouts.

## Essential keys

On the dashboard each action is one key; in a zoom your keystrokes drive the program, so prefix the leader `C-t`.

| Where       | Key           | Does                                                |
| ----------- | ------------- | --------------------------------------------------- |
| Dashboard   | `jk` / `↑↓`   | move the cursor                                     |
|             | `enter`       | open / zoom the selection                           |
|             | `A`           | new agent — workdir, then enter here or `w` isolate |
|             | `n C`         | open the conductor (an agent that drives the fleet) |
|             | `g g` / `g c` | mark / group the marked panels                      |
|             | `T` / `Q`     | dispatch a task / manage the queue                  |
|             | `w` / `x x`   | close the selection / purge exited                  |
| After `C-t` | `d` / `b`     | jump to the dashboard / back one level              |
|             | `a`           | the attention inbox — clear what needs a human      |
|             | `q`           | detach (server keeps running)                       |

Everything else, rebindable with `C-t k`, is in **[docs/KEYS.md](docs/KEYS.md)**.

## Documentation

- **Using** — [SPEC](docs/SPEC.md) · [KEYS](docs/KEYS.md) · [ATTENTION](docs/ATTENTION.md) · [TUI](docs/TUI.md) ·
  [GIT](docs/GIT.md) · [LOGGING](docs/LOGGING.md)
- **Fleet** — [CONTROL](docs/CONTROL.md) · [SCORE](docs/SCORE.md) · [USAGE](docs/USAGE.md) · [LIMITS](docs/LIMITS.md) ·
  [ISOLATION](docs/ISOLATION.md) · [RESTART](docs/RESTART.md)
- **Extending** — [PLUGIN](docs/PLUGIN.md) · [REMOTE](docs/REMOTE.md) · [SERIAL](docs/SERIAL.md)
- **Internals** — [DAEMON](docs/DAEMON.md)

## DDD (Dream-Driven Development)

This project follows DDD (dream-driven development): every feature is built from what I dream of and need.
