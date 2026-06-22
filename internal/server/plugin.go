package server

import (
	"encoding/json"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// This file is the server's side of the Lua plugin subsystem (docs/PLUGIN.md): the
// event emit, the wiring setters, and the exported host methods the plugin's
// baton.* calls land on. Every host method is a thin exported skin over the same
// core action a socket command runs, so a plugin can do nothing a frontend cannot
// and the two can never drift in what an action means.

// panelFields is the event payload for a panel — the same fields a frontend sees,
// shaped as the map the Lua worker turns into a table. Callers add per-event extras
// (from/to, exit_code, data) before emitting.
func panelFields(p panel.Panel) map[string]any {
	return map[string]any{
		"id":       p.ID,
		"kind":     p.Kind.String(),
		"title":    p.Title,
		"state":    p.State.String(),
		"group":    p.Group,
		"activity": p.Activity,
		"pinned":   p.Pinned,
	}
}

// emit posts an event to the plugin worker. It is a non-blocking hand-off (the sink
// drops on a full queue), so it is safe to call under s.mu — it never re-enters the
// server. A nil sink (no plugin) is a no-op.
func (s *Server) emit(name string, fields map[string]any) {
	if s.eventSink != nil {
		s.eventSink(name, fields)
	}
}

// SetEventSink wires the plugin's event worker. Call once before Serve; the sink is
// read by the hot paths without a lock, so it must be set before the fleet serves.
func (s *Server) SetEventSink(fn func(name string, fields map[string]any)) { s.eventSink = fn }

// SetOutputEvents toggles the high-volume panel.output emit. The plugin turns it on
// only while a panel.output handler is registered, so the output path costs nothing
// otherwise. Safe to call any time (atomic).
func (s *Server) SetOutputEvents(on bool) { s.outputEvents.Store(on) }

// SetRunCommand wires the callback a command.run dispatches to — the plugin's
// command runner. Call under no lock before/while serving; stored under mu.
func (s *Server) SetRunCommand(fn func(name string) error) {
	s.mu.Lock()
	s.onRunCommand = fn
	s.mu.Unlock()
}

// SetClientConfig publishes the merged effective config served on config.get. The
// daemon sets it after loading YAML and running the plugin; a reload refreshes it.
func (s *Server) SetClientConfig(cfg json.RawMessage) {
	s.mu.Lock()
	s.clientConfig = cfg
	s.mu.Unlock()
}

// SetPluginCommands publishes the plugin command list the picker shows. Refreshed on
// each (re)load; the previous set is replaced wholesale.
func (s *Server) SetPluginCommands(cmds []proto.PluginCommand) {
	s.mu.Lock()
	s.pluginCmds = cmds
	s.mu.Unlock()
}

// PushConfig re-broadcasts the current config/command set to every attached client.
// The daemon calls it after a reload so open cockpits pick up new keymaps and picker
// commands without reattaching.
func (s *Server) PushConfig() {
	s.mu.Lock()
	msg := proto.ServerMsg{Type: "config", Config: s.clientConfig, Commands: s.pluginCmds, Footer: s.footerText}
	s.mu.Unlock()
	s.broadcast(msg)
}

// SetFooter sets the persistent footer segment a plugin shows (baton.footer) and
// pushes it live to every attached cockpit. An empty string clears it. It is held so
// a freshly attaching client gets the current value on its config snapshot.
func (s *Server) SetFooter(text string) {
	s.mu.Lock()
	s.footerText = text
	s.mu.Unlock()
	s.broadcast(proto.ServerMsg{Type: "footer", Footer: text})
}

// Notify surfaces a transient notice to every attached cockpit — the backing of the
// plugin's baton.notify. It is best-effort, like telemetry: a client whose buffer is
// full simply misses it.
func (s *Server) Notify(msg string) {
	s.broadcast(proto.ServerMsg{Type: "notice", Notice: msg})
	log.Info().Str("notice", msg).Msg("plugin notice")
}

// --- Exported host methods: the baton.* fleet API the plugin calls. Each mirrors
// the matching socket action, broadcasting and persisting the change exactly as the
// wire path does, so a plugin-driven mutation reaches every client and survives a
// restart.

// Spawn creates a panel and, when group is non-empty, files it under that work item;
// it returns the new panel's id. It is baton.spawn.
func (s *Server) Spawn(kind, command string, args []string, dir, group string) (string, error) {
	id, err := s.createPanel(kind, command, args, dir)
	if err != nil {
		return "", err
	}
	if group != "" {
		if err := s.groupPanels([]string{id}, group); err != nil {
			// The panel exists; surface the grouping failure but keep the panel and
			// still broadcast it, so a name clash does not strand a live process.
			s.broadcastFleet()
			return id, err
		}
	}
	s.broadcastFleet()
	return id, nil
}

// Close retires the listed panels. It is baton.close.
func (s *Server) Close(ids []string) error {
	if err := s.closePanels(ids); err != nil {
		return err
	}
	s.broadcastFleet()
	return nil
}

// Respawn re-runs an exited panel from its frozen spec. It is baton.respawn.
func (s *Server) Respawn(id string) error {
	if err := s.respawnPanel(id); err != nil {
		return err
	}
	s.broadcastFleet()
	return nil
}

// Purge drops every exited panel, returning how many went. It is baton.purge.
func (s *Server) Purge() int {
	n := s.purgeExited()
	if n > 0 {
		s.broadcastFleet()
	}
	return n
}

// Signal delivers a named signal to the listed panels' process groups. It is
// baton.signal; it broadcasts nothing, mirroring the wire path (an exit it triggers
// flows back through onPanelExit).
func (s *Server) Signal(ids []string, name string) error {
	return s.signalPanels(ids, name)
}

// Group files the listed panels under one work item. It is baton.group.
func (s *Server) Group(ids []string, name string) error {
	if err := s.groupPanels(ids, name); err != nil {
		return err
	}
	s.broadcastFleet()
	return nil
}

// Ungroup removes panels from their group (by ids) or dissolves a whole named group.
// It is baton.ungroup.
func (s *Server) Ungroup(ids []string, name string) error {
	if err := s.ungroup(ids, name); err != nil {
		return err
	}
	s.broadcastFleet()
	return nil
}

// Rename retitles a panel (by id) or a whole group (by name). It is baton.rename.
func (s *Server) Rename(id, group, name string) error {
	if err := s.rename(id, group, name); err != nil {
		return err
	}
	s.broadcastFleet()
	return nil
}

// Move reorders the fleet, lifting the listed panels to index. It is baton.move.
func (s *Server) Move(ids []string, index int) error {
	if err := s.movePanels(ids, index); err != nil {
		return err
	}
	s.broadcastFleet()
	return nil
}

// SetPinned marks the listed panels pinned (or not). It is baton.pin / baton.unpin.
func (s *Server) SetPinned(ids []string, pinned bool) error {
	if err := s.setPinned(ids, pinned); err != nil {
		return err
	}
	s.broadcastFleet()
	return nil
}

// GroupShow sets a group's live-tile count. It is baton.group_show.
func (s *Server) GroupShow(name string, count int) error {
	if err := s.setGroupShown(name, count); err != nil {
		return err
	}
	s.broadcastFleet()
	return nil
}

// PanelInfos returns the current fleet as wire panels — the read behind
// baton.panels / baton.panel.
func (s *Server) PanelInfos() []proto.Panel {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]proto.Panel, len(s.panels))
	for i, p := range s.panels {
		out[i] = p.ToProto()
	}
	return out
}

// GroupInfos returns each group's view settings — the read behind baton.groups.
func (s *Server) GroupInfos() []proto.GroupView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]proto.GroupView, 0, len(s.groupShown))
	for g, shown := range s.groupShown {
		if shown != 0 {
			out = append(out, proto.GroupView{Group: g, Shown: shown})
		}
	}
	return out
}
