# Security Policy

## Supported versions

Fixes land on the latest release. There are no maintained backport branches — if
you are on an older tag, upgrading is the fix.

| Version | Supported |
| ------- | --------- |
| 1.0.x   | ✅        |
| < 1.0   | ❌        |

## Reporting a vulnerability

Report privately through
[GitHub's advisory form](https://github.com/cmj0121/baton/security/advisories/new),
or by email to <cmj0121@gmail.com>. Please do not open a public issue for
something exploitable.

Include what you need to make it reproducible: the version (`baton --version`),
the platform, and the sequence that triggers it. A patch is welcome but never
required.

This is a personal project, not a funded one — expect an acknowledgement within
a week and a fix timed to how much of the fleet it puts at risk. There is no
bounty.

## What baton is, and is not, a boundary for

Read this before deciding whether something is a vulnerability, because baton
draws its line in an unusual place.

Baton runs a background daemon that owns real terminals, spawns the programs you
tell it to, and hands one of those programs — the conductor — a control surface
over the rest of the fleet. **Every one of those runs as your user, with your
filesystem and your network.** The confinement in baton is a guardrail against
agent accidents, not a sandbox against a hostile program:

- **The conductor's fence** ([docs/CONTROL.md](docs/CONTROL.md)) narrows what a
  conductor-role connection may ask the server to do. It does not stop the agent
  process from doing anything a shell of yours could do, including reaching
  outside its workspace by absolute path.
- **Resource limits** ([docs/LIMITS.md](docs/LIMITS.md)) cap cpu, memory and pids
  for a panel's whole process tree via cgroup v2. They keep a runaway build from
  taking the machine; they are not a security boundary, and the docs say so.
- **The socket is uid-private.** Any local process running as you can connect —
  that is the intended trust level, not a flaw.
- **Lua plugins** (`$HOME/.baton/plug-in.lua`) run unsandboxed, with your
  privileges, by design. Treat a plugin like a shell script you are about to run.

So this is **not** a vulnerability: "an agent panel can read files outside its
workdir", "a plugin can run any command", "another process of my own user can
drive the socket". Those are the documented trust model.

This **is** worth reporting:

- Anything that lets a **non-owner** — another uid, a remote host, an unattached
  client — reach the socket, the daemon, or a panel.
- A conductor-role connection performing an op the fence is supposed to refuse,
  or escalating to a full cockpit role.
- Anything that makes baton execute a command you never configured — a crafted
  panel title, task brief, agent output, config or `TUI.yaml` that reaches a
  shell.
- Terminal-escape handling that lets panel output forge cockpit UI, drive the
  host terminal, or leak one panel's scrollback into another.
- Secrets landing somewhere they should not: the state file, the daemon log, the
  usage footer, or a crash dump.
- A crash or hang the daemon cannot recover from that is reachable from panel
  output or a socket message.
