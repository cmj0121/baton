# Baton — Specification

Design detail behind the concept sketched in the [README](../README.md). Start there for the pitch and vocabulary;
this document covers how the pieces fit together.

## Two views, one cockpit

Baton is keyboard-driven and has exactly two ways to look at your agents:

- **Dashboard** — see everything at once. Navigate panels, spawn new ones, group them into work items, reorder them
  (`shift`+arrows), retire the dead ones.
- **Zoom** — see one thing fully. Drive a single panel as if it were your only terminal; `C-t [` opens a tmux-style
  scroll mode (`↑`/`↓` a line, `b`/`space` or `PgUp`/`PgDn` a page, `g`/`G` top/bottom, `esc` exits) to read back through
  its history. Every bare key drives the program (vim, a BBS), never baton, so the leader works on any terminal — then
  pop back out to the dashboard. How much history is
  kept and replayed on attach is `panel.replay-kb` in the config (a larger value pages back further; full-screen programs
  keep no scrollback).

You never juggle windows or tabs. You conduct from the dashboard, and you zoom in only when a player needs you.

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

On the dashboard a work item collapses into a single card: a member count and a state that **rolls up to its most urgent
member** (attention beats running beats spawning beats idle beats exited), so one card speaks for the whole task.

**The group split.** Zooming a work item opens a split — every member rendered live in its own tile, all streaming at
once. By default the split is an _overview you navigate_, not a surface you type into: `tab` moves the focus between
tiles, `+`/`-` adjusts the column count, `shift`+`←`/`→` reorders the focused member within the group, `C-t [` opens
scroll mode on the focused tile, `x` removes the focused
member from the group, `enter` drops into the focused panel's own single zoom, and `d`/`esc` returns to the dashboard.
From a zoomed member,
the always-on `C-t g` escape pops back to the split.

**Pinning, for crowded groups.** Live tiles are capped (`maxGroupTiles`) so a huge group cannot spawn unbounded
terminals. Rather than stranding the overflow, the split shows the capped set of tiles beside a **tree list** of the
remaining members; `tab` walks the tiles and then the list as one ring, so every member is reachable. `p` **pins** the
focused member: a pinned panel always holds a live tile (a `⊙` marks it), promoting it out of the list and demoting an
auto-filled tile to keep the tile count at the cap. So you curate which of a busy group's panels stream live and which
stay a compact, navigable list. From a list row, `enter` still zooms the panel and `x` still removes it; interact (`i`)
needs a live tile, so it asks you to pin the panel first. Pins are per-view and reset when you leave the split.

**Interact mode.** Pressing `i` hands the keyboard to the focused tile so you can drive its program _in place_, without
the full-screen zoom — the tile glows green and wears a keyboard badge, and every keystroke is forwarded to that panel.
Like a zoom, the prefix is the only way out: `C-t i` (or `C-t g`) returns to navigation, `C-t d` leaves for the
dashboard, `C-t q` detaches, and `C-t C-t` sends a literal prefix. Only the focused tile receives input; the others stay
passive, so the navigation keys are never ambiguous with what a panel might want until you opt in. If the panel being
typed into leaves the group, interact ends rather than silently retargeting the tile the focus falls onto.

Under the hood a single client attaches to every member at once; the server tags each output message with its panel id
and the client demuxes it into the matching tile, while each tile's input side is forwarded so interact can reach the
PTY. The split reconciles on every snapshot — members added or removed elsewhere appear and disappear in place, an
emptied group exits to the dashboard, and live tiles are capped so a very large group cannot spawn unbounded terminals.

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
