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

// sessionProbe is the matching probe for those platforms. claimSession there
// grants every claim without recording one, so there is nothing a probe could
// read; false is the answer that leaves its caller tidying rather than
// signalling, which is the safe half to be wrong on.
//
// It being hardcoded false is the reason baton has two stop paths rather than
// one — see stopDaemon, which says so.
type sessionProbe struct{}

func openSessionProbe(string) *sessionProbe { return &sessionProbe{} }
func (p *sessionProbe) claimed() bool       { return false }
func (p *sessionProbe) close()              {}
