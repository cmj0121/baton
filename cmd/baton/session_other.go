//go:build !unix

package main

// claimSession is the no-op claim for platforms with no advisory file locking.
// Binding the socket remains the guard there; it is enough in every case but a
// simultaneous cold start against a socket a crash left behind (see the unix
// build of this file). baton ships only for darwin and linux, so this exists to
// keep the package building elsewhere rather than to be relied on.
func claimSession(string) (release func(), held bool, err error) {
	return func() {}, true, nil
}
