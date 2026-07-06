//go:build !linux && !darwin && !freebsd

package server

import "syscall"

// peerUID cannot read peer credentials on this platform, so it reports the
// unsupported sentinel. sameUserPeer then skips the check and privacy rests on
// the socket's directory and file permissions. baton targets Linux, macOS, and
// FreeBSD — all of which have a real implementation — so this only guards an
// exotic build rather than a supported one.
func peerUID(_ syscall.RawConn) (uint32, error) {
	return 0, errPeerCredUnsupported
}
