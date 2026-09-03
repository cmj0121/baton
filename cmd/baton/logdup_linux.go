//go:build linux

package main

import (
	"os"
	"syscall"
)

// swapFD makes the descriptor number to refer to the same open file as from,
// atomically, so a writer holding to never sees an invalid number and never
// needs a lock to be told the file changed.
//
// Linux has no dup2 on every architecture it builds for — arm64 dropped it — so
// this is dup3 with no flags, which is what the C library's dup2 calls there.
// The one difference that matters is that dup3 refuses equal descriptors with
// EINVAL where dup2 makes them a no-op, and that case is reachable: a parent
// that closed stdout before exec leaves fd 1 free for the log itself to land on.
func swapFD(from *os.File, to int) error {
	fd := int(from.Fd())
	if fd == to {
		return nil
	}
	return syscall.Dup3(fd, to, 0)
}
