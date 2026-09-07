package serial

import "golang.org/x/sys/unix"

// The ioctl requests that read and write a termios on linux. darwin spells the
// same two TIOCGETA/TIOCSETA; they are the only part of the termios work that is
// not portable between the two, along with the baud table below.
const (
	reqGetTermios = unix.TCGETS
	reqSetTermios = unix.TCSETS
)

// baudCodes is every rate linux's termios has a constant for. Unlike darwin the
// constant is an index and not the rate — B115200 is 0x1002 — which is exactly
// why the lookup exists: a number written straight into the field would set some
// other speed entirely.
//
// A rate that is not here needs BOTHER and the termios2 ioctl, which baton does
// not make. It is refused by name — see Config.Validate for why that beats the
// alternative.
var baudCodes = map[int]uint32{
	50:      unix.B50,
	75:      unix.B75,
	110:     unix.B110,
	134:     unix.B134,
	150:     unix.B150,
	200:     unix.B200,
	300:     unix.B300,
	600:     unix.B600,
	1200:    unix.B1200,
	1800:    unix.B1800,
	2400:    unix.B2400,
	4800:    unix.B4800,
	9600:    unix.B9600,
	19200:   unix.B19200,
	38400:   unix.B38400,
	57600:   unix.B57600,
	115200:  unix.B115200,
	230400:  unix.B230400,
	460800:  unix.B460800,
	500000:  unix.B500000,
	576000:  unix.B576000,
	921600:  unix.B921600,
	1000000: unix.B1000000,
	1152000: unix.B1152000,
	1500000: unix.B1500000,
	2000000: unix.B2000000,
}

// supportedBauds is baudCodes' rates in ascending order.
var supportedBauds = sortedBauds(baudCodes)

// setBaud writes the rate into the termios, reporting false for a rate linux
// cannot set.
//
// The speed goes in the CBAUD bits of c_cflag and nowhere else. The Ispeed and
// Ospeed fields this struct also has belong to termios2, past the end of the
// `struct termios` that TCGETS fills and TCSETS reads, so writing them would
// look like it set the speed twice and would in fact set it zero times.
func setBaud(t *unix.Termios, baud int) bool {
	code, ok := baudCodes[baud]
	if !ok {
		return false
	}
	t.Cflag = (t.Cflag &^ unix.CBAUD) | code
	return true
}

// sameBaud reports whether two termios carry the same rate. It is where the
// read-back check asks its baud question, and it is per-platform because the
// rate lives in different fields on each.
func sameBaud(want, got *unix.Termios) bool {
	return want.Cflag&unix.CBAUD == got.Cflag&unix.CBAUD
}
