# Account usage footer

**English** · [繁體中文](USAGE.zh-TW.md)

Baton can show your account's **token usage and cost for the day** as a footer
segment — `⊙ 1.2M tok · ≈$12.34 API` — in every view. The server polls it in the
background and pushes it to every attached cockpit; press **`U`** to show or hide
it (the choice persists, and it defaults on).

The cost is written `≈…$ API` on purpose: it is the **API-equivalent** price of
the day's tokens, not a bill. See [What it is — and is not](#what-it-is--and-is-not).

## Data sources

There are two sources, because "your usage" means different things depending on
how you run your agents. Baton picks one with the `usage.source` setting.

| Source  | Reads                                                            | Works for                                                   |
| ------- | ---------------------------------------------------------------- | ----------------------------------------------------------- |
| `local` | Claude Code's own session transcripts under `~/.claude/projects` | A personal **Pro/Max subscription** (and API-key use alike) |
| `api`   | The Anthropic **Admin** usage & cost API                         | A **Console / API-key organization**                        |

The **local** source is the default and the one that works for a subscription:
every Claude Code run — including the agent panels Baton spawns — appends a JSONL
transcript with per-message token counts, and Baton sums the day's messages and
prices each by its model. It reads only files touched since local midnight, so a
fleet of hundreds of sessions still scans in a fraction of a second. Set
`CLAUDE_CONFIG_DIR` to point it somewhere other than `~/.claude`.

The **api** source reports your whole organization's Console/API-key billing from
the Admin API. It needs an **Admin API key** (`sk-ant-admin01-…`), which Baton
reads from the `BATON_ANTHROPIC_ADMIN_KEY` environment variable — never from the
config file. Data lags real usage by about five minutes.

## Configuration

The main config (`$HOME/.baton/config`):

```yaml
usage:
  source: auto # auto | local | api  (auto: api when an admin key is set, else local)
  interval: 30 # refresh seconds; 0 = default (30s local / 60s api); clamped to ≥ 10

settings:
  usage-footer: true # show the segment (also toggled live with U)
```

The Admin key, when using the `api` source, goes in the environment:

```sh
export BATON_ANTHROPIC_ADMIN_KEY=sk-ant-admin01-…
```

`usage.source` and `usage.interval` are read when the daemon starts; change them
and restart the server (`C-t S`) to pick them up. The `U` toggle is live.

## What it is — and is not

- **Cost is API-equivalent, not a bill.** The figure prices your tokens at the
  published per-model rates. On a flat-rate Pro/Max subscription that is a "what
  this would cost on the API" gauge, not what you are charged.
- **It does not show remaining quota.** There is no API for a subscription's
  remaining allowance, so Baton reports what you have _consumed_ today, not what
  is left.
- **The local source covers Claude Code only.** Other agent CLIs (Copilot, …)
  are not in the transcripts, so they are not counted.
- **The api source needs an organization.** The Admin API is unavailable for
  individual accounts and does not carry Pro/Max subscription usage; a personal
  subscription should use the `local` source.
