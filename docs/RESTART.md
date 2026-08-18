# Panel restart policy

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

## What this is not

- **Not a general supervisor.** The only health check is "the process is alive".
  No readiness probes, no restart-on-unhealthy.
- **Not session replay.** A restarted panel is a new process with a clean screen,
  not a restored one. Its scrollback starts empty.
- **Not a way back from a deliberate close.** `w` is final; the panel and its
  spawn spec are gone.
