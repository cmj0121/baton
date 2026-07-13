package server

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestPeerUIDMatchesSelf proves the peer-credential check reads a connecting
// process's real uid from the kernel: a same-process unix peer resolves to this
// process's own uid, so sameUserPeer admits it. This is the mechanism that makes
// the "uid-private socket" claim enforced rather than assumed — before it, any
// process reaching the socket was trusted with full fleet control.
func TestPeerUIDMatchesSelf(t *testing.T) {
	server := acceptOneUnix(t)

	got, err := peerUID(rawConn(t, server))
	if err != nil {
		t.Skipf("peer credentials unsupported here: %v", err)
	}
	if int(got) != os.Getuid() {
		t.Fatalf("peerUID = %d, want this process's uid %d", got, os.Getuid())
	}

	ok, err := sameUserPeer(server)
	if err != nil {
		t.Fatalf("sameUserPeer error: %v", err)
	}
	if !ok {
		t.Fatalf("sameUserPeer rejected a same-uid peer; the socket owner must be admitted")
	}
}

// TestSameUserPeerAllowsNonUnix confirms a non-unix conn (an in-memory test pipe)
// is not policed by the peer check — it has no OS peer to read — so the server's
// pipe-based tests keep working and only real socket peers are verified.
func TestSameUserPeerAllowsNonUnix(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()

	ok, err := sameUserPeer(a)
	if err != nil {
		t.Fatalf("sameUserPeer(pipe) error: %v", err)
	}
	if !ok {
		t.Fatalf("sameUserPeer rejected a non-unix conn; test pipes must pass through")
	}
}

// TestSameUserPeerFailsClosedOnUnreadableUnixPeer proves the check fails CLOSED:
// when the peer is a real unix socket but its credentials cannot be read (here the
// connection is closed first, so the kernel query on its fd errors), sameUserPeer
// returns the error rather than admitting the connection. An unverifiable unix peer
// is rejected, never trusted.
func TestSameUserPeerFailsClosedOnUnreadableUnixPeer(t *testing.T) {
	server := acceptOneUnix(t)
	if err := server.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ok, err := sameUserPeer(server)
	if err == nil {
		t.Skip("this platform still reports peer creds on a closed conn; cannot exercise the error path")
	}
	if ok {
		t.Fatalf("sameUserPeer admitted a peer whose creds could not be read (err=%v); it must fail closed", err)
	}
}

// acceptOneUnix binds a throwaway unix socket, dials it, and returns the server
// side of the resulting connection — a real unix peer whose kernel-recorded uid
// is this test process's own.
func acceptOneUnix(t *testing.T) *net.UnixConn {
	t.Helper()
	dir, err := os.MkdirTemp("", "bt-peer")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	type accepted struct {
		conn net.Conn
		err  error
	}
	ch := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		ch <- accepted{c, err}
	}()

	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	select {
	case a := <-ch:
		if a.err != nil {
			t.Fatalf("accept: %v", a.err)
		}
		uc, ok := a.conn.(*net.UnixConn)
		if !ok {
			t.Fatalf("accepted conn is %T, want *net.UnixConn", a.conn)
		}
		t.Cleanup(func() { _ = uc.Close() })
		return uc
	case <-time.After(2 * time.Second):
		t.Fatal("accept timed out")
		return nil
	}
}

// rawConn extracts the syscall.RawConn peerUID needs from a unix connection.
func rawConn(t *testing.T, uc *net.UnixConn) syscall.RawConn {
	t.Helper()
	raw, err := uc.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	return raw
}
