# Baton — Cockpit appearance (`TUI.yaml`)

The cockpit reads its **look** from `$HOME/.baton/TUI.yaml`: the colour **theme** and the group-split **layouts**. It is a
separate file from the main `config` so you can reshape the appearance without touching your bindings and behaviour
settings. The server reads it, merges it into the effective config, and broadcasts it to every frontend, so a `C-t R` (or a
`SIGHUP` to the daemon) hot-reloads it with no fleet restart. The file is **optional** — an absent or partial `TUI.yaml`
leaves the built-in look untouched.

```yaml
# $HOME/.baton/TUI.yaml
theme:
  brand: "33" # primary accent — banner, active borders, selection
  brand-hi: "117" # brighter accent — titles, pins, the summary tile, search hits
  attention: "#ff5f5f" # the attention-state LED

default-layout: main-vertical # the layout a group opens with

layouts:
  - name: review # a custom layout, selectable alongside the presets
    areas:
      - [diff, diff, log]
      - [diff, diff, sh]

scratch: # the floating scratch pane (C-t ~)
  command: "" # program to run; empty = the default shell
  width: 0.8 # box size as a fraction of the terminal
  height: 0.6
```

## Theme

Each token is a colour string — an ANSI 256 index (`"33"`) or a hex `"#rrggbb"`. An **empty or absent token keeps its
built-in default**, so a partial theme only changes what it names. An unknown colour string renders as the terminal default
rather than wedging the cockpit.

| Token       | Colours                                              |
| ----------- | ---------------------------------------------------- |
| `brand`     | the banner, active tile/card borders, the selection  |
| `brand-hi`  | titles, the pin glyph, the summary tile, search hits |
| `spawning`  | the `spawning`-state LED                             |
| `running`   | the `running`-state LED                              |
| `idle`      | the `idle`-state LED                                 |
| `attention` | the `attention`-state LED                            |
| `exited`    | the `exited`-state LED                               |

## Layouts

A group's **split** (see [SPEC.md](./SPEC.md#work-items)) arranges its live tiles. The default — `tiled` — is the even grid.
`L` in the split **cycles** the arrangement through the built-in presets and any custom layouts you define; the choice is
**server-owned per group**, rides the snapshot beside the visible count, and **persists across a restart**. With
[nested groups](./SPEC.md#work-items), a sub-group appears as a `▣` rollup tile — sized like a live panel — listing a small
roster of the members / sub-sub-groups it holds, each with a state LED, plus an `↵ descend` hint (the overflow `▦` summary
tile carries the same roster + `↵ open`). `enter` **descends** into it (the header shows the path as a breadcrumb,
`backend › api`); `esc` / `b` pop back one level. A `⊙` on a sub-group tile marks a **pinned default** — exactly one of its
direct panels is pinned — and descending then drops straight into that panel's zoom rather than the sub-group's split, the
same single-pin shortcut a top-level group has from the dashboard (**back**, `C-t b`, pops back to the sub-group's split).
That split `⊙` is not the dashboard **favourite**: `*` favourites the selected card — a separate, server-owned flag that
floats a panel or group to the **front** of the grid and tree, and never touches which tiles stream live or the single-pin
descend. Each level keeps its own layout, visible count, and resize ratios, since all of those are keyed by the group's path.

### Presets

| Name              | Arrangement                                                            |
| ----------------- | ---------------------------------------------------------------------- |
| `tiled`           | the even grid — every tile the same size (the default)                 |
| `main-vertical`   | the first member fills a tall left column; the rest stack on the right |
| `main-horizontal` | the first member fills a wide top row; the rest line up below          |
| `stack`           | every member full-width, stacked top to bottom                         |

`default-layout` names the one a group opens with before you cycle it (empty = `tiled`).

### Custom layouts

A custom layout names regions in an `areas` grid — one row of region names per line, each cell naming the region that owns
it, so a region spanning several cells repeats its name (the CSS grid-template-areas model). Members fill the regions in
reading order (row-major, by first appearance); a `.` marks an empty cell.

```yaml
layouts:
  - name: review
    areas:
      - [main, main, side]
      - [main, main, side]
      - [main, main, foot]
```

Here `main` spans a 3×2 block on the left, `side` the top two cells of the right column, and `foot` the bottom-right cell —
so the first member gets the big pane and the next two get the stacked side panes. A region **must be a solid rectangle**;
a custom layout that is non-rectangular, unknown to this frontend, or too small for the terminal **falls open to the even
grid**, so a layout that only exists in one frontend's config never wedges the split. Members past the region count fold
into the **summary tile**, exactly as they do in the even grid.

### Resize

`z` in the split enters **resize mode**: the arrows (or `h` / `j` / `k` / `l`) grow and shrink the focused tile, `tab`
moves the focus to another tile, and `z` / `esc` finish. The sizing skews the current layout's rows and columns — so it
applies to every layout **except** the even `tiled` grid, which has no per-track sizing to adjust (press `L` for a split
layout first). A nudge that would shrink any tile too small to render is refused, so the split never snaps back to the even
grid mid-resize. Resize is **view-local**: it lives in this cockpit only (never sent to the server), holds until you cycle
the layout or leave the group, and **resets on reattach** — unlike the layout and visible-count, which the server owns.

## Scratch pane

`C-t ~` floats a **scratch pane** — a throwaway shell (or any `command`) — over whatever view you are in, tmux's
`display-popup` for a quick `git`/`ls`/`htop` without leaving the fleet. It is a server-side **ephemeral** PTY: it never
joins the fleet, the dashboard, or the persisted state, and it is reaped when you close it or the cockpit disconnects.
Inside it, every key drives the shell; the leader is the only escape — `C-t ~` **hides** it (the shell keeps running, so
reopening resumes where you left off), `C-t w` **closes** it for good, and `C-t C-t` sends a literal prefix. The box
centres on the terminal at the configured `width`/`height` fraction (defaults `0.8`×`0.6`), floored at a legible minimum,
and reflows when the terminal resizes.

| Field     | Meaning                                              |
| --------- | ---------------------------------------------------- |
| `command` | the program the pane runs (empty = the shell)        |
| `width`   | box width as a fraction of the terminal (0 = `0.8`)  |
| `height`  | box height as a fraction of the terminal (0 = `0.6`) |

## Related cockpit keys

These ride alongside the appearance config (full key reference in [SPEC.md](./SPEC.md#keys)):

| Where                  | Key       | Does                                                             |
| ---------------------- | --------- | ---------------------------------------------------------------- |
| Any view               | `C-t ~`   | toggle the floating scratch pane (a throwaway shell)             |
| Group split            | `L`       | cycle the tile layout (presets, then your custom layouts)        |
|                        | `z`       | resize mode — arrows grow / shrink the focused tile (view-local) |
| Scroll mode (`C-t [`)  | `v`       | start a whole-line selection                                     |
|                        | `V`       | start a **block** (rectangular) selection                        |
|                        | `h` / `l` | in a block selection, pull the column edge in / out              |
| Group split (mouse on) | click     | focus the tile under the pointer (toggle the mouse in `C-t k`)   |

See [PLUGIN.md](./PLUGIN.md#programmable-titles--paneltitle) for the `panel.title` hook, which makes the per-panel title
itself programmable from Lua.

## Screen protector 🟢

Leave the cockpit idle for a few minutes and it slips into a screen protector: a full-screen curtain of digital rain with
the **BATON** wordmark and a big clock glowing at its centre. Any key or click wakes it — and that keystroke is swallowed,
so you never nudge the fleet on your way back. It only ever draws over a resting view (never a live zoom, split, scratch
pane, or an open prompt), and a backend hiccup pulls it aside at once so an outage is never hidden behind the rain.

Impatient? The leader summons it on demand — the key is left off the key map on purpose. It is only rain and a clock; it
touches nothing on the server. _(Hint: the leader, then the letter this whole feature is named for.)_
