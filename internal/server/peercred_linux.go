//go:build linux

package server

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// peerUID reads the uid of the process connected on raw via SO_PEERCRED, the
// Linux kernel's record of who opened the socket. It is taken at connect time by
// the kernel, so it cannot be spoofed by the peer.
func peerUID(raw syscall.RawConn) (uint32, error) {
	var uid uint32
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, sockErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if sockErr == nil {
			uid = cred.Uid
		}
	}); err != nil {
		return 0, err
	}
	return uid, sockErr
}
