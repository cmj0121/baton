//go:build !linux

package cgroup

import (
	"fmt"
	"os/exec"
)

// detectRoot reports that this host has no enforcement backend. cgroup v2 is a
// Linux interface; macOS has no equivalent that caps a process tree's CPU and
// memory, so a cap there needs a different backend entirely (a VM or container,
// or the per-process rlimits, which cannot hold a tree). The manager degrades to
// ModeNone with this as its reason rather than pretending.
func detectRoot() (string, error) {
	return "", fmt.Errorf("cgroup v2 is Linux-only")
}

// open has no descriptor to take. A handle off Linux has nothing to place a
// child into, so it holds its directory and confines nothing; in production it is
// never reached, because Prepare stops at ModeNone long before this.
func (h *Handle) open() error { return nil }

// closeFD has no descriptor to drop off Linux. Caller holds h.mu.
func (h *Handle) closeFD() error { return nil }

// Confine is a no-op where there is no cgroup to place the child into.
func (h *Handle) Confine(*exec.Cmd) error { return nil }
