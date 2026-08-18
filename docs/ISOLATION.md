# Baton — Container isolation

**English** · [繁體中文](ISOLATION.zh-TW.md)

Everything else baton confines is a guardrail against an agent's **accidents**: the resource caps stop a runaway build,
the conductor fence narrows what a control connection may ask for. None of it helps against an agent that is simply
**wrong in a costly direction** — and that is the failure people actually fear when they leave a fleet running
unattended.

Isolation runs an agent panel inside a container, so an agent that goes wrong is confined to a workspace instead of
confined to a promise.

It is **off by default and opt-in per agent profile**. An isolated panel starts slower, debugs harder, and needs an
image you chose. Nothing changes for a profile that does not ask for it.

```yaml
# $HOME/.baton/config
panel:
  agents:
    claude:
      command: claude
      isolate: docker # none (the default) | docker
      image: baton/agent:latest # you name it; baton ships none
      mount: workspace # workspace | workspace+home
      network: host # host | bridge | none
      env-allow: [ANTHROPIC_API_KEY] # nothing else crosses
      user: "" # empty = your own uid:gid
```

## Baton ships no image

The moment it did, it would own a toolchain matrix it cannot maintain — your Go version, your node, your test database
client. So `image` is required and you name it. A first image is usually four lines on top of whatever your project
already builds with, plus the agent CLI.

A container that cannot start — no runtime installed, no such image, a flag the runtime refuses — **fails the spawn with
the runtime's own message**. There is no fallback to an unisolated panel anywhere on the path, because falling back
would produce exactly the thing the setting exists to prevent.

## What the agent keeps

Isolation is easy; a _useful_ isolated agent is not. An agent earns its keep by running your build, reading your repo,
and reaching its own API — confine it properly and it can do none of that. Each knob is one of those trades:

| Key         | Default     | Confined                                 | Kept                                                    |
| ----------- | ----------- | ---------------------------------------- | ------------------------------------------------------- |
| `mount`     | `workspace` | everything outside the working directory | the tree the agent was pointed at — its blast radius    |
| `network`   | `host`      | `none`: nothing, not even the model API  | `host`: your localhost and the internet, as you have it |
| `env-allow` | _empty_     | every variable you do not name           | only what you list, by name                             |
| `user`      | your uid    | writing as root into a tree you own      | files that belong to you afterwards                     |

### `mount`

`workspace` binds the panel's working directory and nothing else. `workspace+home` adds `$HOME`, which is what an agent
CLI that authenticates through a file under `$HOME` needs in order to start at all — and it hands the container
everything you have. It is an escape hatch, and this document is calling it one.

The workspace is mounted **at its own absolute path**, not at a tidy `/workspace`. Baton's git tooling — the diff pop-up,
the git menu, worktree creation — runs on the _host_ against that same path, so a container that saw a different one
would make every path in the agent's output wrong on the outside.

### `network`

- `host` — the container shares your network namespace. The agent reaches its model API, your registries, and services
  on your localhost. This is the default, because an agent that cannot reach its own API is not an agent.
- `bridge` — the runtime's own NAT network. Egress works; your localhost does not. Worth choosing on macOS, where host
  networking is not the same feature it is on Linux.
- `none` — no network. Honest, and usable only for an agent that needs none.

There is no egress allowlist. Filtering by destination is a much larger build than choosing between three stances, and
it is not in this cut.

### `env-allow`

Nothing from your environment reaches the container unless `env-allow` names it. Every variable crosses as a full
assignment, never as a bare name that would inherit whatever the daemon happens to hold — which is what makes the
promise checkable rather than hopeful. A name that is unset on the host passes **nothing**, rather than an empty string.

### `user`

By default the container runs as your own uid and gid, so an agent cannot leave root-owned build output in a tree you
then need `sudo` to clean up — a harm that isolation itself would otherwise create. Set `user: root` for an image that
genuinely needs it (one that installs packages on start), or an explicit `1000:1000` to pin something else.

An image with no passwd entry for your uid has no `$HOME`, and a tool writing to an unset `$HOME` writes to `/`. When
`$HOME` is not mounted, baton points it at `/tmp` inside the container.

### Signals

A signal from the cockpit is handed to the container's PID 1 rather than to the panel's process group — that group is the
runtime client, and ending it would close the panel instead of interrupting the job the key was aimed at. `--init` runs a
reaper as PID 1 that forwards the signal on, so the same key does the same thing isolated or not. Typing `Ctrl-C` into
the panel is unchanged either way: that is a byte on the terminal, delivered by the container's own TTY.

A signal the runtime refuses is reported rather than retried against the client, because delivering it there would be a
different action from the one you asked for. Closing the panel force-removes the container, so "make it stop" always has
a working key.

## Resource limits still apply

The caps from **[LIMITS.md](LIMITS.md)** work exactly as before — the same config, the same two layers, the same
values — but a different enforcer holds them:

```txt
unisolated:  cgroup v2 around the panel's process tree   (Linux only)
isolated:    the runtime's own --cpus / --memory / --pids-limit / --ulimit
```

The cgroup is deliberately **not** used for an isolated panel. It would confine the runtime _client_ — a few hundred
kilobytes that talk to a daemon — while the container itself is that daemon's child and would run entirely uncapped: a
limit that reads as applied and holds nothing.

Two consequences worth knowing:

- `nofile` **is** enforced under isolation. It is the one cap cgroup v2 has to report as unenforced, and a runtime sets
  it on the process it starts.
- Caps are enforced on **macOS** for the first time. cgroup v2 is Linux-only, so an unisolated macOS panel has always
  run uncapped; an isolated one does not.
- A cap changed by `C-t R` does **not** reach a running container. It applies to the next spawn.

## What you give up

These are real losses, listed here rather than discovered later:

- **The fleet socket.** `BATON_SOCK` is not passed in and the socket is not mounted, so an isolated agent cannot drive
  the fleet — no `baton ctl`, and a conductor cannot be isolated. This is the right direction (a conductor that could
  still reach the socket would be isolated from the filesystem and not from the thing it can actually move), but it is a
  loss. The panel id still crosses.
- **The process view.** `C-t o` and `track-cwd: proc` walk the panel's process tree on the host, and the host sees only
  the runtime client. The agent's real processes are inside the container.
- **Per-panel usage.** The usage footer's panel view reads Claude Code's transcript by session id; under isolation that
  file is written inside the container. The account-wide window view is unaffected.
- **Startup time and page cache.** Twenty isolated panels are twenty container starts. Isolation is per panel in this
  cut; sharing one container across a work item's panels is not offered.

## What this is, and is not

It **is** a workspace boundary. An agent that is wrong — that deletes the wrong tree, rewrites the wrong config, runs
the wrong script — reaches only what you mounted.

It is **not a security boundary against a hostile agent.** The runtime is driven by your uid over your docker socket,
and anything that can reach that socket can reach the host. Do not read "container" as "sandbox" here. See
**[SECURITY.md](../SECURITY.md)**.

It is **not applied to the daemon, the conductor, or shell panels.** Agents only.

It is **not a toolchain manager.** Baton neither builds nor supplies an image, and never pulls one on your behalf beyond
what the runtime does for a `docker run`.

## Related keys

| Key     | Does                                                                       |
| ------- | -------------------------------------------------------------------------- |
| `C-t R` | reload the config; a changed policy applies to panels spawned from then on |
| `r`     | re-run an exited panel — also how a changed policy lands on an old panel   |
| `C-t P` | panel config — the caps, which apply inside a container too                |
