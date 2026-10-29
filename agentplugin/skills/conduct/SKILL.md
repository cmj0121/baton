---
name: conduct
description: Guides an agent driving a baton fleet — dispatch versus send, the backlog, groups, signals, guardrails, memory upkeep. Use when orchestrating panels.
---

# Conducting a baton fleet

You drive the fleet; the workers do the work. Without MCP, every tool below
but the three memory corrections has a `baton ctl` spelling. As the conductor,
your workspace holds no source code and the "Operator's brief" in your
briefing (`BATON.md`, also written as `CLAUDE.md`) sets what to do; driving
from outside (the `drive` skill), the operator's request does.

If `BATON_PANEL_ID` is set and `BATON_ROLE` is not `conductor`, you are a
worker inside baton: do not drive the fleet from here — keep to your task and
the worker's skills, `score` and `peers`.

## The loop

1. `baton_list` — who is live, their state and group. Start here every time:
   ids are not stable across closes and respawns.
2. Make workers. `baton_spawn` with `agent` and `dir`; for parallel workers on
   one repository set `worktree` and `branch` so each gets its own checkout.
   Use `run` for a watched build or test process that is never given work.
3. `baton_group` them under a work item.
4. Give work: `baton_dispatch`, `baton_dispatch_group`, or `baton_enqueue`.
5. Follow `baton_list` (an idle agent has finished its turn) and
   `baton_queue`. When a worker is done, summarise its result and
   `baton_close` it.

## Dispatch, not send

`baton_dispatch` assigns a task: it records the brief on the panel's card,
delivers it as one unit, survives a restart, and the server watches it to
completion. A dispatch to a busy panel waits until that panel settles.

`baton_send` only types keystrokes. Use it to answer a prompt, press a key
(`submit` false), or run a command in a shell panel — never to hand an agent an
objective, because nothing records or tracks it.

## The backlog

`baton_enqueue` parks a task for the scheduler to drain onto the next free idle
agent, optionally within a work item — use it when the work is ready and no
particular panel has to do it. Give `command` to make it spawn-on-demand (a
fresh agent when none is free) and `close` to reap that agent once done: a way
to burst a temporary fleet through a batch. The scheduler provisions one agent
per tick and pays from your spawn rate. `baton_queue` reads the backlog;
`baton_reorder` moves a still-queued task to the `head` or `tail`.

## Arranging

- `baton_group` files panels under a work item; `baton_dispatch_group` then
  races every member on one prompt.
- `baton_rename` renames a panel (`id`) or a group (`group`). Name panels for
  their job so the operator can read the fleet at a glance.
- `baton_pin` and `baton_unpin` choose which panels are live tiles in their
  group's split view.
- As the conductor, you cannot add a panel to a peers-on group or switch its
  flag: ask the operator.

## Stopping

`baton_signal` with `SIGINT` interrupts a runaway agent and is the gentle
first step. `baton_close` ends a panel for good. An exited panel holds a fleet
slot until purged — a fleet that feels half empty can be at the ceiling.

## Asking for a human

As the conductor, `baton_attention` with a one-sentence reason when you need
a decision, approval, or credential; `baton_resolve` once it has passed. Both
act on your own panel only.

## Guardrails

As the conductor, the server refuses, before anything happens: acting on
your own panel (send, signal, close, dispatch to yourself); spawning faster
than the rate cap or past the fleet ceiling of 64; draining the queue;
sweeping worktrees; switching peers or using peer verbs; logging a panel to a
file; reloading or stopping the server. A refusal is an answer. Do not retry
around it — adjust, or raise your hand if it blocks the mission.

## The fleet's memory

The server runs the memory's policy — recording, folding, ranking, injecting —
and you cannot change it. You may add to it like any worker: `score_submit`
records one short observation about how this fleet behaves. It is not a lever;
repeating yourself to move an entry is the fleet lying to itself.

The conductor's alone, when it has nothing else to do, is proofreading:

- `score_merge` joins two entries that say the same thing in different words.
- `score_reword` fixes an entry's wording; the old wording still folds into it.
- `score_lower` pulls an entry down one rung when it was raised in error.

None counts as a reinforcement and nothing raises an entry — the top rung is
the operator's. They are rate-limited, and merges taking over half the memory
raise a warning in the daemon log. Read the memory first with
`baton ctl score list`; leave an entry alone rather than guess its meaning.
