//go:build linux || darwin

package serial

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// openPTY gives a pty and the name of its slave, which is the closest thing to a
// serial port that exists on a machine with nothing plugged in: a character
// device with a real termios that honours every setting in Config.
//
// It is a STAND-IN, not hardware. What the port code does against a real device
// was measured separately against an ESP32-S3 on /dev/cu.usbmodem2101, and the
// two do not agree about everything — see TestDiffLineNamesTheSettingTheDriverDropped.
func openPTY(t *testing.T) string {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	t.Cleanup(func() { _ = tty.Close(); _ = ptmx.Close() })
	return tty.Name()
}

// line is a config for the pty under test.
func line(dev string, baud, bits int, p Parity, stop int, f Flow) Config {
	return Config{Device: dev, Baud: baud, DataBits: bits, Parity: p, StopBits: stop, Flow: f}
}

// termiosOf reads the line settings back off an open port.
func termiosOf(t *testing.T, p any) *unix.Termios {
	t.Helper()
	f, ok := p.(*os.File)
	if !ok {
		t.Fatalf("Open returned %T, want *os.File", p)
	}
	raw, err := f.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var out *unix.Termios
	var inner error
	_ = raw.Control(func(fd uintptr) { out, inner = unix.IoctlGetTermios(int(fd), reqGetTermios) })
	if inner != nil {
		t.Fatalf("IoctlGetTermios: %v", inner)
	}
	return out
}

// Every setting the operator can name actually lands on the descriptor. Framing
// is the one thing nothing downstream can detect for itself: a port running 7E2
// while the code believes 8N1 delivers plausible-looking wrong bytes.
func TestOpenSetsEverySettingItWasGiven(t *testing.T) {
	dev := openPTY(t)
	for _, tc := range []struct {
		cfg    Config
		parenb bool
		parodd bool
		cstopb bool
		rtscts bool
		xon    bool
	}{
		{cfg: line(dev, 115200, 8, ParityNone, 1, FlowNone)},
		{cfg: line(dev, 9600, 7, ParityEven, 2, FlowNone), parenb: true, cstopb: true},
		{cfg: line(dev, 19200, 6, ParityOdd, 1, FlowRTSCTS), parenb: true, parodd: true, rtscts: true},
		{cfg: line(dev, 300, 5, ParityNone, 1, FlowXONXOFF), xon: true},
	} {
		t.Run(tc.cfg.Line(), func(t *testing.T) {
			p, err := Open(tc.cfg)
			if err != nil {
				t.Fatalf("Open(%s): %v", tc.cfg.Line(), err)
			}
			defer func() { _ = p.Close() }()
			got := termiosOf(t, p)

			// The expected CSIZE is built here rather than tabulated, because the
			// constants are a different width on each platform and a table would have
			// to name one of them.
			var csize unix.Termios
			switch tc.cfg.DataBits {
			case 5:
				csize.Cflag |= unix.CS5
			case 6:
				csize.Cflag |= unix.CS6
			case 7:
				csize.Cflag |= unix.CS7
			case 8:
				csize.Cflag |= unix.CS8
			}
			if got.Cflag&unix.CSIZE != csize.Cflag&unix.CSIZE {
				t.Errorf("CSIZE = %#x, want %#x for %d data bits",
					got.Cflag&unix.CSIZE, csize.Cflag&unix.CSIZE, tc.cfg.DataBits)
			}
			if (got.Cflag&unix.PARENB != 0) != tc.parenb || (got.Cflag&unix.PARODD != 0) != tc.parodd {
				t.Errorf("PARENB/PARODD = %v/%v, want %v/%v for parity %q",
					got.Cflag&unix.PARENB != 0, got.Cflag&unix.PARODD != 0, tc.parenb, tc.parodd, tc.cfg.Parity)
			}
			if (got.Cflag&unix.CSTOPB != 0) != tc.cstopb {
				t.Errorf("CSTOPB = %v, want %v for %d stop bits", got.Cflag&unix.CSTOPB != 0, tc.cstopb, tc.cfg.StopBits)
			}
			if (got.Cflag&unix.CRTSCTS != 0) != tc.rtscts {
				t.Errorf("CRTSCTS = %v, want %v for flow %q", got.Cflag&unix.CRTSCTS != 0, tc.rtscts, tc.cfg.Flow)
			}
			if (got.Iflag&(unix.IXON|unix.IXOFF) != 0) != tc.xon {
				t.Errorf("IXON/IXOFF = %v, want %v for flow %q", got.Iflag&(unix.IXON|unix.IXOFF) != 0, tc.xon, tc.cfg.Flow)
			}
			// The rate went in, whichever field this platform keeps it in.
			want := *got
			if !setBaud(&want, tc.cfg.Baud) || !sameBaud(&want, got) {
				t.Errorf("the port is not running at %d baud", tc.cfg.Baud)
			}
		})
	}
}

// The port is raw in both directions, and it is raw because the panel is talking
// to a machine: anything this layer interprets is a byte the board never sees.
func TestOpenLeavesNothingBetweenThePanelAndTheWire(t *testing.T) {
	p, err := Open(line(openPTY(t), 115200, 8, ParityNone, 1, FlowNone))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = p.Close() }()
	got := termiosOf(t, p)

	for _, tc := range []struct {
		name string
		on   bool
	}{
		{"ICANON", got.Lflag&unix.ICANON != 0},
		{"ECHO", got.Lflag&unix.ECHO != 0},
		{"ISIG", got.Lflag&unix.ISIG != 0},
		{"IEXTEN", got.Lflag&unix.IEXTEN != 0},
		{"OPOST", got.Oflag&unix.OPOST != 0},
		{"ICRNL", got.Iflag&unix.ICRNL != 0},
		{"INLCR", got.Iflag&unix.INLCR != 0},
		{"ISTRIP", got.Iflag&unix.ISTRIP != 0},
	} {
		if tc.on {
			t.Errorf("%s is still on; the port is interpreting bytes it should be passing", tc.name)
		}
	}
	// CLOCAL, or a port whose adapter never raises carrier detect delivers nothing.
	if got.Cflag&unix.CLOCAL == 0 || got.Cflag&unix.CREAD == 0 {
		t.Error("CLOCAL/CREAD are not both set; the port may never deliver a byte")
	}
	// One byte wakes a read, and nothing times out waiting for a second.
	if got.Cc[unix.VMIN] != 1 || got.Cc[unix.VTIME] != 0 {
		t.Errorf("VMIN/VTIME = %d/%d, want 1/0", got.Cc[unix.VMIN], got.Cc[unix.VTIME])
	}
}

// The character-device check is the shape of paths.OpenRegular's, inverted. A
// serial device is exactly what that helper refuses, so this one had to be
// written rather than reused — and it still has to refuse the things that are
// not devices, or `baton serial ./notes.txt` reports an ioctl error instead of
// the mistake the operator made.
func TestOpenRefusesWhatIsNotACharacterDevice(t *testing.T) {
	dir := t.TempDir()

	regular := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(regular, []byte("not a port"), 0o600); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{regular, fifo} {
		_, err := Open(line(path, 115200, 8, ParityNone, 1, FlowNone))
		if err == nil {
			t.Fatalf("Open(%s) = nil error, want a refusal", path)
		}
		if !strings.Contains(err.Error(), "character device") {
			t.Errorf("Open(%s) = %v, want the refusal to say it is not a character device", path, err)
		}
	}
	// A directory never reaches the check — open(2) refuses it first — but it must
	// still be refused rather than configured.
	if _, err := Open(line(dir, 115200, 8, ParityNone, 1, FlowNone)); err == nil {
		t.Errorf("Open(%s) = nil error, want a refusal", dir)
	}
}

// A device that is not there is an ordinary error and not a panic, because it is
// the state the bridge spends its whole reconnect loop in.
func TestOpenReportsAMissingDevice(t *testing.T) {
	_, err := Open(line(filepath.Join(t.TempDir(), "cu.nothing"), 115200, 8, ParityNone, 1, FlowNone))
	if err == nil {
		t.Fatal("Open of a missing device = nil error")
	}
	if !os.IsNotExist(err) {
		t.Errorf("Open of a missing device = %v, want a not-exist error the loop can wait on", err)
	}
}

// Open is the last line of defence on the baud rate, not Validate: a caller that
// skipped Validate must still not end up on a speed the platform silently
// substituted for the one it asked for.
func TestOpenRefusesABaudThePlatformCannotSet(t *testing.T) {
	_, err := Open(line(openPTY(t), 12345, 8, ParityNone, 1, FlowNone))
	if err == nil {
		t.Fatal("Open at 12345 baud = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "12345") {
		t.Errorf("Open at 12345 baud = %v, want the refusal to name the rate", err)
	}
}

// Closing the port has to end a read that is already parked on it — the
// reconnect loop and the cancel path both let go of a device that way, and a
// Close that queued up behind the read instead would wedge the panel.
//
// It guards a CONJUNCTION, and neither half alone: taking O_NONBLOCK off the
// open leaves this passing, and so does reaching the descriptor through Fd
// instead of SyscallConn. Do both and the read blocks for good — Fd un-registers
// the descriptor from the runtime's poller only when Go was the one that made it
// non-blocking, which is the case the flag removes. Measured, not reasoned:
// three of the four combinations end the read in under a millisecond and the
// fourth was still blocked after two seconds.
func TestClosingThePortEndsAReadAlreadyWaiting(t *testing.T) {
	p, err := Open(line(openPTY(t), 115200, 8, ParityNone, 1, FlowNone))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = p.Read(make([]byte, 16))
		close(done)
	}()

	// Long enough that the read is certainly parked rather than not yet started.
	time.Sleep(50 * time.Millisecond)
	_ = p.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not end the read in flight; the panel would wedge here")
	}
	wg.Wait()
}

// The read-back check, driven by the case that produced it. TIOCSETA returned
// success for CRTSCTS on /dev/cu.usbmodem2101 and the very next TIOCGETA showed
// the bit clear — the port has no RTS/CTS lines. A pty keeps every setting, so
// the hardware's answer is reproduced here by hand.
func TestDiffLineNamesTheSettingTheDriverDropped(t *testing.T) {
	cfg := line("/dev/cu.usbmodem2101", 9600, 7, ParityOdd, 2, FlowRTSCTS)
	base := &unix.Termios{}
	base.Cflag |= unix.CS7 | unix.PARENB | unix.PARODD | unix.CSTOPB | unix.CRTSCTS
	base.Iflag |= unix.IXON | unix.IXOFF
	if !setBaud(base, cfg.Baud) {
		t.Fatal("9600 baud is not in the platform's table")
	}

	if err := diffLine(cfg, base, base); err != nil {
		t.Fatalf("a driver that took every setting was reported as refusing one: %v", err)
	}

	for _, tc := range []struct {
		name string
		drop func(*unix.Termios)
		says string
	}{
		{"the baud rate", func(t *unix.Termios) { setBaud(t, 115200) }, "9600"},
		{"the data bits", func(t *unix.Termios) { t.Cflag = t.Cflag&^unix.CSIZE | unix.CS8 }, "7"},
		{"the parity", func(t *unix.Termios) { t.Cflag &^= unix.PARODD }, "odd"},
		{"the stop bits", func(t *unix.Termios) { t.Cflag &^= unix.CSTOPB }, "2"},
		{"RTS/CTS", func(t *unix.Termios) { t.Cflag &^= unix.CRTSCTS }, "rtscts"},
		{"XON/XOFF", func(t *unix.Termios) { t.Iflag &^= unix.IXOFF }, "rtscts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := *base
			tc.drop(&got)
			err := diffLine(cfg, base, &got)
			if err == nil {
				t.Fatalf("a driver that dropped %s was reported as taking it", tc.name)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("refusal %q does not name %q", err, tc.says)
			}
			if !strings.Contains(err.Error(), cfg.Device) {
				t.Errorf("refusal %q does not name the port that refused", err)
			}
		})
	}
}

// MakeRaw turns off everything on the panel's own terminal that would eat a byte
// before it reached the wire, and puts it all back afterwards. ISIG is the one
// that matters most: with it on, the Ctrl-C meant for a bootloader kills the
// bridge instead.
func TestMakeRawFreesTheTerminalAndPutsItBack(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer func() { _ = tty.Close(); _ = ptmx.Close() }()

	before := termiosOf(t, tty)
	if before.Lflag&(unix.ICANON|unix.ECHO|unix.ISIG) == 0 {
		t.Skip("this pty came up already raw; there is nothing to turn off")
	}

	restore, err := MakeRaw(tty)
	if err != nil {
		t.Fatalf("MakeRaw: %v", err)
	}
	during := termiosOf(t, tty)
	if during.Lflag&unix.ISIG != 0 {
		t.Error("ISIG is still on: Ctrl-C would kill the bridge instead of reaching the board")
	}
	if during.Lflag&(unix.ICANON|unix.ECHO) != 0 {
		t.Error("the terminal is still line-editing and echoing")
	}
	if during.Oflag&unix.OPOST != 0 {
		t.Error("OPOST is still on: the panel would rewrite the bytes on their way out")
	}

	restore()
	after := termiosOf(t, tty)
	if after.Lflag != before.Lflag || after.Iflag != before.Iflag || after.Oflag != before.Oflag {
		t.Error("the terminal was not put back the way it was found")
	}
}

// A stdin that is not a terminal has no termios to set. That is not a failure —
// `baton serial` is a command panel like any other and can be fed from a script
// — so the caller has to be able to tell this apart, which it does by the error.
func TestMakeRawReportsANonTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if _, err := MakeRaw(r); err == nil {
		t.Fatal("MakeRaw on a pipe = nil error, want the caller to be able to tell")
	}
}
