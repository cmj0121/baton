# Changelog

Every release is cut from an annotated tag whose message _is_ the release note, so the
[GitHub releases](https://github.com/cmj0121/baton/releases) always carry the full story —
the upgrade notes, the caveats, and why each change exists. This file is the index.

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
