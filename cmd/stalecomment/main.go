// Command stalecomment finds names that survive only in comments a branch adds.
//
// Usage: stalecomment [base-revision]
//
//	BASE=origin/main stalecomment
//
// Exit status:
//
//	0  comment lines were examined and every name in them exists in code
//	1  a name was found that exists in comments but not in code
//	2  NOTHING WAS CHECKED -- a failure, never a pass
//
// It is a developer tool, not part of the baton binary; `make build` and `make
// install` name ./cmd/baton explicitly, so this never ships.
package main

import (
	"os"

	"github.com/cmj0121/baton/internal/stalecomment"
)

// Swapped in the test, which needs the status without ending the test binary.
var exit = os.Exit

func main() {
	exit(stalecomment.Main(os.Args[1:], os.Stdout, os.Stderr))
}
