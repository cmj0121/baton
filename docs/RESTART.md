# Panel restart policy and remembered directory

**English** · [繁體中文](RESTART.zh-TW.md)

A panel whose process dies can come back on its own. It is **off by default** —
Baton has never started a process you did not ask for, and a policy that does
should be opted into rather than inherited on upgrade.

```yaml
# $HOME/.baton/config
panel:
  restart: on-failure # never (default) | on-failure
  restart-max: 5 # consecutive failures before giving up
  restart-backoff: 2s # base of the exponential wait between attempts
  restart-healthy: 30s # a run lasting this long resets the failure counter
```

## The hard part is knowing when _not_ to

Restarting is easy. Not restarting the things you meant to stop is what makes a
supervisor liveable.

| What happened                 | Restart? | How Baton tells                                       |
| ----------------------------- | -------- | ----------------------------------------------------- |
| ssh dropped                   | **yes**  | exit 255                                              |
| the agent CLI crashed         | **yes**  | any non-zero exit                                     |
| you typed `exit` in a shell   | no       | exit 0 — a finished job, not a failure                |
| the agent finished its task   | no       | exit 0                                                |
| you closed the panel with `w` | no       | the panel is gone before its process is reaped        |
| you sent it a signal with `s` | no       | the stop was recorded before the signal was delivered |
| the daemon is shutting down   | no       | the whole fleet is being killed on purpose            |

The last two are the ones that need saying. A signalled process and a crashed
process exit **identically** — both arrive as exit code `-1`, because a
signal-terminated process has no exit status to report. So the exit code alone
cannot tell "I asked it to stop" from "it fell over", and Baton records the
intent when you press `s` rather than guessing afterwards.

That record is deliberately short-lived. A signal is not proof of intent to kill
— `SIGINT` to an agent interrupts a task the process survives — so the
suppression lasts ten seconds. A genuine crash an hour later still brings the
panel back.

## Giving up is loud

A crash loop must not quietly eat an hour, so the wait grows and the attempts are
counted. With the defaults, a panel that keeps dying reads:

```text
exited · restarting in 2s (1/5)
exited · restarting in 4s (2/5)
exited · restarting in 8s (3/5)
…
exited · restart limit reached after 5 failures
```

and then stays there until you re-run it yourself with `r`. The backoff doubles
per consecutive failure and is capped at five minutes: past that a "wait" reads
as "gave up" while still claiming to be trying.

`restart-healthy` is what keeps the counter honest. A run that stays up that long
was a good run, so the count resets — a panel that has been up for a day gets the
full budget again rather than the tail of an old crash loop.

A restart is visible where the exit that caused it was: the panel's viewers get a
`[restarting in 2s (1/5)]` line in place of the usual `[process exited]`, and a
`[restarted]` line when the new process comes up. It is the one thing that tells
a fresh process apart from a program that cleared its own screen.

## Per-agent override

The policy layers exactly like the resource limits: the fleet-wide block is the
floor, and a profile restates only what it changes.

```yaml
panel:
  restart: on-failure # the fleet reconnects and retries
  agents:
    claude:
      command: claude
      restart: never # …but if this one dies I want to look at it myself
```

All four keys are available per profile, not just `restart`.

## Why there is no `always`

systemd has it; Baton does not offer it. For an agent panel "restart forever" is
almost always wrong: an agent that finished its task is _supposed_ to stop, and a
mode that cannot tell that apart turns every completed run into an infinite loop.

The case `always` would serve — a dropped ssh, a supervised tunnel — is already
`on-failure`'s case, because both exit non-zero. Writing `always` in the config
is refused by name rather than quietly treated as `on-failure`, so the file never
says something Baton does not mean.

## When the config is wrong

A malformed policy is reported in the log and **dropped**, leaving a fleet that
restarts nothing. That is the deliberate failure direction: a policy Baton only
half understood would start processes on a schedule you did not write, which is
worse than a fleet that does not restart and says why.

```text
WRN restart policy ignored; panels will not be restarted
    error="panel.restart \"always\" is not a mode baton offers (never, on-failure)"
```

The whole block is hot-reloadable — `C-t R`, or a `SIGHUP` to the daemon — like
the resource limits beside it. A policy change applies to the next exit; it does
not disturb a running panel.

## Remembering where a panel was

Two halves of one promise: a panel whose process dies comes back, and it comes
back **where you left it**.

```yaml
panel:
  track-cwd: auto # auto (default) | osc7 | proc | off
  restore-cwd: shells # shells (default) | all | off
```

### Two ways to know

| Mechanism                                                       | Accuracy         | Cost                   | Limit                                                                                    |
| --------------------------------------------------------------- | ---------------- | ---------------------- | ---------------------------------------------------------------------------------------- |
| **OSC 7** — the shell reports its own directory                 | exact, immediate | **none**               | needs a cooperating shell: zsh and most Linux distributions send it, macOS bash does not |
| **Process table** — `/proc/<pid>/cwd`, `proc_pidinfo` on darwin | exact            | one syscall per answer | asked only at moments that matter, never on a tick                                       |

`auto` prefers the report and falls back to the process table. A shell that
reports its own directory is never asked again, so the common case costs nothing
at all.

The process-table half is read when a panel **settles at a prompt**, and again
when something is about to use the path. Settling is the right moment: the
directory is stable, it is where you are about to do something, and the
transition happens a handful of times per panel rather than once per tick.
Sampling fifty panels every second to keep a string fresh that nobody is looking
at is the cost this project avoids elsewhere.

The two mechanisms disagree about one thing worth knowing: the process table
answers with **symlinks resolved**, so a shell in `/tmp` on macOS reports
`/private/tmp`, while the shell's own report says what you typed.

### A report from another host is ignored

OSC 7 carries a hostname, and inside an ssh session the shell that speaks is the
**remote** one — it reports a remote host and a remote path. Baton discards those:
taking a remote path for a local directory would put a re-run in a same-named
local directory, and landing somewhere else in silence is the outcome worth
avoiding most.

The consequence is worth stating plainly, because it limits the flagship case: a
dropped ssh **reconnects**, but it reconnects to the remote shell's own idea of
where it is. Baton cannot put a remote shell back in a remote directory without
typing into your session, which it will not do.

### What the directory buys

1. **Respawn in place** — the re-run lands where you were, not where the panel
   started. This is the half that only exists because both parts are here.
2. **Open a panel here** — **`n .`** spawns a shell in the focused panel's current
   directory, tmux's `-c "#{pane_current_path}"` idiom. A panel whose directory is
   not known opens in the default workdir and says so.
3. **Identity at scale** — fifty panels called `shell #1`…`#50` are
   indistinguishable; the path on each card is what tells them apart. It is
   shortened from the front (`…/mylab/baton`), because every worktree under one
   repo shares a prefix and differs at the end.
4. **Git operations follow you** — the diff and git menus target where the agent
   is _now_, so they follow it into a worktree instead of staying pinned to the
   directory it was launched in.

The path is shown on the card and reaches the daemon's log. That is the same
trust level as everything else Baton holds, but it is worth saying.

### Why agents are excluded by default

`restore-cwd: shells` restores shells and leaves agents where they were launched.
A shell is wherever you last left it and going back there is the whole point; an
agent's task was set relative to the directory it was **started** in, and one that
wandered into `/tmp` before dying should not come back in `/tmp`. Set
`restore-cwd: all` if your agents disagree.

A directory that has since been removed — a worktree that was cleaned up — falls
back to the spawn directory **and says so**, on the panel and in the log:

```text
[last directory is gone (/repo-worktrees/api); starting where the panel was created]
```

## What this is not

- **Not a general supervisor.** The only health check is "the process is alive".
  No readiness probes, no restart-on-unhealthy.
- **Not session replay.** A restarted panel is a new process with a clean screen,
  not a restored one. Its scrollback starts empty.
- **Not a way back from a deliberate close.** `w` is final; the panel and its
  spawn spec are gone.
