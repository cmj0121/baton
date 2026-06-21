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

| Where                  | Key                         | Does                                      |
| ---------------------- | --------------------------- | ----------------------------------------- |
| Anywhere (after `C-t`) | `C-t d`                     | go to the dashboard                       |
|                        | `C-t g`                     | go to the group view                      |
|                        | `C-t [`                     | enter scroll mode                         |
|                        | `C-t k`                     | edit the key map                          |
|                        | `C-t P`                     | panel config (default shell, workdir, …)  |
|                        | `C-t R`                     | reload config (backend + cockpit)         |
|                        | `C-t q`                     | detach (server keeps running)             |
| Dashboard              | `hjkl` / arrows             | move the cursor                           |
|                        | `enter`                     | open / zoom the selection                 |
|                        | `p`                         | new shell panel                           |
|                        | `A`                         | new agent panel                           |
|                        | `c`                         | new panel (pick the command)              |
|                        | `w`                         | close the selection                       |
|                        | `x`                         | purge exited panels                       |
|                        | `s`                         | send a signal to the selection            |
|                        | `S-←` / `S-→`               | reorder the selected item                 |
|                        | `g`                         | mark / unmark a panel                     |
|                        | `G`                         | group the marked panels                   |
|                        | `a`                         | add marked panels to the selected group   |
|                        | `u`                         | ungroup the selected work item            |
|                        | `e`                         | rename the panel or group                 |
| Group view             | `tab`                       | focus the next panel                      |
|                        | `+` / `-`                   | more / fewer columns                      |
|                        | `p`                         | pin / unpin the focused panel             |
|                        | `s`                         | send a signal to the focused panel        |
|                        | `S`                         | send a signal to every panel in the group |
|                        | `i`                         | interact (type into the focused tile)     |
|                        | `x`                         | remove the focused panel from the group   |
|                        | `S-←` / `S-→`               | reorder the focused panel                 |
|                        | `enter`                     | zoom the focused panel                    |
| Zoom / interact        | type                        | drive the program directly                |
|                        | `C-t C-t`                   | send a literal `C-t`                      |
|                        | `C-t s`                     | send a signal to this panel               |
| Scroll mode (`C-t [`)  | `↑` / `↓` (`k`/`j`)         | scroll a line                             |
|                        | `b` / `Spc` (`PgUp`/`PgDn`) | scroll a page                             |
|                        | `g` / `G`                   | jump to top / bottom                      |
|                        | `esc` / `q`                 | exit scroll mode                          |

Names stay unique unless you set `allow-name-conflict`.

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

## Architecture

A headless **baton server** (a background daemon) owns all state and every terminal. Pluggable frontends attach over a
single Unix domain socket — commands up, events down — so you detach and reattach without losing a thing.

See [docs/SPEC.md](docs/SPEC.md) for the full diagram and interaction model.

## DDD (Dream-Driven Development)

This project follows DDD (dream-driven development): every feature is built from what I dream of and need.
