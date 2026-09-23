# Changelog

Every release is cut from an annotated tag whose message _is_ the release note, so the
[GitHub releases](https://github.com/cmj0121/baton/releases) always carry the full story —
the upgrade notes, the caveats, and why each change exists. This file is the index.

## [v2.4.2](https://github.com/cmj0121/baton/releases/tag/v2.4.2) — SYS follows the session you are in

2026-09-23

- **`/clear` and `/resume` are followed.** A Claude Code panel's status line now reports the session the panel is on,
  and the next usage poll moves the panel onto it, so `SYS` stops going blank after a `/clear`.
- **The panel's usage share follows too.** Spend after a `/clear` is counted for the panel, and a `/resume` back to an
  earlier session counts that transcript once.
- **Scoped to what baton launched.** Only panels with baton's status line are followed; one started with its own
  `--settings` is not, and neither is a server that keeps no state on disk.

## [v2.4.1](https://github.com/cmj0121/baton/releases/tag/v2.4.1) — what an agent pays before it says a word

2026-09-23

- **`SYS` in the footer.** When the cockpit points at one Claude Code panel, the footer shows that panel's opening
  cost after `CPU` and `MEM` (`SYS 44.0K`): the system prompt, tools, `CLAUDE.md`, the `MEMORY.md` index and the
  listings its session pays for on every turn. Shells, other agents and a selected group show nothing.
- **Measured, not estimated.** It is the first assistant turn's input less the typed prompt before it, so a dispatched
  brief does not inflate it; the typed prompt is the only estimate.
- **Read once per session.** A memory edit shows up in the next session's figure; a re-run (`r`, `n r`) reads anew.
  A `/clear` or `/resume` inside the panel is not followed yet.

## [v2.4.0](https://github.com/cmj0121/baton/releases/tag/v2.4.0) — where the week's tokens went

2026-09-23

- **The usage overlay is three tabs.** `v U` opens on Account — the quota bars, panel roster and vendor roll, as before.
  `tab` / `shift+tab` switch to Session and Week, and the tab is remembered while the cockpit runs.
- **Session and Week show spend by project, across agents.** Each project that spent in the scope gets a share bar, its
  share, tokens and cost, with the agents that spent it indented beneath; claude and grok land under one project. The
  week is the vendor's own 7-day quota week when it has ever stated a reset, else the last 7 days, and the note line
  says which.
- **A project is the directory a session was launched in.** Worktrees under `.claude/worktrees/`, baton's own
  `<repo>-worktrees/`, and grok's worktrees fold onto their main checkout; temporary paths fold into `(temporary)`. The
  daemon sends the top 20 projects plus one `(other)` row per agent.
- **The week costs the quota bars nothing.** It is rescanned every 5 minutes and on a passed reset, each agent within
  its own budget, after the tick's fresh bars have gone out.
- **A zoomed panel replays at the size it was painted, exactly once.** Stale Claude Code text in the input area and
  spurious wraps in grok's input box no longer wait for a re-zoom (#133).

## [v2.3.0](https://github.com/cmj0121/baton/releases/tag/v2.3.0) — the GitHub board the cockpit could not see

2026-09-21

- **`I` / `C-t I` opens the GitHub board for the selected panel's repo.** Cards sit in BACKLOG, IN PROCESS and PR. Tab
  and the arrows move between columns that have cards; empty columns are skipped. Space or enter opens detail. The
  overlay owns the keyboard until esc; its legend stays the last inner row of the box, and the cockpit footer stays the
  last row of the terminal.
- **The card under the cursor is the one you dispatch.** `T` dispatches, `t` enqueues with the issue number stamped on
  the task, `b` binds the worktree, `B` records a blocked-by, `p` jumps to the handling panel. An in-flight task
  (`dispatched` / `running` plus Issue N) is a live handler and sits in IN PROCESS; a queued brief stays BACKLOG. `T`
  does not auto-bind.
- **`git` and `gh` run on the daemon host, as the operator.** A remote attach still sees the repo the fleet sits in. The
  GitHub remote is a github.com URL, or a remote named GITHUB — origin is never chosen just because it is origin. Both
  `issues.board` and `issues.block` are an operator surface: a conductor connection is refused.
- **Milestones filter the board without leaving it.** `m` then Tab / Shift-Tab rotates chips with live column counts;
  `/` opens a name prompt and Enter selects a unique exact, prefix, or substring match (blank is all). A miss keeps the
  prompt. `[` `]` still cycle.
- **`issues.interval` polls while the overlay is open** — default 60s, floor 30s, `0` is `r` only.
- **The conductor is the documented fleet path.** `n C` plus `$HOME/.baton/CONDUCTOR.md`. `A` still spawns one agent.
  Dispatch-only: spawn, group, dispatch, enqueue, signal, close — not inbox, not `panel.tail`, not auto-start.

## [v2.2.2](https://github.com/cmj0121/baton/releases/tag/v2.2.2) — the memory the fleet could not write to

2026-09-17

- **An agent panel gains the memory's tool every time it starts.** `panel.agent-mcp` appended `--mcp-config` once, at
  spawn, and the flag was frozen into the spec the daemon persists — so a panel whose spec predated the setting could
  never gain it however often it was re-run, and every panel a restart rebuilt came back mute. A whole fleet sat unable
  to write to its own memory with no log line saying so. The wiring is derived at the one fork point now, beside the
  session id and the status line, which already worked this way for the same reason.
- **`score.status` says which panels can actually write.** `entries: 0` had two meanings — a fleet with nothing to
  remember, and a fleet holding no pen — and every other field read healthy for both. The new `agent_mcp` section names
  the panels that carry the tool and groups the rest by cause: a backend that takes no such flag, a command line that
  already names its own config, a config baton could not write.
- **A dead slot refuses the zoom it cannot fill.** `exited` was two different things. A panel that died under the
  running daemon still holds its last screen and opens as a result view; one the daemon rebuilt from its snapshot has
  no terminal behind it, and `enter` opened a black screen you had to press `esc` to leave. The snapshot carries the
  difference now, and the refusal names `r`.
- **`n r` re-launches, where `r` restarts.** `r` replays the spec a panel was launched with. `n r` resolves its agent
  profile against the config in force and starts the slot on that command line — the answer after editing
  `panel.agents` and reloading. It keeps the panel's id, and everything hanging off it; the directory does not move.
- **Grok's weekly credit pool reaches the vendor roll.** The `7d left` cell was dashed for every agent but Claude,
  because the guard against lending Anthropic's ceiling around was the vendor's name. It is the window's label now, so
  a vendor that publishes its own ceiling gets its own number. Grok's `5h` stays a dash: it publishes no session
  throttle.
- **A connection hears nothing before its own welcome.** A client joined the daemon's fan-out set when it was accepted
  rather than when it greeted, so a frame could arrive ahead of its welcome — and every client reads the handshake by
  position, so one early frame left it a message behind for the rest of its life.
- **zh-TW:** the exited zoom's status line says 已結束 rather than `(exited)`, and the group re-run status renders its
  own numbers instead of `%!s(int=3)`.

## [v2.2.1](https://github.com/cmj0121/baton/releases/tag/v2.2.1) — the usage overlay answers per agent

2026-09-17

- **`v U` prices every agent, not just the one.** Each readable backend gets five columns — `5h left`, `7d left`,
  `resets`, `spent`, `panels` — in place of the single line of free text it used to get. The quota pair belongs to the
  one account baton holds books for and is dashed out for everybody else: no other vendor publishes a ceiling, and
  lending it Anthropic's would print a limit nobody stated. `spent` is the vendor's own reader, everything on the
  machine; `panels` is baton's attribution, grouped by the profile each panel was spawned from.
- **Every readable agent counts down to its own reset.** The countdown used to ride inside the quota figure, so only the
  account that publishes a quota ever had one. Two instants land in the new column and the docs say which is which: the
  quota's own reset for the account it belongs to, the end of the measured window for everybody else, and a mark for an
  agent that has stated neither.
- **The overlay lines up in the language it is read in.** Three rulers were in play: `fmt` pads by counting runes (代理
  is two runes and four columns), `runewidth` calls an East Asian ambiguous rune two cells, and lipgloss — which lays
  every row out — calls it one. The roster's header, the bar labels and the agent roll all measure the way the finished
  screen is measured now. The em dash went with them, for being two cells on a CJK terminal and one to the layout.
- **A row baton has no reading for is `---`.** It was forty characters of "baton has no usage source for this agent",
  repeated down every agent the machine has not got — and it was English in every language, because the reason is the
  daemon's words printed verbatim. Which state a row is in was already the mark's job.
- **A message the footer cannot hold takes the whole row.** The strip composed every cap first and handed the status
  whatever was left, so a daemon error carrying git's own stderr arrived clipped mid-word or vanished. A key run in the
  air, a backend outage and a message past its TTL still outrank it.
- **The footer's resting line is the endpoint alone** — `local`, not `attached · local`. The cap it sits on is already
  the connection indicator.
- **zh-TW:** the roster header is 誰在消耗. 本窗口的消耗來源 read as "the source of the consumption figures", and on
  that screen 來源 is already taken by 用量來源.

## [v2.2.0](https://github.com/cmj0121/baton/releases/tag/v2.2.0) — the cockpit speaks your language

2026-09-16

- **The whole cockpit is translated, not just its help.** The catalog covered the `?` key list and the key map;
  everything else answered in English whatever the language — the panel-config page, eighteen text-input pop-ups, both
  pickers, the dashboard frame, the git surfaces, the inbox, the task queue, the group split and 161 status lines. 135
  catalog entries became 649. Key names, config keys (`cpus`, `nofile`), other people's words (`SIGINT`, `git add -A`,
  `claude`) and serialized values stay English on purpose, each for a reason the catalog states.
- **Completeness is tested rather than asserted.** `TestEveryMessageKeyIsTranslated` reads the package's own source,
  collects every key paired with the English beside it, and fails on any whose zh-TW is the same string — reaching the
  five hundred-odd messages no fixture can draw, because most need the keystroke that sets them or the failure that
  raises them. Two scanners sweep the rendered screens for English words a line-by-line diff cannot see.
- **No floating widget outgrows its terminal, or changes size while you walk it.** The panel-config page was drawn two
  rows taller than the screen, so its legend and the footer sat below the bottom edge: its reserved figure said 12
  beside a comment reading "two hints" long after the page had four. That count is computed now. The wordmark yields
  when an overlay needs the room, over-tall pop-ups are clipped without ever dropping the legend, and the tabs, the git
  menu and the directory browser all hold one height.
- **The panel-config page splits into tabs** — defaults, limits, feedback — each carrying its own rows and hints.
  On an 80×32 terminal the old page left one resource-limit row visible between its headings and its hints.
- **An agent panel starts knowing it can write to the fleet memory.** `score.submit` was never fenced, but the only
  place an agent was told so is a dispatched brief, which a panel you open and talk to yourself never receives. Agent
  panels now launch pointing at an MCP config baton writes in its own directory, carrying `score_submit` and nothing
  else — the fleet-control table stays the conductor's. `panel.agent-mcp`, default on, read at spawn.
- **The dashboard tree draws the stems it hangs its rows from.** Indentation alone left a sub-group's contents floating
  with nothing beside them, reading as a second root rather than as the contents of the work item above.
- **Fixes:** a SIGHUP handler that outlived the loop that armed them; `score.feedback` stamped into the config on every
  unrelated save; `e` on the panel-config page editing a row from the tab you came from; a CJK column that padded by
  runes instead of display cells; and a test suite that read the developer's locale.

## [v2.1.2](https://github.com/cmj0121/baton/releases/tag/v2.1.2) — the memory learns to ask

2026-09-15

- **Agents are told they may feed the fleet's memory** — v2.0.0 built a memory that learns from repetition and gave
  every panel the block that reads it, and no panel anything that writes it. `score_submit` is served from a
  `.mcp.json` written into the conductor's workspace and nowhere else, so an ordinary agent had no door; the brief said
  nothing about submitting; and `renderBlock` returns the empty string for an empty store, so a fresh install carried
  no score section at all. The result was a subsystem that was on, healthy and permanently silent — entries 0, and
  `score-events.jsonl` never created. Briefs now carry one line naming `baton ctl score submit`, which is the door
  every panel actually has.
- **It is rendered for an empty store, and it is the only part of the block that is.** That asymmetry is the whole fix:
  the first entry a fleet records is one it was told it could.
- **Two switches over it** — `score.feedback` fleet-wide, and `panel.agents.<name>.score-feedback` per profile, layered
  the way the caps and the restart policy already layer. Both reload on `SIGHUP`.
- **A hint, not a fence** — switching it off stops the telling and not the submitting. A panel's profile is read from an
  identity the connection declares and nobody verifies, so a refusal built on it would look like a boundary without
  being one.
- **`C-t P` carries a SCORE FEEDBACK section**, one row per configured profile, `e` cycling `inherit → on → off`. The
  edit is saved and the daemon told to re-read it, so it lands on the next brief.
- **`score status` reports `feedback` and `feedback_profiles`**, because both keys reload and the page had no other way
  to show that the daemon took the change.

## [v2.1.1](https://github.com/cmj0121/baton/releases/tag/v2.1.1) — panels the fleet could not see

2026-09-15

- **A panel that exited before it was registered is no longer alive forever** — `createPanel` forked the child twenty-eight
  lines before it appended the panel, so an exit that landed in between reached `onPanelExit`, found no such panel, and
  was dropped: no state change, no log line, no broadcast. The panel then joined the fleet as `spawning` for a process
  that was already dead. This is the cause of the intermittent CI red that had been chased three times as a flaky test.
- **The panel you spawn is put on screen** — on a fleet whose tree scrolls, `A` created the panel at the end of the list
  and left the cursor where it was, so the key read as having done nothing. The cockpit already did this for the
  conductor and the global shell; `A`, `p`, `n c` and `n .` never got the same treatment.
- **A new panel is sized for your screen** rather than the 24x80 floor, so a TUI agent's first paint is already right.
- **The conductor's briefing names every tool it has** — it named 13 of 20, and the seven it omitted were between them
  the whole task-assignment surface, so the agent whose job is handing work to panels had never been told it could.
  The list is derived from the registry now, in both directions.
- **A swept conductor workspace takes its boot stamp with it** — 22 orphaned stamps had accumulated on one machine.
- **Two polling loops let go of the CPU**, and a guard reads the suite for the next copy of that loop.

## [v2.1.0](https://github.com/cmj0121/baton/releases/tag/v2.1.0) — the memory gets a door, and the boxes close

2026-09-15

- **`n s` opens the fleet memory in your `$EDITOR`** — v2.0.0 gave the fleet a memory and left it reachable only by
  knowing where the daemon put the file. It now opens as a server-owned panel, which is what makes it work under
  `--remote` too. It sits under `n` and not the free `v s`, because every member of the view family is read-only and
  this one writes.
- **An editor left open no longer eats what the fleet learned** — an editor writes back the buffer it opened with, so
  entries submitted while you were typing vanished on save, silently, because deleting a line is how you retire one.
  Ids separate the two cases: an entry missing from the save whose id your snapshot never carried was never on your
  screen, so it comes back.
- **A panel's PTY is no longer born 0x0** — `pty.Start` set no winsize, so every panel began in a zero-row terminal
  until a cockpit attached. Shells never noticed; anything that draws a screen painted nothing. The git menu's commit
  had always had this and escaped it by accident.
- **`tab` cycles which bucket the inbox shows**, and both the inbox and the diff popup now fit the terminal — they were
  six and five rows taller than the screen at every height, so the bottom edge of the box was never drawn.
- **A `score.md` written before the bare-bullet rule existed is brought forward** — matched byte for byte against the
  headers baton has shipped, so a header you edited or deleted stays exactly as you left it.
- **The commit editor is fenced from the conductor role** — `panel.git commit` opens `$EDITOR` on the daemon's host, as
  you, which is `panel.log`'s shape and `score.edit`'s. The capture ops stay open; the line is "spawns an interactive
  program", not "touches git".

## [v2.0.0](https://github.com/cmj0121/baton/releases/tag/v2.0.0) — the fleet remembers, and says what it cannot see

2026-09-11

- **Score, a fleet-scope memory** — a store of what the fleet has learned, folded from repeats, promoted by recurrence,
  and prepended to the briefs agents receive. It is a file an operator owns (`score.md`) with an append-only event log
  beside it, and every door into it is argued: what an agent may submit, what only the conductor may merge or reword,
  and what only a person editing the file can do.
- **Worktrees as a first-class verb** — open a branch in its own checkout with an agent in it, from the dashboard, from
  `baton ctl`, or from MCP; list what has accumulated and sweep it with a confirmation. A tree baton did not create is
  never touched.
- **A third kind of panel** — `command` runs a plain binary as the panel's process. It is watched like an agent and
  never given work, and it HOLDS when it finishes rather than vanishing, so a build or a test run leaves its output on
  the screen. `n c` is how the cockpit opens one.
- **Serial ports** — `baton serial /dev/… 115200` puts a port in a panel, with the line settings named and a reconnect
  that outlives the cable. No `screen`, no second prefix key, and the panel's own close is how you leave.
- **Usage stops being one vendor's** — the footer names the agent whose reading it shows, and the overlay lists every
  agent baton detected with an honest word for each: a reading, no usage source, or not installed. `grok` reads its own
  accounting; a vendor that keeps none is said to keep none rather than drawn as a zero.
- **Ten rounds of hardening** — an ssh destination read as an option, two atomic writers that silently ignored the mode
  they were given, an `open(2)` on a FIFO that never returned and stopped the daemon before it served, a score store
  whose one boot failure could not heal. Each round is in the tag's note.
- **Upgrading**: the daemon must be restarted rather than reloaded, a `plug-in.lua` that anyone but you can write is now
  refused loudly, `n c` opens a command panel rather than a shell running a command, and the cockpit now pays the fleet
  ceiling it was previously exempt from. The wire is unchanged — still `baton/1` — and nothing on disk needs migrating.

## [v1.6.0](https://github.com/cmj0121/baton/releases/tag/v1.6.0) — the backends you have not installed

2026-08-25

- **The catalogue stops hiding its own misses** — the daemon filtered the agent CLIs down to what was on `PATH` and
  dropped the rest before anything left it, so a name baton knows and did not find never reached the cockpit. On a
  machine with one agent CLI the other five did not exist as far as you were concerned: baton looked for them, found
  nothing, and said nothing. `Scan` now reports every candidate with a verdict, and `C-t P` lists what is known and not
  installed with where to get each.
- **A preset says where to get it** — a name on its own tells someone who already knows what `opencode` is that they
  have not got it, and tells everyone else nothing. It says where, never how: a release process is someone else's to
  change, and an install command compiled into a binary rots where only a release can fix it.
- **`grok` joins the catalogue** — spawned as `grok`, from the `bin` entry its published package installs rather than
  the `grok-build` its announcement page reads as. That is the product and the repo; `PATH` is the only thing the scan
  asks about.
- **The spawn picker is unchanged** — `A` still lists only what it can spawn, because an entry you cannot choose is a
  trap and greying one out only moves the trap. A default naming a backend the machine has lost now fails in the open
  instead of dying in a fresh panel.

## [v1.5.0](https://github.com/cmj0121/baton/releases/tag/v1.5.0) — keys that teach themselves

2026-08-25

- **The which-key hint finally renders** — pressing a landing key was meant to show the family it opens; it showed
  nothing. The status bar built the line, measured it, and threw it away when it did not fit, and it never fit: the four
  families needed 206, 336, 138 and 25 columns against the ~55 a 128-column terminal can spare. The gap is now measured
  first and the hint is fitted to it — labelled where there is room, the keys alone where there is not, and the keys it
  can show with a `+N` for the rest when even that is too wide.
- **Every binding carries a short label** — `mark`, `create`, `add`, `ungroup`, against a line of prose each. The key
  map keeps its sentences, where two columns make them right. The families drop to 49, 70, 37 and 7 columns, so three
  of the four now render fully labelled, and `docs/KEYS.md` stops advertising a line the code could not produce.
- **Browse for a workdir** — `C-o` at the new-agent workdir prompt opens a picker: the directories the fleet's panels
  are already working in (busiest first), then a directories-only filesystem browse. What it picks fills the field and
  stays editable, so typing a path you know is still the shortest route and tab still completes it.
- **Two stale issues closed** — remote-over-SSH had shipped without its proposal being closed, and the which-key
  hint's own issue is what this release answers.

## [v1.4.1](https://github.com/cmj0121/baton/releases/tag/v1.4.1) — the reload reaches further

2026-08-25

- **The conductor's brief hot-reloads** — `$HOME/.baton/CONDUCTOR.md` reached the agent only when the conductor was
  spawned or re-run, so the one file whose whole purpose is to steer it was the one file a reload ignored. `C-t R` (or a
  `SIGHUP`) now rewrites it into the running conductor's workspace and prints a line in its panel saying so. It refreshes
  the brief; it does not change a running agent's mind — an agent reads its project instructions when its session starts,
  so the new brief is what it sees the next time it looks, and the notice is there to tell you there is something new.
- **The queue caps hot-reload** — `queue.max` and `queue.concurrency` were seeded at construction and nowhere else, so
  changing either meant restarting the daemon and losing every panel. Both now swap under a running backlog. Removing
  `queue.max` from the config restores the built-in default, and lowering it below what is already queued refuses the
  next enqueue rather than dropping a task.
- **One source for the backlog caps** — the construction path and the reload path used to read them separately; they now
  come from the same place, so the two can no longer drift apart.

## [v1.4.0](https://github.com/cmj0121/baton/releases/tag/v1.4.0) — the conductor remembers

2026-08-25

- **One conductor workspace, not one per open** — the conductor ran in a fresh throwaway directory every time it was
  opened, so nothing it collected there survived: the permission grants it writes beside itself were asked for again on
  every open, and an agent that keys its transcripts on the working directory left a fresh orphan behind each time. It
  is now one fixed directory per control socket, created only if it is not already there and kept when the panel is
  closed. `BATON_SOCK` still gets a workspace of its own.
- **Cleared when the host reboots** — the workspace carries a stamp of the boot it belongs to and is rebuilt from
  scratch when that no longer matches. Putting it somewhere temporary is not enough on its own: `$XDG_RUNTIME_DIR` is
  emptied on logout, but macOS keeps `$TMPDIR` across a reboot and sweeps `/private/tmp` on a three-day timer.
- **The leak is gone** — the old cleanup only ran if the daemon reached it, so every crash or hard kill left a
  `conductor-*` directory behind, for as long as baton had been installed. There is one workspace now, and the daemon
  sweeps the directories older versions leaked at start, logging each one it removes.
- **`baton ctl conductor reset`** — a workspace that is kept is a workspace that can go bad, so there is a way to clear
  it without waiting for a reboot. It is refused while a conductor still exists (close it first) and fenced from the
  conductor role itself: an agent that has gone wrong is the one that must not be able to erase its own state.
- **Upgrading** — nothing to do. The first conductor opened under v1.4.0 starts in a new, empty workspace; the throwaway
  directories earlier versions left in `$XDG_RUNTIME_DIR/baton/` or `$HOME/.baton/` are removed when the daemon next
  starts. Note the workspace's base is read from the environment the daemon was started in, so a daemon started from an
  ssh session and one started from a desktop terminal can resolve different workspaces.

## [v1.3.1](https://github.com/cmj0121/baton/releases/tag/v1.3.1) — one fleet per machine

2026-08-21

- **One backend per user** — the control socket was named after the caller's login session, so opening baton in a
  second terminal started a second daemon instead of attaching to the fleet you already had. It is now one fixed path
  (`$XDG_RUNTIME_DIR/baton/baton.sock`, or `$HOME/.baton/baton.sock`), so the first launch starts the daemon and every
  launch after it attaches another cockpit to the backend already running.
- **The guards finally mean what they said** — the session lock, the stale-socket sweep and the liveness probe are all
  keyed on the socket path, so they had only ever enforced one backend per terminal. A race between two cold starts
  against a socket a crash left behind is still settled by the advisory lock rather than by whoever binds first.
- **The remote bridge stops guessing** — `baton --stdio` used to scan the runtime dir for the newest socket that
  answered, because sshd runs it in a session of its own. With one fixed path there is nothing to search.
- **Upgrading** — a daemon started by an older baton keeps running on its `baton-<sid>.sock` and is not found by this
  one; stop it before starting the new fleet. `BATON_SOCK` still overrides the path for a deliberately separate fleet.

## [v1.3.0](https://github.com/cmj0121/baton/releases/tag/v1.3.0) — how much is left

2026-08-20

- **The account's quota, as bars** — `v u` gains a fourth view: the 5-hour and weekly rate-limit windows with a
  countdown to each reset. The footer could say what you had spent; it can now say whether the next turn will be
  refused, which is the number a fleet is actually run against.
- **`v U` opens it in full** — every window the source reported, the per-model weekly ceilings, the extra-usage credit
  balance, and the panels spending them. The last column is a panel's share of the window against how much of the
  five-hour quota is gone: how much of your real ceiling one agent has eaten.
- **The reading costs nothing** — Claude Code hands its session state to whatever runs as its status line, so Baton
  launches its panels **wrapping** the status line you already had. No network call, no credential, no token spent, and
  a panel inside Baton renders exactly what it would outside one. `usage.limits: oauth` opts into the account endpoint
  instead — the only source for the credit balance, and the only one that reads a credential.
- **Absent stays absent** — a window no source reported gets no row, a countdown past its reset goes away rather than
  resting at `0:00:00`, and a reading nobody has restated in five minutes is marked rather than dropped or trusted. A
  bar at 0% would assert a full tank on an account minutes from a refusal.
- **The countdown settles into two shapes** — `2:12:23` under a day, `2d8h` past it. `usage.countdown-format` is gone;
  an old config that still carries it is ignored, not an error.

## [v1.2.1](https://github.com/cmj0121/baton/releases/tag/v1.2.1) — press it to find it

2026-08-19

- **Four landing keys** — `n` spawns, `v` draws the cockpit, `g` takes the work items, and `x x` purges the dead. Eleven
  letters come back, and the footer names what the run can still take while it waits, so a family is discovered by
  pressing it.
- **A binding is a run of keys** — `e` in the key map collects the run and `enter` binds it; the `C-t` leader now lapses
  after `settings.key-timeout` (1.2s, `0` restores the old forever) instead of waiting all session.
- **The key list is tabbed by purpose** — Navigation, Panels, Work items, View, Session — with every family shown under
  its landing, and ←/→ or tab to walk them.
- **The overlays share one alphabet** — j/k and the arrows move, g/G jump to the ends, x removes the row, X clears the
  lot, r refreshes, q or esc closes. Draining the queue asks first.
- **The language is the terminal's business** again, and a zoom reaches every escape it always documented.
- **`docs/KEYS.md` is new** and is the single source of truth for keys, in English and 繁體中文.

## [v1.2.0](https://github.com/cmj0121/baton/releases/tag/v1.2.0) — anywhere, any size

2026-08-19

- **Remote access over SSH** — `baton --remote user@host` attaches the cockpit to a fleet on another machine, gated by a
  passkey with a failed-attempt limiter; `C-t @` shows the address and who is connected.
- **The dashboard draws two layouts** — a grid of cards for a small fleet, the full-width tree above it, and `V` to
  switch by hand.
- **`space` opens and shuts a work item** at any depth; grab-and-move is `m` now, and rebindable like everything else.
- **The heading counts the fleet**, not the rows on screen, and the quiet fold can no longer flip the layout under you.

## [v1.1.0](https://github.com/cmj0121/baton/releases/tag/v1.1.0) — know where to look

2026-08-19

- **Attention at scale** — a quiet ladder (`done`, `stuck`, failed), the `C-t a` inbox, dashboard folds, notifications.
- **The dashboard is a tree** — work items, groups and panels in one full-width tree; grab a row and carry it, or
  re-lens the whole fleet with `z`.
- **Agent backends** — baton detects which agent CLIs the machine actually has, and `A` offers only the ones you can run.
- **Opt-in per profile** — run an agent in a container, pipe a panel's output to a file, or bring a failed panel back.
- **A remembered working directory**, and a usage footer that reads the real billing window, per panel.

## [v1.0.0](https://github.com/cmj0121/baton/releases/tag/v1.0.0) — one line to install

2026-08-15

- **Homebrew** — `brew install cmj0121/tap/baton` lands a prebuilt binary on macOS.
- **Prebuilt binaries** — every tag ships darwin and linux tarballs for amd64 and arm64, with checksums.
- **Seven languages** — the README reads in English, 繁體中文, 日本語, 한국어, Français, Deutsch and Español.
- **Releases cut from the tag** — the annotated tag's message becomes the release note, mirrored to the tap.

## [v0.7.0](https://github.com/cmj0121/baton/releases/tag/v0.7.0) — cap what it can take

2026-08-14

- **Resource limits** — cap a panel's cpu, memory and pids, held against its whole process tree.
- **The global shell** — `H` opens one plain host shell the server keeps in `$HOME`.
- **What the process tree costs** — per-process CPU% and RSS in the tree view.
- **繁體中文** — the key list and key map read in English or Traditional Chinese.

## [v0.6.0](https://github.com/cmj0121/baton/releases/tag/v0.6.0) — see what it's running

2026-07-16

- **The process tree** — see every process a panel spawned, not just the one baton started.
- **nvim (and friends) no longer wedge** — full-screen programs behave on re-attach.
- **A cleaner re-attach** and a scroll mode that keeps the leader live.

## [v0.5.0](https://github.com/cmj0121/baton/releases/tag/v0.5.0) — grep the fleet

2026-07-13

- **Fleet-wide search** — `/` greps every panel's output at once and groups the hits.
- **Docs in Traditional Chinese** — the first locale of the doc set.
- Cleaner text fields and a quality pass under the hood.

## [v0.4.1](https://github.com/cmj0121/baton/releases/tag/v0.4.1) — green CI

2026-07-09

- Coverage brought over the per-package floor, and the server timing tests de-flaked.

## [v0.4.0](https://github.com/cmj0121/baton/releases/tag/v0.4.0) — steadier under load

2026-07-09

- The diff popup can no longer OOM the daemon; the MCP server survives a malformed frame.
- A fleet that stays bounded, a tighter exit path, and bounded usage polling.

## [v0.3.0](https://github.com/cmj0121/baton/releases/tag/v0.3.0) — tasks, and a cockpit you can shape

2026-07-06

- **Tasks and the queue** — dispatch a brief, track it through its lifecycle, drain a backlog.
- **Nested work items**, a cockpit you can shape, scratch pane and tile resize, favourites.

## [v0.2.0](https://github.com/cmj0121/baton/releases/tag/v0.2.0) — conductor mode

2026-06-25

- **The conductor** — an agent that drives the fleet over the socket, fenced by role.
- Two control surfaces over the socket: `baton ctl` and the MCP tools.

## [v0.1.0](https://github.com/cmj0121/baton/releases/tag/v0.1.0) — the agent-friendly terminal multiplexer

2026-06-22

- **Headless core, swappable frontend** — a daemon owns every terminal; frontends attach over a socket.
- Agents and shells as panels, three views under one key map, work items, and hot reload.
