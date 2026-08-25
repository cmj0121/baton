package main

import (
	"strings"

	"github.com/cmj0121/baton/internal/agents"
	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/isolate"
	"github.com/cmj0121/baton/internal/proto"
)

// detectAgents works out which agent backends this machine can spawn: the
// built-in catalogue with the user's own panel.agents profiles layered over it,
// each marked with whether the command behind it is actually there.
//
// It reports the misses rather than dropping them. A name baton knows and did not
// find is the one thing the old filtering could not say, and the silence read as
// "this machine has one agent backend" when the truth was "one of the six baton
// knows". The cockpit shows the misses on the panel-config page, where you go to
// learn what the fleet could do; the spawn picker still lists only what can be
// spawned, because an entry you cannot choose is a trap wherever it appears.
//
// It runs in the DAEMON, on every config load — boot and reload alike — because
// the PATH that decides whether an agent can be spawned is the one the panels
// are spawned from. A cockpit attached over --remote would answer with the
// binaries on its own machine, which is a worse answer than none.
//
// Nothing here is written back to the config file. Which agent CLIs a machine
// happens to have installed is a fact about the machine, and serialising it into
// the user's file only makes stale entries for the next machine they open it on.
// The one line that is intent — panel.default-agent — is written by the cockpit,
// when the user picks one.
func detectAgents(cfg config.Config) []proto.AgentBackend {
	user := make([]agents.Backend, 0, len(cfg.Panel.Agents))
	for name, prof := range cfg.Panel.Agents {
		user = append(user, agents.Backend{
			Name:     name,
			Command:  strings.TrimSpace(prof.Command),
			Args:     prof.Args,
			Isolated: isolationIntended(prof),
		})
	}
	scanned := detectBackends(agents.Merge(user))

	out := make([]proto.AgentBackend, 0, len(scanned))
	for _, b := range scanned {
		out = append(out, proto.AgentBackend{
			Name:     b.Name,
			Command:  b.Command,
			Args:     b.Args,
			Homepage: b.Homepage,
			Missing:  b.Missing,
		})
	}
	return out
}

// detectBackends is the PATH scan, held as a variable so a test can answer for a
// machine it does not have without reaching into the agents package's own hooks.
var detectBackends = agents.Scan

// isolationIntended reports whether a profile meant to run inside a container.
// Such a profile is never tested against the host's PATH: its command runs in an
// image, so an absence here says nothing about it, and hiding the backend would
// lose one that works. It reads the raw value the same way AgentProfile.Isolation
// does — anything but a bare "none" is an intent to isolate, a value baton cannot
// parse included, so a typo cannot quietly become "not isolated".
func isolationIntended(a config.AgentProfile) bool {
	s := strings.TrimSpace(a.Isolate)
	return s != "" && !strings.EqualFold(s, string(isolate.ModeNone))
}
