# Baton — Specification

Design detail behind the concept sketched in the [README](../README.md). Start there for the pitch and vocabulary;
this document covers how the pieces fit together.

## Two views, one cockpit

Baton is keyboard-driven and has exactly two ways to look at your agents:

- **Dashboard** — see everything at once. Navigate panels, spawn new ones, group them into work items, retire the dead ones.
- **Zoom** — see one thing fully. Drive a single panel as if it were your only terminal, then pop back out to the dashboard.

You never juggle windows or tabs. You conduct from the dashboard, and you zoom in only when a player needs you.

## Panels

A panel is one PTY (pseudo-terminal) the server owns. There are two kinds:

- **Agent panel** — runs an agent CLI directly as the panel's process (for example `claude` or `copilot`, per the default
  settings). There is no shell and no shell prompt in between; the agent CLI _is_ the program the PTY runs.
- **Shell panel** — runs a plain host shell, for ad-hoc commands on the machine.

Both are ordinary PTYs and share the lifecycle below; they differ only in what process they launch and in how loudly the
Monitor flags them for your attention.

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

| Block                | Role                                                                      |
| -------------------- | ------------------------------------------------------------------------- |
| **Frontends**        | Stateless clients — render events, send commands. TUI, browser, …         |
| **The socket**       | The one wire — semantic, versioned, negotiated. Commands up, events down. |
| **baton server**     | Background daemon owning all state and terminals; outlives any frontend.  |
| **Connection layer** | Multi-client attach / detach / broadcast; commands → core actions.        |
| **baton.\* API**     | The only gate in — socket, Lua, and events all pass through it.           |
| **Lua runtime**      | Config, hooks, and commands as Lua, all calling `baton.*`.                |
| **Core actions**     | Single source of truth; every request lands here, and only here.          |
| **State**            | Holds panels, work items, and layout.                                     |
| **PTY manager**      | Spawns and feeds the real processes behind each panel.                    |
| **Monitor**          | Watches panels for liveness, idleness, and notable output.                |
| **Event dispatcher** | Broadcasts every change to subscribers and hooks.                         |
| **Panels**           | The live terminals themselves — each an agent or a shell.                 |
