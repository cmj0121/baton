# Baton — Remote access over SSH

**English** · [繁體中文](REMOTE.zh-TW.md)

Your fleet is on the build box. You are on the laptop. `baton --remote` attaches **the same cockpit** to it — same
binary, same keys, same views — over the ssh you already use to reach that machine.

```sh
baton --remote          # a form: address, then passkey — and you are attached
```

There is no web frontend, no mobile app, and no second tool. Baton's architecture has always drawn pluggable frontends
over a socket; remote is what makes that drawing true, and it stays terminal-native, which is the whole point.

## How it works

The transport is `ssh(1)`. Nothing else.

```text
  laptop                                       build box
  ┌────────────────┐                        ┌──────────────────────────────┐
  │ baton --remote │                        │ baton --stdio                │
  │  address       │  ssh -p 22 host …      │  find-or-start the daemon    │
  │  passkey       │───────────────────────►│  copy ⇄ the session socket   │
  └───────┬────────┘   stdin / stdout       └───────────────┬──────────────┘
          │                                                 │
          │  hello { role: "remote", passkey, source }       │
          ▼                                                 ▼
      the cockpit  ◄──── the ordinary protocol ────────  the fleet
```

`baton --remote` runs `ssh <address> baton --stdio`. The far side bridges that pipe to its own unix socket, finding or
starting the daemon exactly as a local attach does, and the two ends then speak the same protocol they always have.

This means baton **opens no listening port, ships no TLS, and invents no key exchange.** The stream carries every
panel's full terminal input and output, and it is protected by exactly the thing that already protects your shell —
your keys, your `known_hosts`, your `~/.ssh/config`. A jump host, a key per host, an alias: all of it keeps working,
because ssh reads that file and baton does not re-implement any of it.

`--stdio` is not something a person types. It is the far-side bridge, and it is listed here only so the command line in
your `ps` output is not a mystery.

## Turning it on

Remote is **off by default**, and stays off until you say otherwise. Two ways in:

```yaml
# $HOME/.baton/config
settings:
  remote: true # accept a cockpit attached over the ssh bridge
```

…or press **`C-t @`** in the cockpit and then `e`. The key is prefix-gated on purpose: it exposes the machine, and that
is not a key to put a fingertip from the arrow keys. `@` rather than a letter, because a key reached after the prefix
shadows the command on that same key — and because `@` reads as what the overlay lists: `user@host`.

`settings.remote` is read as a **change**, not as a value. If you switch remote off in the cockpit and then reload the
config for an unrelated edit, it stays off — a reload does not undo a decision you made since.

## The passkey

Enabling generates an 8-character passkey. It is held in memory and **never written to disk**, so a daemon restart
always means a new one. A cockpit attaching over the bridge sends it on `hello`; without the current one the attach is
refused, rate-limited, and logged.

Be clear about what this buys, because the transport already decided the hard part. The far side of the pipe runs as
your uid — and your uid can run `baton` on that machine anyway. So the passkey is:

- ✅ proof you **deliberately enabled** remote for this window
- ✅ a **revocation handle** — rotate it, and new attaches need the new code
- ❌ **not** an authentication boundary against someone who can already ssh in as you

That is the same line [SECURITY.md](../SECURITY.md) draws for the conductor's fence and the resource caps: a guardrail
against accidents, not a sandbox.

One thing it does buy outright: taken in the form rather than as a flag, it **never enters argv**, so it stays out of
your shell history and out of `ps` for every other process on the client machine.

## The remote view — `C-t @`

```text
 REMOTE   enabled · passkey K7m2QxP9

   SOURCE                 ROLE      ATTACHED
 ▸ local ←                cockpit   2h 14m
   cmj@laptop.lan         remote    6m
   cmj@phone              remote    1m

 ↑↓ select · k kick · n new passkey · x disable · esc close
```

| Key     | Does                                                                   |
| ------- | ---------------------------------------------------------------------- |
| `↑` `↓` | move the cursor (`k` is the kick, so it cannot also be "up")           |
| `e`     | enable remote — only when it is off                                    |
| `k`     | kick the selected connection; the far cockpit is told why              |
| `n`     | rotate the passkey — live connections stay, new ones need the new code |
| `x`     | disable remote and drop every remote connection                        |
| `r`     | re-ask the fleet for the list                                          |
| `esc`   | close                                                                  |

The list is **pushed**, not polled: every attach, detach and kick refreshes an open overlay on both sides of the pipe.

`ATTACHED` is measured from the moment the connection said hello. `SOURCE` is what the far end called itself —
`user@hostname` of the machine the cockpit runs on — so it is a label to recognise a connection by, self-declared
exactly as the role is. It is never an identity the server is asked to trust.

### One asymmetry, on purpose

`k` works from **either** side. Kicking is how you deal with a connection you did not expect, and needing to walk to
another machine first would make it useless.

`e`, `n` and `x` are **local only** — the server refuses them over a remote attach, and the overlay stops offering
them. Anyone holding a live remote attach has already proved they had the current code; letting them mint the next one
would turn one window into a permanent one. For the same reason a remote cockpit is never told the passkey at all: it
sees `enabled`, and where to go and read the code.

## What a remote cockpit may do

Everything a local one may. Remote exists to be useful, and a cockpit that could look but not touch would not be worth
the pipe.

It declares `role: "remote"` on `hello`, which is what puts it in the list — and what leaves room for a narrower role
later without a protocol change.

Note that the fleet's machine is the one that matters throughout: the agent backends offered in the picker are the ones
**that host** can run, the panels spawn there, `C-t l` writes logs there, and the footer's CPU and memory are its.

## Failures, and what they say

The connection form reports what actually went wrong and keeps your address, so a mistyped passkey is one keystroke
from being retyped:

| What you see                                 | What happened                                                   |
| -------------------------------------------- | --------------------------------------------------------------- |
| `No route to host` / `Permission denied`     | ssh's own words — a network or a key problem, before baton ran  |
| `is baton on the remote PATH?`               | `ssh host cmd` runs a non-interactive shell; see below          |
| `remote access is not enabled on this fleet` | nobody has switched it on over there                            |
| `wrong passkey`                              | the code has rotated, or was mistyped                           |
| `too many failed attempts`                   | five wrong codes in a minute; the door holds for the rest of it |

### `baton: command not found`

This is the most likely first failure and it is not baton's fault: `ssh host cmd` runs a **non-interactive** login
shell, whose `PATH` often misses `~/.local/bin` or `~/go/bin`. Name the far-side command explicitly:

```yaml
# $HOME/.baton/config — on the machine you dial FROM
settings:
  remote-command: /home/cmj/go/bin/baton --stdio
```

## Losing the connection

If the link drops or ssh dies, the cockpit reports it and exits. **The fleet is untouched** — the daemon keeps running,
every panel keeps its process and its scrollback, and `baton --remote` again puts you back where you were. There is no
reconnect-with-backoff in this first cut; it can be added later without a wire change.

A cockpit the fleet drops on purpose — a kick, or remote being switched off under it — is told **why** before the socket
goes, and prints that reason on the way out.

## Not this

- No listening TCP port, no TLS, no passkey-as-crypto.
- No web or mobile frontend.
- No multi-user accounts and no per-connection ACLs.
- Not a security boundary. See [SECURITY.md](../SECURITY.md).

## Config reference

```yaml
# $HOME/.baton/config
settings:
  remote: false # accept cockpits over the ssh bridge (default: false)
  remote-command: baton --stdio # what `baton --remote` asks ssh to run on the far side
```

`remote` is read by the machine the **fleet** runs on; `remote-command` by the machine you **dial from**.
