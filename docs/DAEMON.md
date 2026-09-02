# Baton — The daemon, and when it does not come up

**English** · [繁體中文](DAEMON.zh-TW.md)

> Everything baton does happens in one background process. The cockpit is a client of it, `baton ctl` is a client of
> it, and `baton mcp` is a client of it. This page is that process's boot: the order it comes up in, what it says while
> it does, and what to do on the morning it does not.

One daemon per control socket, and one socket per user, so every cockpit you open — whichever terminal it is launched
from — attaches to the same fleet. `BATON_SOCK` is how you run a second one.

## What comes up, in what order

Running `baton` with no daemon running re-executes the binary as the daemon and then waits **five seconds** for a
socket to answer. In that window the daemon does this, in this order:

| Step                                        | If it hangs there                               |
| ------------------------------------------- | ----------------------------------------------- |
| 1. claim the session, with an advisory lock | —                                               |
| 2. write the PID file                       | —                                               |
| 3. read `$HOME/.baton/config`               | no socket is ever created                       |
| 4. sweep old conductor workspaces           | no socket is ever created                       |
| 5. open the fleet memory from `score.dir`   | bounded at 10 s, then the fleet runs without it |
| 6. bind the socket, secure it, and serve    | —                                               |

Steps 3 to 5 read your filesystem, and any of them can wait forever on a mount that has stopped answering. **They
happen above the bind**, and that is the point: a `$HOME` or a `score.dir` that has gone away leaves no socket at all,
rather than a socket that accepts a connection and then never answers it. A client gets a connection error in
milliseconds instead of hanging on an accepted connection with nothing in the log to explain the silence.

It is not yet every read. The second config pass, the cockpit theme (`TUI.yaml`) and the Lua plugin are still read
after the socket exists, because each of them configures the server that does not exist until then.

Two of those steps say so **before** they run, at `INF`, in `$HOME/.baton/baton.log`:

```txt
INF boot: reading the config path=/home/you/.baton/config
INF boot: opening the fleet memory dir=/home/you/.baton within=10s
```

They are there for one reason: they are what the log's **last line** is when a boot wedges, so the file names the thing
the daemon stopped on rather than being empty.

## Readiness probes

A supervisor that waits on the socket — a systemd unit, a launchd job, a container healthcheck — now gets the answer it
was asking for. The socket exists once the daemon is ready to serve.

It used to pass early. Measured against a 456 MB fleet memory, a probe on socket existence passed at **90 ms**
while the cockpit behind it hung for about **6.6 s**. That is a correction — and it is still a change to plan for on
upgrade. A restart loop tuned against the old false pass will now see a genuine failure where it used to see success,
and restarting a daemon in the middle of a slow open means the open never finishes: on a large store, that is also the
boot that would have compacted it and made every later one fast. Give the probe more room than one boot needs.

## "baton server did not come up"

```txt
baton server did not come up; see /home/you/.baton/baton.log — its last line names what the daemon
was reading. If it is still wedged there, `baton --force` stops it and starts again
```

`baton` waited its five seconds and no socket appeared. It is one of two things, and the log's last `boot:` line is
what tells them apart.

- **A boot that was merely slow**, which is the common one. The daemon comes up behind the message and serves
  normally; run `baton` again and it attaches. A large fleet memory is what usually does this — see
  **[SCORE.md](SCORE.md#the-first-boot-after-upgrading-a-large-store)**.
- **A boot wedged in one of the reads above.** This one does not clear by itself: the wedged daemon is still holding
  the session claim, so every later `baton` loses the claim to it and exits without a word.

`baton --force` is the way out of the second. It stops whatever daemon holds this session — including one that never
got as far as a socket — waits for it to go, tidies what it left, and starts a fresh one.

It decides by **the session claim, not the PID file**. A PID file outlives the process it names, and the operating
system reuses pids, so signalling one on the strength of the file alone can deliver a `SIGTERM` to an unrelated
program. The claim is a kernel lock, dropped the instant its holder dies — a `SIGKILL` included — so it answers the
only question worth asking: is a daemon for this socket alive right now. When the answer is no, the stale files are
tidied and nothing is signalled.

What `--force` cannot do is fix the thing the daemon was reading. If the wedge is a dead mount, the fresh daemon will
reach the same read and stop in the same place. Restore or unmount the path first. Where the log's last line names the
fleet memory, `score.dir` can also be pointed somewhere local — and the daemon gives that open ten seconds before it
serves the fleet without a memory rather than not serving it at all.

## The files a daemon owns

The socket lives in your runtime directory, and the four files beside it are named from it — so a second fleet under
`BATON_SOCK` gets a set of its own. The last two are per-user rather than per-socket, and two fleets under one `$HOME`
share them.

| File                     | Holds                                                                         |
| ------------------------ | ----------------------------------------------------------------------------- |
| `baton.sock`             | the control socket, clamped to owner-only                                     |
| `baton.lock`             | the session claim — what makes exactly one daemon per socket true             |
| `baton.pid`              | the running daemon's pid, written above the bind so a wedged one is stoppable |
| `baton.state.json`       | the persisted fleet and layout                                                |
| `baton.queue/`           | the task backlog, one file per task                                           |
| `$HOME/.baton/config`    | the config step 3 reads                                                       |
| `$HOME/.baton/baton.log` | the daemon's own log — where every message on this page is written            |

## Related

- **[SCORE.md](SCORE.md)** — the fleet memory opened at step 5: what makes it slow, and what bounds it.
- **[CONTROL.md](CONTROL.md)** — the clients on the other side of the socket: `baton ctl`, `baton mcp`, the conductor.
- **[RESTART.md](RESTART.md)** — a different restart: the policy for a **panel** whose process exits.
