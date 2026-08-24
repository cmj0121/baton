//go:build unix

package paths

import (
	"os"
	"syscall"
)

// ownedByCaller reports whether the directory this FileInfo describes belongs to
// the user baton is running as. It is the check that replaces the randomness the
// conductor workspace used to get from MkdirTemp: a fixed name in a shared
// temporary base is a name another user can create first, and landing in someone
// else's directory is worse than refusing to start the conductor at all.
//
// A FileInfo whose Sys() is not a stat structure tells us nothing, and "nothing"
// is not evidence of a hostile directory, so it passes.
func ownedByCaller(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	return int(st.Uid) == os.Getuid()
}
