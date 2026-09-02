//go:build unix

package control

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// sessionActor is the process's session id, prefixed.
//
// It goes through golang.org/x/sys/unix rather than the standard syscall
// package because syscall.Getsid is a BSD-family function: it exists on darwin
// and does NOT exist on linux, so the obvious spelling builds on a developer's
// laptop and fails the Linux build. That is how it reached main once already.
// x/sys is a direct dependency and internal/server's peercred files already
// reach for it the same way.
func sessionActor() string {
	sid, err := unix.Getsid(0)
	if err != nil {
		return ""
	}
	return "sid:" + strconv.Itoa(sid)
}
