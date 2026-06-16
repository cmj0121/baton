// Package proto defines the semantic, versioned wire protocol spoken between
// baton frontends (clients) and the baton server over a Unix domain socket.
//
// Framing is newline-delimited JSON: clients send Command values up, the server
// sends ServerMsg values down. This is the only formal entry into the core.
package proto

// ProtocolVersion is negotiated on connect. Bump it on breaking wire changes.
const ProtocolVersion = "baton/0"

// KindShell is the default panel kind: a plain host shell.
const KindShell = "shell"

// EventBufferSize is the per-client buffer of outbound server messages.
const EventBufferSize = 16

// Command is sent from a client to the server.
type Command struct {
	Action string `json:"action"`         // "hello" | "panel.list" | "panel.create"
	Kind   string `json:"kind,omitempty"` // panel kind for "panel.create" (default "shell")
}

// Panel is the server-side view of a single live terminal.
type Panel struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`  // "shell" (more kinds later)
	Title string `json:"title"` // human label shown on the dashboard
}

// ServerMsg is broadcast or replied from the server to a client.
type ServerMsg struct {
	Type    string  `json:"type"`              // "welcome" | "panels" | "error"
	Version string  `json:"version,omitempty"` // set on "welcome"
	Error   string  `json:"error,omitempty"`   // set on "error"
	Panels  []Panel `json:"panels,omitempty"`  // full snapshot on "panels"
}
