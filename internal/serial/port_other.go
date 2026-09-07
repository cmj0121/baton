//go:build !linux && !darwin

package serial

import (
	"errors"
	"io"
	"os"
)

// errUnsupported is what every entry point returns off darwin and linux. baton
// ships for those two; this file exists so the package still builds elsewhere,
// the way cmd/baton's session_other.go and internal/panellog's do.
var errUnsupported = errors.New("serial ports are supported on darwin and linux only")

// supportedBauds is empty here, so Config.Validate refuses every rate and no
// caller reaches an Open that could not have worked.
var supportedBauds []int

// Open is unavailable on this platform.
func Open(Config) (io.ReadWriteCloser, error) { return nil, errUnsupported }

// MakeRaw is unavailable on this platform.
func MakeRaw(*os.File) (func(), error) { return nil, errUnsupported }
