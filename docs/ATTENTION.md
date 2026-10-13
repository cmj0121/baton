# Baton — Attention at scale

**English** · [繁體中文](ATTENTION.zh-TW.md)

A fleet is mostly fine. Forty-five panels are doing exactly what you asked them to, and the reason you are looking at the
screen is the other five. At that size you are not an operator working a list — you are an **exception handler**, and
there is only one question worth answering:

> **When does the fleet need a human, and how does that human clear the queue?**

Everything on this page is one half of that question or the other. Finding the panels that want you was never the hard
part; the footer badge has known the count for a long time. The cost is **clearing** them — and a queue you cannot
finish clearing is a queue you stop trusting, which is worse than not having one.

## `idle` was three different things wearing one badge

Before this, everything that fell quiet became `idle`, and `idle` covered three unrelated events:

| What actually happened                                | What Baton said |
| ----------------------------------------------------- | --------------- |
| the agent finished its turn and is waiting for you    | `idle`          |
| the agent has been running and silent for ten minutes | `idle`          |
| a shell nobody has touched since Tuesday              | `idle`          |

Baton already knew the difference for the first row — a panel records whether it is an **agent** or a **shell** — and
never said it. So now it does: **a quiet agent means "done, review me"; a quiet shell means "nobody is typing".** No new
heuristic and no new detection risk, just saying out loud what the fleet already knew. The second row was invisible by
any means, and it gets a state of its own.

## One quiet clock

Every threshold above `running` reads the **same** number: the time since this panel's last byte of output. Not how long
the current state has held — that would reset the ladder at every rung and strand a panel at `done` forever, when the
whole point is that a panel which settled to `done` at a minute must keep climbing to `stuck` at ten without a byte in
between.

```txt
   the quiet clock — time since this panel's last byte of output
   ├──────────────┬──────────────────┬───────────────────────────────>
   0             10s                60s                             10m
                  │                  │                                │
 ┌──────────┐     ▼    ┌──────────┐  ▼   ┌──────────┐     ┌──────────┐
 │ running  │ ───────> │   idle   │ ───> │   done   │ ──> │  stuck   │
 └────▲─────┘          └──────────┘      └──────────┘     └──────────┘
      │                 a shell           review me        this has gone
      │                 rests here                         on too long
      │                                       agent only ──────────┘
      │
      └──── one byte of output takes any rung back to running ────────┘

              ┌──────────────┐      raised by the agent itself, by a
              │  attention   │ ◀──  question-shaped tail, or by a trained
              └──────────────┘      classifier. Outranks the whole ladder above.
```

- **`idle`** at 10 seconds. Not configurable, and deliberately: it means "output stopped", which is a fact about the byte
  stream rather than a judgement about the agent.
- **`done`** at `panel.done-after` (default **60s**), agents only. It is six times `idle` on purpose — an agent waiting
  on a single tool call is quiet for ten seconds routinely, and calling that "finished, review me" would fill the queue
  with work that is still running.
- **`stuck`** at `panel.stuck-after` (default **10m**), agents only. It says nothing about _why_; only that the silence
  has outlasted the budget configured for that agent.

`done` and `stuck` are agent-only, and that exclusion is the point of the whole distinction. A shell nobody has touched
since Tuesday is `idle` on purpose: it is not waiting to be reviewed and it is not wedged, and escalating forty-five of
them every ten minutes is precisely the noise the queue exists to prevent. A shell still reaches `attention` when its own
output asks a question.

**`failed` is not a state.** A panel that exited badly is `exited` carrying a **non-zero exit code**, which the cockpit
renders as failed. The daemon has always logged that code; now it is on the wire, so "which of these failed" is a
question a frontend can answer rather than infer. Keeping it a rendering rather than a state means one fact has one
spelling, everywhere.

## How a state is decided

Four things can claim a quiet panel, and they are not equally trustworthy. Higher wins:

| Priority | Source                                                                                    | Reliability     | Works with                        |
| -------- | ----------------------------------------------------------------------------------------- | --------------- | --------------------------------- |
| **1**    | the agent **declares** it — `baton ctl attention --why "…"`                               | certain         | anything that knows Baton         |
| **2**    | a **timer** — quiet for `stuck-after`, or a task finished                                 | certain         | everything                        |
| **3**    | the **tail heuristic** — last content line, or a permission overlay in the last dozen     | a guess         | any CLI that never heard of Baton |
| **4**    | a **classifier** — a MOLD bundle asked about the same tail, only with `panel.classifier:` | a trained guess | any CLI, once you have taught it  |

Rows 3 and 4 are one rung, not two: either guess raises the panel, and neither can overrule the other — see
[A classifier beside the regex](#a-classifier-beside-the-regex). Two orderings inside that are load-bearing rather
than incidental, and both come out of the same principle — a certain signal beats a guess, and an answerable item beats
a reviewable one:

- **The `stuck` timer beats the tail heuristic.** An agent silent for ten minutes whose scrollback happens to end in a
  `?` is better described as stuck than as waiting on you. The timer is a fact; the tail is a guess about text.
- **On the tick that would promote a panel to `stuck`, a permission overlay is read once more.** A match stays, or
  becomes, `attention`. A bare `?` still loses to the timer. A panel that is already `stuck` is not read again. After
  `baton ctl resolve`, that same screen does not raise its hand a second time.
- **The tail heuristic beats the `done` timer.** A tail reading `Apply this refactor? [y/N]` at twenty seconds is a
  question _now_. Making it wait out `done-after` to be called `done` would bury something you can answer in one
  keystroke underneath something you have to go and read.

The tail is a guess about text, and it is no longer "whatever sits on the last physical line". CSI is stripped; the
window is split on `\n` and `\r`; a line whose visible runes are only whitespace or Unicode Box Drawing (U+2500–U+257F)
is dropped, because a permission TUI's last line is often a border. The existing last-line rules then run on the last
**content** line — that is what catches a `?` or a `(y/n)` sitting above a box. Distinctive overlay phrases may also
flag, looking only at the last dozen content lines of the same tail: `don't ask again`, `do you want to proceed`,
`allow this` / `allow command` / `allow tool`, or a numbered `1. Yes` together with a numbered `2. No` / `3. No`. A
bare `Yes`, a lone `allow`, or a `?` further up the log is ordinary prose. Inbox trust beats recall. It is still one
sniff and still `attention`.

The same logic keeps a task-completion event from demoting a panel already in `attention`: a dispatched task can settle
in the very tick the tail raised its hand, and letting the event win would turn "answer me" into "review me" one tick
after it was raised.

## A classifier beside the regex

The tail heuristic is a list of phrases, and a list of phrases is only ever as long as the prompts somebody has already
seen. `Apply this refactor? [y/N]` is on it. `Your call, and then I continue.` and `Password:` are not, and never will
be, because an agent that writes its waits in prose does not write them twice the same way. On a hand-made holdout of
eight such waits the regex caught three; a bundle trained on fourteen rows caught all eight and flagged none of the
eight negatives. Real tails will score lower than a holdout written by the same hand as the training set — which is why
the loop below exists — but the shape of the gain is the point: the classifier reads the waits the regex cannot be
written to read.

[MOLD](https://github.com/cmj0121/mold) is a small local classifier: a bundle of labelled examples it answers from
over a unix socket, one JSON line per question, with no provider behind it. Baton speaks that socket directly, so a fleet
that has no `mold` has nothing to install and nothing to configure — the block below is absent, and the regex decides as
it always has.

```yaml
panel:
  classifier: # the block's presence is the switch; absent = today's behaviour
    socket: ~/.mold/mold.sock # where `mold serve` listens
    scope: baton-attention # the mold scope that holds the one key, `attention`
    min-confidence: 0.5 # the score an answer must reach, 0–1
    timeout: 2s # the longest one question may take
    record: false # write the row files a bundle is trained from (see below)
```

Every quiet panel the regex passes over is asked once, about the **same tail window the regex read**, in the same form
the rows below are written in — and asked **off the tick**. The Monitor never waits on the socket: the question goes to
one worker with a bounded queue, and a confident `attention` lands on the next tick, applied only if the panel is still
quiet with the same bytes on screen. A screen that changed in between is a question nobody asked. That is also why the
classifier, unlike the regex, can raise a panel that already settled to `idle` or `done`: by the time the answer is back
the settling tick has passed.

Where the answer sits is the whole contract:

- **Below the timers.** A panel silent past `stuck-after` is `stuck`, whatever the bundle thinks of its last line. A
  classifier is still a guess about text, and the timer is a fact.
- **Beside the regex, above `done`.** A confident `attention` raises a panel exactly as a `[y/N]` would — a wait you
  can answer in a keystroke must not sit underneath a review you have to go and read.
- **It never demotes anything.** The two guesses are OR-ed, never AND-ed: a regex match stands whether or not the bundle
  agrees, and a `done` panel the bundle is unsure about stays `done`. A classifier can add rows to the inbox; it cannot
  take one away, because a queue that loses rows to a model's doubt is a queue you stop trusting.

An answer counts only when it comes back from the configured `scope`, its key is `attention`, and its confidence clears
`min-confidence`. The scope check is not pedantry: mold resolves a scope by meaning, so a bundle that has never heard
of `baton-attention` cheerfully answers from whatever scope it does have, and a confident `billing` is not a wait.

**A dead classifier costs one log line.** The first dial error, timeout or malformed reply is logged once, naming the
socket, and the client is treated as absent — not asked again — until a config reload (`C-t R`, `SIGHUP`) or a
successful `baton ctl classifier status`. That verb probes the socket and, when it answers, re-arms the client, so
`mold serve` restarted over lunch is one command away from being used again rather than a daemon restart:

```sh
$ baton ctl classifier status
classifier: on — mold 0.3.1 pid 4772, bundle ~/.mold/bundle, 12 keys, cpu; scope baton-attention ≥ 0.50; rows 37, holdout 12
```

The other two lines it can print are `classifier: off` (no block) and `classifier: unreachable — <the dial error>
(regex decides)`. `[capped]` is appended when a row file has reached its cap, and `record off` when rows are not being
written.

In the inbox a row the classifier raised carries `▸ classifier` at the top of the tail pane, where a declared reason
would sit. The regex prints nothing there, as before. The line is there so that an operator dismissing a wrong guess
knows **which** guess was wrong — and it is how a badly trained bundle is noticed on the first afternoon rather than
the tenth.

### Teaching it

A bundle is only as good as the rows a human certified, and the rows that matter are the ones the regex misses. Baton
collects them where a human — or the agent itself — already says "this is a wait":

1. **Switch recording on** — `panel.classifier.record: true` — and run the fleet as usual. From then on every
   `baton ctl attention --why` an agent makes writes the tail it declared on as a positive row, and you add the ones the
   fleet missed: `m` on an inbox row, `C-t m` in a zoom, or `baton ctl attention-mark --id <panel>` all say "this
   screen is a wait" and write the panel's tail as a positive. A mark changes nothing about the panel; it is a label,
   not a verdict. Dismissing a row the regex or the classifier raised (`x`) writes that tail as a negative.
2. **Train and measure**, once `classifier status` says enough rows wait:

   ```sh
   mold train --labelled ~/.baton/attention-rows.jsonl --scope baton-attention
   mold eval ~/.baton/attention-holdout.jsonl
   ```

   `mold eval` reports how often the bundle would have raised a panel a human dismissed — the false-dispatch rate — so
   you set `min-confidence` against your own fleet's tails rather than against the defaults.

3. **Add the block** (or leave `record: true` inside it) and reload. Keep recording, train again when the count grows,
   and rotate the files by hand when you are done with a training set.

What lands in the two files, and what never does:

| Event                                                                 | File                               | Row                               |
| --------------------------------------------------------------------- | ---------------------------------- | --------------------------------- |
| an agent panel's own `baton ctl attention --why`                      | `~/.baton/attention-rows.jsonl`    | `{"text":…,"keys":["attention"]}` |
| `m` / `C-t m` / `baton ctl attention-mark --id`                       | `~/.baton/attention-rows.jsonl`    | `{"text":…,"keys":["attention"]}` |
| `x` on a row the **regex or the classifier** raised                   | `~/.baton/attention-holdout.jsonl` | `{"text":…,"keys":[]}`            |
| `i` / `1`–`3` on such a row — the reply clears it as **answered**     | nothing                            | —                                 |
| `x` on a `stuck`, `done`, failed or **declared** row · a snooze (`-`) | nothing                            | —                                 |
| typing into a panel from a zoom, a timer firing                       | nothing                            | —                                 |

Only the two certified events are positives, because they are the two places something that **knows** has spoken: the
agent about itself, or a person looking at the screen. "The operator typed into it" is not a label — operators type the
next task into `done` panels all day. A dismissed guess is the one honest negative the fleet has, and it goes to the
holdout rather than the training set because `mold train --labelled` refuses a `keys: []` row and `mold eval` is built
on them; the files are two because mold reads them in two commands. Both shapes are mold's own, with nothing added, so
mold never meets a field it does not know. An inbox reply clears its row as **answered**, which is neither: the guess was
right, but a reply is not a certification — press `m` first if you want the bundle to learn that screen.

A row is the tail's content lines — CSI stripped, border-only lines dropped — scrubbed, and capped at **2000 runes**.
Each file stops at **1 MiB**: at the cap the write is skipped and logged once, and `classifier status` says `[capped]`,
rather than rotating oldest-first and silently forgetting the earliest positives of a training set. A tail already in
the file is not written twice, so the same permission overlay coming back every turn is one row. The files are created
`0600`. Rotate by hand after training (`mv` the file away) and reload (`C-t R`), so the daemon forgets what it had
already written; without the reload it keeps the old file's row count and skips any tail it recorded before the move.

### What this is not a boundary for

- **Scrollback on disk.** `record: true` writes what was on an agent's screen to `~/.baton`, scrubbed as every stored
  text is and capped per row, but scrubbing is pattern-matching and a wait can quote a secret in prose. It is off by
  default for the same reason [LOGGING.md](LOGGING.md) makes logging a decision of its own; switch it on for a fleet
  whose tails you would be willing to read back.
- **A dismiss as a verdict.** `x` sometimes means "wrong guess" and sometimes means "I saw it and chose to wait". The
  holdout cannot tell those apart, which is why it is measured and never trained from: `mold eval` reports a rate and
  you read it knowing that.
- **A substitute for the agent saying so.** A confident bundle is still rung 3. The only certain signal is still
  `attention --why`, and nothing the classifier learns makes it more than a guess.

## An agent can raise its own hand

The timer and the heuristic are both Baton guessing from the outside. The one participant that actually **knows** it is
blocked is the agent, and it now has the words for it:

```sh
# Inside an agent panel: block on a decision, in the sentence the human will read.
baton ctl attention --why "two migrations conflict — which one wins?"

# …and once you have your answer, stand down.
baton ctl resolve
```

Neither takes an id: the panel's own id is already on the connection, because Baton injects `BATON_PANEL_ID` into
**every agent panel**, not only the conductor. So an agent raises its own hand without ever having to work out which
panel it is, and this works fleet-wide rather than for one privileged panel. Shell panels are deliberately left out — a
shell is a launcher, every program a person runs in it would inherit the marking, and the human sitting at it already
has the cockpit and `--id`.

`baton_attention` / `baton_resolve` are the same pair as MCP tools. `--why` is required by the flag parser as well as by
the server: a declaration displaces two guesses precisely because it can say why, and one with nothing to say is not
worth more than a timer. The reason is scrubbed and capped once, on the way in, so every screen downstream is holding
text that is already safe. Full contract — including what `resolve` mutes and why the conductor may raise its own hand
but nobody else's — in **[CONTROL.md § Raising a hand](CONTROL.md#raising-a-hand)**.

## The inbox — `C-t a`

The queue has somewhere to be cleared. `C-t a` opens it from **any** view, and it is a master-detail overlay: the queue
on the left, and on the right the **same tail window the Monitor sniffed** when it raised the flag — pulled one row at a
time, never carried on a snapshot, so what you read is literally what made the decision.

```text
╭──────────────────────────────────────────────────────────────────────────╮
│ I N B O X   1 of 3                                                       │
│ all 3 · attention 1 · stuck 1 · failed 0 · done 1                        │
│                                                                          │
│ ▸ ◆ refactor-api           4m │ ▸ two migrations conflict — which wins?   │
│   ◈ migrate-db            11m │                                           │
│   ◇ docs-sweep            26m │ Files to change: internal/server/server.go│
│                               │ Apply this refactor? [y/N]                │
│                                                                          │
│ j/k move · tab filter · enter zoom · r re-sort · esc close               │
│ i reply  ·  - snooze  ·  x dismiss  ·  m mark as a wait                  │
╰──────────────────────────────────────────────────────────────────────────╯
```

The LED is the state's own — `◆` attention, `◈` stuck, `◇` done, `○` an exit — and the age is the quiet clock. When the
agent declared its need, its **reason** is the `▸` line at the top of the tail pane, in its own words. The tail itself is
bottom-aligned, because the question is always the last thing a program printed.

| Key                  | Does                                                                              |
| -------------------- | --------------------------------------------------------------------------------- |
| `j` / `k`, `↓` / `↑` | move the cursor                                                                   |
| `g` / `G`            | first / last row                                                                  |
| **`i`**              | **reply in place** — the line goes to the panel, the row clears, the cursor stays |
| **`1` / `2` / `3`**  | **answer a permission overlay** — the digit and a newline, then the row clears    |
| `enter`              | zoom the panel — and acknowledge **nothing**                                      |
| `-`                  | snooze the row for `settings.inbox-snooze`                                        |
| `x`                  | dismiss the row until the panel next produces output                              |
| `m`                  | mark the row's screen as a wait — a row for the classifier; the row stays         |
| `tab` / `shift+tab`  | cycle which bucket the list shows                                                 |
| `r`                  | re-sort the queue                                                                 |
| `esc` / `q`          | close                                                                             |

**`i` is the feature.** Handling one item used to cost _navigate, enter, read, type, `C-t d`_ — a full screen swap that
loses sight of the fleet — and twenty of those in a row was the actual bottleneck. `i` opens a small composer inside the
overlay, sends the line with the newline the daemon's own submit uses, acknowledges the row in the same breath (the
reply **is** the acknowledgement), and leaves the cursor at its index — so the queue shrinking under it puts the next
item already in place. An empty reply sends a bare newline on purpose: accepting a `[y/N]` default is a real answer, and
refusing it would make the commonest confirmation the one case the queue cannot handle.

While the composer is open it owns the keyboard outright, so a reply containing the letter `x` — or the digit `1` — is
text rather than a verb. There is **no echo** — the inbox never attaches, because attaching per row would cost a replay
flush and a repaint each time, which is exactly the cost this exists to remove. `enter` zooms for anyone who wants to
watch a reply land.

`1`, `2` and `3` are that same reply with the typing taken out, and only for a permission overlay. The row must be
`attention`, with no declared reason, and the tail the inbox has already pulled must be the overlay the monitor reads.
The digit and a newline go to the panel and the row clears. A `stuck`, `done` or failed row, a declared reason, a bare
`?`, and an empty tail do not send. A tail that has not arrived yet says so and waits.

### The order is the feature

Rows sort by **bucket first, then oldest-first**, with ties broken numerically by panel id so every cockpit draws the
same queue:

| Bucket        | Why it is where it is                                    |
| ------------- | -------------------------------------------------------- |
| 1 `attention` | the only bucket you can clear from here by **typing**    |
| 2 `stuck`     | it should have finished and has not — actionable         |
| 3 failed      | a hard fact, already over: triage, not rescue            |
| 4 `done`      | "review me", and only when `settings.inbox-done` says so |

"Answer me" is never buried under "review me". The conductor and the global shell never appear at all: they are
infrastructure, always present, and a queue with a permanent floor of two is a queue nobody can finish.

**And the order freezes while your cursor is in it.** It re-sorts on open and on `r`, and nowhere else. An arriving
snapshot may change what a row _says_, may append a new qualifier at the tail where it cannot jump the selection, and
greys out a row that stopped qualifying — but it never pulls a row out from under the hand about to act on it. A queue
that re-sorts under you is a queue where the thing you press `x` on is not the thing you read.

### `tab` chooses a bucket

Seeing all four at once is right until you are doing one kind of thing. Clearing failed rows means walking past every
question first; a `done` review sits buried under questions you are not doing right now. `tab` cycles which bucket the
list shows, `shift+tab` the other way:

```text
all → attention → stuck → failed → done → all
```

**It is a mask, not a re-sort.** The order does not move — filtering hides rows, it never re-derives the queue, so
everything the freeze above buys you still holds. `j`/`k` walk the rows you can see, `r` still re-sorts the whole queue
and then re-applies the mask, and `x` can never act on a row that is not on screen.

Every open starts on `all`. `C-t a` exists to clear what needs a human, and a filter remembered from last time would
hide `attention` behind a key you did not press. Empty buckets are **not** skipped either: a cycle whose next stop
depends on the fleet is the same disorientation a re-sorting queue is, so the names stay put and an empty one says so.
`done` is the one stop that can be missing, because with `settings.inbox-done` off the bucket does not exist.

An empty filter keeps the overlay open and says `no failed panels` — not `nothing needs a human right now`, which would
be a lie while other buckets still hold rows. The header names every stop with its count, so you can see whether the
next press is worth it before you press it.

### Nothing leaves by accident, and nothing comes back by surprise

A `done` row leaves the queue on **`x` or `i`, and on nothing else** — opening the panel, zooming into it and coming
back leaves it exactly where it was. A queue you can consume by looking at it is a queue you stop trusting.

Snooze and dismiss are **fleet state on the server**, not cockpit state. A second cockpit re-offering work the first one
just cleared would be the same untrustworthy queue by another route, and with `--remote` a reattach is routine enough
that losing an afternoon's triage on a reconnect would be a daily cost. The consequence is worth stating plainly: two
cockpits share one cleared queue, and so therefore do two people.

A **dismiss** stands until the panel **produces output again** — not until its state changes. A dismissed `done` that
came back as `stuck` ten minutes later, on a timer the human did nothing to cause, is exactly the resurrection `x`
exists to prevent. A **snooze** is an absolute instant computed by your cockpit from your own setting and sent, so two
cockpits configured differently each get what they configured. Neither is persisted: a daemon restart brings panels back
as inert exited slots, and suppressing a row for one of those would be suppressing a row for a panel that no longer
exists in the same sense.

## The dashboard shows where the work is, without moving it

The obvious way to surface need on the dashboard is to sort by it, and that is the one thing this deliberately does not
do. Cards moving under the cursor for reasons you did not cause is the same disorientation the frozen inbox ordering
avoids — and worse here, because the dashboard is the screen you are looking at when it happens. So the tree keeps its
shape and gets **annotated** instead.

**Need counts.** A group header carries `◆N`: how many of its members the inbox would queue right now. It is the queue's
own predicate, called rather than restated, because two definitions of "needs you" is a header claiming two panels are
waiting while `C-t a` offers one, and you left to work out which of Baton's screens is lying. Nested groups fold their
whole subtree into the count — a panel asking for help two levels down is still work inside the top-level item.

**The quiet fold.** Past `settings.fold-quiet` (default **8**, `0` never folds) the panels that say nothing collapse
into one `▸ 12 quiet` row. Quiet means `idle` or **cleanly** exited — the two states that say nothing happened and
nothing is going to. A non-zero exit is not quiet; it is a failure sitting there waiting to be read.

Folded, **never dropped**. The row expands in place and the panels come back exactly where they were, because a
dashboard that silently stops showing you panels is one you have to keep double-checking against `baton ctl list`, and
then it has cost more than it saved. It sits at the position of the _first_ panel it swallowed rather than at the end,
so the card above it and the card below it are the same two cards they were a moment ago.

Four things are never folded, and all four are you having already said this one matters: a **favourite**, a **pin**, a
**marked** panel, and the card **under the cursor**. The last would be a bug rather than a preference.

The row owns no verbs beyond opening and closing itself — `w`, `s`, `g g`, `r` and `*` are refused with the way forward on
the status line, and it answers no panel ids at all, so a bulk verb that forgot to ask acts on nothing rather than on
every quiet panel at once. And the fold deliberately has no say in which layout the dashboard draws: the cards-or-tree
count is of what the top level HOLDS, so the 45 panels behind a fold row count as 45 whether it is open, shut, or not
firing at all. Otherwise zooming a panel — which stops it being foldable — would swap the whole dashboard on the way
back.

## The summary tile folds the lookalikes, not the latecomers

A group split shows a few live tiles and folds the rest into a summary. It used to fold whoever happened to spawn last,
which says nothing at all about who is worth watching. With `settings.fold-similar` (default **on**) it folds the
members matching the **majority** and spends the live tiles on the **outliers** — so after one broadcast to a group of
fifty shells the answer becomes "**48 identical**, 2 differ, and here are the 2".

Who looks like whom is the daemon's judgement, not the cockpit's, because a cockpit holds no output for a panel it never
attached to: it is the panel's state plus the shape of its last output line, with escape sequences stripped (a shell
writes a window title before almost every prompt) and digit runs collapsed to `#` (so a progress counter is not a
difference), hashed to eight hex characters.

The fold **declines**, and the positional split stands, for the four groups it has nothing useful to say about: nothing
alike (a group where everything differs must not fold everything away), everything alike, lookalikes that are all
pinned, and a fleet of agents each on a different frame of a spinner. Guessing which glyphs are "the same animation"
would fold panels that are genuinely doing different things, so it does not guess.

**A pin is not exclusive here**, and that is a deliberate difference from the positional fold. A pinned member is always
a tile and takes from the budget first, but the outliers still get the tiles that are left. A pin means "always show me
this"; reading it as "show me _only_ this" would let one pin in a group of fifty silently suppress every outlier the
fold exists to surface.

A group that fits inside its visible-tile count never folds at all, so a fleet of five is untouched by any of this.

## Reaching a human who is not looking

The escalation path used to stop at the bell, and a bell only reaches the desk the daemon's terminal is on. Since
`--remote` landed that is routinely not the desk you are at, so the loudest thing Baton could do about fifty waiting
agents was ring a room nobody was in.

`settings.notify` writes an **OSC 9** desktop notification — the same argument that chose OSC 52 for the clipboard. It
is bytes to the terminal, so there is no helper binary, no `notify-send`, no per-platform launcher, and it crosses the
ssh hop for free, because the terminal that renders it is the one in front of the person.

**It is coalesced, and that is not optional.** The failure mode at fleet scale is not the missed alert, it is the forty
that arrive at once and teach someone to mute the channel. So the first rising edge does **not** fire: it opens a window
(`settings.notify-coalesce`, default 30s) and one notification goes out when the window closes. Twelve agents finishing
a step together is "**12 agents need you**", never twelve toasts. One panel is named; several are counted, because with
several there is no useful thing to say.

**`done` never notifies**, `inbox-done` or not. Waking someone at 2am to say an agent succeeded is precisely how a
notification channel gets turned off, and it takes the wedges and the failures down with it. What does notify is
`attention`, `stuck`, and an exit with a **non-zero** code — someone who is away wants to hear that an agent wedged or
died and will not be back to read the badge that says so.

**It is off by default**, unlike the bell, and off means not one byte. A terminal you are attached to is a place you
chose to be; a desktop toast arrives wherever you are, and that is not something software may assume it is welcome to
do. The bell is unchanged and independent: the two reach different people — one at this desk, one at another — and
suppressing either because the other fired would silently disable the escalation exactly when it mattered.

## Configuration

Every judgement call on this page is a knob, because every one of them is a guess about somebody else's fleet. The
defaults are the opinionated answer; the config exists to disagree with it.

```yaml
# $HOME/.baton/config
settings:
  # --- the attention queue ---
  inbox-done: true # a finished agent joins the queue; false = only waiting / failed / stuck
  inbox-snooze: 10m # how long `-` defers a row
  notify: false # OSC 9 desktop notifications, off until asked for
  notify-coalesce: 30s # one notification per window, never one per panel

  # --- the dashboard ---
  fold-quiet: 8 # fold quiet panels into one row past this many (0 = never fold)
  fold-similar: true # the summary tile folds by similarity rather than by position

  bell: true # unchanged, and independent of notify: ring on entering attention

panel:
  # --- the quiet ladder ---
  done-on-quiet: true # a quiet AGENT panel resolves to `done` rather than `idle`
  done-after: 60s # …after this much silence (0 = never)
  stuck-after: 10m # quiet this long raises `stuck` (0 = never)
  agents:
    claude:
      stuck-after: 30m # this profile legitimately thinks for longer

  # --- the classifier (absent = off) ---
  classifier:
    socket: ~/.mold/mold.sock # where `mold serve` listens
    scope: baton-attention # the mold scope holding the one key
    min-confidence: 0.5 # an answer below this is not an answer
    timeout: 2s # one question, bounded
    record: false # write the row files a bundle is trained from
```

| Key                               | Default             | Means                                                                |
| --------------------------------- | ------------------- | -------------------------------------------------------------------- |
| `settings.inbox-done`             | `true`              | a finished agent gets a "review me" row in the inbox and a `◆`       |
| `settings.inbox-snooze`           | `10m`               | how long `-` defers a row; applied by the cockpit, not the daemon    |
| `settings.notify`                 | `false`             | OSC 9 desktop notifications                                          |
| `settings.notify-coalesce`        | `30s`               | the window edges are gathered into one notification over             |
| `settings.fold-quiet`             | `8`                 | quiet panels past this many fold into one row; `0` never folds       |
| `settings.fold-similar`           | `true`              | the summary tile folds the lookalikes rather than the latecomers     |
| `panel.done-on-quiet`             | `true`              | whether a quiet agent climbs to `done` at all                        |
| `panel.done-after`                | `60s`               | the silence at which its turn reads as over; `0` = never             |
| `panel.stuck-after`               | `10m`               | the silence at which it reads as a problem; `0` = never              |
| `panel.classifier`                | absent              | the block's presence switches the classifier on; fleet-wide only     |
| `panel.classifier.socket`         | `~/.mold/mold.sock` | the unix socket `mold serve` listens on; `~` is expanded             |
| `panel.classifier.scope`          | `baton-attention`   | the mold scope whose `attention` key counts; any other scope is "no" |
| `panel.classifier.min-confidence` | `0.5`               | the floor an `attention` answer must clear, 0–1                      |
| `panel.classifier.timeout`        | `2s`                | dial, ask and read, together                                         |
| `panel.classifier.record`         | `false`             | write `attention-rows.jsonl` / `attention-holdout.jsonl`             |

An **absent** key inherits; an explicit **`0`** switches a rung off. Those are different statements, which is why they
are spelled differently: `stuck-after: 0` is the right setting for a fleet of shells, and it must survive the cockpit
rewriting the config file on the next settings toggle. A threshold Baton cannot parse is reported and **dropped**, so it
inherits rather than quietly promoting panels into a state you did not ask for; a ladder whose rungs are out of order
(`stuck` at or below `done`) has its higher rung disabled rather than silently reordered. The classifier block is the
one exception: a `min-confidence` or `timeout` it cannot parse is reported and that key keeps its **default**, with the
block staying on — a classifier you wrote a block for and then lost to one typo would silently hand the fleet back to
the regex alone. It takes no per-profile override, because one bundle answers one question for the whole fleet.

The whole `panel` block is hot-reloadable — `C-t R`, or a `SIGHUP` to the daemon — and the thresholds are resolved from
each panel's profile **every tick** rather than frozen at spawn, so a reload takes hold under a running fleet without
touching a live panel.

### Why `done-on-quiet` exists

`done` is the rung that makes the queue **clearable**. Without it the inbox holds only questions, failures and wedges,
which is a strictly smaller promise than "here is everything that wants a human, and here is how you finish with it".
That is why it defaults to on.

But it defaults to on for the **fleet** case, and the fleet case is not everybody's. Someone running a single agent is
already watching it: they saw the turn finish, they are looking at the screen it finished on, and a second badge for a
state they can see is **noise rather than signal**. Turn it off and a quiet agent stays `idle`, exactly as it did before
this ladder existed:

```yaml
panel:
  done-on-quiet: false # I am watching this one; do not tell me it stopped
```

It is also the flag a group header's `◆N` counts, so switching it off narrows both screens in the same direction — which
is the point. The two never disagree with each other.

### Why `stuck-after` takes a per-profile override

The right value for "silent long enough that something is wrong" **is a property of the agent, not of Baton**. A shell
script that prints a line a second is wedged after thirty of them. A model planning a refactor is legitimately silent
for ten minutes, and one running a full test suite for thirty. Baton cannot tell those apart from the byte stream, and
guessing wrong is expensive in **both** directions: set it too low and every thinking agent cries wolf until the queue
is ignored; set it too high and a genuinely wedged agent sits unnoticed for the afternoon.

There is no single number that is right for a fleet running more than one kind of agent — so there is no single number.
It layers exactly as the [resource limits](LIMITS.md) and the [restart policy](RESTART.md) do: the fleet-wide value is
the floor, and a profile restates **only the one line it changes**.

```yaml
panel:
  stuck-after: 10m # the fleet's assumption
  agents:
    claude:
      stuck-after: 30m # …but this one thinks for longer, and that is not a fault
    lint-loop:
      stuck-after: 45s # …and this one is wedged if it stops for a minute
```

`done-after` and `done-on-quiet` take the same override, for the same reason.

## Related keys

| Where       | Key           | Does                                                                                       |
| ----------- | ------------- | ------------------------------------------------------------------------------------------ |
| Any view    | `C-t a`       | open the attention inbox                                                                   |
| Inbox       | `i`           | reply in place and clear the row                                                           |
|             | `-` / `x`     | snooze / dismiss the row                                                                   |
|             | `m`           | mark the row's screen as a wait — a positive row for the classifier                        |
|             | `enter` / `r` | zoom the panel (clears nothing) / re-sort the queue                                        |
|             | `tab`         | cycle the bucket the inbox shows                                                           |
| Zoom        | `C-t m`       | the same mark, on the panel you are looking at                                             |
| Dashboard   | `enter`       | on the `▸ N quiet` row: expand it (`esc` folds it again)                                   |
|             | `space`       | the same fold, from the disclosure key every other row answers to                          |
| Group split | `p`           | pin a member — under the similarity fold a pin adds a tile rather than hiding the outliers |

## What this is not

- **Not a re-sorted dashboard.** The tree keeps the shape you arranged; need is an annotation on it, never a reordering
  of it.
- **Not auto-answering on your behalf.** The inbox makes replying cheap; it never decides what the reply is.
- **Not a model you have to run.** The classifier is a block you add when a fleet's waits outgrow the regex; without
  it nothing dials anything, and with it a dead socket costs one log line.
- **Not a spend boundary.** Nothing here — and nothing anywhere in Baton — caps how many tokens an agent uses. See the
  note in **[LIMITS.md](LIMITS.md#what-this-is-and-is-not)**.
- **Not persisted triage.** Snoozes and dismissals are live opinions about live processes; a daemon restart clears them,
  because the panels they were about no longer exist in the same sense.
