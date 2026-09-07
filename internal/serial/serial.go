// Package serial bridges a terminal to a serial port: it opens the device, sets
// the line, and copies bytes both ways until the terminal goes away.
//
// It exists so `baton serial /dev/cu.usbmodem2101 115200` can be an ordinary
// command panel. A serial port has no process, and baton's panel machinery is
// process-shaped throughout — a pid, an exit code, a signal, a SIGWINCH — so
// rather than teach that machinery about a second lifecycle, the port gets a
// process of its own. The panel then behaves exactly like every other command
// panel: it can be signalled, restarted, logged and closed by the same keys.
//
// Two things follow from that choice and are worth stating here rather than
// discovering later:
//
//   - The bridge outlives the device. A USB adapter disappears when somebody
//     knocks the cable, and `screen` exits and takes its scrollback with it. This
//     one says the port is gone and re-opens it with the same settings when it
//     comes back, so the panel's ring, log and tail survive the unplug.
//   - Every byte passes through raw, Ctrl-C included. The bridge does not own the
//     terminal — baton does — so there is no escape key to reserve and no second
//     prefix for the operator to hold. Closing the panel is how you get out, and
//     Ctrl-C reaches the bootloader on the other end of the cable, which is where
//     you wanted it.
package serial

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Parity is the parity bit setting of a serial line.
type Parity string

// The parity settings baton offers. They are the three every adapter has; mark
// and space parity are not here because nothing this decade asks for them.
const (
	ParityNone Parity = "none"
	ParityEven Parity = "even"
	ParityOdd  Parity = "odd"
)

// Flow is the flow control setting of a serial line.
type Flow string

// The flow control settings baton offers: none (the default, and what `screen`
// does), hardware RTS/CTS, and software XON/XOFF.
const (
	FlowNone    Flow = "none"
	FlowRTSCTS  Flow = "rtscts"
	FlowXONXOFF Flow = "xonxoff"
)

// Config is a serial line, whole: the device and the five settings that decide
// how bytes are framed on it.
type Config struct {
	Device   string
	Baud     int
	DataBits int
	Parity   Parity
	StopBits int
	Flow     Flow
}

// Defaults are `screen`'s: 8 data bits, no parity, one stop bit, no flow
// control. An operator who has typed `screen /dev/… 115200` for years gets the
// same line from `baton serial /dev/… 115200`, and one who has not is not being
// handed a setting they have to know about.
const (
	DefaultBaud     = 115200
	DefaultDataBits = 8
	DefaultStopBits = 1
)

// Validate reports whether the line can be set as asked, and says which field is
// wrong when it cannot.
//
// The baud rate is checked against the platform's own table rather than against
// a rate this package invented, and a rate that is not in it is refused by name.
// The alternative — quietly falling back to the nearest rate, which is what a
// termios that is handed an unknown speed does — puts a wrong number on the wire
// and shows the operator a stream of garbage that looks exactly like a bad
// cable. An hour of somebody's afternoon is worth more than the convenience.
func (c Config) Validate() error {
	switch {
	case c.Device == "":
		return fmt.Errorf("no device given")
	case !baudSupported(c.Baud):
		return fmt.Errorf("baud rate %d is not one this platform can set; supported: %s", c.Baud, baudList())
	case c.DataBits < 5 || c.DataBits > 8:
		return fmt.Errorf("data bits %d is not 5, 6, 7 or 8", c.DataBits)
	case c.StopBits != 1 && c.StopBits != 2:
		return fmt.Errorf("stop bits %d is not 1 or 2", c.StopBits)
	}
	switch c.Parity {
	case ParityNone, ParityEven, ParityOdd:
	default:
		return fmt.Errorf("parity %q is not none, even or odd", string(c.Parity))
	}
	switch c.Flow {
	case FlowNone, FlowRTSCTS, FlowXONXOFF:
	default:
		return fmt.Errorf("flow control %q is not none, rtscts or xonxoff", string(c.Flow))
	}
	return nil
}

// Line renders the settings the way a serial line is written down — "115200 8N1"
// — with the flow control appended when there is any. It is what the bridge
// prints when it opens the port, so the operator can see the line it actually
// set rather than the one they meant to type.
func (c Config) Line() string {
	s := strconv.Itoa(c.Baud) + " " + strconv.Itoa(c.DataBits) + parityLetter(c.Parity) + strconv.Itoa(c.StopBits)
	if c.Flow != FlowNone && c.Flow != "" {
		s += " " + string(c.Flow)
	}
	return s
}

// parityLetter is the middle character of the "8N1" shorthand.
func parityLetter(p Parity) string {
	switch p {
	case ParityEven:
		return "E"
	case ParityOdd:
		return "O"
	default:
		return "N"
	}
}

// SupportedBauds is the rates this platform's termios has a constant for, in
// ascending order. It differs between darwin and linux — darwin has 7200 and
// 76800 that linux does not, linux has 460800 and up that darwin does not — and
// saying so is better than pretending to a portable set that neither platform
// actually has.
func SupportedBauds() []int {
	out := make([]int, len(supportedBauds))
	copy(out, supportedBauds)
	return out
}

// baudSupported reports whether the platform can set this rate.
func baudSupported(baud int) bool {
	for _, b := range supportedBauds {
		if b == baud {
			return true
		}
	}
	return false
}

// sortedBauds is the rates of a platform's baud table, ascending. The tables are
// maps keyed by rate so the lookup is the fast path; this is the slow path that
// turns one into a list an operator can read.
func sortedBauds[T any](m map[int]T) []int {
	out := make([]int, 0, len(m))
	for b := range m {
		out = append(out, b)
	}
	sort.Ints(out)
	return out
}

// baudList renders the supported rates for an error message.
func baudList() string {
	parts := make([]string, len(supportedBauds))
	for i, b := range supportedBauds {
		parts[i] = strconv.Itoa(b)
	}
	return strings.Join(parts, ", ")
}
