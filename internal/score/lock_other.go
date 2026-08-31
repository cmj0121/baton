//go:build !unix

package score

// lockDir is the no-op claim for platforms with no advisory file locking. baton
// ships only for darwin and linux, so this exists to keep the package building
// elsewhere; the single-writer rule degrades to the statement in Open's comment
// rather than an enforced one, which the caller reports through Unlocked.
func lockDir(string) (release func(), locked bool, err error) {
	return func() {}, false, nil
}
