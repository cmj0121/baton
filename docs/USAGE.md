# Account usage footer

**English** · [繁體中文](USAGE.zh-TW.md)

Baton can show your account's **token usage over the current billing window** as
a footer segment in every view, together with **how long is left before it
resets**:

```text
⊙ 1.2M tok · ≈$12.34 API · ⏳ 2:14:31
```

Press **`U`** to cycle the segment through its views. The choice persists.

| View     | Shows                                                               | Answers                  |
| -------- | ------------------------------------------------------------------- | ------------------------ |
| `window` | The account's spend, and the countdown to the window's reset        | "Am I going to make it?" |
| `panel`  | The focused panel's (or group's) spend, and its share of the window | "Who is burning it?"     |
| `off`    | Nothing                                                             | —                        |

The second view is the one a fleet needs. With a dozen agents running and two
hours left, the decision is which one to stop — and that takes a **share of the
window**, not a raw token count.

The cost is written `≈…$ API` on purpose: it is the **API-equivalent** price of
the tokens, not a bill. See [What it is — and is not](#what-it-is--and-is-not).

## Data sources

There are two sources, because "your usage" means different things depending on
how you run your agents. Baton picks one with the `usage.source` setting.

| Source  | Reads                                                            | Works for                                                   |
| ------- | ---------------------------------------------------------------- | ----------------------------------------------------------- |
| `local` | Claude Code's own session transcripts under `~/.claude/projects` | A personal **Pro/Max subscription** (and API-key use alike) |
| `api`   | The Anthropic **Admin** usage & cost API                         | A **Console / API-key organization**                        |

The **local** source is the default and the one that works for a subscription:
every Claude Code run — including the agent panels Baton spawns — appends a JSONL
transcript with per-message token counts, and Baton sums the window's messages
and prices each by its model. It reads only files touched recently enough to hold
one, so a fleet of hundreds of sessions still scans in a fraction of a second.
Set `CLAUDE_CONFIG_DIR` to point it somewhere other than `~/.claude`.

The **api** source reports your whole organization's Console/API-key billing from
the Admin API. It needs an **Admin API key** (`sk-ant-admin01-…`), which Baton
reads from the `BATON_ANTHROPIC_ADMIN_KEY` environment variable — never from the
config file. Data lags real usage by about five minutes.

## The window, and the countdown

Only the **local** source can count down, and the reason is worth stating plainly
rather than hiding behind a number.

The transcripts are timestamped, so the start of a window is _inferable_: it is
the message that opened it — the first one to land after the previous window ran
out. The reset is that start plus `usage.window`, and Baton counts down to it on
the cockpit's own clock — once a second, not once per poll.

The window stays where it opened. That matters: anchoring it on "the last
`usage.window` of activity" instead would drag the reset along with the clock, so
under continuous use the reset would forever be a moment away and the countdown
would sit at `0:00:00`. Once a window closes with nothing after it there is no
countdown at all — the next message opens the next window, with the whole of it
to run.

The **api** source has no such handle. Rate limits surface on real API response
headers, which Baton never receives, and the admin reports carry no limit at all.
So it reports the period it actually queried and **shows no countdown**. When the
reset is unknown, Baton shows nothing — not a zero, not a guess. A wrong number
here is worse than no number, because the whole point is that you make a decision
on it.

The segment takes colour as the window fills — quiet, then amber, then red — for
the same reason: the point is to act _before_ it runs out, not to watch it hit
zero.

## Per-panel attribution

In the `panel` view, Baton reports what the focused panel has spent inside this
window. It can do that because it gives every Claude Code panel it launches a
**session id of its own** (`--session-id`), which names that panel's transcript —
so the spend can be traced back to the panel that made it, subagents included.

A few consequences follow from how that flag works, and they are visible in the
footer:

- **A panel that is not Claude Code has no attribution.** Another agent CLI would
  reject the flag, so Baton does not pass it. The segment reads `not attributed`
  rather than showing a zero that looks like "this one is free".
- **The same is true if you set the session yourself.** A profile whose `args`
  already carry `--session-id`, `--resume` or `--continue` is left exactly as you
  wrote it.
- **Every spawn gets a fresh id.** Re-using one is a hard error in Claude Code, so
  a re-run (`r`) mints a new session; a panel's spend is the sum over all of them.
- **A restored panel starts blank.** After a daemon restart, panels come back as
  exited slots with no live process, so their old sessions are not carried over.
- **The `api` source cannot attribute at all.** Its reports are organization-wide
  aggregates.

Selecting a **group** rolls up every member, since a work item is as natural a
thing to ask "who is burning it" about as a single panel.

## Configuration

The main config (`$HOME/.baton/config`):

```yaml
usage:
  source: auto # auto | local | api  (auto: api when an admin key is set, else local)
  interval: 30 # refresh seconds; 0 = default (30s local / 60s api); clamped to ≥ 10
  window: 5h # how long a window lasts once use opens one; 0 = no countdown, calendar day
  countdown-format: auto # auto (2:14:31, widening to 3d 04:12) | dd:hh:mm
  warn-at: 0.75 # fraction of the window spent before the segment turns amber
  alarm-at: 0.9 # …and red

settings:
  usage-mode: window # off | window | panel  (also cycled live with U)
```

`window` is configurable rather than baked in because plans differ and the vendor
can change the figure — tracking that should not need a Baton release. Set it to
`0` if your plan bills on something Baton cannot model: no countdown is better
than a wrong one.

`warn-at` and `alarm-at` are honoured as a pair. A setting that does not describe
rising pressure — outside 0–1, or an alarm at or below the warning — falls back to
the defaults wholesale, so the colours always mean what they look like.

The Admin key, when using the `api` source, goes in the environment:

```sh
export BATON_ANTHROPIC_ADMIN_KEY=sk-ant-admin01-…
```

Everything under `usage:` is read when the daemon starts; change it and restart
the server (`C-t S`) to pick it up. The `U` cycle is live.

> An older config that set `settings.usage-footer: false` still hides the segment.
> `usage-mode` supersedes it and wins whenever both are present.

## What it is — and is not

- **Cost is API-equivalent, not a bill.** The figure prices your tokens at the
  published per-model rates. On a flat-rate Pro/Max subscription that is a "what
  this would cost on the API" gauge, not what you are charged.
- **It does not show remaining quota.** There is no API for a subscription's
  remaining allowance, so Baton reports what you have _consumed_ in the window,
  and how long the window has left — not how many tokens are left in it.
- **The local source covers Claude Code only.** Other agent CLIs (Copilot, …)
  are not in the transcripts, so they are not counted.
- **The api source needs an organization.** The Admin API is unavailable for
  individual accounts and does not carry Pro/Max subscription usage; a personal
  subscription should use the `local` source.
