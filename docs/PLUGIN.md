# Baton — Plugins (Lua)

> **Status: shipped.** All four pillars are live — fleet API, hooks, commands, and Lua config — plus the
> client-fetches-config wire, so a plugin can reshape the cockpit, not just the daemon. The implementation lives in
> [`internal/plugin`](../internal/plugin); this document is its contract.

A baton plugin is a single Lua file that the server loads at startup. Through one object — `baton` — it can read the
fleet, drive every core action, react to lifecycle events, and add its own commands. The plugin is to baton what an
`init.lua` is to Neovim: trusted code you own that reshapes the tool to your workflow.

The architecture already names this seat. In [SPEC.md](./SPEC.md) the **Lua runtime** sits inside the daemon, behind the
one **`baton.*` API** gate that socket commands, Lua calls, and event registrations all pass through. This document fills
that box in.

## Why server-side

The plugin runs in the **daemon**, not the cockpit. The daemon is the single source of truth: it owns the fleet, every
core action, and the event stream. One plugin loaded there:

- affects **every frontend** at once (TUI today, browser later) — the rules live below the wire, not in one client;
- **survives** a cockpit restart, a detach, a reattach — it lives as long as the fleet does;
- reaches **events no client sees** — a panel falling to `attention` while you are detached still fires your hook.

The trade-off: a plugin acts on the **fleet**, not on "the cockpit's current selection." Selection, views, and zoom are
client state. A plugin can _offer_ a command the cockpit runs against its selection (the client passes it in), but the
plugin itself thinks in panel ids and groups, never in "the panel I am looking at."

## The file

|                  |                                                                                                     |
| ---------------- | --------------------------------------------------------------------------------------------------- |
| **Default path** | `$HOME/.baton/plug-in.lua`                                                                          |
| **Override**     | `--plugin FILE` flag, or `BATON_PLUGIN=FILE`                                                        |
| **When**         | loaded once at daemon start, after the YAML config, before the fleet serves                         |
| **Reload**       | re-run fresh on `C-t R` / `SIGHUP`, same as the config — edit and reload, no restart, no panel lost |
| **Errors**       | a load error is logged and **non-fatal** — the daemon runs on with the YAML defaults, never wedged  |
| **Missing**      | no file is a clean no-op — plugins are opt-in                                                       |

A reload rebuilds the Lua world from scratch (a fresh interpreter), so your hooks and commands are exactly what the file
says now — no stale registrations from the previous version linger.

## The `baton` object

Everything is reached through one global table, `baton`. It is Go-backed: each call lands on the same core action a
socket command would, so a plugin can do nothing a frontend cannot, and nothing bypasses the one gate.

### Drive the fleet

```lua
local id = baton.spawn{ kind = "agent", command = "claude", dir = "~/src/api", group = "api" }
baton.signal(id, "SIGINT")
baton.group({ id1, id2 }, "api")
baton.rename{ id = id, name = "claude·api" }
baton.pin(id)
baton.close(id)
```

| Call                                                | Core action                      | Notes                                          |
| --------------------------------------------------- | -------------------------------- | ---------------------------------------------- |
| `baton.spawn{kind=, command=, args=, dir=, group=}` | `panel.create` (+ `panel.group`) | returns the new panel id                       |
| `baton.respawn(id)`                                 | `panel.respawn`                  | re-run an exited panel                         |
| `baton.close(id \| {ids})`                          | `panel.close`                    |                                                |
| `baton.purge()`                                     | `panel.purge`                    | drop every exited panel                        |
| `baton.signal(id \| {ids}, name)`                   | `panel.signal`                   | name or number, e.g. `"SIGTERM"` or `15`       |
| `baton.group({ids}, name)`                          | `panel.group`                    |                                                |
| `baton.ungroup({ids} \| name)`                      | `panel.ungroup`                  |                                                |
| `baton.rename{id= \| group=, name=}`                | `panel.rename`                   |                                                |
| `baton.move({ids}, index)`                          | `panel.move`                     | reorder the fleet                              |
| `baton.pin(id)` / `baton.unpin(id)`                 | `panel.pin` / `panel.unpin`      |                                                |
| `baton.group_show(name, n)`                         | `group.show`                     | live-tile count for a group                    |
| `baton.dispatch(id, prompt)`                        | `panel.dispatch`                 | assign a brief and deliver it as a unit        |
| `baton.dispatch_group(group, prompt)`               | `panel.dispatch-group`           | fan a brief to every member; returns the count |
| `baton.enqueue(prompt, group)`                      | `task.enqueue`                   | add to the backlog; returns the task id        |

`baton.spawn` also takes a `prompt =` — spawn an agent and dispatch its first task in one call. Plugin-originated
dispatches go straight to the core action and **bypass the `task.pre` filter** (see below), so a hook that enqueues can
never re-enter itself.

Every write returns `ok, err` (Lua idiom): `nil, "the name \"api\" is already taken"` on the same failures the socket
reports, so a plugin handles a rejected action instead of crashing.

### Read the fleet

```lua
for _, p in ipairs(baton.panels()) do
  if p.state == "attention" then print(p.title, p.group) end
end
```

- `baton.panels()` → array of `{ id, kind, title, state, group, activity, pinned }`
- `baton.panel(id)` → one panel table, or `nil`
- `baton.groups()` → array of `{ group, shown }`

Reads are snapshots of the moment you call them — the same view the dashboard renders from.

### React to events — `baton.on`

```lua
baton.on("panel.attention", function(p)
  baton.notify(string.format("%s needs you", p.title))
end)

baton.on("panel.exit", function(p)
  if p.exit_code ~= 0 then baton.log("warn", p.title .. " failed") end
end)
```

A handler runs on the daemon's single Lua worker, **off every server lock**, so it may freely call back into `baton.*`.
Handlers are best-effort: a slow or throwing handler is logged and isolated, never stalling the Monitor, and a flood of
events that outruns the worker drops oldest-first (like telemetry) rather than blocking the fleet.

The event set (derived from the lifecycle in SPEC.md):

| Event             | Fires when                          | Payload                                                           |
| ----------------- | ----------------------------------- | ----------------------------------------------------------------- |
| `panel.spawn`     | a panel is created                  | the panel                                                         |
| `panel.state`     | any lifecycle transition            | panel + `from`, `to`                                              |
| `panel.attention` | a panel enters `attention`          | the panel                                                         |
| `panel.idle`      | a panel settles to `idle`           | the panel                                                         |
| `panel.exit`      | a process ends on its own           | panel + `exit_code`                                               |
| `panel.close`     | a panel is retired                  | `{ id }`                                                          |
| `group.change`    | membership / rename / show changes  | the group                                                         |
| `task.change`     | a task is recorded or changes state | the task — `id`, `prompt`, `status`, `panel`, `group`, `attempts` |
| `server.reload`   | config/plugin reloaded              | —                                                                 |
| `panel.output`    | bytes arrive on a panel             | panel + `data` — **high-volume, opt-in, off by default**          |

`panel.attention` and `panel.exit` are the headline hooks: "ping me when an agent needs me," "kick off the next step when
this one finishes," "desktop-notify on completion" all fall out of them.

### Tasks and the queue

A dispatch ([SPEC.md](./SPEC.md#tasks-and-the-queue)) is a tracked task, and a plugin can both **watch** it and **shape**
it. Watching is an ordinary hook — `task.change` fires on every transition, so "log every brief," "notify when a task
fails," or "chain the next step when one finishes" all read the same way as the panel hooks:

```lua
baton.on("task.change", function(t)
  if t.status == "failed" then baton.notify(t.id .. " failed: " .. (t.prompt or "")) end
end)
```

Shaping is the one **filter hook**, `task.pre`. Unlike every other hook — which is fire-and-forget and ignores its return
value — `task.pre` runs **synchronously before a brief is delivered** and its return value changes the action. It is the
single place a plugin can rewrite or veto work, the natural seat for an allow-list, a prompt preamble, or a routing tag:

```lua
baton.on("task.pre", function(t)
  if t.prompt:find("rm %-rf") then return false end           -- veto: drop the task
  if t.group == "review" then return "[read-only] " .. t.prompt end  -- rewrite the brief
  -- return nothing to pass it through unchanged
end)
```

The hook receives a `{ prompt, group }` table and returns one of:

| Return                      | Effect                                               |
| --------------------------- | ---------------------------------------------------- |
| `nil` / nothing / `true`    | pass the brief through unchanged                     |
| a string                    | rewrite the prompt to that string                    |
| `{ prompt = "…" }`          | rewrite the prompt                                   |
| `false` / `{ drop = true }` | veto — the task is dropped, the caller gets an error |

Hooks **chain** (a later hook sees the earlier one's rewrite) and the **first veto stops the chain**. It runs at the
`dispatch`, `dispatch-group`, and `enqueue` intake points; plugin-originated dispatches bypass it, so it never re-enters
itself.

`task.pre` is **fail-open** by contract: no hook, a throwing hook, or a hook that runs past the timeout all leave the
brief unchanged. The filter blocks the dispatch on the single Lua worker, so a slow hook is bounded — past the deadline
the caller proceeds with the original brief and the late result is discarded. A broken or slow plugin can never drop a
task or wedge the fleet; the worst it does is fail to filter.

### Add commands — `baton.command`

```lua
baton.command{
  name = "spawn-api-stack",
  desc = "three claude agents on the API repo, grouped",
  run = function()
    for _, dir in ipairs({ "api", "api/worker", "api/web" }) do
      baton.spawn{ kind = "agent", command = "claude", dir = "~/src/" .. dir, group = "api" }
    end
  end,
}
```

A registered command becomes a first-class verb: surfaced in the cockpit's command picker (the `c` key) and bindable to a
key. This is how a plugin grows baton's vocabulary rather than just scripting it once at load.

### Configure — `baton.config`

The "config as Lua" pillar: everything the YAML config holds, settable (and computable) from Lua.

```lua
baton.config{
  prefix = "ctrl+t",
  default_agent = "claude",
  workdir = os.getenv("HOME") .. "/src",
  allow_name_conflict = false,
  replay_kb = 256,
}

baton.agent{ name = "claude", command = "claude", args = { "--dangerously-skip-permissions" } }
baton.agent{ name = "aider",  command = "aider" }

baton.bind("D", "diff")   -- key → action, client-side binding
```

### Utilities

- `baton.log(level, msg)` — into the daemon log (`info` / `warn` / `debug` / `error`).
- `baton.notify(msg)` — surface a **transient** notice to attached cockpits (a toast on the footer status line).
- `baton.footer(text)` — set a **persistent** footer segment shown in every cockpit; `""` clears it. Unlike a notice it
  does not fade, so it suits a live readout (a token counter, a build status). The daemon holds the latest value and
  hands it to a freshly attaching cockpit, so it is the same on every client.
- The Lua standard library is available, so `os`, `io`, `string`, and friends are in reach (see _Trust_ below).

## Example: used tokens in the footer

A complete plugin lives at [`examples/token-footer.lua`](../examples/token-footer.lua). It watches agent output for a
token count and shows the fleet-wide total in the footer — the headline use of `panel.output` + `baton.footer`:

```lua
local used = {} -- panel id -> last token count
local last = -1
local function redraw()
  local total = 0
  for _, n in pairs(used) do total = total + n end
  if total ~= last then
    last = total
    baton.footer(total > 0 and string.format("⊙ %d tok", total) or "")
  end
end

baton.on("panel.output", function(p)
  for m in p.data:gmatch("(%d[%d,]*)%s*tokens") do  -- adjust the pattern to your agent
    used[p.id] = tonumber((m:gsub(",", "")))
  end
  redraw()
end)

baton.on("panel.exit", function(p) used[p.id] = nil; redraw() end)
```

Copy it to `$HOME/.baton/plug-in.lua`, reload with `C-t R`, and the footer carries `⊙ N tok` as your agents work. The
file itself strips ANSI escapes and only repaints when the total moves; tune `TOKEN_PATTERN` to match how your agent
prints usage.

## Config: YAML and Lua together

The YAML config (`$HOME/.baton/config`) stays — it is the simple path and nothing forces a plugin. The order:

```txt
built-in defaults  →  YAML config  →  plug-in.lua
```

The plugin loads **after** YAML and can read the effective settings and override them, so YAML is your base and Lua has
the last word. Both feed the same reload path, so `C-t R` re-reads config _and_ re-runs the plugin in one step.

## Trust

The plugin file is **trusted code you wrote**, like a shell rc or a Neovim `init.lua`. It runs with the daemon's full
privileges and (by default) the full Lua standard library — that is the point of "control almost everything." It lives at
a private path (`0600`, under `$HOME/.baton`). Baton does not sandbox it. A future opt-in restricted mode (no `os`/`io`,
no network) is possible if there is demand, but the default is full power.

## Implementation

- **Engine:** [`gopher-lua`](https://github.com/yuin/gopher-lua) — a pure-Go Lua 5.1 VM. No cgo, so baton stays a single
  static binary with no new build step.
- **Package:** [`internal/plugin`](../internal/plugin) owns the `*lua.LState`, the event queue, and the `baton` table;
  the server feeds it through the event-dispatcher hook the architecture reserves for it.
- **One Lua goroutine.** The VM is single-threaded. A dedicated goroutine owns the `LState`; loads, hooks, and commands
  all run on it. Server events are posted to a buffered channel it drains — **never under `s.mu`** — so a hook calling
  back into `baton.*` re-enters the (independently locked) core actions without deadlock. The one synchronous hook,
  `task.pre`, rides the same worker but **blocks the dispatch** on a bounded request: the server reads the brief result
  off a buffered channel with `s.mu` released, and a timeout makes it fail open, so a wedged filter never stalls the
  fleet.
- **Mapping is mechanical.** Each `baton.*` write marshals its Lua args into the same call the socket handler makes, so
  the plugin and the wire can never drift in what an action means or how it can fail.

## Design decisions

The choices the implementation is built on:

1. **Scope — all four pillars.** Fleet API, hooks, `baton.command`, and `baton.config`/`agent`/`bind` are all live.
2. **Client reach — the client fetches its config from the daemon.** A plugin may set keybindings, the prefix, and the
   cockpit toggles; the daemon serves the merged effective config over the socket and the cockpit applies it on attach
   (and on reload). Registered commands ride the same channel and appear in the cockpit's command picker.
3. **`panel.output` is opt-in and rate-limited** — a handler is only attached when a plugin registers for it, and the
   stream is coalesced so a chatty panel cannot drown the worker.
4. **`baton.notify`** is a server→client notice, alongside `baton.log` into the daemon log.
5. **Config precedence is `defaults → YAML → Lua`** — YAML is the base, the plugin loads after and wins.
6. **Engine is `gopher-lua`** — pure-Go Lua 5.1, no cgo.
