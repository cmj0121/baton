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
**server-owned per group**, rides the snapshot beside the visible count, and **persists across a restart**.

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

## Related cockpit keys

These ride alongside the appearance config (full key reference in [SPEC.md](./SPEC.md#keys)):

| Where                  | Key       | Does                                                           |
| ---------------------- | --------- | -------------------------------------------------------------- |
| Group split            | `L`       | cycle the tile layout (presets, then your custom layouts)      |
| Scroll mode (`C-t [`)  | `v`       | start a whole-line selection                                   |
|                        | `V`       | start a **block** (rectangular) selection                      |
|                        | `h` / `l` | in a block selection, pull the column edge in / out            |
| Group split (mouse on) | click     | focus the tile under the pointer (toggle the mouse in `C-t k`) |

See [PLUGIN.md](./PLUGIN.md#programmable-titles--paneltitle) for the `panel.title` hook, which makes the per-panel title
itself programmable from Lua.
