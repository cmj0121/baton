# Account usage footer

**English** · [繁體中文](USAGE.zh-TW.md)

Baton shows two different things about your account, and the difference is the
point:

- **What you have spent** — tokens over the current billing window, and their
  API-equivalent cost. Baton counts these itself, out of Claude Code's
  transcripts, which is the only way they can be traced back to the panel that
  spent them.
- **How much of your quota is gone** — the 5-hour and weekly rate-limit windows,
  as progress bars with the countdown to each reset. This is the vendor's own
  reading of the account, and no amount of token arithmetic can produce it.

The first answers "who is burning it". The second answers "is there anything left
to burn". A fleet needs both.

```text
⊙ 1.2M tok · ≈$12.34 API · ⏳ 2:14:31
⊙ 5h ▓▓▓▓▓░░░ 2:14:31 · 7d ▓▓▓░░░░░ 3d4h
```

Press **`v u`** to cycle the footer segment through its views, and **`v U`** to
open the whole picture. The choice persists.

| View     | Shows                                                               | Answers                   |
| -------- | ------------------------------------------------------------------- | ------------------------- |
| `window` | The account's spend, and the countdown to the window's reset        | "Am I going to make it?"  |
| `panel`  | The focused panel's (or group's) spend, and its share of the window | "Who is burning it?"      |
| `limits` | The 5h and weekly quota bars, each with its countdown               | "Is there anything left?" |
| `off`    | Nothing                                                             | —                         |

The cost is written `≈…$ API` on purpose: it is the **API-equivalent** price of
the tokens, not a bill. See [What it is — and is not](#what-it-is--and-is-not).

## The quota overlay

`v U` opens the account's standing in full: every window the source reported, the
extra-usage balance if you have one, and the panels spending them.

```text
 A C C O U N T   U S A G E   statusline · 12s ago

 Session (5h)     ▓▓▓▓▓▓▓▓▓▓░░░░░░   resets 2:14:31
 Week (all)       ▓▓▓▓▓░░░░░░░░░░░   resets 3d4h
 Week (Opus)      ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓░   resets 3d4h
 Extra credit     ▓▓▓░░░░░░░░░░░░░   $11.70 / $65.00

 Burning this window          share      tokens    of 5h
 ▸ zerg / agent-2               67%  800.0K tok      41%
 ▸ baton / conductor            25%  300.0K tok      16%
```

The last column is what the overlay exists for. A panel's **share of the
window's tokens** is baton's own reading and says nothing about limits; the
**5-hour utilisation** is the vendor's and says nothing about panels. Multiplied,
they say how much of your actual ceiling one panel has eaten — which is what
"which one do I stop" is really asking.

A window the source did not report gets **no row at all**, and a bar never rests
at 0% to stand in for one: an empty bar reads as a full tank, on an account that
might be minutes from a refusal.

The header says how old the reading is. That matters because the default source
is a **push** (see below): it arrives when a panel renders and stops arriving
when the fleet goes quiet, so a reading can be perfectly true and half an hour
old. Past five minutes the footer marks it with a leading `~`.

## Data sources

There are two settings, because the two halves come from different places.
`usage.source` picks where the **token totals** come from; `usage.limits` picks
where the **quota bars** come from. They are independent — the totals can be
attributed to a panel and the quota never can, and the quota knows a ceiling the
totals cannot see.

### Token totals — `usage.source`

| Source  | Reads                                                            | Works for                                                   |
| ------- | ---------------------------------------------------------------- | ----------------------------------------------------------- |
| `local` | Claude Code's own session transcripts under `~/.claude/projects` | A personal **Pro/Max subscription** (and API-key use alike) |
| `api`   | The Anthropic **Admin** usage & cost API                         | A **Console / API-key organization**                        |

The **local** source is the default and the one that works for a subscription:
every Claude Code run — including the agent panels Baton spawns — appends a JSONL
transcript with per-message token counts, and Baton sums the window's messages
and prices each by its model. It reads only files touched recently enough to hold
one — the last calendar day plus a window, so up to six times as many files just
before midnight as just after it — and a fleet of hundreds of sessions still
scans in a fraction of a second. Set `CLAUDE_CONFIG_DIR` to point it somewhere
other than `~/.claude`.

The **api** source reports your whole organization's Console/API-key billing from
the Admin API. It needs an **Admin API key** (`sk-ant-admin01-…`), which Baton
reads from the `BATON_ANTHROPIC_ADMIN_KEY` environment variable — never from the
config file. Data lags real usage by about five minutes.

### Quota bars — `usage.limits`

| Source       | Reads                                                | Gives you                                      |
| ------------ | ---------------------------------------------------- | ---------------------------------------------- |
| `statusline` | The status line of the Claude Code panels Baton runs | 5h + weekly windows                            |
| `oauth`      | The account usage endpoint, directly                 | …plus per-model weekly, and the credit balance |
| `off`        | Nothing                                              | —                                              |

**`statusline` is the default, and it costs nothing.** Claude Code hands its whole
session state to whatever command is configured as its status line, and that state
carries the account's rate-limit windows. Baton is already launching the panels, so
it launches them with `baton usage-sink` as their status line: no network call, no
credential, no token spent.

It **wraps** your status line rather than replacing it. Baton resolves whatever you
configured — `.claude/settings.local.json`, then the project's `.claude/settings.json`,
then `~/.claude/settings.json` — and runs it with the same input, printing its output
verbatim. **A panel inside Baton renders exactly what it would outside one.** Three
cases end in no injection at all, because each would change something that is not
Baton's to change:

- the panel is not Claude Code (no other agent CLI has the flag);
- you passed `--settings` yourself in the panel's arguments;
- your status line is in a form Baton cannot reproduce.

> **If you have no status line configured, Baton's injection adds one** — a row that
> was not there, showing the quota bars. Claude Code hides some of its footer key
> hints (`esc to interrupt`, `? for shortcuts`) whenever any status line is set, so
> this is a visible change. Set `usage.limits: off` if you would rather keep the
> hints.

The reading only appears **on a Claude.ai subscription (Pro/Max), and only after a
session's first API response** — that is Claude Code's own contract, not Baton's.
Until then there is nothing to show, and Baton shows nothing.

**`oauth` is opt-in, and it is the only way to see the extra-usage credit balance
or the per-model weekly ceilings.** It queries the account usage endpoint directly.
That buys more, and costs more:

- It reads your Claude Code **OAuth access token** — from `~/.claude/.credentials.json`,
  or the login keychain on macOS. Baton reads it per request, sends it to one fixed
  host, never writes it anywhere, and never puts it in a log or an error. The refresh
  token is never even decoded. See [SECURITY.md](../SECURITY.md).
- The endpoint is **not a documented API**. It can change or vanish, so every failure
  degrades to "no reading" rather than to a wrong one.
- It is **rate-limited hard**. Baton fetches at most once every three minutes — a floor
  the config cannot lower — and a refusal backs off to as much as half an hour rather
  than retrying. Asking too often would spend the very quota it exists to report on.

Whichever source is used, a failure **holds the last reading** rather than blanking
it. A refused endpoint or a quiet fleet has not made the quota untrue; it has only
stopped Baton hearing about it, which is what the age in the header is for.

## The window, and the countdown

Only the **local** source can count down, and the reason is worth stating plainly
rather than hiding behind a number.

The transcripts are timestamped, so the start of a window is _inferable_: it is
the message that opened it — the first one to land after the previous window ran
out. The reset is that start plus `usage.window`, and Baton counts down to it on
the cockpit's own clock — once a second, not once per poll.

The window stays where it opened, and Baton carries it from one poll to the next
rather than working it out afresh each time. That matters: a scan only reaches so
far back, so the oldest message it can see is not reliably the one that opened
anything, and re-deriving from it every poll drags the boundaries onto the edge
of whatever the scan covered — at which point the reset moves with the clock, the
countdown sits at `0:00:00`, and the spend appears to reset at midnight.

**Baton re-derives the window when it has none to carry** — on daemon start, or
after a stretch with no cockpit attached (it stops polling when nobody is
watching) long enough that the carried window falls outside the scan. It then
reads the oldest message it can see as a window start, which it may not be, so
the boundaries can be off for that first reading. They settle from there.

**Between windows the whole segment goes away**, not just the countdown. Once a
window closes with nothing after it, there is no window to report on: the next
message opens the next one, with the whole of it to run, and until then Baton
shows nothing rather than a finished window's spend that would read as this
one's. So an idle stretch longer than `usage.window` empties the segment, and the
next agent turn brings it back.

The **api** source has no such handle. Rate limits surface on real API response
headers, which Baton never receives, and the admin reports carry no limit at all.
So it reports the period it actually queried and **shows no countdown**. When the
reset is unknown, Baton shows nothing — not a zero, not a guess. A wrong number
here is worse than no number, because the whole point is that you make a decision
on it.

The segment takes colour as the window fills — quiet, then amber, then red — for
the same reason: the point is to act _before_ it runs out, not to watch it hit
zero. The colour follows the countdown exactly: with no window to measure
against, it does not move at all rather than staying locked on the last reading.

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
  limits: statusline # statusline | oauth | off — where the quota bars come from
  interval: 30 # refresh seconds; 0 = default (30s local / 60s api); clamped to ≥ 10
  window: 5h # window length once use opens one; 0 = no countdown, calendar day
  warn-at: 0.75 # fraction of the window spent before the segment turns amber
  alarm-at: 0.9 # …and red

settings:
  usage-mode: window # off | window | panel | limits  (also cycled live with `v u`)
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
the server (`C-t S`) to pick it up. The `v u` cycle is live.

> An older config that set `settings.usage-footer: false` still hides the segment.
> `usage-mode` supersedes it and wins whenever both are present.

## What it is — and is not

- **Cost is API-equivalent, not a bill.** The figure prices your tokens at the
  published per-model rates. On a flat-rate Pro/Max subscription that is a "what
  this would cost on the API" gauge, not what you are charged.
- **The quota bars are proportions, not token counts.** Anthropic reports how much
  of each window is _used_ and when it resets — never an absolute allowance. So a
  bar can show you that most of your 5-hour window is gone; it cannot tell you how
  many tokens that leaves, and neither can anything else.
- **The bars carry no number beside them.** The fill _is_ the reading, and printing
  the percentage next to it says the same thing twice — once in a form you see, once
  in a form you have to read. The space goes to the countdown instead, which is the
  half a bar cannot draw.
- **The countdown has two forms and no setting.** Under a day it is a ticking clock
  (`2:12:23`), because that is a wait you sit through and the seconds moving are the
  point. A day or more it is `2d8h` — nobody waits out a weekly reset at the
  terminal, so minutes are noise. There is no `countdown-format` option; a countdown
  whose shape varies by config has to be parsed before it can be understood.
- **`warn-at` / `alarm-at` mean two things.** In the `window` view they colour by how
  far into the window the clock has run, because the spend has no ceiling to compare
  against. In the `limits` view they colour by the **fullest window** — there is a
  ceiling there, and an account at 90% with four hours left is in far more trouble
  than one at 20% with ten minutes left.
- **The local source covers Claude Code only.** Other agent CLIs (Copilot, …)
  are not in the transcripts, so they are not counted.
- **The api source needs an organization.** The Admin API is unavailable for
  individual accounts and does not carry Pro/Max subscription usage; a personal
  subscription should use the `local` source.
