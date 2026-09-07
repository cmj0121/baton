package serial_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/serial"
)

// fakePort is a device the test drives: bytes handed to it arrive at the panel,
// bytes the panel sends land in sent, and unplug is a call to vanish.
//
// It stands in for a device that goes away. Real unplug could not be produced on
// the machine this was written on — the only serial device attached is an
// ESP32-S3 whose USB-serial-JTAG survives a chip reset without re-enumerating,
// so /dev/cu.usbmodem2101 never disappears without a hand on the cable.
type fakePort struct {
	mu     sync.Mutex
	sent   []byte
	closed bool

	in   chan []byte // bytes the device sends to the panel
	gone chan struct{}
	rest []byte
}

func newFakePort() *fakePort {
	return &fakePort{in: make(chan []byte, 8), gone: make(chan struct{})}
}

func (p *fakePort) Read(b []byte) (int, error) {
	if len(p.rest) > 0 {
		n := copy(b, p.rest)
		p.rest = p.rest[n:]
		return n, nil
	}
	select {
	case chunk := <-p.in:
		n := copy(b, chunk)
		p.rest = chunk[n:]
		return n, nil
	case <-p.gone:
		return 0, errors.New("device not configured")
	}
}

func (p *fakePort) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, b...)
	return len(b), nil
}

func (p *fakePort) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.gone)
	}
	return nil
}

// vanish is the unplug: the read in flight fails, the way it does when the
// adapter's node goes away under a blocked read.
func (p *fakePort) vanish() { _ = p.Close() }

func (p *fakePort) received() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.sent)
}

func (p *fakePort) wasClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// panelOut is the panel's screen: written by the bridge, read by the test.
type panelOut struct {
	mu sync.Mutex
	b  strings.Builder
}

func (o *panelOut) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

func (o *panelOut) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

// panelIn is the panel's keyboard: the test types into it and closes it to end
// the session, the way closing a panel ends the real one.
type panelIn struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func newPanelIn() *panelIn {
	r, w := io.Pipe()
	return &panelIn{r: r, w: w}
}

func (i *panelIn) Read(p []byte) (int, error) { return i.r.Read(p) }
func (i *panelIn) typ(s string) {
	_, _ = i.w.Write([]byte(s))
}
func (i *panelIn) close() { _ = i.w.Close() }

// runBridge starts a bridge and returns a wait function that fails the test if
// Run has not returned by the time it is called plus a second.
func runBridge(t *testing.T, b *serial.Bridge, ctx context.Context) func() {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	return func() {
		t.Helper()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run = %v, want nil", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return")
		}
	}
}

// waitFor polls until cond holds, and fails with what the panel showed if it
// never does.
func waitFor(t *testing.T, out *panelOut, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; the panel showed:\n%s", what, out.String())
}

// The device's bytes reach the panel and the panel's bytes reach the device,
// unchanged and uninterpreted — including the bytes a terminal would otherwise
// eat. This is the whole job in one test.
func TestBridgeCopiesBothWaysByteForByte(t *testing.T) {
	port := newFakePort()
	out := &panelOut{}
	in := newPanelIn()
	b := &serial.Bridge{
		Cfg:      good(),
		In:       in,
		Out:      out,
		OpenPort: func(serial.Config) (io.ReadWriteCloser, error) { return port, nil },
	}
	wait := runBridge(t, b, context.Background())

	// \x03 is Ctrl-C and \x1b is ESC: at a bootloader both are payload, and a
	// bridge that swallowed either would be useless for the job it exists for.
	in.typ("reset\r\x03\x1b[A\xc0\x00")
	waitFor(t, out, "the panel's keystrokes to reach the device",
		func() bool { return port.received() == "reset\r\x03\x1b[A\xc0\x00" })

	port.in <- []byte("ESP-ROM:esp32s3\r\n\x1b[31m")
	waitFor(t, out, "the device's bytes to reach the panel",
		func() bool { return strings.Contains(out.String(), "ESP-ROM:esp32s3\r\n\x1b[31m") })

	in.close()
	wait()
}

// The unplug, which is the reason this is a loop and not an io.Copy: the device
// goes, the bridge says so, and when it comes back the SAME settings are used
// and bytes flow again. `screen` exits here and takes the scrollback with it.
func TestBridgeReopensThePortAfterItGoesAway(t *testing.T) {
	first, second := newFakePort(), newFakePort()
	var opens int
	var lines []serial.Config
	var mu sync.Mutex

	out := &panelOut{}
	in := newPanelIn()
	b := &serial.Bridge{
		Cfg:   good(),
		In:    in,
		Out:   out,
		Retry: time.Millisecond,
		OpenPort: func(c serial.Config) (io.ReadWriteCloser, error) {
			mu.Lock()
			defer mu.Unlock()
			opens++
			lines = append(lines, c)
			if opens == 1 {
				return first, nil
			}
			return second, nil
		},
	}
	wait := runBridge(t, b, context.Background())

	first.in <- []byte("before")
	waitFor(t, out, "the first port's bytes", func() bool { return strings.Contains(out.String(), "before") })

	first.vanish()
	waitFor(t, out, "the bridge to say the port is gone",
		func() bool { return strings.Contains(out.String(), "the port is gone") })

	second.in <- []byte("after")
	waitFor(t, out, "the reopened port's bytes", func() bool { return strings.Contains(out.String(), "after") })

	in.typ("x")
	waitFor(t, out, "keystrokes to reach the reopened port", func() bool { return second.received() == "x" })

	in.close()
	wait()

	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 2 {
		t.Fatalf("opened %d times, want 2", len(lines))
	}
	// "with the same settings" is the promise; a reconnect that quietly fell back
	// to a default line would be worse than not reconnecting at all.
	if lines[0] != lines[1] {
		t.Errorf("reopened with %+v, want the original %+v", lines[1], lines[0])
	}
}

// A device that is not there yet is waited for rather than given up on, so
// `baton serial` can be started before the board is plugged in.
func TestBridgeWaitsForADeviceThatIsNotThereYet(t *testing.T) {
	port := newFakePort()
	var tries int
	var mu sync.Mutex
	out := &panelOut{}
	in := newPanelIn()
	b := &serial.Bridge{
		Cfg:   good(),
		In:    in,
		Out:   out,
		Retry: time.Millisecond,
		OpenPort: func(serial.Config) (io.ReadWriteCloser, error) {
			mu.Lock()
			defer mu.Unlock()
			tries++
			if tries < 4 {
				return nil, errors.New("open /dev/cu.usbmodem2101: no such file or directory")
			}
			return port, nil
		},
	}
	wait := runBridge(t, b, context.Background())

	waitFor(t, out, "the bridge to report the missing device",
		func() bool { return strings.Contains(out.String(), "no such file or directory") })
	port.in <- []byte("hello")
	waitFor(t, out, "bytes once the device turns up", func() bool { return strings.Contains(out.String(), "hello") })

	// The reason is said once per outage, not once per attempt: three failed opens
	// at one second apart would otherwise fill the panel with the same line.
	if n := strings.Count(out.String(), "no such file or directory"); n != 1 {
		t.Errorf("the failure was reported %d times, want once per outage", n)
	}

	in.close()
	wait()
}

// Bytes typed while the port is down are dropped, not queued. Replaying a
// minute of keystrokes into a board the instant it enumerates is a worse answer
// than losing them, and the operator can see the port is down because the
// bridge said so.
func TestBridgeDropsWhatIsTypedWhileThePortIsDown(t *testing.T) {
	port := newFakePort()
	var ready bool
	var mu sync.Mutex
	out := &panelOut{}
	in := newPanelIn()
	b := &serial.Bridge{
		Cfg:   good(),
		In:    in,
		Out:   out,
		Retry: time.Millisecond,
		OpenPort: func(serial.Config) (io.ReadWriteCloser, error) {
			mu.Lock()
			defer mu.Unlock()
			if !ready {
				return nil, errors.New("no such file or directory")
			}
			return port, nil
		},
	}
	wait := runBridge(t, b, context.Background())

	waitFor(t, out, "the bridge to report the missing device",
		func() bool { return strings.Contains(out.String(), "no such file or directory") })
	in.typ("lost")
	// Give the pump every chance to deliver them somewhere before the port exists.
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	ready = true
	mu.Unlock()
	waitFor(t, out, "the port to open", func() bool { return strings.Contains(out.String(), "open at") })
	in.typ("kept")
	waitFor(t, out, "the live keystrokes", func() bool { return port.received() == "kept" })

	if got := port.received(); strings.Contains(got, "lost") {
		t.Errorf("the device got %q; keystrokes from while it was gone were replayed into it", got)
	}

	in.close()
	wait()
}

// Cancelling stops the bridge and closes the port, even while the read is parked
// waiting for a device that will never say anything.
func TestBridgeStopsOnCancelAndLetsThePortGo(t *testing.T) {
	port := newFakePort()
	out := &panelOut{}
	in := newPanelIn()
	defer in.close()
	b := &serial.Bridge{
		Cfg:      good(),
		In:       in,
		Out:      out,
		OpenPort: func(serial.Config) (io.ReadWriteCloser, error) { return port, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	wait := runBridge(t, b, ctx)
	waitFor(t, out, "the port to open", func() bool { return strings.Contains(out.String(), "open at") })

	cancel()
	wait()
	if !port.wasClosed() {
		t.Error("the port was left open after the bridge stopped")
	}
}

// The terminal ending is the other way out, and it must not depend on where in
// the open loop the bridge happens to be. The first hardware run of this hung
// exactly here: stdin was /dev/null, the cancel landed between the open and the
// swap, so nothing closed the port and the copy blocked for good.
func TestBridgeStopsWhenTheTerminalEndsDuringAnOpen(t *testing.T) {
	port := newFakePort()
	out := &panelOut{}
	b := &serial.Bridge{
		Cfg: good(),
		In:  strings.NewReader(""), // an EOF as immediate as /dev/null's
		Out: out,
		OpenPort: func(serial.Config) (io.ReadWriteCloser, error) {
			// Long enough that the terminal's EOF always lands mid-open, which is the
			// window the bug lived in.
			time.Sleep(30 * time.Millisecond)
			return port, nil
		},
	}
	runBridge(t, b, context.Background())()
	if !port.wasClosed() {
		t.Error("the port opened during the shutdown was never closed")
	}
}

// The bridge's own lines are scrubbed, because the device name in them is not
// baton's: an agent driving `ctl spawn --run baton serial …` picks that string
// and it lands in a real terminal. The port's own bytes are NOT scrubbed — those
// are escape sequences and passing them through is the job — so this checks the
// two are treated differently.
func TestBridgeScrubsItsOwnLinesButNotThePortsBytes(t *testing.T) {
	port := newFakePort()
	out := &panelOut{}
	in := newPanelIn()
	cfg := good()
	cfg.Device = "/dev/\x1b]0;pwned\x07tty"
	b := &serial.Bridge{
		Cfg:      cfg,
		In:       in,
		Out:      out,
		OpenPort: func(serial.Config) (io.ReadWriteCloser, error) { return port, nil },
	}
	wait := runBridge(t, b, context.Background())
	waitFor(t, out, "the open line", func() bool { return strings.Contains(out.String(), "open at") })

	port.in <- []byte("\x1b[2J")
	waitFor(t, out, "the device's escape sequence", func() bool { return strings.Contains(out.String(), "\x1b[2J") })

	if strings.Contains(out.String(), "\x1b]0;pwned\x07") {
		t.Errorf("the device name's escape sequence reached the terminal:\n%q", out.String())
	}
	in.close()
	wait()
}

// Every line the bridge prints ends CRLF. The terminal is in raw mode with OPOST
// off, so a bare LF steps each line one column further right until the notices
// walk off the screen.
func TestBridgeEndsItsLinesWithCRLF(t *testing.T) {
	port := newFakePort()
	out := &panelOut{}
	in := newPanelIn()
	b := &serial.Bridge{
		Cfg:      good(),
		In:       in,
		Out:      out,
		OpenPort: func(serial.Config) (io.ReadWriteCloser, error) { return port, nil },
	}
	wait := runBridge(t, b, context.Background())
	waitFor(t, out, "the open line", func() bool { return strings.Contains(out.String(), "open at") })
	in.close()
	wait()

	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\r\n"), "\r\n") {
		if strings.Contains(line, "\n") {
			t.Errorf("a bridge line ends in a bare LF: %q", line)
		}
	}
}
