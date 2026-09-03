# Baton — Control

**English** · [繁體中文](CONTROL.zh-TW.md)

> Let an agent conduct the fleet. Baton's socket is a full control plane: the same commands the cockpit sends can be
> driven by a program. The **conductor** is an agent baton spawns to do exactly that — it spawns, groups, signals, and
> prompts the other panels, the way you would.

You hold the baton; the conductor is a second hand on it. This document is the contract for the three ways into the
control plane — the **conductor** panel, the **`baton ctl`** CLI, and the **`baton mcp`** server — and the guardrails
that keep an agent driving its own host from wrecking it.

## The conductor

Press **`n C`** on the dashboard to open the conductor. It is a normal agent (your default agent profile, `claude` out of
the box), with four differences:

- **Singleton.** There is one and only one conductor per server; the server refuses a second. It is **not a card in the
  fleet** — it shows as a **mark in the `FLEET` heading** (with its live state), since it drives the fleet rather than
  being one of it, and stays out of the roster, the counts, and the attention nudges. `n C` is how you reach it: it **zooms**
  a live conductor so you can watch its work, **re-runs** an exited one (its wiring is rewritten, so it reloads its
  brief) and zooms the restart, or **spawns** one when there is none and zooms it the moment it lands.
- **Control-only workspace.** The conductor runs in a private directory of baton's own — never your source tree. Its
  only local surface is the control wiring baton drops in: the briefing (written as both `BATON.md` and `CLAUDE.md`,
  the latter so the default Claude conductor auto-reads it as project instructions) and a `.mcp.json`. So the agent's
  path of least resistance is to drive baton, not to wander your code.
- **One workspace, kept until you reboot.** There is one workspace per control socket, not one per open, so closing the
  conductor and opening it again lands in the same directory with the settings it had collected — the permission grants
  an agent writes beside itself survive a restart instead of being asked for every time. It lives under
  `$XDG_RUNTIME_DIR/baton/` (or your temporary directory), it is created only if it is not already there, and it is
  **cleared when the host reboots**: baton stamps it with the boot it belongs to and rebuilds it from scratch when that
  no longer matches. `baton ctl conductor reset` clears it on demand — close the conductor first, and note that the
  conductor itself is fenced from that verb. The path is logged when the conductor opens, since the directory is named
  after a hash of the socket. One caveat: the base comes from the environment the daemon was started in, so a daemon
  started from an ssh session and one started from a desktop terminal can resolve different workspaces.
- **Fenced.** The conductor acts under a scoped role (see [Guardrails](#guardrails)): it drives the rest of the fleet but
  cannot act on its own panel, stop the server, or fork-bomb the host.

The isolation is a **guardrail, not a sandbox**: the agent still runs as your user, so it could reach outside the
workspace with an absolute path. Baton shapes the environment so control is the easy path; it does not jail the process.
[Resource limits](LIMITS.md) do put a real ceiling on what it can consume — CPU, memory, processes — but that is a
resource boundary, not a filesystem or network one.

### The operator's brief — `$HOME/.baton/CONDUCTOR.md`

The built-in primer tells the conductor _how_ to drive baton; you tell it _what to do_. Write a goal and guide in
`$HOME/.baton/CONDUCTOR.md` and baton appends it to the conductor's briefing under an **Operator's brief** heading every
time the conductor is spawned or re-run. It is also **hot-reloadable**: `C-t R` (or a `SIGHUP` to the daemon) rewrites the
brief into the running conductor's workspace and prints a line in its panel saying so — no need to close and reopen it.
What a reload cannot do is change an agent's mind mid-session: an agent reads its project instructions when its session
starts, so the refreshed brief is what it will see the next time it looks, and the notice is there to tell you there is
something new to look at. The file is optional and never replaces the primer: the agent always keeps the control
mechanics and the forbidden actions. For example:

```md
# Mission

Keep a reviewer agent running on each open PR worktree. When one finishes, summarise its findings into a shell panel
named "report" and pause for me.
```

## `baton ctl` — the CLI

`baton ctl` is a thin, synchronous client over the session socket. Run from a plain shell it acts with the full-power
cockpit role; run inside the conductor panel it inherits the conductor identity and is fenced. Each command connects,
acts, and exits.

| Command                                                               | Does                                                                             |
| --------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `baton ctl list`                                                      | print the fleet as JSON (id, title, state, group, …)                             |
| `baton ctl tree [--json]`                                             | draw the process tree (groups → panels → OS children), with CPU%/RSS             |
| `baton ctl spawn [--agent CMD] [--arg A] [--dir D]`                   | spawn a panel (agent if `--agent`, else a shell); prints the new id              |
| `baton ctl spawn --worktree --dir <repo> --branch <name> --agent CMD` | branch `<repo>`, open a git worktree for it, and spawn the agent **in the tree** |
| `baton ctl send <id> <text> [--no-enter]`                             | type text into a panel; submits with a newline unless `--no-enter`               |
| `baton ctl attention --why <text> [--id ID]`                          | say this panel needs a human, and why — see [Raising a hand](#raising-a-hand)    |
| `baton ctl resolve [--id ID]`                                         | say the reason has passed; the panel leaves the queue                            |
| `baton ctl group <name> <id>...`                                      | file panels under a work item (a slash-`path` nests: `backend/api`)              |
| `baton ctl rename [--id ID \| --group G] <name>`                      | rename a panel or a group (rename a group to a path to re-parent it)             |
| `baton ctl pin <id>...` / `unpin <id>...`                             | pin/unpin panels to live tiles                                                   |
| `baton ctl signal <signal> <id>...`                                   | send a signal, e.g. `SIGINT`                                                     |
| `baton ctl close <id>...`                                             | close panels                                                                     |
| `baton ctl dispatch <id> <prompt>`                                    | assign a task brief to a panel and deliver it as a unit                          |
| `baton ctl dispatch-group <group> <prompt>`                           | fan one brief to a work item's whole subtree (nested groups too)                 |
| `baton ctl queue add <prompt> [--group G]`                            | enqueue a task for the scheduler to drain onto a free agent                      |
| `baton ctl queue add <prompt> --command <cmd> [--dir D] [--close]`    | spawn-on-demand: provision an agent when none is free                            |
| `baton ctl queue list`                                                | print the backlog as JSON (id, prompt, status, panel, group, …)                  |
| `baton ctl queue cancel <id>`                                         | cancel a queued task by id                                                       |
| `baton ctl queue promote <id>` / `demote <id>`                        | move a queued task to the head / tail of the backlog                             |
| `baton ctl queue drain`                                               | clear every queued task                                                          |
| `baton ctl conductor reset`                                           | delete the conductor's workspace so the next one starts clean                    |
| `baton ctl worktree list`                                             | print the worktrees baton opened as JSON, each `live` / `dead-slot` / `orphan`   |
| `baton ctl worktree sweep [--yes]`                                    | remove the **orphaned** ones; confirms on a terminal, needs `--yes` in a script  |

```sh
# Stand up a reviewer next to a worker and hand it the task.
id=$(baton ctl spawn --agent claude --dir ~/src/api)
baton ctl group review "$id"
baton ctl dispatch "$id" "review the open diff and list correctness risks"

# Or queue a batch and let the scheduler fan it across whoever comes free.
baton ctl queue add "audit the auth module"   --group review
baton ctl queue add "audit the billing module" --group review
baton ctl queue list

# Burst a fresh worker fleet through the backlog: each task spawns its own
# ephemeral agent when none is free, and closes it when the task is done.
baton ctl queue add "port module A" --command claude --dir ~/src --close
baton ctl queue add "port module B" --command claude --dir ~/src --close

# Give each worker its own checkout instead of sharing one. --dir is the
# REPOSITORY here, not the workdir: the workdir is the tree baton creates, and
# the new panel is filed under the branch as a work item.
baton ctl spawn --worktree --dir ~/src/api --branch feat/login --agent claude

# Inspect what the daemon is actually running: the fleet joined to the real OS
# processes each panel spawned. --json feeds a monitor or a script.
baton ctl tree
```

With `--worktree`, `--dir` changes meaning: it names the repository to branch from, and the panel's working directory is
the worktree baton opens for it. `--branch` is required, and both a missing branch and a `--dir` that is not a
repository are refused before any tree is made. Without `--worktree`, `spawn` is unchanged — `--dir` is the working
directory and no git runs. A conductor should prefer this whenever its workers would otherwise share a single checkout,
since agents editing one tree in parallel overwrite each other's work.

**The process tree.** `tree` roots at the daemon, scaffolds the fleet's nested work-item groups, files each panel under
its group with its process-group-leader pid, and hangs the panel's live OS descendant processes beneath it — the picture
`ps`/`pstree` can't give you because only baton knows which pid is which agent. Each pid-bearing line also trails its
cumulative CPU% since start and resident memory (RSS); groups and exited panels carry no columns. `--json` carries the
same figures as `cpu`/`rss` fields on every node.

```text
baton (daemon) pid=41022  baton  0.3%  28.4M
├─ [group: feature-x]
│  ├─ [hale/running] pid=41180  claude  12.5%  180.2M
│  │  └─ pid=41199  node  3.1%  95.7M
│  └─ [ellis/idle] pid=41205  bash  0.0%  2.1M
└─ [ungrouped]
   └─ [shell/running] pid=41240  zsh  0.0%  3.4M
```

**Dispatch vs. send.** `send` types raw keystrokes; `dispatch` hands the server the _objective_, which it records on the
panel (so it reaches every card and the snapshot) and delivers as a unit — waiting for the agent to be ready rather than
interleaving with a running command. See [Tasks and the queue](./SPEC.md#tasks-and-the-queue) for the model.

## Raising a hand

Every other verb here is something you do **to** a panel. `attention` and `resolve` are the two an agent says about
**itself** — the one participant that actually knows when it is blocked.

```sh
# Inside an agent panel: block on a decision, with the sentence the human will read.
baton ctl attention --why "two migrations conflict — which one wins?"

# …and once you have your answer, stand down.
baton ctl resolve
```

Neither takes an id in the form above, because the connection already knows which panel it is: baton injects
`BATON_PANEL_ID` into **every agent panel** (see the table below), the control client declares it on `hello`, and the
daemon addresses that panel — so an agent raises its own hand without ever having to discover its own id, anywhere in the
fleet. Anywhere else — a **shell** panel, a script, your own terminal — name the panel with `--id`; with neither, the
daemon answers `no panel id, and this connection declared no self` rather than acting on nothing. A **conductor**
connection may only ever name itself (see [Guardrails](#guardrails)).

**Why this outranks everything else.** Baton has two ways to notice a panel needs you, and both are guesses from the
outside: a **timer** that reads silence, and a **heuristic** that reads the last line of output for a question. A
declaration is the only signal that came from the thing being described, so it wins — see
[the lifecycle](./SPEC.md#lifecycle) for the states, and [ATTENTION.md](./ATTENTION.md) for the full precedence and the
queue a raised hand lands in. Concretely:

- **It takes effect immediately**, not on the next monitor tick. The task scheduler's free pool reads the panel's state,
  so a tick's delay would be a window in which baton hands queued backlog work to an agent that has already said it is
  waiting on a person.
- **It survives output.** An agent that prints a spinner while it waits on you keeps its raised hand; a heuristic-raised
  attention is withdrawn by the next byte. Only `resolve`, or the process ending, takes a declaration back.
- **`--why` is required.** A declaration displaces two guesses precisely because it can say why, and the person reading
  the queue sees that sentence instead of a scraped terminal line. A declaration with nothing to say is refused.
- **It is not persisted.** A declaration is a live process's statement about itself; it dies with the panel and does not
  come back on restore.

**`resolve` is the half that makes the other half trustworthy.** An agent that can put its hand down is one whose raised
hand means something. It is a no-op rather than an error when nothing stands, so it is safe to run unconditionally, and
after it the daemon derives the panel's state again from the ordinary lifecycle rather than guessing at one. It also
mutes the tail heuristic for that panel until the panel next produces output — otherwise the same unchanged question
still sitting in the scrollback would raise the flag again a second later, and `resolve` would be a verb that undid
itself.

**The reason is sanitised on the way in.** The daemon scrubs control characters and escape sequences out of `--why` when
it accepts the declaration, drops Unicode **format** characters with them (a bidi override such as `U+202E` would
otherwise render the row backwards in the operator's terminal), folds any whitespace to single spaces, and caps the
result at **200 runes** — it is a sentence for a person to read, and it rides every fleet snapshot to every client. So
it is one short, safe line by the time it is stored.

This is deliberately the boundary the scrubbing happens at, because the text is fanned out to the cockpit's queue (drawn
into a real terminal), to `baton ctl list`, to MCP tool results, and to plugin event handlers — **a frontend rendering
`reason` is holding text that is already safe and must not escape it a second time.** Panel _output_ is the opposite
case and is passed through byte-exact: it is a terminal stream, and the emulator is what interprets it.

## `baton mcp` — the MCP server

`baton mcp` is a [Model Context Protocol](https://modelcontextprotocol.io) server on stdio (newline-delimited JSON-RPC
2.0). It exposes the same verbs as MCP tools, so an MCP-speaking agent drives the fleet through structured tool calls
instead of shelling out:

`baton_list` · `baton_spawn` · `baton_send` · `baton_attention` · `baton_resolve` · `baton_dispatch` ·
`baton_dispatch_group` · `baton_enqueue` · `baton_queue` · `baton_reorder` · `baton_group` · `baton_rename` ·
`baton_pin` · `baton_unpin` · `baton_signal` · `baton_close`

`baton_spawn` takes `{agent, args, dir}`, and `{worktree: true, branch}` alongside them to spawn into a fresh git
worktree instead of into `dir` — the same verb rather than a second tool, so a conductor that can already spawn needs to
discover nothing new. With `worktree`, `dir` is the repository to branch from; `worktree` without `branch`, or a `dir`
that is not a repository, is a tool error and the fleet is unchanged.

**There is no worktree tool here at all** — neither listing nor sweeping. A conductor opens worktrees (through
`baton_spawn`) and the operator retires them, and a tool that showed an agent residue it is not allowed to clear could
only prompt it to nag. That absence is not the whole fence, though: a conductor panel has `BATON_ROLE` injected, so an
agent that shells out to `baton ctl worktree sweep` reaches the daemon as a conductor connection — and the daemon
refuses `worktree.sweep` to one. The missing tool closes the MCP route; the refusal closes the `ctl` route.

`baton_dispatch` / `baton_dispatch_group` assign a task brief to a panel or a whole work item; `baton_enqueue` adds one
to the backlog (optionally spawn-on-demand, with a `command` to provision a worker when none is free), `baton_queue`
reads it back, and `baton_reorder` moves a waiting task to the head or tail. These are the verbs a conductor uses to run
the flagship **you → conductor → fleet** flow: you hand the conductor a batch of objectives, it enqueues them, and the
scheduler drains them onto the workers as they come free.

`baton_attention` / `baton_resolve` are the pair an agent uses on **itself** rather than on the fleet: the `why` is
required and becomes the sentence the human reads, and both default to the caller's own panel when given no `id`. See
[Raising a hand](#raising-a-hand) for what makes a declaration outrank baton's own guesses.

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

`panel.attention` carries `reason` (required) and an `id` that may be left empty to mean the connection's own `self`;
`panel.resolve` carries the same `id`. Both reply with an error or with the `panels` snapshot the change produced, and
the panel's declared reason comes back on `proto.Panel.reason` — already sanitised, as above.

A dispatch carries two more fields: `prompt` (the brief) and an optional `submit` override (the keys appended to send it,
default a newline) on `panel.dispatch` / `panel.dispatch-group`; `task.enqueue` / `task.cancel` / `task.promote` /
`task.demote` / `task.drain` / `task.list` drive the backlog and reply with a `tasks` snapshot. A spawn-on-demand
`task.enqueue` carries the worker's `path` / `args` / `dir` and an `ephemeral` close-on-done flag.

Baton injects the wiring into an agent panel's process, which both `baton ctl` and `baton mcp` read automatically:

| Variable         | Is                                                | Injected into                            |
| ---------------- | ------------------------------------------------- | ---------------------------------------- |
| `BATON_SOCK`     | the control socket to dial                        | **every agent panel**                    |
| `BATON_PANEL_ID` | the panel's own id — the `self` it declares       | **every agent panel**                    |
| `BATON_ROLE`     | `conductor` — the scoped role to declare on hello | **the conductor only** (it is the fence) |

Every agent panel is told which panel it is, because a control client that cannot name itself cannot say anything about
itself — which is the whole point of `attention` and `resolve`. Being told grants nothing: an empty `BATON_ROLE` is the
plain, unscoped connection an agent panel has always had, so a worker's reach is exactly what it was before an id was
ever injected. Only the conductor is handed the role, because only the conductor is fenced by it.

A **shell** panel is deliberately given neither. A shell is a launcher — every program a person runs in it would inherit
the marking — and the human sitting at one already has what an agent lacks: the cockpit names the panel and `--id` is one
flag away. `BATON_SOCK` still reaches it, but by inheritance rather than injection: the daemon pins it into its own
environment and every panel starts from that.

## Guardrails

The conductor role is enforced server-side, before any command takes effect. It is keyed off the self-declared role over
a **uid-private socket** — a guardrail against agent accidents, not a security boundary (a local process of your user can
always speak the socket directly).

| A conductor may                           | A conductor may not                                            |
| ----------------------------------------- | -------------------------------------------------------------- |
| list, spawn, group, rename, pin, move     | close, signal, or send input to **its own** panel              |
| signal and send input to **other** panels | **dispatch a task to its own** panel                           |
| dispatch to other panels, enqueue tasks   | **drain the queue** — clearing the backlog is operator-only    |
| **list** the worktrees baton opened       | **sweep** them — removing trees from disk is operator-only     |
| read a panel's title, state and telemetry | **log a panel to a file**, or read a log back — see below      |
| reorder queued tasks (promote / demote)   |                                                                |
| **raise its own hand** and stand down     | raise or lower **another panel's** hand                        |
| close other panels, purge exited          | reload or stop the server                                      |
|                                           | spawn faster than the rate cap, or past the fleet ceiling (64) |
|                                           | **name an agent profile** on a spawn — see below               |

A worktree spawn is a spawn, so it draws on the same two limits from the same purse: the fleet ceiling and the rate cap
count `spawn --worktree` exactly as they count `spawn`, and a conductor cannot dodge one by switching to the other.

Opening a worktree and **retiring** one are fenced differently on purpose. A conductor may open them, under those caps,
and may see what its spawns have left behind — reading the residue is not removing it. Clearing it is the operator's,
because a sweep is the one verb in this surface that deletes work from a disk rather than bookkeeping from a fleet.

So a conductor can fill and dispatch from the backlog but cannot wipe it, and the queue gives it no way around the
self-fence: a brief it enqueues is drained by the scheduler onto _other_ idle agents, never back onto itself.

The fence stops a conductor acting **destructively** on itself — closing, signalling, feeding input. Saying "I need a
decision" is the opposite, so `panel.attention` and `panel.resolve` are the one pair fenced the other way round: a
conductor may raise and lower **its own** hand, and only its own. Allowing it at all is because the conductor is the one
agent the server always has an identity for, and refusing it the verb that exists for agents would be a strange way to
build a control plane for them. Restricting it to itself is because a declaration takes its panel out of the scheduler's
free pool until something withdraws it — a conductor free to raise hands across the fleet is a conductor that can freeze
the backlog for every other agent, one looping call at a time.

Panel logging (`C-t l` / `C-t L`, [LOGGING.md](LOGGING.md)) is refused outright, in both directions. `panel.log` asks
the daemon to **write files on its own host, as you**, which is the shape of request the remote actions are already
fenced away from; and `panel.logview` would hand an agent another panel's transcript to read at leisure, which is the
surface the inbox's `panel.tail` fence exists to keep shut. Opening either later is deleting one line, and the interface
room is left for exactly that.

A spawn from a conductor has its **profile name stripped**, so the panels it creates resolve to the fleet-wide
[resource limits](LIMITS.md) rather than to any profile's own. The name is what a panel's caps resolve through, so an
agent free to name one would be an agent free to name its way into wider caps than the fleet's.

A plain cockpit connection declares no role and is never fenced.
