---
name: drive
description: Guides an agent running outside baton in reaching a running baton session and driving its fleet for the operator. Use when asked to operate baton.
---

# Driving baton from outside

This skill is for an agent the operator runs in their own terminal, outside
any baton panel, and asks to work the fleet for them.

If `BATON_PANEL_ID` is set and `BATON_ROLE` is not `conductor`, you are a
worker inside baton: do not drive the fleet from here — keep to your task and
the worker's skills, `score` and `peers`.

If `BATON_ROLE` is `conductor`, you are the conductor: read `conduct`, not this.

## Connecting

baton is one daemon per user by default, holding the fleet, and a cockpit the
human watches it through. You reach the daemon over its control socket:

- The default socket is `$XDG_RUNTIME_DIR/baton/baton.sock`, or
  `~/.baton/baton.sock` when that variable is unset. `BATON_SOCK` points at a
  different daemon's socket instead.
- `baton ctl list` is the check. It prints the fleet as JSON; "is the server
  running?" means no daemon answers on that socket.
- Do not start one yourself: plain `baton` opens the cockpit, a full-screen
  terminal UI meant for the human. Ask them to start it.

Every `baton ctl` command connects, acts, and exits. Run them through your
shell; `baton ctl --help` and `baton ctl <verb> --help` print the exact forms.

## MCP, if you want tools

Nothing is wired for you. To get structured tools instead of shelling out, the
operator can register baton's MCP server once:

    claude mcp add baton -- baton mcp

That serves the full fleet table — the same one the conductor gets — over the
same socket. The `conduct` skill names every tool in it and when to use each.
Without MCP, `baton ctl` covers the same ground.

## Your role: unscoped

From outside, you connect with no role and no panel: the cockpit's own,
unscoped role. The conductor is fenced — it may not drain the queue, sweep
worktrees, switch peers, or act on its own panel. You are not: apart from the
fleet ceiling of 64, the conductor-only memory corrections and the rate cap on
`baton ctl score submit`, nothing in the server stops you, so the judgment is
yours.

- **Look first.** These only read: `baton ctl list`, `baton ctl tree`,
  `baton ctl queue list`, `baton ctl worktree list`, `baton ctl score list`,
  `baton ctl score status`. `baton ctl classifier status` reads too, but its
  probe re-arms a classifier the daemon had written off.
- **Ask before anything destructive**, naming what will be lost, and wait for
  the human's explicit go-ahead:
  - `baton ctl close` ends panels, and `baton ctl signal` can kill them.
  - `baton ctl send` types into a live agent's terminal.
  - `baton ctl queue drain` and `baton ctl queue cancel` discard queued work.
  - `baton ctl worktree sweep` deletes worktrees from disk.
  - `baton ctl conductor reset` wipes the conductor's workspace.
  - `baton ctl group peers` switches a group's peer channel.
  - `baton --force` force-stops the running server.
- Spawning, grouping and dispatching build on the human's fleet and machine.
  Do them when the task asked for them, and say what you made.

## Driving

Once connected, read `conduct` for how: the loop, dispatch versus send, the
backlog, groups, stopping, guardrails. Its "as the conductor" lines are not
yours — you have no workspace or brief, and the conductor's fence does not hold
you back. Ids are not stable across closes: list again before acting on one.

You have no panel of your own, so you have no hand to raise: when you need a
decision, ask the human in your own conversation. Correcting the fleet's
memory is the conductor panel's alone, and baton refuses it from you.
