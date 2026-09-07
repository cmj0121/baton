// Package docs exists so the site's index can be checked by `go test ./...`
// rather than by remembering.
//
// There is no Go code here and there should not be. The one test beside this
// file holds a rule nothing else in the pipeline can see: `make ci` does not
// read HTML, the stale-comment sweep reads only .go, and the EN/zh-TW parity
// check compares markdown against markdown. The landing page is the one place
// where adding a file does not make it appear, and SCORE.md -- the largest
// subsystem in the tree -- went unlisted from the day it was written until a
// hand audit found it.
package docs
