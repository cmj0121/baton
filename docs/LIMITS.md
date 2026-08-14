# Baton — Resource limits

**English** · [繁體中文](LIMITS.zh-TW.md)

An agent left alone can eat the machine: a runaway test suite, a build that forks until the fan screams, a process that
grows until everything else swaps. Baton can cap what each panel is allowed to use — CPU, memory, processes — and hold
its **whole process tree** to it, not just the agent binary you launched.

Limits are **off by default**. An unconfigured baton caps nothing and never touches a cgroup.

```yaml
# $HOME/.baton/config
panel:
  # The fleet-wide floor: every panel runs under these unless a profile says otherwise.
  limits:
    cpus: "2" # CPU cores; fractional is fine ("1.5")
    memory: 4Gi # hard ceiling — reaching it kills the tree
    memory-high: 3Gi # throttle-before-kill watermark
    pids: 512 # most processes/threads in the tree — the fork-bomb cap
    nofile: 4096 # open files per process (see "What is not enforced")

  agents:
    claude:
      command: claude
      limits: { memory: 8Gi } # inherits cpus/pids from above, raises memory

    heavy-build:
      command: claude
      limits: { cpus: "8", memory: 16Gi, pids: unlimited }
```

## The three states of a cap

Every cap is written as a string, because it has to say three different things:

| Written as     | Means                                    |
| -------------- | ---------------------------------------- |
| _field absent_ | **inherit** whatever the layer above set |
| `"2"` / `4Gi`  | **cap** at this value                    |
| `unlimited`    | **lift** a cap the layer above set       |

This is why a cap is not a number. `0` would have to mean both "inherit" and "no limit", and a profile could never lift
a cap the fleet-wide block imposed — it could only ever narrow it.

**Quantities.** Sizes take both unit families: binary `4Gi` / `512Mi` / `1.5Gi`, decimal `2G` / `500M`, a plain byte
count, and a trailing `B` is fine (`4GiB`). `cpus` is a core count, not a percentage — `"2"` means two cores' worth of
CPU time per wall-clock second. A quantity baton cannot read is dropped back to "inherit" when the config loads, so a
typo costs you that one cap rather than wedging the daemon.

## Two layers

A panel's effective caps are the fleet-wide `panel.limits` with its **agent profile's** own layered over them, field by
field:

```txt
panel.limits:        cpus 2 · memory 4Gi · pids 512
agents.claude:                 memory 8Gi
                     ─────────────────────────────────
effective:           cpus 2 · memory 8Gi · pids 512
```

A profile restates only what it changes. A shell panel, the scratch shell, and any agent spawned without a profile get
the fleet-wide caps alone.

**The server resolves the policy, never the client.** A `panel.create` names its profile; the daemon looks that name up
in its own config. This matters because one of baton's clients _is an agent_ — the conductor drives the fleet over the
same socket — so a policy a client could send is a policy an agent could widen. Scoped connections have the profile name
stripped at the fence, which means agent-driven spawns (`baton ctl`, the MCP tools, the conductor) always land on the
fleet-wide caps.

## Editing them in the cockpit

`C-t P` opens **panel config**, which carries the fleet-wide caps under a RESOURCE LIMITS section — `↑↓` to move, `e` to
edit, `enter` to save. A value baton cannot read is refused with the overlay left open on what you typed, so you correct
the typo rather than retype the line.

```txt
╭───────────────────────────────────────────────────────────────────────╮
│   P A N E L   C O N F I G                                             │
│                                                                       │
│     default shell   /bin/zsh                                          │
│     replay buffer   512 KiB                                           │
│                                                                       │
│   R E S O U R C E   L I M I T S                                       │
│                                                                       │
│     cpus            2                                                 │
│   ▸ memory          4Gi                                               │
│     memory-high     no cap                                            │
│     pids            512                                               │
│     nofile          no cap                                            │
│                                                                       │
│   limits cap a panel's whole process tree · enforced by cgroup        │
╰───────────────────────────────────────────────────────────────────────╯
```

The last line is the one to read first: it says whether the caps on this screen **actually bite on this host** (see
[Where they are enforced](#where-they-are-enforced)). Per-agent caps are hand-edited in `$HOME/.baton/config` — the
cockpit edits the fleet-wide block only.

## Reloading them

`C-t R` (or a `SIGHUP` to the daemon) applies new caps **to the running fleet**, with nothing to restart:

- **cpus, pids, memory-high** — take hold immediately, on every live panel.
- **memory** — a **raised** ceiling applies at once. A **lowered** one waits for the panel's next re-run (`r`): writing a
  smaller hard limit than the tree is currently using makes the kernel reclaim against it and kill the agent mid-task,
  which is worse than the cap landing a moment later. `memory-high` still throttles at the new value straight away, so a
  lowered ceiling slows the tree down immediately either way.

A panel records the **name** of the profile it was spawned from, not the caps that name resolved to. So a reload
re-points every live panel at the new policy, and a profile you delete from the config falls back to the fleet-wide caps
rather than leaving a stale override behind. It survives a restart too: a restored panel resolves through the same
profile the live one did.

## Where they are enforced

| Host                                     | Backend  | What you get                                        |
| ---------------------------------------- | -------- | --------------------------------------------------- |
| Linux with a delegated cgroup v2 subtree | `cgroup` | cpus, memory, memory-high, pids — on the whole tree |
| Linux without delegation                 | `none`   | nothing; the reason is reported                     |
| macOS, everything else                   | `none`   | nothing; cgroup v2 is Linux-only                    |

Enforcement is **cgroup v2**, and it is cgroups rather than `ulimit` for one reason: the unit has to be the process
tree. An agent forks node, git, test runners; an rlimit is per-process and inherited, so a 4Gi rlimit means every
descendant may take 4Gi. A cgroup caps them together.

Each panel is placed in its cgroup by `clone3(CLONE_INTO_CGROUP)` — the child's first instruction already runs inside
the cap, so there is no window in which it could fork a descendant that escapes.

**A host that cannot enforce says so, loudly.** It is reported in the daemon log at startup, and on the panel-config
footer as `NOT enforced here · <why>`. A cap that looks applied and is not is worse than one you never set, so baton
will not let that be silent. On macOS the honest options are a Linux VM or a container per agent.

**What is not enforced.** `nofile` is an open-file limit, which is per-process `RLIMIT_NOFILE` territory rather than a
cgroup knob — it is accepted in the config and reported as unenforceable, pending an rlimit backend. If the host
delegates only some controllers, the caps that _can_ apply still do, and the rest are logged per spawn.

## What this is, and is not

It is a **resource** boundary: an agent cannot take more CPU, memory, or processes than you allowed it, whatever it runs.

It is **not a security boundary**. The agent still runs as your user, in your filesystem, with your network. It can read
your files and reach the internet exactly as before — see the guardrails note in **[CONTROL.md](CONTROL.md#guardrails)**.
Filesystem and network confinement need a different backend (a container, `bwrap`, `sandbox-exec`), which baton does not
have yet.

## Related keys

| Key     | Does                                                             |
| ------- | ---------------------------------------------------------------- |
| `C-t P` | panel config — the fleet-wide caps, plus the enforcement readout |
| `C-t R` | reload the config; applies new caps to the running fleet         |
| `r`     | re-run an exited panel — also how a deferred memory cap lands    |
| `C-t o` | process tree — what each panel is actually running               |
