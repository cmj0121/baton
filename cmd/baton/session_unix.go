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

// sessionProbe answers "does a daemon hold this session right now" from ONE
// open descriptor, so a caller that asks repeatedly pays a flock per question
// rather than an open, a flock, an unlock and a close. signalAndWait asks up to
// daemonPollTries times.
//
// It is the liveness oracle for a daemon that has not bound its socket yet, and
// the reason stopUnboundDaemon can be trusted with a pid it did not watch being
// written. It is the only caller: the other stop has a socket that answers,
// which says the same thing and says it about a daemon far enough along to have
// bound one. The claim is an flock, so the kernel drops it the instant its holder
// dies — a SIGKILL included — which is exactly the thing no file left on disk
// can say about itself.
//
// What it settles is the false direction, completely: no daemon holds this
// socket, so whatever a PID file beside it names, that process is not one, and
// it must not be signalled. True is the weaker half — it says a daemon is alive,
// not that the PID file has caught up with which one — and stopUnboundDaemon's
// doc has the one gap that leaves.
type sessionProbe struct{ f *os.File }

// openSessionProbe opens the descriptor claimed answers from. A probe with no
// descriptor answers false for as long as it lives, which is the safe direction
// and the right one: the file is opened before the first question is asked, and
// the first false ends the only wait this type is used for.
//
// It never creates the lock file. An absent one means no daemon has ever claimed
// this socket, and a probe that made one would leave litter behind every `baton
// --force` run against a session nothing is using. Any other failure to open —
// a permission, a filesystem with no locking — answers false too, which leaves
// the caller signalling nothing and tidying instead.
func openSessionProbe(path string) *sessionProbe {
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return &sessionProbe{}
	}
	return &sessionProbe{f: f}
}

// claimed reports whether a daemon holds the session this probe was opened on.
//
// It decides by TAKING the lock, briefly, because flock will not report its
// holder any other way. Note which branch does that: a claim somebody holds is
// answered by the failure to take it, so the only time this touches the lock at
// all is when the session is free. See stopUnboundDaemon for what that costs a
// claimSession running in the same instant, and why polling does not multiply it.
func (p *sessionProbe) claimed() bool {
	if p.f == nil {
		return false
	}
	if err := syscall.Flock(int(p.f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	_ = syscall.Flock(int(p.f.Fd()), syscall.LOCK_UN)
	return false
}

// close gives the descriptor back. It holds no lock of its own by the time
// claimed returns, so this releases nothing but the file.
func (p *sessionProbe) close() {
	if p.f != nil {
		_ = p.f.Close()
	}
}
