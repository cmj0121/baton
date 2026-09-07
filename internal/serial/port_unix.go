//go:build linux || darwin

package serial

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// Open opens the device and sets the line to cfg, returning the port.
//
// # Why not paths.OpenRegular
//
// baton has a shared helper for opening a path it did not choose, and it is the
// wrong tool here on purpose: OpenRegular refuses anything that is not a regular
// file, and a serial device is a character device. Weakening it to let this
// through would weaken it for the peer-named data files it was written to
// defend. So this is its own open, with the character-device check standing in
// for the regular-file one — same question ("is the thing on the end of this
// name what I think it is?"), different answer.
//
// # Why O_NONBLOCK
//
// On macOS a device has two names: /dev/tty.usbmodem2101 and
// /dev/cu.usbmodem2101. Opening the tty. name BLOCKS IN open(2) until the modem
// asserts carrier detect, and on an adapter that never asserts it, it blocks
// forever — measured on this machine at "still blocked after 4 seconds" against
// 7ms for the same device opened with O_NONBLOCK. The cu. (call-up) name is the
// one for outgoing connections and does not wait, but operators type tty. names
// because that is what every tutorial shows, and a panel that hangs on open
// looks like a panel that is broken.
//
// The flag is kept after the open rather than cleared, and what that buys is
// narrower than it first looks — measured, because the obvious answer was wrong.
// Go registers a kindOpenFile descriptor with its poller either way, setting it
// non-blocking itself when the caller did not, so reads park rather than spin on
// EAGAIN and a Close from another goroutine ends a read in flight either way.
// The difference is os.File.Fd, which un-registers the descriptor ONLY when Go
// was the one that made it non-blocking. So passing the flag here is what makes
// this port immune to a later Fd call quietly putting it back into blocking
// mode, where the reconnect loop's Close would never reach the read it has to
// end. See configure, which reaches the descriptor the other way for that reason.
func Open(cfg Config) (io.ReadWriteCloser, error) {
	f, err := os.OpenFile(cfg.Device, os.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		_ = f.Close()
		return nil, fmt.Errorf("%s is not a character device", cfg.Device)
	}
	if err := configure(f, cfg); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// configure sets the line on an already-open port.
//
// It reaches the descriptor through SyscallConn rather than Fd, because Fd can
// take the file out of the runtime's poller and back into blocking mode — the
// property Open's comment explains it is defending. internal/server's peercred
// files reach for a descriptor the same way.
func configure(f *os.File, cfg Config) error {
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var inner error
	if err := raw.Control(func(fd uintptr) { inner = setLine(int(fd), cfg) }); err != nil {
		return err
	}
	return inner
}

// setLine applies cfg to the descriptor's termios.
func setLine(fd int, cfg Config) error {
	t, err := unix.IoctlGetTermios(fd, reqGetTermios)
	if err != nil {
		return fmt.Errorf("read the line settings: %w", err)
	}

	// Raw in both directions: no canonical line editing, no echo, no signal
	// generation, no CR/LF translation, no 8th-bit stripping. The panel is talking
	// to a machine, and anything this layer interprets is a byte the machine on the
	// other end of the cable does not get.
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.PARODD | unix.CSTOPB | unix.CRTSCTS

	// CREAD to actually receive, CLOCAL to ignore the modem control lines: without
	// it a port whose adapter never raises carrier detect delivers nothing, which
	// is the same defect as the tty. open one layer down.
	t.Cflag |= unix.CREAD | unix.CLOCAL

	switch cfg.DataBits {
	case 5:
		t.Cflag |= unix.CS5
	case 6:
		t.Cflag |= unix.CS6
	case 7:
		t.Cflag |= unix.CS7
	default:
		t.Cflag |= unix.CS8
	}
	switch cfg.Parity {
	case ParityEven:
		t.Cflag |= unix.PARENB
	case ParityOdd:
		t.Cflag |= unix.PARENB | unix.PARODD
	}
	if cfg.StopBits == 2 {
		t.Cflag |= unix.CSTOPB
	}
	switch cfg.Flow {
	case FlowRTSCTS:
		t.Cflag |= unix.CRTSCTS
	case FlowXONXOFF:
		t.Iflag |= unix.IXON | unix.IXOFF
	}

	// One byte is enough to wake a read, and never time out waiting for a second.
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0

	if !setBaud(t, cfg.Baud) {
		return fmt.Errorf("baud rate %d is not one this platform can set; supported: %s", cfg.Baud, baudList())
	}
	if err := unix.IoctlSetTermios(fd, reqSetTermios, t); err != nil {
		return fmt.Errorf("set the line settings: %w", err)
	}

	got, err := unix.IoctlGetTermios(fd, reqGetTermios)
	if err != nil {
		return fmt.Errorf("read the line settings back: %w", err)
	}
	return diffLine(cfg, t, got)
}

// diffLine reports the first setting the driver did not take.
//
// The read-back is here because the ioctl's success is not the driver's
// agreement. Measured on this machine against an ESP32-S3's USB-serial-JTAG
// port: TIOCSETA returns no error for CRTSCTS and the very next TIOCGETA shows
// the bit clear — the port has no RTS/CTS lines, so the driver dropped a setting
// it had just said yes to. A pty, by contrast, keeps every one of them.
//
// This is the same failure the baud table refuses by name, one layer further
// down: a line that is silently not the line you asked for costs an operator an
// afternoon and looks exactly like a bad cable. So the settings the operator
// named are checked, and only those — the raw-mode flags are baton's own choice
// and not something anyone typed.
func diffLine(cfg Config, want, got *unix.Termios) error {
	refused := func(what string, value any) error {
		return fmt.Errorf("%s does not support %v %s: the driver accepted the setting and dropped it",
			cfg.Device, value, what)
	}
	switch {
	case !sameBaud(want, got):
		return refused("baud", cfg.Baud)
	case want.Cflag&unix.CSIZE != got.Cflag&unix.CSIZE:
		return refused("data bits", cfg.DataBits)
	case want.Cflag&(unix.PARENB|unix.PARODD) != got.Cflag&(unix.PARENB|unix.PARODD):
		return refused("parity", cfg.Parity)
	case want.Cflag&unix.CSTOPB != got.Cflag&unix.CSTOPB:
		return refused("stop bits", cfg.StopBits)
	case want.Cflag&unix.CRTSCTS != got.Cflag&unix.CRTSCTS,
		want.Iflag&(unix.IXON|unix.IXOFF) != got.Iflag&(unix.IXON|unix.IXOFF):
		return refused("flow control", cfg.Flow)
	}
	return nil
}

// MakeRaw puts the terminal on f into raw mode and returns the restore for it.
//
// The bridge passes every byte through, so the terminal it is reading from must
// not be eating any: no echo (the device echoes, or it does not and that is the
// device's business), no line editing, and above all no ISIG — Ctrl-C belongs to
// the bootloader on the far end, not to this process.
//
// A file that is not a terminal is not an error. `baton serial` is a command
// panel like any other and can be run with its input piped from a script, and a
// pipe has no termios to set.
func MakeRaw(f *os.File) (restore func(), err error) {
	raw, err := f.SyscallConn()
	if err != nil {
		return nil, err
	}
	var saved *unix.Termios
	var fd int
	var inner error
	if err := raw.Control(func(h uintptr) {
		fd = int(h)
		saved, inner = unix.IoctlGetTermios(fd, reqGetTermios)
		if inner != nil {
			return
		}
		t := *saved
		t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
			unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
		t.Oflag &^= unix.OPOST
		t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
		t.Cflag &^= unix.CSIZE | unix.PARENB
		t.Cflag |= unix.CS8
		t.Cc[unix.VMIN] = 1
		t.Cc[unix.VTIME] = 0
		inner = unix.IoctlSetTermios(fd, reqSetTermios, &t)
	}); err != nil {
		return nil, err
	}
	if inner != nil {
		return nil, inner
	}
	return func() {
		_ = raw.Control(func(h uintptr) { _ = unix.IoctlSetTermios(int(h), reqSetTermios, saved) })
	}, nil
}
