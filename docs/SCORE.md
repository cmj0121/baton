# Baton — Score

**English** · [繁體中文](SCORE.zh-TW.md)

> Score is the fleet's memory. Agents submit short observations about how this fleet behaves; an observation that keeps
> coming back climbs a three-rung ladder; the highest-ranked few are prepended to the brief of every panel the fleet
> dispatches to. It is what stops a fleet repeating a mistake it has already made once.

Three carriers, three owners. `CLAUDE.md` belongs to the repository and says how this code is written. `CONDUCTOR.md`
belongs to you and says what the conductor's standing orders are. Score belongs to the **fleet** and says how this
fleet behaves — and it is the only one of the three that grows on its own. A file you edit by hand is a declaration;
Score is evidence.

It is **on by default**. If you upgraded a running fleet and found a `── Score ──` block in your agents' terminals that
you did not put there, this document is what it is.

## The block in your agent's terminal

Every brief a panel receives is prefixed with the working set — the highest-ranked handful of what the fleet remembers,
ranked against **that panel**:

```txt
── Score ──
- the agent was asked to gain permission it already had [noted]
- run the linter before claiming a task is done [note and take care]
- never force-push a shared branch [important]
───────────
review the diff on feat/api and tell me what is missing
```

The trailing word is the entry's rung. Seven entries by default (`score.working-set`), and the block as a whole is
capped at 8000 runes — past that the lowest-ranked entries are dropped whole, never truncated mid-entry.

Every delivery the wire makes carries the block: a direct `panel.dispatch`, each member of a group fan-out, and a
queued task at the moment the scheduler drains it onto a panel. **Plugin-originated dispatches are the exception** —
`baton.dispatch`, `baton.dispatch_group` and a task `baton.enqueue` queued deliver the bare prompt, and never come near
the score.

## The file

Your entire interface to Score is a plain markdown file you open in your own editor. There is no accept, no reject, no
pin and no promote — importance is earned, not granted.

```txt
- [e7f3a2] the agent was asked to gain permission it already had
- [1b90cc] run the linter before claiming a task is done
```

Four rules, and they are the whole format:

1. **One entry per line**, shaped `- [id] text`. The id is six hex characters, assigned by baton, and stable for the
   entry's whole life — it is what makes "you changed this line" answerable precisely rather than by guess.
2. **Edit or delete lines freely.** Your text wins over the machine's, always. Reword a line and the old wording is
   kept as an alias, so a later repeat of the old phrasing still folds into the entry. Delete a line and it is retired,
   whatever the log remembers about it.
3. **Anything that is not an entry is ignored** — headings, blank lines, your own prose. Keep notes in the file if you
   like; baton preserves every byte of them verbatim.
4. **A bullet with no id becomes an entry.** Type `- keep the build green` and the next dispatch admits it as a new
   user-sourced entry, writes an id back into the line, and starts it at rung 1.

Rules 3 and 4 are the one trap. "Not an entry" means **not a line beginning with a hyphen and a space**. A heading, a
paragraph, a `#` comment and a numbered list are prose; a markdown bullet is memory. Write your own notes as anything
but a bullet.

Because that is the one rule you cannot infer from looking at the file, a fresh `score.md` **opens with it**, as `#`
comment lines above the first entry. They are prose by rule 3 and so can never become memory themselves; they are also
yours, like every other byte — delete them and they stay deleted. If you would rather see how many of your own lines
have already been taken this way, `baton ctl score status` counts them under `bare_admits`.

A line longer than 300 runes is kept exactly as you wrote it and simply **not injected** — refusing it would mean
rewriting your file, and truncating it would silently rewrite what you meant. `baton ctl score status` counts what is
being withheld under `oversized`.

Baton rewrites the file for exactly two reasons: to append a line for a new entry, and to write back an id it assigned.
Every write is read-reconcile-write, so a save of yours that lands in between is never lost.

### Starting an entry over

**Deleting a line's `[id]` starts that entry over.** Same text, back at rung 1, with nothing it had earned:

```txt
- [e7f3a2] never force-push a shared branch     ← rung 3, twelve reinforcements
- never force-push a shared branch              ← one save later: rung 1, nothing earned
```

One save does it. The pass finds a live entry no line carries and retires it; it finds an id-less bullet and admits it
fresh. The old entry's whole history stays in the event log — nothing is destroyed — and a new id is written back into
the line.

This is the store's **only undo**. Nothing demotes an entry on its own, so a rung reached in error would otherwise look
permanent, and the top rung is reachable: a brief of yours that coincidentally repeated an entry is exactly the kind of
promotion worth taking back. It is pinned by `TestStrippingAnIdStartsAnEntryOver`, because a promise made to someone
who cannot read the code should be a test rather than a belief.

The conductor's `score_lower` moves an entry down one rung at a time. Editing the file is what takes it all the way
back.

## How an entry earns its tier

| Rung | Renders as           | Reached by                                                          |
| ---- | -------------------- | ------------------------------------------------------------------- |
| 1    | `noted`              | every entry, on the submission that created it                      |
| 2    | `note and take care` | being said `score.promote-at` times — 3 by default, from any source |
| 3    | `important`          | the same recurrence, but only once **you** have said it twice       |

An observation is never rejected. It lands at rung 1 immediately, and earns its importance only by coming back. One
that never comes back simply sits at the bottom, which costs nothing.

A repeat does not add a line — it **folds** into the entry that already says it and counts as a reinforcement.
Folding matches on text, so two observations that mean the same thing in different words each sit at rung 1 and never
climb. That is the deliberate way to fail: Score remembers less, rather than remembering wrong.

**No number of agent submissions reaches rung 3.** Recurrence alone stops one rung below it. The top rung takes
`score.user-signals-at` signals that came from you (2 by default), and a signal is identified by the connection it
arrived on, never by anyone's claim about it. Three things count as you saying it again:

- typing a duplicate line into `score.md` (one pass counts one signal, however many duplicate lines carry the wording —
  one paste is one action, not five hundred returns)
- `baton ctl score submit` from your own shell
- **dispatching a brief that matches an existing entry** — a prompt you type is you saying the thing

A user signal lifts the ceiling; it does not skip a rung. The entry still climbs the ordinary ladder to get there.

Correcting a line's wording counts as nothing. One statement re-spelled three times is one statement said once, so a
run of typo fixes cannot walk an entry to the top.

The third bullet has an honest cost: a brief that happens to share wording with an unrelated entry reinforces it. Every
reinforcement is a line in the daemon log naming what it counted, and the fix is the one above — delete the id.

## Why the entry you expected is not in the brief

The working set is **ranked**, never taken in file order, and the rank is a product:

```txt
rank = tier × recency × cwd × profile × group
```

`tier` is the earned ladder — 1, 2, 3 — and is never configurable. The other four are weights you set, each defaulting
to `2.0`. `cwd`, `profile` and `group` are worth their weight when the entry was submitted from a panel whose working
directory, agent profile, or fleet group equals the one being dispatched to, and `1.0` when not. `recency` is not a
match: every entry's factor slides linearly between `1.0` at the oldest position in the event log and the weight at the
newest.

Now the part worth stating outright, because it will otherwise be discovered as a bug:

**Context can outrank the ladder.** At the default weights a rung-1 entry matching cwd, profile and group scores
`1 × 1 × 2 × 2 × 2 = 8`, while a rung-3 entry matching none scores `3 × 1 × 1 × 1 × 1 = 3`. The relevant small thing
beats the important irrelevant one — and not marginally: even at the extremes of the recency ramp the matching rung-1
entry cannot score below 8, and the unmatched rung-3 entry cannot score above 6.

This is the design and not an accident. Choosing a product over a tier-then-tie-break order is what lets the dimensions
compose, and it is the whole purpose of the knob. If you read "recurrence earns importance" and then cannot find your
rung-3 entry in a brief, nothing is broken — it was outranked by something closer to what that panel is doing.

`baton ctl score list <panel>` shows the arithmetic per entry, so the answer to "why is this in the brief" is a reason
rather than a number.

### Two panels, two memories

The consequence, and the single most surprising thing about Score on a first reading: **two panels in the same fleet
genuinely see different memory.** In a fleet of seven entries, a dispatch to a docs panel and a contextless
`baton ctl score list` can rank entirely different entries into the top seven — overlapping by none of them.

That is the feature working. Ranking runs against the panel the brief lands on, and a panel working in another
repository, under another profile, in another group matches nothing the first one matched. It also means
`baton ctl score list` **with no panel named** answers a different question from any real dispatch: a cockpit is not a
panel, so every context factor reads `1.0` and the order is tier and recency alone. The reply echoes the context it
ranked against, so the two are never confused.

### The weights

```yaml
# $HOME/.baton/config
score:
  rank:
    recency: 2.0 # the most recently touched entry is worth this
    cwd: 2.0 # entry submitted from a panel in this dispatch's directory
    profile: 2.0 # …under this dispatch's agent profile
    group: 2.0 # …in this dispatch's fleet group
```

- **`1.0` means "this dimension does not matter."** A weight multiplies a match and never penalises a miss, so at 1.0
  the dimension multiplies by one either way. Anything below 1.0 is raised to it; a weight is clamped to
  **[1.0, 1e6]**, and an out-of-range value is silently corrected to the nearest end. Unset (or `0`) means "I did not
  set this" and lands on the default of 2.0 — it is not a spelling of "off".
- **Set all four to 1.0 and the ladder is the only thing that ranks.** That is how you ask for earned importance to
  dominate.
- **Raise `cwd` and the current repository dominates earned importance.** That is the other end of the same knob.
- A factor reading exactly `1.0` in a `score list` breakdown is a dimension that either did not match or was switched
  off. `baton ctl score status` reports the weights actually in force, which is what tells those two apart.

Entries **you** submitted from your own cockpit record no panel, profile, group or directory at all, so they never
match a context dimension and rank on tier and recency alone.

One more asymmetry worth knowing if you are tuning `recency`: an entry's position moves when you **edit its wording**,
even though an edit counts no reinforcement. A line you just reworded ranks as fresh without having earned anything.
Raising `rank.recency` above the largest ratio between two entries' other factors is what makes post-compaction
reordering likely; at the default 2.0 it needs a tier-3 entry to happen at all.

## The commands

```sh
# Record one short observation about how this fleet behaves.
baton ctl score submit "the agent asks for permission it already has"

# Every entry the store holds, in rank order, as one line of JSON.
baton ctl score list          # ranked against nothing — tier and recency alone
baton ctl score list 3        # ranked against panel 3: the brief THAT panel gets

# Whether the memory runs, how many entries it holds, its tuning, and where.
baton ctl score status
```

`submit` prints the id, and says so when the store recognised the text as a repeat of one it already holds — the id
stays the first token, so `cut` and `read` are unaffected.

`list` is **uncapped**: every entry, not the few a brief carries, each with its tier, its rank, the five factors that
multiply out to that rank, and its standing. It stays one line of JSON because the reader is as often `jq` as a person:

```sh
baton ctl score list 3 | jq '.entries[] | select(.active)'                     # the working set
baton ctl score list 3 | jq '.entries[] | select(.standing == "block-full")'   # what the block had no room for
baton ctl score list   | jq '.entries[] | select(.tier > 1)'                   # what the fleet takes care over
```

An entry outside the working set carries **one** of three standings, naming the cap you can act on:

| Standing       | Means                                                    | You fix it by       |
| -------------- | -------------------------------------------------------- | ------------------- |
| `below-budget` | ranked below `score.working-set`; higher ones were ahead | widening the budget |
| `block-full`   | inside the budget, but the 8000-rune block ran out       | shortening entries  |
| `oversized`    | over 300 runes, so injectable at no budget at all        | editing the line    |

`status` answers three questions that are not one question:

| Field                      | Says                                                                    |
| -------------------------- | ----------------------------------------------------------------------- |
| `enabled`                  | what `score.enabled` asked for                                          |
| `available`                | whether a store actually opened                                         |
| `reason`                   | why not, when it did not — or that reads or writes have stopped working |
| `entries` / `rendered`     | what the store holds, and what a dispatch would carry                   |
| `oversized` / `block_full` | which cap made those two disagree                                       |
| `bare_admits`              | how many of your lines became entries on a bare bullet alone            |
| `promote_at`, `rank`, …    | the tuning **in force**, which is not always what the file says         |
| `unlocked`                 | the store is running without its single-writer claim                    |
| `dir`                      | where the files are                                                     |

Off, unavailable and broken are three states, not one. You must never have to read the daemon log to learn that the
fleet has no memory.

## How fast one actor may submit

`score.submit` — the verb behind both `baton ctl score submit` and the `score_submit` MCP tool — is capped at roughly
**one a second per actor**, with an allowance of **four spendable at once** that refills one a second.

What the cap bounds is **fold records, not entries**. A repeat folds into the entry that already says it, so an agent
resubmitting one sentence a million times creates no new entry at all — it creates one `folded` record per attempt, at
a little over 200 bytes each. A retry loop measured at 73 submissions a second is 1.47 GB of event log in a day; under
the cap the same loop settles to one a second, because that is what the allowance refills at. Nothing about how _much_
one actor may say is capped: a single agent can still put forty times a healthy fleet's daily volume into the memory,
which is why nothing an honest fleet does comes near this.

**A refused submission is refused, not quietly dropped.** The reply is an error — `submitting too fast, slow down` —
and it is decided before the store is touched at all, so nothing is appended and nothing is folded. An agent told that
can say the same thing a second later and lose nothing by having tried; a silent success would have made the one thing
that reply exists to say — new, or folded into what the fleet already knows — a lie.

**The burst is what makes a turn's worth of observations land.** Three or four distinct lessons written as a task
finishes arrive together by construction, and a plain one-second minimum gap refuses two of every three of them — which
is not slowing an agent down, it is throwing three lessons away. With four spendable at once, three measured arrival
patterns are refused 0%, 0.7% and 16% of the time, while the retry loop's refusal rate is unchanged at 98.6%: the
sustained rate is all the bucket ever admits, and the burst buys at most three extra records each time an actor comes
back from being idle.

That 16% is the turn shape itself, and it is measured at **216 times** what a healthy fleet writes in a whole day. At a
healthy fleet's own volume the same shape is refused 0.03%. The burst takes most of a turn back; it does not make a
fleet whose turns arrive on top of each other free of the cap.

Refusals are visible without being loud: one `WRN` per actor per minute, naming the actor, how long until the
allowance is back, and the gap and burst it was measured against. It says outright that the store is healthy, because
"my submissions are failing" has three different answers and this is only one of them.

### Who counts as one actor

| The client                   | Spends the allowance of      |
| ---------------------------- | ---------------------------- |
| an agent inside a panel      | that panel                   |
| `baton ctl`, from your shell | that shell **session**       |
| `baton mcp`, outside a panel | that `baton mcp` **process** |

Inside a panel the panel id is already an identity. Outside one there was none, so every client outside the fleet —
your own `baton ctl score submit` and every MCP server started by hand — shared a single allowance between them: three
different observations back to back reached the store as one record and two refusals, under a log line that named
nobody. They now declare an identity of their own. `baton ctl` is a whole process per command, so it declares its
session — which a shell and everything under it share, so two terminals are two actors and a loop inside one of them is
still one. `baton mcp` outlives its connections and dials afresh per tool call, so it declares itself; declaring its
session would put it straight back in one slot with the shell that started it.

A session is stable for a shell and not for a launcher above one. cron, `ssh host baton ctl …`, `systemd-run` and agent
runtimes start each command in a session of their own, so a client arriving that way is a fresh actor on every
invocation and spends a fresh allowance. Agents inside panels are untouched by that — they carry a panel id — and so is
`baton mcp`; what it leaves is a shell panel and those launchers, under exactly the limit the next paragraph states.

**It is a cooperative key, not a boundary.** The identity is declared by the client on its handshake and the daemon
never checks it. It grants nothing and fences nothing — it picks which allowance this client spends and nothing else.
So every number above holds for a client with a stable identity, which is every real one, and a client that varied it
would walk straight through the cap, exactly as it could already by varying the panel it claims to be. This bounds a
loop that is filling your disk by accident; it is not a defence against a client that has decided not to be counted.
See **[SECURITY.md](../SECURITY.md)**.

## The conductor's corrections

Where a [conductor](CONTROL.md#the-conductor) is running it gets **correction** rights, not execution rights. The
policy — record, fold, count, promote, rank, inject — is deterministic arithmetic that runs in the daemon, in Go,
whether or not a conductor exists. Three MCP tools, refused to any connection that is not the conductor panel's:

| Tool           | Does                                                                         |
| -------------- | ---------------------------------------------------------------------------- |
| `score_merge`  | join two entries that say the same thing in different words                  |
| `score_reword` | fix an entry's wording; the old wording is kept, so repeats of it still fold |
| `score_lower`  | pull one entry down a single rung when it was raised in error                |

**None of them counts as anything, and there is no tool that raises an entry.** A reword cannot make an entry more
important; a lowered entry climbs again only by being said again. Corrections are rate-capped at four a second, and a
run of merges that takes more than half the fleet's memory inside a minute raises a warning in the daemon log — because
that has no undo beyond reading the event log by hand.

## Configuration

```yaml
# $HOME/.baton/config
score:
  enabled: true # the default; false switches the whole subsystem off
  dir: ~/.baton # where score.md and score-events.jsonl live
  promote-at: 3 # occurrences, from any source, before rung 1 → 2
  user-signals-at: 2 # signals from YOU before rung 3 is reachable
  working-set: 7 # entries one brief carries — the highest-ranked few
  rank:
    recency: 2.0
    cwd: 2.0
    profile: 2.0
    group: 2.0
```

Every key is optional; unset means the value above. `promote-at` below 2 and `working-set` below 1 are read as unset,
not as switching the feature off — `enabled` is where that is said.

**What a `SIGHUP` (or `C-t R`) reloads, and what it does not:**

| Key                                                                            | Reloads                                        |
| ------------------------------------------------------------------------------ | ---------------------------------------------- |
| `score.promote-at`, `score.user-signals-at`, `score.rank`, `score.working-set` | yes — each is a number the live store compares |
| `score.dir`, `score.enabled`                                                   | **no** — the store is opened once, at boot     |

So a fleet whose entries are climbing too eagerly, or whose briefs are carrying the wrong few, is retuned with `C-t R`
rather than by restarting and returning every panel as exited. Moving the directory or switching the subsystem off
needs a full daemon restart — and the daemon says so at `WRN` when the file you just reloaded asks for a different
`score.dir` or `score.enabled`, because the reload's own line says only that a reload happened. Tiers already earned
are replayed from the log and never move; retuning the ranking changes order and no tier at all.

Two config mistakes the daemon will tell you about rather than absorbing silently: the key is `promote-at`, and a file
still spelling it `promote_at` runs on the default; and a mistyped number anywhere in the section (`working-set: lots`)
fails the whole file's parse, in which case the running values stand and the daemon names the key.

## Switching it off

Three levers, at three scopes:

```yaml
score:
  enabled: false # the whole subsystem: no injection, submissions refused, files untouched
```

Files are left readable and re-enabling resumes from them. This needs a **restart** — the store is opened once at boot.

```lua
-- $HOME/.baton/plug-in.lua — per group, per panel, per anything, and no restart needed
baton.on("task.pre", function(t)
  if t.group == "scratch" then return { score = "" } end
end)
```

`task.pre` receives `{ prompt, group, score, cwd, profile, panel }`, and `score` is its own field rather than something
prepended into `prompt` — so a hook may inspect it, reorder it, replace it or drop it without touching the prompt, and
a hook written before Score existed keeps its exact old meaning. `C-t R` reloads the plugin, so this lever takes effect
without restarting the fleet. See **[PLUGIN.md](PLUGIN.md)**.

And the third, which is not a lever so much as the fact that the file is yours: **empty `score.md`** and every entry
retires on the next dispatch. (Deleting the file entirely does the opposite — an absent file is read as lost rather
than emptied, and the log is re-projected into a fresh one.)

## The directory

`score.dir` — `$HOME/.baton` by default, created `0700`, every file `0600`:

| File                 | Holds                                                   | Written by    |
| -------------------- | ------------------------------------------------------- | ------------- |
| `score.md`           | one entry per line — the truth for text and existence   | you and baton |
| `score-events.jsonl` | the append-only history — the truth for everything else | baton only    |
| `score.lock`         | the single-writer claim                                 | never read    |

**The log is the truth.** Every mutation is an event, and the entries are rebuilt from it at every boot. Delete every
file but the log and restart, and the store comes back exactly as it was.

`score.lock` is an advisory lock held for the daemon's life. Two daemons on two sockets both default to `$HOME/.baton`,
and `BATON_SOCK` is the documented way to run a second fleet — so without it they would append to one log and rewrite
one `score.md` from two views of it, and each rewrite would drop the other's lines. The second daemon's store refuses to
open, plainly, and says so through `score status`. Where the filesystem cannot lock at all — a network `$HOME` is
exactly where the default lands in corporate setups — the store runs **unlocked** rather than refusing to boot, and
`status` reports `unlocked: true`. No fleet memory is the worse outcome of the two.

**A leftover you may find:** a directory written by an older build still contains a `score.json`. This build neither
reads it nor removes it — deleting from your directory is not baton's to do. It is dead bytes you can remove yourself.

## Compaction, and what it costs

Every mutation appends to `score-events.jsonl`, so it grows for as long as the fleet works. Past **8 MiB**, the next
boot rewrites it to one record per id the log has ever named: a state record carrying each live entry's whole current
standing, and a bare retirement record for every id whose entry is gone. Under the threshold nothing is rewritten.

**It runs at two doors, on the same 8 MiB.** The boot rewrites a log that is already over it. And a daemon that is
already up rewrites one that has _gained_ another whole 8 MiB since anyone last looked — in the background, taking its
snapshot under the store's lock and doing the expensive part off it, so no dispatch ever waits on a whole-file
re-marshal. A daemon that is never restarted no longer grows its log until the next start.

Growth rather than size at the second door is what lets one number serve both. A running daemon's log is therefore
bounded by **its compacted size plus the threshold** — not by twice the threshold. For an ordinary store those are the
same statement, because its compacted form is well under 8 MiB. For a store with more ids than that budget covers they
are not, and it is the first that is true: the rewrite is compared against the log as it stands rather than against
the constant, so what compaction bounds is the log's shape and not its size.

The rewrite is atomic — a temp file, an fsync, a rename — so a crash at any moment leaves the next boot a whole file to
replay, and a failure leaves the old log byte-for-byte untouched.

What it costs, stated rather than left to be discovered:

- **Nothing live is destroyed and no id is ever freed.** A live entry keeps its wording, its aliases, its counts and
  its rung; every id the log has ever named keeps a record, so a dead entry's history can never be grafted onto a
  newcomer.
- **Past the threshold, a retired entry's text is gone.** Keeping it would mean holding every retirement the store has
  ever made for the life of the daemon — the growth compaction exists to stop.
- Therefore **"the history shows which agent said this" is true only of entries that are still live.** The action
  records a compaction replaces are who repeated a wording and when, and which brief counted as a signal.
- And the same rule reading in your favour: **a secret an agent leaked into an entry that has since been retired stops
  being on disk.**
- **Recency spacing moves.** Recency is a position in the log, so rewriting the log rewrites every position. The order
  survives, the spacing does not — entries at positions 10, 5000 and 200000 come back at 1, 2 and 3. What you may see
  is a working set reordered by some other dimension winning a comparison recency used to decide. And because the
  second door needs no restart, **you may now see it happen while you are watching**: a panel's working set re-ordered
  with nothing submitted, no config touched and no daemon stopped. The daemon says so when it does — see below.

Compaction declines where it would do harm: on a log at or below the threshold, on one that parsed into no ids at all,
where the store still owes a duplicate-line removal, and where the rewrite would not actually be smaller. A store
running **unlocked** — `status` reports `unlocked: true` — declines the runtime door outright and keeps the boot's
alone. A rewrite is built from one daemon's memory, so renaming it over a log a second daemon is also appending to
destroys what that second daemon wrote; doing that once per boot is the bet that has always been there, and doing it
once per threshold for the life of the process is not the same bet.

The daemon says what happened. The boot's `score recovered` line carries the counters: `compacted` is how many records
the last rewrite wrote, and `0` means none has run; `compactions` is how many rewrites have landed over this store's
life; `compaction_failures` is the rewrite failing — which costs no data, since the old log is intact, but costs the
bound, so the next boot is slower and every one after it slower again. A rewrite that failed also logs its own reason
at `WRN`: a count on its own does not tell you the disk was full.

And a rewrite that **succeeded** logs a `WRN` of its own, because it changed something you did not ask for — the
recency spacing above. It carries `compacted`, `log_before` and `log_after`, which is the only place the daemon names
the log's growth: the record count says how much the store remembers, not how large the file holding it had become.

**One line per rewrite, whichever door it came through.** A boot's is written by the boot. A running daemon's is
written on the **first read that uses the rewritten log** — the next brief, or the next `score.*` command — because
that read is the first thing to hold the re-spaced memory, and the compactor itself sits on no path the daemon logs
from. On an idle fleet the line can therefore arrive well after the rewrite it describes. And two rewrites that land
between two reads produce **one** line rather than two: it describes the log that is on disk, which is the later of
them, and there is nothing left to say about the earlier one.

### The first boot after upgrading a large store

The daemon opens its store **before** it binds its socket, so a slow open delays the daemon rather than leaving a
socket that accepts connections and never answers one. On a store that grew large before the runtime door existed,
that has one visible consequence — exactly once.

`baton` waits five seconds for the socket. Measured by log size, time to a client's first reply: 122 ms at 2.5 MB,
1.2 s at 81 MB, 3.1 s at 202 MB, **7.1 s at 456 MB**. The crossing is around **320 MB**, roughly 480,000 entries. Past
it, `baton` reports that the server did not come up — while the daemon behind that message goes on to open, compact,
and serve normally. Run `baton` again and it attaches to the fleet that is already running.

**It happens once**, and only to a store that grew before this build. That boot compacts it, and the two doors above
keep it bounded from then on. What it is not is a reason to loop on the message: restarting a daemon in the middle of
opening a large store means the open never finishes, and the boot that would have shrunk the log is the boot being
killed. See **[DAEMON.md](DAEMON.md)** for that message in general, and for `baton --force`.

### Rolling a binary back

**A compacted log can only be read by a build that knows about compaction.** This is the one place where an older
baton does not simply miss a new feature.

An older build does not recognise the compaction records, so it skips them and rebuilds every entry from `score.md`
instead — as _your_ entries. Measured on a real store: 1400 agent-submitted entries came back attributed to the
operator, every rung reset to 1, and every repeat count and user signal zeroed. Re-installing the newer build does not
repair it, because the older one has by then written those records into the log as the truth.

Losing the rungs and the counts would be the ordinary cost of reading a newer file with an older program. Turning what
an agent said into something you said is not — it is the one distinction Score is built to keep, and the reason a
signal from you outweighs a repeat from an agent.

So: **if rolling back to an older `baton` is something you do, copy `score.dir` before the first boot that compacts.**
The `score recovered` line tells you which boot that was, and `log_before`/`log_after` tell you it is coming.

## What this is, and is not

It **is** a fleet-scope memory: what this fleet keeps doing, learned by counting.

**Fleet scope crosses repositories.** An entry learned while working in one repo is injected into a panel working in
another. Context matching ranks same-repo entries higher; it does not fence. That is a rule for what an entry should
be: a statement about how this fleet behaves, never a fact about a codebase. The one fence is that you see every entry
in one file.

**The score is exactly as sensitive as a brief.** It is rendered into every prompt, so it must contain nothing an agent
would not already be handed. There is no secret scanning. A submission is stored verbatim — an agent that submits a
token has leaked it to the whole fleet, and (while the entry is live) the history shows which agent did.

**One entry reaches every panel.** A poisoned submission, from a compromised or confused agent, is amplified
fleet-wide. What limits it: it enters at rung 1, rendered as `noted`; it cannot climb past rung 2 without a signal from
you; and it is one line in a file you edit.

It is **not a boundary against a hostile agent.** An agent that unsets `BATON_PANEL_ID` in its own environment is
stamped as you, and one with filesystem access can write `score.md` directly. The submit cap is the same shape: it is
keyed on an identity the client declares and nobody verifies. What the connection check removes is the
case that needs no knowledge at all — declaring a role, which every panel can do. A shell panel gets no identity
environment either, so an agent CLI you start by hand inside one is stamped as you for as long as it runs. See
**[SECURITY.md](../SECURITY.md)**.

It is **not a knowledge base**, and not a place for facts about code. That is what `CLAUDE.md` is for.

It is **not ordered by time.** Nothing in the ranking reads a clock — recency is a position in the log — so a laptop
that slept for a week, an NTP correction, or a timezone change cannot reorder what the fleet is being told. Two runs
over the same log rank identically, on any machine.

## Related keys

| Key     | Does                                                                        |
| ------- | --------------------------------------------------------------------------- |
| `C-t R` | reload the config — retunes the thresholds, the budget and the weights live |
| `n C`   | open the conductor, which holds the three correction tools                  |
