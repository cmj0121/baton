package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// A line the platform cannot set is refused before anything is opened, and the
// refusal names what was wrong. `baton serial` is typed by hand at a panel, so
// the message is the whole user interface for a mistake.
func TestSerialMainRefusesALineItCannotSet(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{"no device at all", nil, "device"},
		{"a baud rate no platform has", []string{"/dev/cu.usbmodem2101", "12345"}, "12345"},
		{"nine data bits", []string{"/dev/cu.usbmodem2101", "115200", "-d", "9"}, "9"},
		{"mark parity", []string{"/dev/cu.usbmodem2101", "115200", "-p", "mark"}, "mark"},
		{"three stop bits", []string{"/dev/cu.usbmodem2101", "115200", "-s", "3"}, "3"},
		{"dtrdsr flow control", []string{"/dev/cu.usbmodem2101", "115200", "-f", "dtrdsr"}, "dtrdsr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runSerial(t, tc.args, nil)
			if code == 0 {
				t.Fatalf("`baton serial %s` exit = 0, want a refusal", strings.Join(tc.args, " "))
			}
			if !strings.Contains(out, tc.says) {
				t.Errorf("stderr = %q, want it to name %q", out, tc.says)
			}
		})
	}
}

// The device name reaches a terminal with nothing in front of it, and an agent
// driving `ctl spawn --run baton serial …` chooses that name. An escape sequence
// in it is dropped rather than executed.
func TestSerialMainScrubsTheDeviceNameOutOfItsError(t *testing.T) {
	out, code := runSerial(t, []string{"/dev/\x1b]0;pwned\x07tty", "12345"}, nil)
	if code == 0 {
		t.Fatal("exit = 0, want a refusal")
	}
	if strings.Contains(out, "\x1b]0;pwned\x07") {
		t.Errorf("the device name's escape sequence reached the terminal: %q", out)
	}
}

// The whole subcommand, end to end, against a pty standing in for a port: the
// panel's bytes reach the device, the device's bytes reach the panel, and
// closing the panel's input is how it ends — no escape key, which is the point.
func TestSerialMainBridgesThePanelToThePort(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer func() { _ = tty.Close(); _ = ptmx.Close() }()

	keys, typed, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = keys.Close() }()

	screen := filepath.Join(t.TempDir(), "screen")
	shown := func() string {
		b, _ := os.ReadFile(screen)
		return string(b)
	}

	code := make(chan int, 1)
	go func() { code <- runSerialWith(t, []string{tty.Name(), "115200"}, keys, screen) }()

	waitUntil(t, "the port to open", func() bool { return strings.Contains(shown(), "open at 115200 8N1") })

	// The panel types; the device sees it, byte for byte, Ctrl-C included.
	if _, err := typed.WriteString("boot\r\x03"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 6)
	_ = ptmx.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := ptmx.Read(got); err != nil {
		t.Fatalf("the device never saw the keystrokes: %v", err)
	}
	if string(got) != "boot\r\x03" {
		t.Errorf("the device saw %q, want %q", got, "boot\r\x03")
	}

	// The device answers; the panel sees it.
	if _, err := ptmx.Write([]byte("ESP-ROM:esp32s3")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the device's bytes on the panel", func() bool { return strings.Contains(shown(), "ESP-ROM:esp32s3") })

	// Closing the panel's input is the exit. There is no key to press.
	_ = typed.Close()
	select {
	case c := <-code:
		if c != 0 {
			t.Errorf("exit = %d, want 0 — a command panel that exits non-zero reads as one that crashed", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("`baton serial` did not exit when the panel's input ended")
	}
}

// runSerial drives serialMain with an empty stdin and returns what it printed on
// either stream, plus the exit code.
func runSerial(t *testing.T, args []string, stdin *os.File) (string, int) {
	t.Helper()
	dir := t.TempDir()
	if stdin == nil {
		f, err := os.Create(filepath.Join(dir, "stdin"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		stdin = f
	}
	out := filepath.Join(dir, "out")
	code := runSerialWith(t, args, stdin, out)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), code
}

// runSerialWith points the process's stdin, stdout and stderr at the given file
// and path for the length of one serialMain call. Both output streams go to the
// same file because a refusal lands on stderr and the bridge's notices on
// stdout, and a test that read only one of them would miss half the surface.
func runSerialWith(t *testing.T, args []string, stdin *os.File, outPath string) int {
	t.Helper()
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()

	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = stdin, out, out
	defer func() { os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr }()
	return serialMain(args)
}

// waitUntil polls cond for two seconds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
