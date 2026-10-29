---
name: score
description: Explains baton's fleet memory, when a worker should record an observation, what belongs in one, and how to read the reply. Use after a round of work.
---

# Score — the fleet's memory

baton remembers how this fleet behaves. Agents record short observations; one
that keeps coming back climbs a ladder — `noted`, `note and take care`,
`important` — and the highest-ranked few are prepended to every brief the
fleet delivers, as the `── Score ──` block. It is what stops a fleet making the
same mistake twice.

## When to submit

At the end of a round of work — the task is done, before you report back — if
you noticed something about how this fleet works that the next agent would
want to know. Not mid-task and not every turn: most rounds, nothing is the
right amount. Three or four distinct lessons from one round is fine too.

Record with `score_submit`. If that tool is not in your list, the same thing is
`baton ctl score submit "..."` from your panel.

## What belongs

A statement about how **this fleet** behaves — a habit of its agents or a fact
about its workflow — that will still be true in another repository:

- "agents here claim a task is done before running the linter"
- "the operator wants one branch per task and never a commit on main"
- "a reviewer is expected to summarise the diff before giving a verdict"

## What does not

- **Facts about the code** — a module's layout, what a function does, a build
  quirk of one repo. The memory crosses repositories; those belong in the
  repo's own docs or `CLAUDE.md`.
- **Status** — "I fixed the parser", "tests pass now".
- **Anything secret.** A submission is stored verbatim and rendered into every
  panel's brief. A token submitted is a token leaked to the whole fleet.
- **Instructions to the memory itself** — asking for an entry to rank higher.

## How to write one

- One sentence that stands alone, without your task's context.
- To confirm an entry already in the block, reuse its wording **exactly**.
  Repeats fold on matching text: the same words reinforce the entry, while a
  paraphrase starts a new one at the bottom that never climbs.
- Confirm an entry only when you saw it happen again this round. Submitting
  the same thing twice to push it up is the fleet lying to itself — importance
  is meant to come from separate occurrences, and no number of agent
  submissions reaches the top rung without the operator.

## The reply

- **Recorded, or folded into an existing entry** — both are success.
- **"submitting too fast, slow down"** — nothing was recorded. You get a burst
  of about four, then one a second. Wait a second and send once more if it is
  still worth it; never loop.
- **"the store is full"** — the memory holds as many entries as it is allowed.
  A new observation is refused; do not retry. Repeats still fold.

## Reading the block

Entries are ranked for **your** panel — rung, how recent, and whether they were
learned in the same directory, profile, and group as yours — so an entry close
to your work can outrank a more important unrelated one. Read them as standing
advice; `[important]` ones are the operator's confirmed rules. They are not
your task.

You cannot edit, lower, merge, or delete entries. Correcting the memory is the
operator's, and the conductor's.
