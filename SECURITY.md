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
- **Container isolation** ([docs/ISOLATION.md](docs/ISOLATION.md)) is opt-in per
  agent profile and confines an agent that is **wrong** — it reaches only the
  workspace you mounted, only the environment you named, and writes as your uid
  rather than root. It is **not** a boundary against an agent that is trying to
  escape: the runtime is driven by your uid over your docker socket, and anything
  that can reach that socket can reach the host. Read "container" as "workspace
  boundary", never as "sandbox". An isolated panel is also not given `BATON_SOCK`,
  so it cannot drive the fleet.
- **The remote passkey** ([docs/REMOTE.md](docs/REMOTE.md)) gates a cockpit attached from another machine. Read it
  as a switch with a revocation handle, not as authentication. The transport is `ssh(1)` — baton opens no port, ships
  no TLS and invents no key exchange — so the far side of the pipe already runs as your uid, and that uid could run
  `baton` on that machine anyway. What the passkey proves is that you **deliberately** enabled remote for this window;
  what it buys you is that rotating it (`C-t @`, then `n`) locks out every new attach. It is generated on enable, held
  in memory, and never written to disk. Failed attempts are rate-limited and logged. Changing it, and switching remote
  off, are refused over a remote attach — that is the one asymmetry, and it exists so a live remote connection cannot
  turn its window into a permanent one.

- **The usage `oauth` source reads a credential** ([docs/USAGE.md](docs/USAGE.md)).
  It is **opt-in** — nothing reads a credential unless you set `usage.limits: oauth`
  — and the default source reads none at all. When it is on, baton reads Claude
  Code's OAuth **access** token from `~/.claude/.credentials.json`, or from the login
  keychain on macOS, which may prompt you: that prompt is the operating system doing
  the right thing, because baton is asking for somebody's credential and that should
  be a visible act. The token is read per request, sent only to the one constant host
  `api.anthropic.com`, never written anywhere, and never placed in a log line or an
  error string — an error outlives the process and ends up in bug reports. The
  **refresh** token is never decoded at all, because a reader that cannot load it
  cannot leak it. What this is not: an authorization boundary. Anything running as
  your uid can read the same file and run the same keychain lookup.
- **Panel logs** ([docs/LOGGING.md](docs/LOGGING.md)) are plain-text transcripts
  the daemon writes into a directory you named, as you, with mode 0600. They are
  a record, not an audit trail: anything that can reach your uid — including the
  agent in the panel being logged — can edit or delete one. A shell panel's log
  holds everything typed into that shell, which is why nothing is logged until
  you set `panel.log-dir`, and why auto-logging is per agent profile rather than
  fleet-wide.
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
  usage footer, or a crash dump. An isolated panel receiving an environment
  variable `env-allow` does not name counts here too.
- A crash or hang the daemon cannot recover from that is reachable from panel
  output or a socket message.
