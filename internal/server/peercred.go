// Peer-credential verification for the control socket. baton's whole trust model
// rests on that socket being private to one OS user: the conductor fence, every
// command handler, and the "spawn any command" power of panel.create all assume
// the peer on the wire is the operator, not a stranger. The socket's private
// directory (0700) and file mode (0600) are the first line of defence; this
// SO_PEERCRED / LOCAL_PEERCRED check is the enforced boundary behind them, so a
// socket that is somehow still reachable by another user — a loose umask on the
// socket file, a pre-existing world-listable runtime dir, an inherited or passed
// fd — cannot drive the fleet. The check is what makes "uid-private socket" a
// guarantee rather than an assumption.
package server

import (
	"errors"
	"net"
	"os"
)

// errPeerCredUnsupported is returned by peerUID on a platform where baton cannot
// read a connected peer's uid. baton targets Linux and macOS (and FreeBSD),
// which are all covered; on anything else the check is skipped and privacy falls
// back to the socket's directory/file permissions.
var errPeerCredUnsupported = errors.New("peer credential check unsupported on this platform")

// sameUserPeer reports whether the peer on conn belongs to the same OS user as
// this server process. It fails CLOSED on a real unix peer whose uid cannot be
// read (returning the error), so an unverifiable connection is rejected rather
// than trusted. It returns (true, nil) only when the peer is confirmed same-uid,
// when the platform cannot report peer creds at all, or when conn is not a unix
// socket — the last case being an in-memory test pipe, which never occurs in
// production where the listener is always the unix control socket.
func sameUserPeer(conn net.Conn) (bool, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return true, nil // not a unix socket (e.g. a test pipe); nothing to police
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false, err
	}
	uid, err := peerUID(raw)
	if errors.Is(err, errPeerCredUnsupported) {
		// This platform cannot report the peer's uid; rely on the socket's
		// directory (0700) and file (0600) permissions for privacy instead.
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return int(uid) == os.Getuid(), nil
}
