//go:build !linux

package main

import (
	"os"
	"syscall"
)

// swapFD makes the descriptor number to refer to the same open file as from.
// See the linux build for what this is for and why it is split; everywhere else
// baton builds — darwin, and the BSDs — has dup2, which is a no-op rather than
// an error when the two numbers are equal.
func swapFD(from *os.File, to int) error {
	return syscall.Dup2(int(from.Fd()), to)
}
