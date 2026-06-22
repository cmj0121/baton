// Package plugin is baton's Lua plugin subsystem (docs/PLUGIN.md): the daemon loads
// one Lua file and, through the single `baton` table, lets it read the fleet, drive
// every core action, react to lifecycle events, and register commands and config.
//
// The Lua VM is single-threaded, so one worker goroutine owns the *lua.LState and is
// the only place Lua ever runs: loads, hooks, and commands are all funnelled onto it.
// Server events arrive through Dispatch as a non-blocking, lossy hand-off (like
// telemetry), so a slow hook never stalls the Monitor; synchronous work (Load,
// RunCommand) rides a request channel and blocks the caller until the worker runs it.
//
// The host is reached through the Host interface, satisfied by *server.Server, so the
// plugin can do nothing a frontend cannot and never imports the server.
package plugin

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/rs/zerolog/log"
	lua "github.com/yuin/gopher-lua"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/proto"
)

// Host is the set of core actions the baton.* table lands on — the same operations a
// socket command runs. *server.Server satisfies it.
type Host interface {
	Spawn(kind, command string, args []string, dir, group string) (string, error)
	Close(ids []string) error
	Respawn(id string) error
	Purge() int
	Signal(ids []string, name string) error
	Group(ids []string, name string) error
	Ungroup(ids []string, name string) error
	Rename(id, group, name string) error
	Move(ids []string, index int) error
	SetPinned(ids []string, pinned bool) error
	GroupShow(name string, count int) error
	PanelInfos() []proto.Panel
	GroupInfos() []proto.GroupView
	Notify(msg string)
}

// LoadResult is what a load produced for the daemon to apply: the merged effective
// config (defaults <- YAML <- Lua), the registered command list for the picker, and
// whether any panel.output handler was registered (so the server can gate the
// high-volume output emit).
type LoadResult struct {
	Config     config.Config
	Commands   []proto.PluginCommand
	WantOutput bool
}

// command is one registered baton.command: its name and description (shown in the
// picker) and the Lua function command.run invokes.
type command struct {
	name string
	desc string
	fn   *lua.LFunction
}

// event is one lifecycle event queued for the worker to fan out to its hooks.
type event struct {
	name   string
	fields map[string]any
}

// call is a synchronous unit of work for the worker thread (a load or a command run):
// the worker runs fn, then closes done to release the caller.
type call struct {
	fn   func()
	done chan struct{}
}

// Plugin owns the Lua world. Everything in the registries (L, hooks, commands, cfg)
// is touched only on the worker goroutine, so it needs no lock; cross-goroutine entry
// is exclusively through Dispatch (lossy) and do (synchronous).
type Plugin struct {
	host Host

	events chan event
	calls  chan call
	quit   chan struct{}
	done   chan struct{}

	// Worker-owned state. Rebuilt from scratch on every load, so a reload leaves no
	// stale hook or command behind.
	L        *lua.LState
	hooks    map[string][]*lua.LFunction
	commands []command
	cfg      config.Config
	loaded   bool // first load fires no server.reload; reloads do
}

// New starts the worker goroutine on a fresh VM and returns the plugin. Wire its
// Dispatch as the server's event sink and RunCommand as the command runner, then call
// Load. Close it on shutdown.
func New(host Host) *Plugin {
	p := &Plugin{
		host:   host,
		events: make(chan event, 1024),
		calls:  make(chan call),
		quit:   make(chan struct{}),
		done:   make(chan struct{}),
		hooks:  map[string][]*lua.LFunction{},
	}
	go p.worker()
	return p
}

// worker is the only goroutine that touches the LState. It builds an initial empty VM
// (so the baton table exists before the first Load) and then services synchronous
// calls and lossy events until quit.
func (p *Plugin) worker() {
	defer close(p.done)
	p.L = p.newState()
	for {
		select {
		case <-p.quit:
			if p.L != nil {
				p.L.Close()
			}
			return
		case c := <-p.calls:
			c.fn()
			close(c.done)
		case ev := <-p.events:
			p.dispatch(ev.name, ev.fields)
		}
	}
}

// do runs fn on the worker thread and blocks until it finishes. A no-op once quit, so
// a late Load/RunCommand after Close cannot hang.
func (p *Plugin) do(fn func()) {
	c := call{fn: fn, done: make(chan struct{})}
	select {
	case p.calls <- c:
		<-c.done
	case <-p.quit:
	}
}

// Dispatch is the server's event sink: a non-blocking, lossy hand-off to the worker.
// On a full queue the event is dropped rather than blocking the caller (which may hold
// the server lock) — hooks are best-effort, like telemetry.
func (p *Plugin) Dispatch(name string, fields map[string]any) {
	select {
	case p.events <- event{name: name, fields: fields}:
	default:
		log.Warn().Str("event", name).Msg("plugin event queue full, dropped")
	}
}

// Load (re)builds the Lua world from base and runs the file at path. It is used for
// both the first load and every reload: a fresh VM each time, so no registration from
// a previous version lingers. A missing file is a clean no-op; a Lua error is returned
// (the caller logs it, non-fatal) but the merged config and any commands registered
// before the error are still returned.
func (p *Plugin) Load(path string, base config.Config) (res LoadResult, err error) {
	p.do(func() { res, err = p.load(path, base) })
	return res, err
}

// RunCommand invokes a registered command by name on the worker thread, returning any
// Lua error. It is the backing of the wire's command.run.
func (p *Plugin) RunCommand(name string) (err error) {
	p.do(func() { err = p.runCommand(name) })
	return err
}

// Close stops the worker and tears down the VM. Safe to call once.
func (p *Plugin) Close() {
	close(p.quit)
	<-p.done
}

// load is the worker-thread body of Load: reset the world, run the file, fire the
// reload event, and collect the result.
func (p *Plugin) load(path string, base config.Config) (LoadResult, error) {
	if p.L != nil {
		p.L.Close()
	}
	p.hooks = map[string][]*lua.LFunction{}
	p.commands = nil
	p.cfg = base
	p.L = p.newState()

	firstLoad := !p.loaded
	p.loaded = true

	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		log.Debug().Str("path", path).Msg("no plugin file; running with config defaults")
		return p.result(), nil
	}

	if err := p.L.DoFile(path); err != nil {
		// Non-fatal: the daemon runs on with whatever (config/commands) ran before the
		// error. Surface it so the caller can log it.
		return p.result(), fmt.Errorf("load plugin %s: %w", path, err)
	}
	log.Info().Str("path", path).Int("commands", len(p.commands)).Msg("plugin loaded")

	if !firstLoad {
		p.dispatch("server.reload", map[string]any{})
	}
	return p.result(), nil
}

// result snapshots the registries into a LoadResult for the daemon to apply.
func (p *Plugin) result() LoadResult {
	cmds := make([]proto.PluginCommand, len(p.commands))
	for i, c := range p.commands {
		cmds[i] = proto.PluginCommand{Name: c.name, Desc: c.desc}
	}
	return LoadResult{Config: p.cfg, Commands: cmds, WantOutput: len(p.hooks["panel.output"]) > 0}
}

// runCommand invokes the named command's Lua function. Worker thread only.
func (p *Plugin) runCommand(name string) error {
	for _, c := range p.commands {
		if c.name == name {
			p.L.Push(c.fn)
			if err := p.L.PCall(0, 0, nil); err != nil {
				return fmt.Errorf("command %q: %w", name, err)
			}
			return nil
		}
	}
	return fmt.Errorf("no command named %q", name)
}

// dispatch fans an event out to its registered hooks. Worker thread only. A throwing
// or erroring hook is logged and isolated — one bad handler never stops the others or
// the worker.
func (p *Plugin) dispatch(name string, fields map[string]any) {
	for _, fn := range p.hooks[name] {
		p.L.Push(fn)
		p.L.Push(mapToTable(p.L, fields))
		if err := p.L.PCall(1, 0, nil); err != nil {
			log.Warn().Str("event", name).Err(err).Msg("plugin hook error")
		}
	}
}
