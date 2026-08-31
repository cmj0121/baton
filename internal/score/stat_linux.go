//go:build linux

package score

import (
	"os"
	"syscall"
	"time"
)

// fileIdentity returns the inode and change time behind a FileInfo. See
// stat_other.go for what they are for.
func fileIdentity(fi os.FileInfo) (inode uint64, ctime time.Time) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, time.Time{}
	}
	sec, nsec := st.Ctim.Unix()
	return st.Ino, time.Unix(sec, nsec)
}
