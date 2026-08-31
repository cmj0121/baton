//go:build unix

package score

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockDir takes the store's exclusive claim on its directory. It returns the
// release that drops it, and whether a claim was actually taken.
//
// This is the same idiom the daemon uses for the fleet itself
// (cmd/baton/session_unix.go over paths.LockFile): an advisory flock held for
// the life of a descriptor nobody else closes. It is copied rather than shared
// because internal/score is stdlib-only by contract (#38), and syscall is
// stdlib.
//
// A lock file is used rather than the directory itself because a directory
// cannot be opened O_RDWR everywhere, and because a named file says what it is
// to whoever lists the directory.
//
// Only EWOULDBLOCK means "another daemon holds it". A filesystem that cannot
// lock at all — NFS without lockd, some SMB and FUSE mounts, which is exactly
// where a corporate $HOME (and therefore the default ~/.baton) lands — reports
// ENOTSUP, EOPNOTSUPP, or ENOLCK. Failing Open there would trade an unguarded
// store for NO fleet memory, which is the worse of the two, so the store runs
// unlocked and the caller says so.
func lockDir(path string) (release func(), locked bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("score: open %s: %w", path, err)
	}
	if lerr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); lerr != nil {
		_ = f.Close()
		switch {
		case errors.Is(lerr, syscall.EWOULDBLOCK):
			return nil, false, fmt.Errorf("score: %s is held by another baton daemon; one writer per score directory", path)
		case errors.Is(lerr, syscall.ENOTSUP), errors.Is(lerr, syscall.EOPNOTSUPP), errors.Is(lerr, syscall.ENOLCK):
			return func() {}, false, nil
		default:
			return nil, false, fmt.Errorf("score: lock %s: %w", path, lerr)
		}
	}
	// The claim lives as long as the descriptor, so the returned func is the only
	// thing that may close f.
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}
