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
| **Dashboard** | The live grid of every panel, with status and a preview.                |
| **Zoom**      | Focus one panel (or work item) and drive it as your only terminal.      |

## Keys

Keys are modal. On the dashboard and the group split every action is a **single key**; in a zoom keystrokes go to the
program, so a Baton action is the leader **`C-t`** then the key. **`C-t d`** (dashboard), **`C-t g`** (group view), and
**`C-t k`** (key map) work in every mode; **`q`** / **`C-t q`** detaches and leaves the server running; **`?`** shows the
rebindable key list for the current view.

Group from the dashboard: **`g`** mark, **`G`** group, **`a`** add, **`u`** ungroup, **`e`** rename — names stay unique
unless you set `allow-name-conflict`. **`enter`** zooms a panel, or opens a group's live split: every member tiled at
once, navigated with `tab`, `+`/`-`, `enter` to drop in, and `x` to remove one.

## Architecture

A headless **baton server** (a background daemon) owns all state and every terminal. Pluggable frontends attach over a
single Unix domain socket — commands up, events down — so you detach and reattach without losing a thing.

See [docs/SPEC.md](docs/SPEC.md) for the full diagram and interaction model.

## DDD (Dream-Driven Development)

This project follows DDD (dream-driven development): every feature is built from what I dream of and need.
