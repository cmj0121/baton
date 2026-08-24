//go:build !unix

package paths

import "os"

// ownedByCaller cannot be answered from a FileInfo off unix, where there is no
// uid in it to compare. The permission check in ensurePrivateDir still applies.
func ownedByCaller(os.FileInfo) bool { return true }
