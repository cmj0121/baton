package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kong"

	"github.com/cmj0121/baton/internal/scrub"
	"github.com/cmj0121/baton/internal/serial"
)

// `baton serial` opens a serial port and copies bytes between it and the panel
// it is running in, which is how baton talks to a board without shelling out to
// screen, picocom or minicom.
//
// It is a plain subcommand and not a fourth panel kind, and that is the whole
// design. A serial port has no process, and the panel machinery is process-
// shaped throughout — StartCmd runs an exec.Cmd, OnClose carries an exit code,
// Pids/Signal/KillAll/Resize are all pid- or pty-shaped. Giving the port a
// process of its own costs one process per port, which is exactly what screen
// costs today, and buys every existing mechanism unchanged: the panel has a pid,
// exits with a code, can be signalled, logged, restarted and closed by the same
// keys as any other.
//
// Usage:
//
//	baton serial <device> [baud] [--data-bits N] [--parity P] [--stop-bits N] [--flow F]
//
// Opened from the cockpit that is `n c`, then the command line above.

// serialCLI is the `baton serial` command line. The defaults are screen's, so
// `baton serial /dev/cu.usbmodem2101` and `screen /dev/cu.usbmodem2101 115200`
// put the same line on the wire.
type serialCLI struct {
	Device   string `arg:"" help:"The serial device, e.g. /dev/cu.usbmodem2101 or /dev/ttyUSB0."`
	Baud     int    `arg:"" optional:"" default:"115200" help:"Baud rate (default 115200)."`
	DataBits int    `name:"data-bits" short:"d" default:"8" help:"Data bits: 5, 6, 7 or 8."`
	Parity   string `name:"parity" short:"p" default:"none" help:"Parity: none, even or odd."`
	StopBits int    `name:"stop-bits" short:"s" default:"1" help:"Stop bits: 1 or 2."`
	Flow     string `name:"flow" short:"f" default:"none" help:"Flow control: none, rtscts or xonxoff."`
}

// serialMain runs `baton serial <device> [baud]`. It returns a process exit code.
func serialMain(args []string) int {
	var cli serialCLI
	parser, err := kong.New(&cli,
		kong.Name("baton serial"),
		kong.Description("Bridge this panel to a serial port."),
		kong.UsageOnError(),
	)
	if err != nil {
		return serialFail(err)
	}
	if _, err := parser.Parse(args); err != nil {
		return serialFail(err)
	}

	cfg := serial.Config{
		Device:   cli.Device,
		Baud:     cli.Baud,
		DataBits: cli.DataBits,
		Parity:   serial.Parity(cli.Parity),
		StopBits: cli.StopBits,
		Flow:     serial.Flow(cli.Flow),
	}
	if err := cfg.Validate(); err != nil {
		return serialFail(err)
	}

	// Raw, so every byte reaches the board — Ctrl-C included, which is what you
	// want at a bootloader and is safe here because this process does not own the
	// terminal: baton does, and closing the panel is the way out. A stdin that is
	// not a terminal (a piped script) has no termios to set and is not an error.
	if restore, err := serial.MakeRaw(os.Stdin); err == nil {
		defer restore()
	}

	// SIGINT is here for a `baton ctl signal` that sends one, not for the keyboard:
	// raw mode has ISIG off, so Ctrl-C is a byte on the wire and never a signal.
	// Catching the three restores the terminal on the way out instead of leaving
	// the panel's line in raw mode for whatever runs next.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	b := &serial.Bridge{Cfg: cfg, In: os.Stdin, Out: os.Stdout}
	if err := b.Run(ctx); err != nil {
		return serialFail(err)
	}
	return 0
}

// serialFail prints one error to stderr and returns the exit code.
//
// It scrubs for the reason ctlFail does: this is a terminal with nothing in
// front of it, and the error quotes the device name, which the caller chose.
func serialFail(err error) int {
	fmt.Fprintln(os.Stderr, "baton serial:", scrub.Text(err.Error()))
	return 2
}
