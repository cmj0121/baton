# Baton — Panel logging

**English** · [繁體中文](LOGGING.zh-TW.md)

When an agent finishes a long run, the thing you actually want is its **full transcript** — to grep, to paste into an
issue, to hand to another agent, to keep for an audit. Baton keeps only an in-memory replay buffer
(`panel.replay-kb`), and that buffer is bounded and dies with the panel.

Logging pipes a panel's output to a file instead. Two keys:

| Key     | Does                                            |
| ------- | ----------------------------------------------- |
| `C-t l` | start / stop logging the selected panel         |
| `C-t L` | open its log in a temporary panel, following it |

Logging is **off by default**, and stays off until you name a directory for it. A feature that writes your terminal to
disk is one you opt into.

```yaml
# $HOME/.baton/config
panel:
  log-dir: ~/.baton/logs # where logs land; empty (the default) disables logging entirely
  log-max-mb: 64 # roll over at this size, keeping one previous generation

  agents:
    claude:
      command: claude
      log: true # this profile logs from the moment it spawns
      log-dir: ~/work/transcripts # optional per-profile override
```

## Where the file lands

On **the machine the fleet runs on**. The daemon owns the PTY, so it is the daemon that writes the file — which over
`--remote` is not the machine your cockpit is on. That is also why `C-t L` opens a _panel_ rather than a viewer: the
cockpit never touches the file itself, so reading a log back works identically local and remote.

The name is `<log-dir>/<yyyy-mm-dd>-<panel-title>-<id>.log`, slugified. The date leads so a directory sorts
chronologically; the id trails so two panels with the same title on the same day never collide.

## What gets written

**Plain text, escape sequences stripped** — the same shape `fleet.search` (`/`) already returns. The file is meant to be
grepped, pasted and re-read by another agent, and none of those want a screenful of `\x1b[2K`.

The cost is honest, and worth knowing before you go looking for it: **an agent that redraws leaves its drafts behind**.
A progress bar, a spinner, a cleared screen — each intermediate state was a real line of output, and a carriage return
that rewrote a line in place becomes a new line in the file rather than vanishing. So a run with a spinner produces a
file with the spinner's every frame in it. A raw-bytes mode may follow later; this is not it.

Colour, cursor moves and window-title sequences are dropped. Tabs and newlines survive.

## What it starts with

Switching logging on **flushes the panel's existing replay buffer into the file first**, then streams from there. You
reach for logging _because_ something interesting just happened, so a log that started at the keypress would miss the
thing that made you press it.

What that prefix cannot honestly claim is _when_ any of it happened — the replay ring is bytes, not events — so it says
so rather than pretending otherwise:

```txt
=== baton log · claude · ~/work/api ===
=== logging started · 2026-08-18T15:04:05+08:00 ===
--- replay buffer: output from before logging started; its timestamps are not known ---
  ✻ Thinking…
  ● Read src/server.go
=== live output follows · 2026-08-18T15:04:05+08:00 ===
```

## Sessions and re-runs

A re-run (`r`) **appends under a new session marker** rather than truncating: the previous run is usually why you are
reading the file.

```txt
=== process exited (code 1) · 2026-08-18T15:31:02+08:00 ===
=== session restarted · 2026-08-18T15:31:40+08:00 ===
```

The file is closed and flushed when the panel's process exits, when logging is switched off, when the panel is closed or
purged, and when the daemon shuts down — each with a marker saying which of those it was. A log never simply stops
mid-line with nothing to explain it.

Switching logging on again later appends too, so one panel's file holds every run you asked to record.

A re-run keeps the logging state it had: a panel that was being logged comes back logging, one that was not stays
unlogged — including a panel whose profile auto-logs but whose logging you switched off by hand.

## Auto-logging a profile

`panel.agents.<name>.log: true` makes that profile log from the moment it spawns. It is per-profile rather than
fleet-wide because "record every agent, never my shells" is the case that actually comes up — and a global switch would
quietly write everything typed into a shell to disk.

If the destination cannot be written when such a panel spawns, **the panel still spawns**, unlogged, and the reason is
reported. The agent is the point and the log is the accessory; a typo'd path should cost you the transcript, not the run.

## Disk

`log-max-mb` (default 64) rolls the file to `<file>.log.1` and starts a new one, keeping the **two most recent
generations** and no more. A runaway build can produce gigabytes in minutes, and baton already argues in
**[LIMITS.md](LIMITS.md)** that nothing should be able to take the machine with it — a log is not an exception.

There is no retention sweeper. Old logs are yours to keep or delete.

## Reading it back

`C-t L` opens the log in a **temporary panel** — the same ephemeral mechanism the git menu uses. It closes on exit and
on the way back to the dashboard, and never becomes a card in the fleet.

It **follows** the file (`less +F`, falling back to `tail -f` on a host with no `less`), because the panel it belongs to
is usually still running. In `less`, `C-c` stops following and leaves you paging through what came before; `q` closes
the panel.

## Telling you it is on

A logging panel is **badged on its dashboard card** — a `◉` before the state LED — and the footer names the file while
that panel is focused:

```txt
◉ LOG …/logs/2026-08-18-claude-3.log
```

A feature that silently writes your terminal to disk has to be visible while it does it. The state is fleet state, not
cockpit state: it lives on the daemon, so it survives a detach and reattach and is the same in every cockpit attached to
that fleet. It is deliberately **not** persisted across a daemon restart — a forgotten logging flag quietly resuming is
the worse failure.

## What this is, and is not

It is a **transcript**: what the panel printed, in plain text, on the daemon's disk.

It is **not an audit boundary**. The file is written by a daemon running as you, into a directory you own, and anything
that can reach that uid can edit or delete it — including the agent in the panel being logged. See
**[SECURITY.md](../SECURITY.md)**.

It is **not a recording format**. There is no asciinema/raw mode, no per-line timestamps, no JSON. It is text.

It is **not reachable by the conductor**. `panel.log` and `panel.logview` are refused for a conductor-role connection,
on the same terms as the inbox verbs: asking the daemon to write files, on the daemon's host, as you, is an operator
action. See **[CONTROL.md](CONTROL.md)**.

## Related keys

| Key     | Does                                                                   |
| ------- | ---------------------------------------------------------------------- |
| `C-t l` | start / stop logging the selected panel                                |
| `C-t L` | open that log in a temporary panel, following it                       |
| `C-t R` | reload the config; a new `log-dir` applies to logs opened from then on |
| `/`     | grep every panel's live replay buffer — the in-memory sibling of a log |
