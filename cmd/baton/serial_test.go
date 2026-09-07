package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// A refusal quotes back what it was given, and this one lands on a terminal with
// nothing in front of it: no cockpit renders it into a footer it controls. An
// agent driving `ctl spawn --run baton serial …` chooses those arguments, so the
// escape sequences in them are dropped rather than executed.
func TestSerialMainScrubsTheArgumentsOutOfItsError(t *testing.T) {
	for _, args := range [][]string{
		// kong names an argument it did not expect, verbatim and unquoted.
		{"/dev/cu.usbmodem2101", "115200", "\x1b]0;pwned\x07"},
		{"/dev/cu.usbmodem2101", "115200", "--", "\x1b]0;pwned\x07"},
	} {
		out, code := runSerial(t, args, nil)
		if code == 0 {
			t.Fatalf("`baton serial %q` exit = 0, want a refusal", args)
		}
		if strings.Contains(out, "\x1b]0;pwned\x07") {
			t.Errorf("an escape sequence from the command line reached the terminal: %q", out)
		}
	}
}

// The whole subcommand, end to end, with a pty standing in for the port and
// another for the panel it runs in: the panel's terminal goes raw, its bytes
// reach the device unedited, the device's bytes reach the panel, closing the
// panel's input is how it ends, and the terminal is put back on the way out.
func TestSerialMainBridgesThePanelToThePort(t *testing.T) {
	device, port, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer func() { _ = port.Close(); _ = device.Close() }()

	keyboard, panel, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer func() { _ = panel.Close(); _ = keyboard.Close() }()

	// Everything either side ever says, accumulated: a pty master from
	// creack/pty is not a pollable file, so it has no read deadline to lean on and
	// the reads live in goroutines that end when the file closes.
	echoed, wire := drain(keyboard), drain(device)

	// A pty comes up cooked, which is what makes the raw-mode assertions below
	// mean something. If this one did not, they would all pass for free.
	if _, err := keyboard.WriteString("cooked?"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the pty to echo, which is what raw mode has to turn off",
		func() bool { return strings.Contains(echoed(), "cooked?") })

	screen := filepath.Join(t.TempDir(), "screen")
	shown := func() string {
		b, _ := os.ReadFile(screen)
		return string(b)
	}

	code := make(chan int, 1)
	go func() { code <- runSerialWith(t, []string{port.Name(), "115200"}, panel, screen) }()

	waitUntil(t, "the port to open", func() bool { return strings.Contains(shown(), "open at 115200 8N1") })

	// The panel types; the device sees it, byte for byte. \r stays \r rather than
	// becoming \n, and \x03 arrives as a byte rather than being taken for a signal
	// — both of which a cooked terminal would have got wrong before the bridge
	// ever saw them.
	if _, err := keyboard.WriteString("boot\r\x03"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the keystrokes to reach the device",
		func() bool { return strings.Contains(wire(), "boot\r\x03") })

	// And nothing of them was echoed locally: what the operator sees of their own
	// typing is the board's business, the way it is under screen.
	if strings.Contains(echoed(), "boot") {
		t.Errorf("the panel's terminal echoed the keystrokes; the bridge did not take it raw:\n%q", echoed())
	}

	// The device answers; the panel sees it.
	if _, err := device.Write([]byte("ESP-ROM:esp32s3")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the device's bytes on the panel", func() bool { return strings.Contains(shown(), "ESP-ROM:esp32s3") })

	// Closing the panel's terminal is the exit. There is no key to press: the
	// bridge reserves none, because it does not own the terminal.
	_ = panel.Close()
	select {
	case c := <-code:
		if c != 0 {
			t.Errorf("exit = %d, want 0 — a command panel that exits non-zero reads as one that crashed", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("`baton serial` did not exit when the panel's input ended")
	}
}

// drain reads a file into a buffer until it closes, and hands back a look at
// what has arrived so far.
func drain(f *os.File) func() string {
	var mu sync.Mutex
	var b strings.Builder
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				mu.Lock()
				b.Write(buf[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return b.String()
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
