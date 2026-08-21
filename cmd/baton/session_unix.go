//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// claimSession takes the daemon's exclusive claim on the fleet, so exactly one
// backend can ever own the control socket.
//
// Binding the socket is very nearly a mutex on its own, but not quite. Two
// daemons starting together against a socket left behind by a crash can both
// find it dead, both unlink it, and both bind — leaving two backends for one
// socket, one of them unreachable yet still spawning panels and writing the
// fleet's state file. An advisory lock closes that window, and the kernel
// drops it when the holder dies, so it cannot go stale the way a file with a pid
// in it would.
//
// held is false when another daemon already owns the socket. That is an
// ordinary outcome, not an error: several cockpits starting at once each try to
// bring a backend up, and all but one are meant to lose.
func claimSession(path string) (release func(), held bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open session lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lock session %s: %w", path, err)
	}
	// The lock lives as long as the descriptor. Closing it releases the claim, so
	// the returned func is the only thing that may close f.
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}
