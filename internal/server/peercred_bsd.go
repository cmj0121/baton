//go:build darwin || freebsd

package server

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// peerUID reads the uid of the process connected on raw via LOCAL_PEERCRED, the
// BSD/macOS equivalent of Linux's SO_PEERCRED. Like SO_PEERCRED it is recorded by
// the kernel at connect time, so the peer cannot forge it.
func peerUID(raw syscall.RawConn) (uint32, error) {
	var uid uint32
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		cred, sockErr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if sockErr == nil {
			uid = cred.Uid
		}
	}); err != nil {
		return 0, err
	}
	return uid, sockErr
}
