package serial

import "golang.org/x/sys/unix"

// The ioctl requests that read and write a termios on darwin. Linux spells the
// same two TCGETS/TCSETS; they are the only part of the termios work that is not
// portable between the two, along with the baud table below.
const (
	reqGetTermios = unix.TIOCGETA
	reqSetTermios = unix.TIOCSETA
)

// baudCodes is every rate darwin's termios has a constant for. On darwin the
// constant IS the number — B115200 is 115200 — but going through the table
// anyway keeps the two platforms the same shape, and keeps the answer to "can
// this machine set that rate?" a lookup rather than an opinion.
//
// Rates above 230400 are absent because darwin has no constant for them: they
// need the IOSSIOSPEED ioctl, which is a different call with a different
// argument, and baton does not make it. A rate that is not here is refused by
// name — see Config.Validate for why that beats the alternative.
var baudCodes = map[int]uint64{
	50:     unix.B50,
	75:     unix.B75,
	110:    unix.B110,
	134:    unix.B134,
	150:    unix.B150,
	200:    unix.B200,
	300:    unix.B300,
	600:    unix.B600,
	1200:   unix.B1200,
	1800:   unix.B1800,
	2400:   unix.B2400,
	4800:   unix.B4800,
	7200:   unix.B7200,
	9600:   unix.B9600,
	14400:  unix.B14400,
	19200:  unix.B19200,
	28800:  unix.B28800,
	38400:  unix.B38400,
	57600:  unix.B57600,
	76800:  unix.B76800,
	115200: unix.B115200,
	230400: unix.B230400,
}

// supportedBauds is baudCodes' rates in ascending order.
var supportedBauds = sortedBauds(baudCodes)

// setBaud writes the rate into the termios, reporting false for a rate darwin
// cannot set. darwin keeps the speed in the two speed fields and nowhere else,
// which is what its own cfsetspeed writes.
func setBaud(t *unix.Termios, baud int) bool {
	code, ok := baudCodes[baud]
	if !ok {
		return false
	}
	t.Ispeed, t.Ospeed = code, code
	return true
}

// sameBaud reports whether two termios carry the same rate. It is where the
// read-back check asks its baud question, and it is per-platform because the
// rate lives in different fields on each.
func sameBaud(want, got *unix.Termios) bool {
	return want.Ispeed == got.Ispeed && want.Ospeed == got.Ospeed
}
