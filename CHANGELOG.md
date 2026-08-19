# Changelog

Every release is cut from an annotated tag whose message _is_ the release note, so the
[GitHub releases](https://github.com/cmj0121/baton/releases) always carry the full story —
the upgrade notes, the caveats, and why each change exists. This file is the index.

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
