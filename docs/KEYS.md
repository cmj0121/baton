# Baton — Keys

**English** · [繁體中文](KEYS.zh-TW.md)

The complete key reference. [SPEC.md](./SPEC.md) covers the views and the model behind them; this page covers only how
you drive them. Press `?` in any view for the live list of that view's keys, and `C-t k` to rebind anything here.

## The model

Three things decide what a keystroke means: the **mode** you are in, whether the action needs the **leader**, and
whether it sits under a **landing key**.

### Modal

On the **dashboard** the keys are baton's own, so an action fires on its bare key. In a **zoom** or an **interact**
tile the keys belong to the program you are driving, so a baton action is the leader `C-t` followed by that same key.
Nothing else changes — the key you learn on the dashboard is the key you press in a zoom, with `C-t` in front of it.

A **group split** is in between. Its own keys are bare (`tab`, `L`, `z`, `p`, `i`, `x`, `s`, `S`), and so are the few
commands it shares with the dashboard, but the landing families below are **not reachable there yet** — press `C-t d`
for the dashboard, or `?` for the list that split actually has.

```text
dashboard      p              spawn a shell panel
zoom           C-t p          spawn a shell panel
```

### The leader

`C-t` is the leader. A handful of actions are reached **only** through it, in every view including the dashboard —
either because their bare key belongs to navigation (`k` is up, `l` is right) or because they are too consequential to
sit a fingertip away from the arrow keys (`C-t S` ends the whole fleet). Those are the **escapes**, marked `C-t` in the
table below.

`C-t C-t` sends a literal `C-t` to the program in a zoom.

### Landing keys

Four keys are **landings**: they do nothing on their own and open a small family of related actions.

| Landing | Family                                                       |
| ------- | ------------------------------------------------------------ |
| `n`     | **n**ew — the spawns that are not the everyday `p` and `A`   |
| `v`     | **v**iew — what the cockpit draws, never what the fleet does |
| `g`     | **g**roup — everything about work items                      |
| `x`     | destructive — the double tap **is** the confirmation         |

Press a landing and the status bar names what it can take next, so a family is discovered by pressing it rather than by
reading this page:

```text
 DASHBOARD │ g …  g mark · c create · a add · u ungroup            ⏱ 09:34:51  ● attached · local
```

No landing key is also an action on its own, so a completed sequence fires the moment you press its last key. Nothing
waits, and nothing is ambiguous.

### The timeout

A landing left hanging **expires**, clearing itself and its hint from the status bar. The default is `1.2s`:

```yaml
# $HOME/.baton/config
settings:
  key-timeout: 1.2s # how long a landing key waits for the key after it; 0 = never expire
```

`0` disables expiry, so a landing (the leader included) waits indefinitely — the behaviour baton had before this
setting existed. Values outside `200ms`–`10s` fall back to the default.

The same timeout resolves an **ambiguous** binding — one whose key is also the start of a longer one, the way `d` and
`dd` relate in vim. The default key map has none, so you will only meet this if you bind one yourself, and `C-t k`
warns you when you do.

## Every key

**Leader** — `C-t` means the leader is needed in every view; `(C-t)` means only a zoom needs it; `·` means never.
**Landing** — the key you press first, if the action sits under one. Landings work on the dashboard and, with the
leader, in a zoom; the group split has its own keys and does not reach them. A `(C-t)` row whose action needs a
dashboard row — the work-item verbs — is refused in a zoom with the way forward on the status line.

| Purpose         | Leader | Landing | Key                 | Does                                             |
| --------------- | ------ | ------- | ------------------- | ------------------------------------------------ |
| **Panels**      | (C-t)  | ·       | `p`                 | new shell panel                                  |
|                 | (C-t)  | ·       | `A`                 | new agent panel                                  |
|                 | (C-t)  | `n`     | `c`                 | new panel, choosing the command                  |
|                 | (C-t)  | `n`     | `.`                 | new shell in the focused panel's directory       |
|                 | (C-t)  | `n`     | `C`                 | the conductor, found or created                  |
|                 | (C-t)  | `n`     | `h`                 | the global shell, found or created               |
|                 | (C-t)  | ·       | `w`                 | close the selection                              |
|                 | (C-t)  | ·       | `r`                 | re-run the exited panels under the focus         |
|                 | (C-t)  | `x`     | `x`                 | purge every exited panel                         |
|                 | (C-t)  | ·       | `s`                 | send a signal                                    |
|                 | (C-t)  | ·       | `f`                 | find panels · search the scrollback in a zoom    |
|                 | (C-t)  | ·       | `/`                 | fleet search — grep every panel's output         |
|                 | (C-t)  | ·       | `D`                 | the work-tree diff of an agent panel             |
|                 | (C-t)  | ·       | `T`                 | dispatch a task to the agent                     |
|                 | (C-t)  | ·       | `t`                 | enqueue a task for any free agent                |
|                 | (C-t)  | ·       | `Q`                 | manage the task queue                            |
| **Work items**  | (C-t)  | `g`     | `g`                 | mark or unmark the selection                     |
|                 | (C-t)  | `g`     | `c`                 | create a work item from the marked panels        |
|                 | (C-t)  | `g`     | `a`                 | add the marked panels to the selected work item  |
|                 | (C-t)  | `g`     | `u`                 | dissolve the selected work item                  |
|                 | (C-t)  | ·       | `e`                 | rename the panel or work item                    |
|                 | (C-t)  | ·       | `*`                 | favourite it, sorting it to the front            |
|                 | (C-t)  | ·       | `m`                 | pick a row up; arrows carry it, enter drops it   |
|                 | (C-t)  | ·       | `space`             | show or hide what is nested under the row        |
| **View**        | (C-t)  | ·       | `?`                 | the key list for this view                       |
|                 | (C-t)  | `v`     | `u`                 | cycle the usage footer: off, window, panel       |
|                 | (C-t)  | `v`     | `k`                 | the key-press readout in the footer              |
|                 | (C-t)  | `v`     | `p`                 | the detail pane beside the dashboard tree        |
|                 | (C-t)  | `v`     | `l`                 | the dashboard layout: cards or tree              |
|                 | (C-t)  | `v`     | `g`                 | cycle the group-by lens                          |
|                 | (C-t)  | ·       | `b`                 | back one level: zoom, group, dashboard           |
| **Escapes**     | C-t    | ·       | `d`                 | go to the dashboard                              |
|                 | C-t    | ·       | `a`                 | the attention inbox                              |
|                 | C-t    | ·       | `o`                 | the process tree                                 |
|                 | C-t    | ·       | `@`                 | remote access                                    |
|                 | C-t    | ·       | `~`                 | the floating scratch shell                       |
|                 | C-t    | ·       | `c`                 | the plugin command picker                        |
|                 | C-t    | ·       | `k`                 | edit the key map                                 |
|                 | C-t    | ·       | `P`                 | panel defaults                                   |
|                 | C-t    | ·       | `[`                 | scroll mode                                      |
|                 | C-t    | ·       | `l`                 | start or stop logging the panel to a file        |
|                 | C-t    | ·       | `L`                 | open that log in a temporary panel               |
|                 | C-t    | ·       | `G`                 | the git menu, on a zoomed agent panel            |
| **Session**     | C-t    | ·       | `S`                 | force-restart the server, ending the fleet       |
|                 | (C-t)  | ·       | `R`                 | reload config; the fleet keeps running           |
|                 | (C-t)  | ·       | `q`                 | detach; the server keeps running                 |
| **Navigation**  | ·      | ·       | `hjkl` / arrows     | move; on the tree they also fold and unfold      |
|                 | ·      | ·       | `enter`             | open or zoom the selection                       |
|                 | ·      | ·       | `esc`               | cancel, or leave one layer                       |
|                 | ·      | ·       | `tab` / `shift+tab` | next / previous                                  |
|                 | ·      | ·       | `shift+←` / `→`     | reorder the selection                            |
| **Group split** | ·      | ·       | `tab` / `shift+tab` | focus the next / previous tile                   |
|                 | ·      | ·       | `+` / `-`           | show more / fewer live tiles                     |
|                 | ·      | ·       | `L`                 | cycle the tile layout                            |
|                 | ·      | ·       | `z`                 | resize mode — arrows grow and shrink the tile    |
|                 | ·      | ·       | `p`                 | pin or unpin the focused member                  |
|                 | ·      | ·       | `i`                 | interact — drive the focused tile in place       |
|                 | ·      | ·       | `x`                 | remove the focused member from the work item     |
|                 | ·      | ·       | `S`                 | signal every member                              |
|                 | ·      | ·       | `enter`             | zoom the tile, or descend into a sub-group       |
| **Zoom**        | C-t    | ·       | `C-t`               | send a literal `C-t` to the program              |
| **Scroll mode** | ·      | ·       | `j` / `k`           | scroll a line                                    |
|                 | ·      | ·       | `b` / `space`       | scroll a page                                    |
|                 | ·      | ·       | `g` / `G`           | jump to the top / bottom                         |
|                 | ·      | ·       | `n` / `N`           | next / previous search match                     |
|                 | ·      | ·       | `v`                 | start a whole-line selection                     |
|                 | ·      | ·       | `V`                 | start a block selection; `h` / `l` set its width |
|                 | ·      | ·       | `y`                 | copy the selection to the clipboard              |
|                 | ·      | ·       | `q` / `esc`         | leave scroll mode                                |
| **Overlays**    | ·      | ·       | `j` / `k` / arrows  | move                                             |
|                 | ·      | ·       | `g` / `G`           | jump to the top / bottom                         |
|                 | ·      | ·       | `b` / `space`       | scroll a page                                    |
|                 | ·      | ·       | `enter`             | open or apply the row                            |
|                 | ·      | ·       | `r`                 | refresh                                          |
|                 | ·      | ·       | `x`                 | remove the row under the cursor                  |
|                 | ·      | ·       | `X`                 | clear every row, after a `y`/`n`                 |
|                 | ·      | ·       | `q` / `esc`         | close                                            |
| **Inbox**       | ·      | ·       | `i`                 | step into the panel that wants you               |
|                 | ·      | ·       | `-`                 | snooze the row                                   |
| **Queue**       | ·      | ·       | `shift+↑` / `↓`     | promote / demote the task                        |
| **Remote**      | ·      | ·       | `e` / `E`           | enable / disable remote access                   |
|                 | ·      | ·       | `n`                 | rotate the passkey                               |
| **Git menu**    | ·      | ·       | `d` `l` `s`         | diff · log · status                              |
|                 | ·      | ·       | `a` `c` `p`         | stage all · commit · push                        |
|                 | ·      | ·       | `b` `w` `W` `x`     | branch · worktree · worktrees · remove worktree  |

Everything from **Panels** through **Session** is rebindable. Navigation and the per-view keys below it are fixed: they
are what the view _is_, and a movable `enter` would leave a view with no way in.

## Rebinding

`C-t k` edits the key map live and writes it to `$HOME/.baton/config`. `e` on a row starts collecting keys, **`enter`
binds the run** and `esc` abandons it — a binding may be more than one key, and nothing else can say where the run
ended. Only what you change from the default is stored, so a later change to a default reaches you instead of being
masked by a stale copy:

```yaml
# $HOME/.baton/config
prefix: ctrl+t # the leader
keys:
  new-agent: "F2" # a single key
  ungroup: "g d" # a sequence: the tokens are separated by spaces
  purge-exited: "x" # collapse a double tap back to one key if you prefer
settings:
  key-timeout: 1.2s
```

A binding is a **sequence of keys separated by spaces**. `space` is the token for the space bar, and a modifier is
written as bubbletea names it: `ctrl+t`, `shift+left`, `alt+x`. Keys are matched against a binding's stable name, never
against its key, so rebinding one never disturbs the rest — and a default that moves in a later release moves under
your custom key too.

Bind a sequence whose start is already a binding of its own and the cockpit says so as it saves:

```text
"add" (a) starts "add-all" (a a) — a waits 1.2s before firing
```

That is a warning, not a refusal. The delay is `settings.key-timeout`, and it is the only way to meet it.

## Moving from v1.2

Every key below still does what it always did; only the way you reach it changed. Nothing that was rebound in your
config needs touching — bindings are stored by name.

| Was     | Now     | Why                                                                                     |
| ------- | ------- | --------------------------------------------------------------------------------------- |
| `c`     | `n c`   | the spawn family moved under `n`, freeing four letters                                  |
| `.`     | `n .`   | as above                                                                                |
| `C`     | `n C`   | as above                                                                                |
| `H`     | `n h`   | as above                                                                                |
| `G`     | `g c`   | every work-item action now starts with `g`                                              |
| `a`     | `g a`   | as above — and freeing `a` un-shadowed `C-t a`, the inbox                               |
| `u`     | `g u`   | as above                                                                                |
| `g`     | `g g`   | mark stays the cheapest key in its family: the same finger, twice                       |
| `U`     | `v u`   | the display toggles moved under `v`, freeing four more letters                          |
| `K`     | `v k`   | as above                                                                                |
| `v`     | `v p`   | as above                                                                                |
| `V`     | `v l`   | as above                                                                                |
| `z`     | `v g`   | as above — and `z` now means resize in the split and nothing else anywhere              |
| `x`     | `x x`   | purging the fleet's dead is a double tap, and the second tap is the confirmation        |
| `S`     | `C-t S` | bare `S` signalled a whole group in the split and restarted the server on the dashboard |
| `C-t g` | `C-t G` | `C-t g` is now the leader plus the `g` landing, so the git menu took the shifted key    |

The keys they left behind answer for one release: press `U` and the status bar tells you it lives at `v u` now.

## What changed underneath

Two long-standing gaps closed with this:

- **A zoom reaches every escape, and resolves commands the same way the dashboard does.** The escapes used to be
  enumerated by hand in the zoom, which is why `C-t o`, `C-t c` and `C-t P` were documented but did nothing there; they
  are now looked up from the same table that decides what an escape _is_. Commands go through the same matcher as the
  dashboard, so a landing works in a zoom (`C-t v u`) exactly as it does outside one. A command whose target is a
  dashboard row — mark, group, add — has nothing to act on in a zoom and says so instead of acting on a cursor you
  cannot see.
- **A hanging leader no longer eats your next keystroke.** `C-t` used to wait forever; now it expires like every other
  landing, and the status bar says it is waiting while it does.
