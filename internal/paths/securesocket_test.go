package paths

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestSecureSocketClampsToOwnerOnly demonstrates the socket-privacy fix: a socket
// bound under a permissive umask carries group/other bits, and SecureSocket
// clamps it to owner-only (0600) so no other user on the host can connect and
// spawn processes through baton. It sets a wide umask, binds a socket to prove
// the loose bits actually appear, then asserts SecureSocket removes them.
func TestSecureSocketClampsToOwnerOnly(t *testing.T) {
	// A permissive umask is what makes the vulnerability observable: without it the
	// socket may already be tight and the test would not prove the clamp. Restore
	// the process umask afterwards so other tests are unaffected.
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	dir, err := os.MkdirTemp("", "bt-sock")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	sock := filepath.Join(dir, "s.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Precondition: under the wide umask the freshly bound socket is reachable by
	// group/other. If the platform already binds tight this assertion is skipped —
	// the clamp below is still verified.
	if fi, err := os.Stat(sock); err == nil && fi.Mode().Perm()&0o077 == 0 {
		t.Logf("socket bound tight already (%v); clamp is defence in depth here", fi.Mode().Perm())
	}

	if err := SecureSocket(sock); err != nil {
		t.Fatalf("SecureSocket: %v", err)
	}

	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket mode = %v, want 0600 (owner-only); group/other must not reach the control socket", perm)
	}
}

// TestSecureSocketMissingIsNoError confirms clamping an already-unlinked socket is
// tolerated, so a shutdown race that removes the socket first cannot turn into a
// fatal error on the bind path.
func TestSecureSocketMissingIsNoError(t *testing.T) {
	if err := SecureSocket(filepath.Join(t.TempDir(), "does-not-exist.sock")); err != nil {
		t.Fatalf("SecureSocket on a missing socket = %v, want nil", err)
	}
}
