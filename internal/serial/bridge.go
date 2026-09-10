package serial

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/cmj0121/baton/internal/scrub"
)

// DefaultRetry is how long the bridge waits between attempts to re-open a device
// that is not there. It is short enough that pushing the cable back in feels
// immediate and long enough that a port which is never coming back costs one
// open(2) a second rather than a spin.
const DefaultRetry = time.Second

// Bridge copies bytes between a terminal and a serial port, and keeps doing it
// across an unplug.
//
// The loop is the whole feature. `screen /dev/… 115200` exits when the adapter
// disappears and takes the scrollback with it; this sits there, says the port is
// gone, and re-opens it with the same settings when it comes back — so the
// panel's ring, log and tail span the unplug instead of ending at it.
//
// While the port is gone, bytes typed at the panel are DROPPED rather than
// buffered. There is nothing to deliver them to, and a buffer that replayed a
// minute of keystrokes into a board the instant it enumerated would be a worse
// answer than losing them: the operator can see the port is down, because the
// bridge said so.
type Bridge struct {
	Cfg Config

	// In and Out are the terminal side: the panel's stdin and stdout.
	In  io.Reader
	Out io.Writer

	// OpenPort opens the device. It is a field so tests can drive the loop
	// without hardware; nil means the real Open.
	OpenPort func(Config) (io.ReadWriteCloser, error)

	// Retry is the wait between re-open attempts. Zero means DefaultRetry.
	Retry time.Duration

	mu   sync.Mutex
	port io.ReadWriteCloser // the open port, nil while disconnected
}

// Run bridges the terminal and the port until ctx is cancelled or the terminal
// goes away, which for a panel means the panel was closed. The terminal goes
// away in either direction: its input ends, or it stops taking bytes.
//
// It returns nil for all of those: none is a failure, and a command panel that
// exited non-zero would be reported by baton as one that crashed.
func (b *Bridge) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The terminal ending is one of the two ways out, so the input pump cancels on
	// its way down rather than leaving the open loop waiting for a device nobody is
	// listening to any more.
	go func() {
		b.pumpIn()
		cancel()
	}()

	// Cancellation reaches a Read that is parked on the port only by closing the
	// port under it — see Open on why the descriptor stays non-blocking, which is
	// what makes that Close land as an error on the pending Read rather than
	// hanging behind it.
	go func() {
		<-ctx.Done()
		b.swap(nil)
	}()

	// The reason the last open failed, so a device that is not there costs one
	// pair of lines rather than one a second — and so a reason that CHANGES (the
	// node came back but baton cannot open it) is still said out loud.
	last := ""
	for ctx.Err() == nil {
		port, err := b.open()
		if err != nil {
			if msg := err.Error(); msg != last {
				// The error names the device itself — os.OpenFile's carries the path it failed
				// on, and so does every refusal Open raises — prefixing it would say it twice.
				b.say("%v", err)
				b.say("waiting for the port; close the panel to give up")
				last = msg
			}
			if !sleep(ctx, b.retry()) {
				break
			}
			continue
		}
		last = ""
		b.swap(port)
		// The cancel watcher above fires once, and it can fire while this iteration
		// is between open and swap — where there is no port for it to close, so it
		// closes nothing and never comes back. Without this re-check the port it
		// missed is then handed to a Copy that blocks forever, which is how the very
		// first run of this against /dev/cu.usbmodem2101 hung on stdin's EOF instead
		// of exiting. Closing twice is harmless; not closing at all is a wedged panel.
		if ctx.Err() != nil {
			b.swap(nil)
			break
		}
		b.say("%s open at %s — every byte passes through, close the panel to leave", b.Cfg.Device, b.Cfg.Line())

		terminalGone := b.copyOut(port)

		b.swap(nil)
		if ctx.Err() != nil || terminalGone {
			break
		}
		b.say("%s: the port is gone — waiting for it to come back", b.Cfg.Device)
		// The same wait as the failed-open path, and for the same reason. A device
		// that opens and then fails its first read — a cable half out of its socket,
		// a board re-enumerating, a node the kernel has not finished tearing down —
		// comes back here with no time spent, and this loop's only cost per turn is
		// an open(2) and two lines of prose. Measured without this: 67,930 opens in
		// 300ms, and 6.2MB of notices in 200ms, every byte of which a panel writes
		// to its ring and to its log file on disk. The pacing belongs on the loop,
		// not on one of its two exits.
		if !sleep(ctx, b.retry()) {
			break
		}
	}
	return nil
}

// copyOut copies the port's bytes to the terminal until one of the two ends,
// and reports which one it was.
//
// It is here rather than io.Copy because io.Copy folds the two directions into
// one error, and the difference is the whole diagnosis. A port that stopped is
// worth waiting for — that is what the loop around this is. A terminal that
// stopped is the panel going away, the same event as the input side's EOF, and
// re-opening the device after it means re-opening a device that was never gone,
// for as long as the process lives, while telling an operator who can no longer
// read the message to go and check the cable.
//
// The buffer is io.Copy's own size and is the only thing accumulated: nothing
// here is kept per line, per frame or per session, so a device that never sends
// a newline is a device that sends bytes, not one that fills memory.
func (b *Bridge) copyOut(port io.Reader) (terminalGone bool) {
	buf := make([]byte, 32*1024)
	for {
		n, rerr := port.Read(buf)
		if n > 0 {
			if _, werr := b.Out.Write(buf[:n]); werr != nil {
				return true
			}
		}
		if rerr != nil {
			return false
		}
	}
}

// pumpIn copies the terminal's input to whatever port is open, forever, or until
// the terminal's input ends.
func (b *Bridge) pumpIn() {
	buf := make([]byte, 4096)
	for {
		n, err := b.In.Read(buf)
		if n > 0 {
			b.send(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// send writes to the open port, and drops the bytes when there is none.
func (b *Bridge) send(p []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.port == nil {
		return
	}
	_, _ = b.port.Write(p)
}

// swap installs a port as the current one and closes whichever it replaced.
// Passing nil is how both the cancel path and the reconnect path let go.
func (b *Bridge) swap(port io.ReadWriteCloser) {
	b.mu.Lock()
	old := b.port
	b.port = port
	b.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// open opens the device, through the test seam when one is set.
func (b *Bridge) open() (io.ReadWriteCloser, error) {
	if b.OpenPort != nil {
		return b.OpenPort(b.Cfg)
	}
	return Open(b.Cfg)
}

// retry is the wait between re-open attempts.
func (b *Bridge) retry() time.Duration {
	if b.Retry > 0 {
		return b.Retry
	}
	return DefaultRetry
}

// say prints one of the bridge's own lines to the terminal.
//
// It ends in CRLF and not LF because the terminal is in raw mode, where OPOST is
// off and nothing turns a newline into a carriage return: a plain \n would step
// each line one column further right until the notices walked off the screen.
//
// It scrubs, because the device name is in most of these lines and the bridge
// did not choose it — an agent driving `ctl spawn --run baton serial …` picks
// that string, and it lands in a real terminal. The port's own bytes are not
// scrubbed and must not be: those ARE escape sequences, and passing them through
// is the whole job. Only the bridge's own prose goes through here.
func (b *Bridge) say(format string, args ...any) {
	_, _ = io.WriteString(b.Out, "baton serial: "+scrub.Text(fmt.Sprintf(format, args...))+"\r\n")
}

// sleep waits for d, reporting false if ctx was cancelled first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
