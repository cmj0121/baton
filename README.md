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
- **Group** — a work item's live split: its panels tiled side by side, all streaming at once. Pin a few to watch large
  while the rest stay a navigable list, drive the focused one in place with **`i`**, or **`enter`** to drop into it.
- **Zoom** — one panel as your only terminal. Keystrokes go straight to the program; the leader **`C-t`** is how you act
  or step back out.

## Keys

Keys are modal: on the **dashboard** and in a **group** each action is a single key; in a **zoom** or **interact**
keystrokes drive the program, so a Baton action is the leader **`C-t`** then the key. Press **`?`** for the full,
rebindable list of the current view.

| Context         | Keys                                                                                                               |
| --------------- | ------------------------------------------------------------------------------------------------------------------ |
| After `C-t`     | `d` dashboard, `g` group, `k` key map, `q` detach                                                                  |
| Dashboard       | `hjkl` move, `S-←`/`S-→` reorder, `enter` open, `p` new panel, `A` new agent, `c` pick cmd, `w` close              |
| Dashboard group | `g` mark, `G` group, `a` add, `u` ungroup, `e` rename                                                              |
| Group           | `tab` focus, `+`/`-` columns, `p` pin, `i` interact, `x` remove, `S-←`/`S-→` reorder, `C-t [` scroll, `enter` zoom |
| Zoom / interact | type to drive the program, `C-t [` scroll mode, `C-t C-t` literal `C-t`                                            |
| Scroll mode     | `↑`/`↓` line, `b`/`Spc` (or `PgUp`/`PgDn`) page, `g`/`G` top/bottom, `esc`/`q` exit                                |

Names stay unique unless you set `allow-name-conflict`.

## Architecture

A headless **baton server** (a background daemon) owns all state and every terminal. Pluggable frontends attach over a
single Unix domain socket — commands up, events down — so you detach and reattach without losing a thing.

See [docs/SPEC.md](docs/SPEC.md) for the full diagram and interaction model.

## DDD (Dream-Driven Development)

This project follows DDD (dream-driven development): every feature is built from what I dream of and need.
