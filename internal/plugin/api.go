package plugin

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/rs/zerolog/log"
	lua "github.com/yuin/gopher-lua"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/proto"
)

// newState builds a fresh VM with the full standard library (the trust model is "your
// own code", docs/PLUGIN.md) and the single `baton` global wired to the host.
func (p *Plugin) newState() *lua.LState {
	L := lua.NewState()
	p.registerAPI(L)
	return L
}

// registerAPI installs the `baton` table. Every function closes over p, so it uses the
// live LState, host, and registries when called.
func (p *Plugin) registerAPI(L *lua.LState) {
	t := L.NewTable()
	fns := map[string]lua.LGFunction{
		// drive the fleet
		"spawn":      p.luaSpawn,
		"respawn":    p.luaRespawn,
		"close":      p.luaClose,
		"purge":      p.luaPurge,
		"signal":     p.luaSignal,
		"group":      p.luaGroup,
		"ungroup":    p.luaUngroup,
		"rename":     p.luaRename,
		"move":       p.luaMove,
		"pin":        p.luaPin,
		"unpin":      p.luaUnpin,
		"group_show": p.luaGroupShow,
		// read the fleet
		"panels": p.luaPanels,
		"panel":  p.luaPanel,
		"groups": p.luaGroups,
		// react and extend
		"on":      p.luaOn,
		"command": p.luaCommand,
		"config":  p.luaConfig,
		"agent":   p.luaAgent,
		"bind":    p.luaBind,
		// utilities
		"log":    p.luaLog,
		"notify": p.luaNotify,
		"footer": p.luaFooter,
	}
	for name, fn := range fns {
		t.RawSetString(name, L.NewFunction(fn))
	}
	L.SetGlobal("baton", t)
}

// --- drive the fleet ---

func (p *Plugin) luaSpawn(L *lua.LState) int {
	tbl := L.CheckTable(1)
	id, err := p.host.Spawn(
		fieldStr(tbl, "kind"),
		fieldStr(tbl, "command"),
		fieldStrSlice(tbl, "args"),
		fieldStr(tbl, "dir"),
		fieldStr(tbl, "group"),
	)
	if err != nil {
		return fail(L, err)
	}
	L.Push(lua.LString(id))
	return 1
}

func (p *Plugin) luaRespawn(L *lua.LState) int {
	return result(L, p.host.Respawn(L.CheckString(1)))
}

func (p *Plugin) luaClose(L *lua.LState) int {
	return result(L, p.host.Close(idsArg(L, 1)))
}

func (p *Plugin) luaPurge(L *lua.LState) int {
	L.Push(lua.LNumber(p.host.Purge()))
	return 1
}

func (p *Plugin) luaSignal(L *lua.LState) int {
	return result(L, p.host.Signal(idsArg(L, 1), signalToken(L, 2)))
}

// signalToken reads argument n as a signal: a name ("SIGTERM") or a number,
// accepted as either a Lua string or a Lua number so baton.signal(id, "SIGKILL")
// and baton.signal(id, 9) both work — signals.Lookup resolves either form. Any
// other type raises a Lua error, like the package's other typed arguments.
func signalToken(L *lua.LState, n int) string {
	switch v := L.Get(n).(type) {
	case lua.LString:
		return string(v)
	case lua.LNumber:
		return strconv.Itoa(int(v))
	default:
		L.RaiseError("signal must be a name or number, got %s", v.Type().String())
		return ""
	}
}

func (p *Plugin) luaGroup(L *lua.LState) int {
	return result(L, p.host.Group(idsArg(L, 1), L.CheckString(2)))
}

// luaUngroup takes either a group name (string → dissolve it) or a list of panel ids
// (table → drop just those members), mirroring panel.ungroup.
func (p *Plugin) luaUngroup(L *lua.LState) int {
	switch v := L.Get(1).(type) {
	case lua.LString:
		return result(L, p.host.Ungroup(nil, string(v)))
	case *lua.LTable:
		return result(L, p.host.Ungroup(idsArg(L, 1), ""))
	default:
		return fail(L, errors.New("baton.ungroup takes a group name or a list of ids"))
	}
}

func (p *Plugin) luaRename(L *lua.LState) int {
	tbl := L.CheckTable(1)
	return result(L, p.host.Rename(fieldStr(tbl, "id"), fieldStr(tbl, "group"), fieldStr(tbl, "name")))
}

func (p *Plugin) luaMove(L *lua.LState) int {
	return result(L, p.host.Move(idsArg(L, 1), L.CheckInt(2)))
}

func (p *Plugin) luaPin(L *lua.LState) int {
	return result(L, p.host.SetPinned(idsArg(L, 1), true))
}

func (p *Plugin) luaUnpin(L *lua.LState) int {
	return result(L, p.host.SetPinned(idsArg(L, 1), false))
}

func (p *Plugin) luaGroupShow(L *lua.LState) int {
	return result(L, p.host.GroupShow(L.CheckString(1), L.CheckInt(2)))
}

// --- read the fleet ---

func (p *Plugin) luaPanels(L *lua.LState) int {
	arr := L.NewTable()
	for _, pan := range p.host.PanelInfos() {
		arr.Append(panelTable(L, pan))
	}
	L.Push(arr)
	return 1
}

func (p *Plugin) luaPanel(L *lua.LState) int {
	id := L.CheckString(1)
	for _, pan := range p.host.PanelInfos() {
		if pan.ID == id {
			L.Push(panelTable(L, pan))
			return 1
		}
	}
	L.Push(lua.LNil)
	return 1
}

func (p *Plugin) luaGroups(L *lua.LState) int {
	arr := L.NewTable()
	for _, g := range p.host.GroupInfos() {
		t := L.NewTable()
		t.RawSetString("group", lua.LString(g.Group))
		t.RawSetString("shown", lua.LNumber(g.Shown))
		arr.Append(t)
	}
	L.Push(arr)
	return 1
}

// --- react and extend ---

func (p *Plugin) luaOn(L *lua.LState) int {
	event := L.CheckString(1)
	fn := L.CheckFunction(2)
	p.hooks[event] = append(p.hooks[event], fn)
	return 0
}

func (p *Plugin) luaCommand(L *lua.LState) int {
	tbl := L.CheckTable(1)
	name := fieldStr(tbl, "name")
	fn, ok := tbl.RawGetString("run").(*lua.LFunction)
	if name == "" || !ok {
		return fail(L, errors.New("baton.command needs a name and a run function"))
	}
	p.commands = append(p.commands, command{name: name, desc: fieldStr(tbl, "desc"), fn: fn})
	return 0
}

// luaConfig mutates the effective config. Only the keys present are touched, so a
// plugin overrides exactly what it sets and leaves the YAML base for the rest.
func (p *Plugin) luaConfig(L *lua.LState) int {
	tbl := L.CheckTable(1)
	if s := fieldStr(tbl, "prefix"); s != "" {
		p.cfg.Prefix = s
	}
	if s := fieldStr(tbl, "default_agent"); s != "" {
		p.cfg.Panel.DefaultAgent = s
	}
	if s := fieldStr(tbl, "workdir"); s != "" {
		p.cfg.Panel.Workdir = s
	}
	if s := fieldStr(tbl, "shell"); s != "" {
		p.cfg.Panel.Shell = s
	}
	if s := fieldStr(tbl, "diff_command"); s != "" {
		p.cfg.Panel.DiffCommand = s
	}
	if n := fieldInt(tbl, "replay_kb"); n > 0 {
		p.cfg.Panel.ReplayKB = n
	}
	if b := fieldBoolPtr(tbl, "allow_name_conflict"); b != nil {
		p.cfg.Settings.AllowNameConflict = b
	}
	if b := fieldBoolPtr(tbl, "confirm_close"); b != nil {
		p.cfg.Settings.ConfirmClose = b
	}
	if b := fieldBoolPtr(tbl, "bell"); b != nil {
		p.cfg.Settings.Bell = b
	}
	if b := fieldBoolPtr(tbl, "mouse"); b != nil {
		p.cfg.Settings.Mouse = b
	}
	return 0
}

func (p *Plugin) luaAgent(L *lua.LState) int {
	tbl := L.CheckTable(1)
	name := fieldStr(tbl, "name")
	command := fieldStr(tbl, "command")
	if name == "" || command == "" {
		return fail(L, errors.New("baton.agent needs a name and a command"))
	}
	if p.cfg.Panel.Agents == nil {
		p.cfg.Panel.Agents = map[string]config.AgentProfile{}
	}
	p.cfg.Panel.Agents[name] = config.AgentProfile{Command: command, Args: fieldStrSlice(tbl, "args")}
	return 0
}

// luaBind maps a key to an action's stable name (config stores action -> key).
func (p *Plugin) luaBind(L *lua.LState) int {
	key := L.CheckString(1)
	action := L.CheckString(2)
	if p.cfg.Keys == nil {
		p.cfg.Keys = map[string]string{}
	}
	p.cfg.Keys[action] = key
	return 0
}

// --- utilities ---

func (p *Plugin) luaLog(L *lua.LState) int {
	level := L.CheckString(1)
	msg := L.CheckString(2)
	ev := log.Info()
	switch level {
	case "debug":
		ev = log.Debug()
	case "warn":
		ev = log.Warn()
	case "error":
		ev = log.Error()
	}
	ev.Str("from", "plugin").Msg(msg)
	return 0
}

func (p *Plugin) luaNotify(L *lua.LState) int {
	p.host.Notify(L.CheckString(1))
	return 0
}

// luaFooter sets the persistent footer segment; an empty string clears it.
func (p *Plugin) luaFooter(L *lua.LState) int {
	p.host.SetFooter(L.CheckString(1))
	return 0
}

// --- helpers ---

// result pushes the ok,err Lua idiom: true on success, (nil, message) on error.
func result(L *lua.LState, err error) int {
	if err != nil {
		return fail(L, err)
	}
	L.Push(lua.LTrue)
	return 1
}

// fail pushes (nil, message) — two return values a plugin can pattern-match.
func fail(L *lua.LState, err error) int {
	L.Push(lua.LNil)
	L.Push(lua.LString(err.Error()))
	return 2
}

// idsArg reads argument n as a panel id (string → one) or a list of ids (table).
func idsArg(L *lua.LState, n int) []string {
	switch v := L.Get(n).(type) {
	case lua.LString:
		return []string{string(v)}
	case *lua.LTable:
		return collectStrings(v, "panel id")
	}
	return nil
}

// collectStrings reads a Lua table's array part as a string slice. A non-string
// element is a plugin-author mistake (e.g. a bare number where a string was
// meant); dropping it silently hides the bug, so warn and skip it while the call
// proceeds with what it could read. what labels the values in that warning.
func collectStrings(tbl *lua.LTable, what string) []string {
	var out []string
	tbl.ForEach(func(_, val lua.LValue) {
		if s, ok := val.(lua.LString); ok {
			out = append(out, string(s))
			return
		}
		log.Warn().Str("what", what).Str("type", val.Type().String()).Msg("plugin: ignoring non-string element")
	})
	return out
}

func fieldStr(tbl *lua.LTable, key string) string {
	if s, ok := tbl.RawGetString(key).(lua.LString); ok {
		return string(s)
	}
	return ""
}

func fieldInt(tbl *lua.LTable, key string) int {
	if n, ok := tbl.RawGetString(key).(lua.LNumber); ok {
		return int(n)
	}
	return 0
}

// fieldBoolPtr distinguishes an explicit boolean from an absent key, so a config
// toggle the plugin did not set stays at its YAML/default value.
func fieldBoolPtr(tbl *lua.LTable, key string) *bool {
	if b, ok := tbl.RawGetString(key).(lua.LBool); ok {
		v := bool(b)
		return &v
	}
	return nil
}

// fieldStrSlice reads a table field's array part as a string slice (e.g. args).
func fieldStrSlice(tbl *lua.LTable, key string) []string {
	inner, ok := tbl.RawGetString(key).(*lua.LTable)
	if !ok {
		return nil
	}
	return collectStrings(inner, key)
}

// panelTable renders a wire panel as the Lua table hooks and reads receive.
func panelTable(L *lua.LState, p proto.Panel) *lua.LTable {
	t := L.NewTable()
	t.RawSetString("id", lua.LString(p.ID))
	t.RawSetString("kind", lua.LString(p.Kind))
	t.RawSetString("title", lua.LString(p.Title))
	t.RawSetString("state", lua.LString(p.State))
	t.RawSetString("group", lua.LString(p.Group))
	t.RawSetString("activity", lua.LString(p.Activity))
	t.RawSetString("pinned", lua.LBool(p.Pinned))
	return t
}

// mapToTable converts an event payload into a Lua table, recursing through nested
// maps and slices so structured fields arrive as tables.
func mapToTable(L *lua.LState, m map[string]any) *lua.LTable {
	t := L.NewTable()
	for k, v := range m {
		t.RawSetString(k, toLua(L, v))
	}
	return t
}

func toLua(L *lua.LState, v any) lua.LValue {
	switch x := v.(type) {
	case nil:
		return lua.LNil
	case string:
		return lua.LString(x)
	case bool:
		return lua.LBool(x)
	case int:
		return lua.LNumber(x)
	case int64:
		return lua.LNumber(x)
	case float64:
		return lua.LNumber(x)
	case map[string]any:
		return mapToTable(L, x)
	case []any:
		t := L.NewTable()
		for _, e := range x {
			t.Append(toLua(L, e))
		}
		return t
	default:
		return lua.LString(fmt.Sprintf("%v", x))
	}
}
