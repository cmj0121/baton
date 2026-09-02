//go:build !unix

package control

// sessionActor falls back to the process where there are no sessions to ask
// about.
//
// A per-command client then declares a different actor every invocation, which
// is the self-rotation this identity already documents as accepted rather than
// defended. The alternative — declaring nothing — is worse: an empty actor puts
// every out-of-panel client on this host into ONE rate-cap slot, which is the
// over-grouping that the panel-less identity exists to undo.
func sessionActor() string { return processActor() }
