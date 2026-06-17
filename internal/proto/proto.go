// Package proto defines the semantic, versioned wire protocol spoken between
// baton frontends (clients) and the baton server over a Unix domain socket.
//
// Framing is newline-delimited JSON: clients send Command values up, the server
// sends ServerMsg values down. This is the only formal entry into the core.
package proto

// ProtocolVersion is negotiated on connect. Bump it on breaking wire changes.
const ProtocolVersion = "baton/0"

// Panel kinds carried on the wire.
const (
	KindShell = "shell" // a plain host shell (the default)
	KindAgent = "agent" // an agent CLI run as the panel process
)

// EventBufferSize is the per-client buffer of outbound server messages.
const EventBufferSize = 16

// Command is sent from a client to the server.
type Command struct {
	Action string `json:"action"`         // "hello" | "panel.list" | "panel.create" | "panel.close"
	Kind   string `json:"kind,omitempty"` // panel kind for "panel.create" (default "shell")
	ID     string `json:"id,omitempty"`   // target panel for "panel.close"
	Path   string `json:"path,omitempty"` // init command (binary path) for "panel.create"; empty = default shell
}

// Panel is the server-side view of a single live terminal.
type Panel struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`               // "shell" | "agent"
	Title    string `json:"title"`              // human label shown on the dashboard
	State    string `json:"state,omitempty"`    // lifecycle: spawning|running|idle|attention|exited
	Group    string `json:"group,omitempty"`    // work item the panel belongs to, if any
	Activity string `json:"activity,omitempty"` // short status line (mock telemetry for now)
}

// ServerMsg is broadcast or replied from the server to a client.
type ServerMsg struct {
	Type    string  `json:"type"`              // "welcome" | "panels" | "error"
	Version string  `json:"version,omitempty"` // set on "welcome"
	Error   string  `json:"error,omitempty"`   // set on "error"
	Panels  []Panel `json:"panels,omitempty"`  // full snapshot on "panels"
}
