# Baton — Control

> Let an agent conduct the fleet. Baton's socket is a full control plane: the same commands the cockpit sends can be
> driven by a program. The **conductor** is an agent baton spawns to do exactly that — it spawns, groups, signals, and
> prompts the other panels, the way you would.

You hold the baton; the conductor is a second hand on it. This document is the contract for the three ways into the
control plane — the **conductor** panel, the **`baton ctl`** CLI, and the **`baton mcp`** server — and the guardrails
that keep an agent driving its own host from wrecking it.

## The conductor

Press **`C`** on the dashboard to open the conductor. It is a normal agent (your default agent profile, `claude` out of
the box), with three differences:

- **Singleton.** There is one and only one conductor per server. `C` spawns it when there is none, re-runs an exited
  one, or — on a running conductor — offers to **restart** it (a `y`/`n` confirm), which reloads its brief at the cost of
  its in-progress work; `enter` still zooms a running conductor like any panel. The server refuses a second.
- **Control-only workspace.** The conductor runs in a fresh, private, throwaway directory under baton's runtime dir —
  never your source tree. Its only local surface is the control wiring baton drops in: the briefing (written as both
  `BATON.md` and `CLAUDE.md`, the latter so the default Claude conductor auto-reads it as project instructions) and a
  `.mcp.json`. So the agent's path of least resistance is to drive baton, not to wander your code. The workspace is
  regenerated on respawn and removed when the conductor is closed.
- **Fenced.** The conductor acts under a scoped role (see [Guardrails](#guardrails)): it drives the rest of the fleet but
  cannot act on its own panel, stop the server, or fork-bomb the host.

The isolation is a **guardrail, not a sandbox**: the agent still runs as your user, so it could reach outside the
workspace with an absolute path. Baton shapes the environment so control is the easy path; it does not jail the process.

### The operator's brief — `$HOME/.baton/CONDUCTOR.md`

The built-in primer tells the conductor _how_ to drive baton; you tell it _what to do_. Write a goal and guide in
`$HOME/.baton/CONDUCTOR.md` and baton appends it to the conductor's briefing under an **Operator's brief** heading every
time the conductor is opened or re-run — so editing the file and then pressing `C` (which restarts a running conductor or
re-runs an exited one) updates its standing instructions. The file is optional and never replaces the primer: the agent
always keeps the control mechanics and the forbidden actions. For example:

```md
# Mission

Keep a reviewer agent running on each open PR worktree. When one finishes, summarise its findings into a shell panel
named "report" and pause for me.
```

## `baton ctl` — the CLI

`baton ctl` is a thin, synchronous client over the session socket. Run from a plain shell it acts with the full-power
cockpit role; run inside the conductor panel it inherits the conductor identity and is fenced. Each command connects,
acts, and exits.

| Command                                             | Does                                                                |
| --------------------------------------------------- | ------------------------------------------------------------------- |
| `baton ctl list`                                    | print the fleet as JSON (id, title, state, group, …)                |
| `baton ctl spawn [--agent CMD] [--arg A] [--dir D]` | spawn a panel (agent if `--agent`, else a shell); prints the new id |
| `baton ctl send <id> <text> [--no-enter]`           | type text into a panel; submits with a newline unless `--no-enter`  |
| `baton ctl group <name> <id>...`                    | file panels under a work item                                       |
| `baton ctl rename [--id ID \| --group G] <name>`    | rename a panel or a group                                           |
| `baton ctl pin <id>...` / `unpin <id>...`           | pin/unpin panels to live tiles                                      |
| `baton ctl signal <signal> <id>...`                 | send a signal, e.g. `SIGINT`                                        |
| `baton ctl close <id>...`                           | close panels                                                        |
| `baton ctl dispatch <id> <prompt>`                  | assign a task brief to a panel and deliver it as a unit             |
| `baton ctl dispatch-group <group> <prompt>`         | fan one brief to every member of a work item                        |
| `baton ctl queue add <prompt> [--group G]`          | enqueue a task for the scheduler to drain onto a free agent         |
| `baton ctl queue list`                              | print the backlog as JSON (id, prompt, status, panel, group, …)     |
| `baton ctl queue cancel <id>`                       | cancel a queued task by id                                          |
| `baton ctl queue drain`                             | clear every queued task                                             |

```sh
# Stand up a reviewer next to a worker and hand it the task.
id=$(baton ctl spawn --agent claude --dir ~/src/api)
baton ctl group review "$id"
baton ctl dispatch "$id" "review the open diff and list correctness risks"

# Or queue a batch and let the scheduler fan it across whoever comes free.
baton ctl queue add "audit the auth module"   --group review
baton ctl queue add "audit the billing module" --group review
baton ctl queue list
```

**Dispatch vs. send.** `send` types raw keystrokes; `dispatch` hands the server the _objective_, which it records on the
panel (so it reaches every card and the snapshot) and delivers as a unit — waiting for the agent to be ready rather than
interleaving with a running command. See [Tasks and the queue](./SPEC.md#tasks-and-the-queue) for the model.

## `baton mcp` — the MCP server

`baton mcp` is a [Model Context Protocol](https://modelcontextprotocol.io) server on stdio (newline-delimited JSON-RPC
2.0). It exposes the same verbs as MCP tools, so an MCP-speaking agent drives the fleet through structured tool calls
instead of shelling out:

`baton_list` · `baton_spawn` · `baton_send` · `baton_dispatch` · `baton_dispatch_group` · `baton_enqueue` ·
`baton_queue` · `baton_group` · `baton_rename` · `baton_pin` · `baton_unpin` · `baton_signal` · `baton_close`

`baton_dispatch` / `baton_dispatch_group` assign a task brief to a panel or a whole work item; `baton_enqueue` adds one
to the backlog and `baton_queue` reads it back. These are the verbs a conductor uses to run the flagship **you →
conductor → fleet** flow: you hand the conductor a batch of objectives, it enqueues them, and the scheduler drains them
onto the workers as they come free.

The conductor's workspace ships a `.mcp.json` pointing at this very binary run as `baton mcp`, so a Claude conductor
auto-loads the tools — no setup. The MCP subprocess inherits the conductor panel's environment, so it is fenced exactly
like the CLI. A tool failure (bad arguments, a rejected command, the daemon down) returns as an MCP error result the
model can read and recover from, not a dropped connection.

## The wire, directly

Both surfaces are thin wrappers over the socket — an agent that prefers raw JSON-RPC can speak it. A control client
declares its identity on the `hello` handshake:

| Field  | Meaning                                                                        |
| ------ | ------------------------------------------------------------------------------ |
| `role` | `"conductor"` to be fenced; empty (the cockpit) for full power.                |
| `self` | the client's own panel id — the panel the server will refuse to let it act on. |

A dispatch carries two more fields: `prompt` (the brief) and an optional `submit` override (the keys appended to send it,
default a newline) on `panel.dispatch` / `panel.dispatch-group`; `task.enqueue` / `task.cancel` / `task.drain` /
`task.list` drive the backlog and reply with a `tasks` snapshot.

Baton injects the wiring into the conductor panel's process, which both `baton ctl` and `baton mcp` read automatically:

| Variable         | Is                                                |
| ---------------- | ------------------------------------------------- |
| `BATON_SOCK`     | the control socket to dial                        |
| `BATON_ROLE`     | `conductor` — the scoped role to declare on hello |
| `BATON_PANEL_ID` | the conductor's own panel id (its `self`)         |

## Guardrails

The conductor role is enforced server-side, before any command takes effect. It is keyed off the self-declared role over
a **uid-private socket** — a guardrail against agent accidents, not a security boundary (a local process of your user can
always speak the socket directly).

| A conductor may                           | A conductor may not                                            |
| ----------------------------------------- | -------------------------------------------------------------- |
| list, spawn, group, rename, pin, move     | close, signal, or send input to **its own** panel              |
| signal and send input to **other** panels | **dispatch a task to its own** panel                           |
| dispatch to other panels, enqueue tasks   | **drain the queue** — clearing the backlog is operator-only    |
| close other panels, purge exited          | reload or stop the server                                      |
|                                           | spawn faster than the rate cap, or past the fleet ceiling (64) |

So a conductor can fill and dispatch from the backlog but cannot wipe it, and the queue gives it no way around the
self-fence: a brief it enqueues is drained by the scheduler onto _other_ idle agents, never back onto itself.

A plain cockpit connection declares no role and is never fenced.
