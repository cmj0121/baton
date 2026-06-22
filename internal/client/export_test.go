package client

import "time"

// SetReadTimeout overrides the client's idle read deadline and returns a restore
// func. It exists only for tests, which drive readLoop's timeout path in
// milliseconds rather than the 45s production default. Call it before Dial.
func SetReadTimeout(d time.Duration) (restore func()) {
	prev := readTimeout
	readTimeout = d
	return func() { readTimeout = prev }
}
