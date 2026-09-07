# Baton — Serial ports

**English** · [繁體中文](SERIAL.zh-TW.md)

`baton serial` opens a serial port and copies bytes between it and the panel it is running in. It is what you reach for
instead of `screen /dev/cu.usbmodem2101 115200`, and it is not a wrapper around screen: baton ships as one static binary
with no cgo, and the bridge is part of it.

```sh
baton serial /dev/cu.usbmodem2101 115200
```

## Opening one

A serial panel is an ordinary command panel. There is no new panel kind and nothing new to configure.

| From        | Do this                                                                            |
| ----------- | ---------------------------------------------------------------------------------- |
| the cockpit | `n c`, then type `baton serial /dev/cu.usbmodem2101 115200`                        |
| a shell     | `baton serial /dev/cu.usbmodem2101 115200`                                         |
| `baton ctl` | `baton ctl spawn --run baton --arg serial --arg /dev/cu.usbmodem2101 --arg 115200` |

Because it is a command panel and baton cannot tell it from any other, everything already built works on it unchanged:
it has a pid, it exits with a code, `C-t l` logs it, the restart policy applies to it, and `C-t w` closes it.

## The line

```sh
baton serial <device> [baud] [--data-bits N] [--parity P] [--stop-bits N] [--flow F]
```

| Flag                | Default  | Takes                                                   |
| ------------------- | -------- | ------------------------------------------------------- |
| `<device>`          | —        | required, e.g. `/dev/cu.usbmodem2101` or `/dev/ttyUSB0` |
| `[baud]`            | `115200` | see the table below                                     |
| `-d`, `--data-bits` | `8`      | `5`, `6`, `7`, `8`                                      |
| `-p`, `--parity`    | `none`   | `none`, `even`, `odd`                                   |
| `-s`, `--stop-bits` | `1`      | `1`, `2`                                                |
| `-f`, `--flow`      | `none`   | `none`, `rtscts`, `xonxoff`                             |

The defaults are screen's — 8N1 and no flow control — so `baton serial /dev/… 115200` puts the same line on the wire as
`screen /dev/… 115200` for anyone who has typed the second for years.

## Which device name

On macOS the same adapter appears twice, as `/dev/tty.usbmodem2101` and `/dev/cu.usbmodem2101`. Use the `cu.` one. The
`tty.` name is the dial-in device, and `open(2)` on it waits for the adapter to raise carrier detect — on an adapter that
never raises it, that wait never ends. Measured on a real ESP32-S3: the `tty.` name was still blocked in `open` after
four seconds, the `cu.` name returned in fourteen milliseconds.

`baton serial` opens either name without hanging, because it passes `O_NONBLOCK`. But `cu.` is the name for talking to
something, and it is the one to type. On Linux there is only one name — `/dev/ttyUSB0`, `/dev/ttyACM0` — and the
question does not arise.

## Baud rates

The rates baton can set are the ones the platform's `termios` has a constant for, and the two platforms do not agree:

| Platform | Rates                                                                                                                                  |
| -------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| macOS    | 50, 75, 110, 134, 150, 200, 300, 600, 1200, 1800, 2400, 4800, 7200, 9600, 14400, 19200, 28800, 38400, 57600, 76800, 115200, 230400     |
| Linux    | the same up to 230400 without 7200, 14400, 28800 and 76800, plus 460800, 500000, 576000, 921600, 1000000, 1152000, 1500000 and 2000000 |

Anything else is refused by name, with the list, before the port is opened:

```text
baton serial: baud rate 12345 is not one this platform can set; supported: 50, 75, 110, …
```

That refusal is deliberate. A `termios` handed a speed it does not know does not fail — it runs at some other speed, and
a wrong baud on a working cable produces a stream of garbage that looks exactly like a bad cable. An hour of somebody's
afternoon is worth more than the convenience of a rate that would not have worked anyway. Rates above the table need
`IOSSIOSPEED` on macOS and `BOTHER`/`termios2` on Linux, and baton makes neither call.

## A port that will not take a setting

Setting the line can succeed and still not happen. `TIOCSETA` returns success for RTS/CTS on a port that has no RTS and
no CTS lines, and the very next read of the same `termios` shows the bit clear — measured on an ESP32-S3's
USB-serial-JTAG port, where it is not a bug but the honest answer to a request the hardware cannot honour.

So baton reads the line back and refuses anything the driver dropped:

```text
baton serial: /dev/cu.usbmodem2101 does not support rtscts flow control: the driver accepted the setting and dropped it
```

Drop the flag, or use the port that has the lines. Silently running without the flow control you asked for is the
failure mode this exists to prevent.

## Unplug

This is the reason `baton serial` exists rather than a `--run screen`. A USB adapter disappears when somebody knocks the
cable; screen exits, and the panel's scrollback goes with it. baton keeps a ring, a log and a tail per panel, so the
bridge outlives the device instead:

```text
baton serial: /dev/cu.usbmodem2101 open at 115200 8N1 — every byte passes through, close the panel to leave
[the board's output]
baton serial: /dev/cu.usbmodem2101: the port is gone — waiting for it to come back
baton serial: /dev/cu.usbmodem2101 open at 115200 8N1 — every byte passes through, close the panel to leave
[the board's output again]
```

It retries once a second, forever, with the same settings — a reconnect that quietly fell back to a default line would
be worse than not reconnecting. Everything above the break is still in the panel, still in the log, still greppable.

The same loop means you can start the panel before the board is plugged in. It says why it cannot open the port, once
per outage rather than once per attempt, and opens it when it appears.

Keystrokes typed while the port is down are dropped, not queued. Replaying a minute of keystrokes into a board the
instant it enumerates is the worse answer, and the panel has already said the port is down.

## Getting out, and Ctrl-C

There is no escape key. `screen` needs `Ctrl-a k` because screen owns the terminal; this does not — baton does. So every
byte passes through raw, `Ctrl-C` included, which is what you want when you are talking to a bootloader or a busybox
prompt. Close the panel the way you close any other (`C-t w`), and that is the exit.

This is the one real improvement over `screen /dev/… 115200`, and it is worth saying out loud: you no longer hold two
escape vocabularies at once, and `Ctrl-a` goes back to meaning start-of-line.

## What it does not do

Bytes in, bytes out. No local echo, no expansion, no interpretation:

- A device that sends a bare `\n` with no `\r` will staircase down the screen. That is what `screen` and `picocom` do
  too, with their default output mapping, and it is the device telling you something about its firmware.
- Nothing is echoed locally. If you cannot see what you type, the far end is not echoing it, which is a fact about the
  far end and not about baton.
- The port's own bytes are never filtered. They are escape sequences as often as not, and passing them through is the
  job. Only baton's own notice lines are scrubbed, because the device name in them came from whoever typed the command.

## `baton ctl dispatch` reaches the device

A serial panel is a command panel, so it is not an agent, and the fleet offers it no work. But the daemon has no
kind-gate on `panel.dispatch`, and baton cannot tell this panel from any other — so a deliberate

```sh
baton ctl dispatch <panel-id> "…"
```

writes those bytes to the port, and out of the cable, and into whatever is on the other end.

There is no gate here because a gate would be a lie about what baton can enforce: `--run` accepts any binary, and a
panel running a program that talks to hardware is indistinguishable from one running a shell. Treat a serial panel's id
the way you would treat the device: give it to the agents that should be driving the board, and to no others.

## See also

- [docs/LOGGING.md](LOGGING.md) — piping a panel's output to a file, which is what makes the unplug survivable.
- [docs/CONTROL.md](CONTROL.md) — `baton ctl`, including the `spawn --run` this rides on and the `dispatch` above.
- [docs/RESTART.md](RESTART.md) — the restart policy, which applies to a serial panel like any other command panel.
