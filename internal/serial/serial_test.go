package serial_test

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/serial"
)

// good is a line every platform baton ships for can set, used as the base for
// the one-field-wrong cases below.
func good() serial.Config {
	return serial.Config{
		Device:   "/dev/cu.usbmodem2101",
		Baud:     115200,
		DataBits: 8,
		Parity:   serial.ParityNone,
		StopBits: 1,
		Flow:     serial.FlowNone,
	}
}

// A rate no platform has a constant for is refused BY NAME, and the refusal
// carries the list. This is the guard the whole baud table exists for: termios
// handed a speed it does not know does not fail, it runs at some other speed,
// and a wrong baud on a working cable looks exactly like a broken one.
func TestValidateRefusesARateThePlatformCannotSet(t *testing.T) {
	c := good()
	c.Baud = 12345
	err := c.Validate()
	if err == nil {
		t.Fatal("baud 12345 = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "12345") {
		t.Errorf("refusal %q does not name the rate that was refused", err)
	}
	if !strings.Contains(err.Error(), "115200") {
		t.Errorf("refusal %q does not list a rate the operator could use instead", err)
	}
}

// 115200 is the rate the default uses and every platform has. If this ever fails
// the table lost its most important entry.
func TestValidateAcceptsTheDefaultLine(t *testing.T) {
	if err := good().Validate(); err != nil {
		t.Fatalf("the default line was refused: %v", err)
	}
}

// Every other field gets its own refusal, and each names the field. A message
// that just said "bad config" would send the operator back to the flags one at
// a time.
func TestValidateRefusesEachFieldByName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mut   func(*serial.Config)
		names string
	}{
		{"no device", func(c *serial.Config) { c.Device = "" }, "device"},
		{"nine data bits", func(c *serial.Config) { c.DataBits = 9 }, "9"},
		{"four data bits", func(c *serial.Config) { c.DataBits = 4 }, "4"},
		{"three stop bits", func(c *serial.Config) { c.StopBits = 3 }, "3"},
		{"mark parity", func(c *serial.Config) { c.Parity = "mark" }, "mark"},
		{"dtrdsr flow", func(c *serial.Config) { c.Flow = "dtrdsr" }, "dtrdsr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := good()
			tc.mut(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("%s = nil error, want a refusal", tc.name)
			}
			if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("refusal %q does not name %q", err, tc.names)
			}
		})
	}
}

// 5, 6, 7 and 8 data bits and both stop-bit counts are all real settings, and
// none of them may be refused.
func TestValidateAcceptsEveryRealFraming(t *testing.T) {
	for _, bits := range []int{5, 6, 7, 8} {
		for _, stop := range []int{1, 2} {
			for _, p := range []serial.Parity{serial.ParityNone, serial.ParityEven, serial.ParityOdd} {
				for _, f := range []serial.Flow{serial.FlowNone, serial.FlowRTSCTS, serial.FlowXONXOFF} {
					c := good()
					c.DataBits, c.StopBits, c.Parity, c.Flow = bits, stop, p, f
					if err := c.Validate(); err != nil {
						t.Fatalf("%d%s%d %s was refused: %v", bits, string(p), stop, string(f), err)
					}
				}
			}
		}
	}
}

// Line is what the bridge prints when the port opens, so it has to read the way
// a serial line is written down rather than as a struct dump.
func TestLineReadsAsASerialLine(t *testing.T) {
	for _, tc := range []struct {
		cfg  serial.Config
		want string
	}{
		{good(), "115200 8N1"},
		{serial.Config{Baud: 9600, DataBits: 7, Parity: serial.ParityEven, StopBits: 2, Flow: serial.FlowNone}, "9600 7E2"},
		{serial.Config{Baud: 9600, DataBits: 7, Parity: serial.ParityOdd, StopBits: 1, Flow: serial.FlowRTSCTS}, "9600 7O1 rtscts"},
		{serial.Config{Baud: 300, DataBits: 8, Parity: serial.ParityNone, StopBits: 1, Flow: serial.FlowXONXOFF}, "300 8N1 xonxoff"},
	} {
		if got := tc.cfg.Line(); got != tc.want {
			t.Errorf("Line() = %q, want %q", got, tc.want)
		}
	}
}

// The rates come back sorted and de-duplicated, because they are printed to a
// human in a refusal. An unsorted list of thirty numbers is not a list anyone
// reads.
func TestSupportedBaudsIsSortedAndUsable(t *testing.T) {
	rates := serial.SupportedBauds()
	if len(rates) == 0 {
		t.Fatal("no supported baud rates on the platform baton is being tested on")
	}
	for i := 1; i < len(rates); i++ {
		if rates[i] <= rates[i-1] {
			t.Fatalf("SupportedBauds not ascending at %d: %v", i, rates[i-1:i+1])
		}
	}
	// The eight rates every device baton is likely to meet has, on both platforms.
	for _, want := range []int{300, 1200, 2400, 4800, 9600, 19200, 57600, 115200} {
		found := false
		for _, b := range rates {
			if b == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%d baud is missing from the table", want)
		}
	}
}

// The slice handed out is a copy: a caller that sorts or truncates it must not
// be able to break the next refusal message.
func TestSupportedBaudsHandsOutACopy(t *testing.T) {
	first := serial.SupportedBauds()
	first[0] = -1
	if serial.SupportedBauds()[0] == -1 {
		t.Fatal("SupportedBauds returned the package's own slice")
	}
}
