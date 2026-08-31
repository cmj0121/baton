//go:build !darwin && !linux

package score

import (
	"os"
	"time"
)

// fileIdentity returns the inode and change time behind a FileInfo, which the
// portable os.FileInfo does not expose. Both widen score.md's staleness
// fingerprint past mtime and size:
//
//   - most editors save by writing a temp file and renaming it over the target,
//     which gives the path a NEW inode;
//   - any write moves ctime, even where the filesystem's mtime granularity is a
//     whole second, or where a tool restores the previous mtime.
//
// Together they catch the same-size edit inside one mtime tick that size and
// mtime alone cannot see.
//
// Off darwin and linux there is no portable answer, so the fingerprint falls
// back to size and mtime alone — plus the always-stale window, which is what
// bounds a missed edit rather than merely narrowing it (see mdMovedLocked).
// baton ships only for darwin and linux; this exists to keep the package
// building elsewhere.
func fileIdentity(os.FileInfo) (inode uint64, ctime time.Time) {
	return 0, time.Time{}
}
