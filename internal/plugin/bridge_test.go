package plugin_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/plugin"
	"github.com/cmj0121/baton/internal/proto"
)

// run loads a Lua snippet against h, executing it at load time. The snippet is the
// body of the plugin: any baton.* call it makes drives (or reads) the host. It
// fails the test on a Lua error, so callers that expect a clean run stay terse.
func run(t *testing.T, h *fakeHost, src string) {
	t.Helper()
	p := plugin.New(h)
	defer p.Close()
	path := writeLua(t, src)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("load: %v", err)
	}
}

// TestDriveTheFleet runs one Lua snippet per fleet-driving function and asserts the
// host received exactly the arguments the bridge marshaled from Lua.
func TestDriveTheFleet(t *testing.T) {
	h := &fakeHost{}
	run(t, h, `
		baton.respawn("p7")
		baton.close({ "a", "b" })
		baton.close("solo")
		baton.purge()
		baton.signal({ "x", "y" }, "SIGTERM")
		baton.group({ "g1", "g2" }, "feat")
		baton.ungroup("oldgroup")
		baton.ungroup({ "m1", "m2" })
		baton.rename{ id = "p3", group = "feat", name = "renamed" }
		baton.move({ "p4" }, 2)
		baton.pin({ "p5" })
		baton.unpin("p6")
		baton.group_show("feat", 3)
	`)

	if got := h.respawned; !reflect.DeepEqual(got, []string{"p7"}) {
		t.Errorf("respawn = %v, want [p7]", got)
	}
	if got := h.closed; !reflect.DeepEqual(got, [][]string{{"a", "b"}, {"solo"}}) {
		t.Errorf("close = %v, want [[a b] [solo]]", got)
	}
	if h.purgeCalls != 1 {
		t.Errorf("purge calls = %d, want 1", h.purgeCalls)
	}
	if got := h.signals; len(got) != 1 || got[0].name != "SIGTERM" ||
		!reflect.DeepEqual(got[0].ids, []string{"x", "y"}) {
		t.Errorf("signal = %+v, want ids[x y] name SIGTERM", got)
	}
	if got := h.groups; len(got) != 1 || got[0].name != "feat" ||
		!reflect.DeepEqual(got[0].ids, []string{"g1", "g2"}) {
		t.Errorf("group = %+v, want ids[g1 g2] name feat", got)
	}
	// ungroup by name → (nil ids, "oldgroup"); ungroup by table → (ids, "").
	if got := h.ungroups; len(got) != 2 ||
		got[0].name != "oldgroup" || got[0].ids != nil ||
		got[1].name != "" || !reflect.DeepEqual(got[1].ids, []string{"m1", "m2"}) {
		t.Errorf("ungroup = %+v, want [{nil oldgroup} {[m1 m2] \"\"}]", got)
	}
	if got := h.renames; len(got) != 1 || got[0] != (renameRec{"p3", "feat", "renamed"}) {
		t.Errorf("rename = %+v, want {p3 feat renamed}", got)
	}
	if got := h.moves; len(got) != 1 || got[0].index != 2 ||
		!reflect.DeepEqual(got[0].ids, []string{"p4"}) {
		t.Errorf("move = %+v, want ids[p4] index 2", got)
	}
	if got := h.pins; len(got) != 2 ||
		!got[0].pinned || !reflect.DeepEqual(got[0].ids, []string{"p5"}) ||
		got[1].pinned || !reflect.DeepEqual(got[1].ids, []string{"p6"}) {
		t.Errorf("pin/unpin = %+v, want pin[p5]=true unpin[p6]=false", got)
	}
	if got := h.groupShows; len(got) != 1 || got[0] != (groupShowRec{"feat", 3}) {
		t.Errorf("group_show = %+v, want {feat 3}", got)
	}
}

// TestResultMarshaling checks the ok,err idiom: a nil host error returns true to
// Lua, while a host error returns (nil, message).
func TestResultMarshaling(t *testing.T) {
	t.Run("success returns true", func(t *testing.T) {
		h := &fakeHost{}
		run(t, h, `
			local ok, err = baton.respawn("p1")
			baton.notify(tostring(ok))
			baton.notify(tostring(err))
		`)
		if got := h.notifies(); len(got) != 2 || got[0] != "true" || got[1] != "nil" {
			t.Errorf("success marshaling = %v, want [true nil]", got)
		}
	})

	t.Run("error returns nil and message", func(t *testing.T) {
		h := &fakeHost{respawnErr: errors.New("boom")}
		run(t, h, `
			local ok, err = baton.respawn("p1")
			baton.notify(tostring(ok))
			baton.notify(tostring(err))
		`)
		if got := h.notifies(); len(got) != 2 || got[0] != "nil" || got[1] != "boom" {
			t.Errorf("error marshaling = %v, want [nil boom]", got)
		}
	})
}

// TestSpawnMarshaling covers the spawn return (an id string) and its error path.
func TestSpawnMarshaling(t *testing.T) {
	t.Run("returns the id", func(t *testing.T) {
		h := &fakeHost{spawnID: "panel-42"}
		run(t, h, `
			local id = baton.spawn{ kind = "agent", command = "claude" }
			baton.notify(id)
		`)
		if got := h.notifies(); len(got) != 1 || got[0] != "panel-42" {
			t.Errorf("spawn id = %v, want [panel-42]", got)
		}
	})

	t.Run("error path", func(t *testing.T) {
		h := &fakeHost{spawnErr: errors.New("no slot")}
		run(t, h, `
			local id, err = baton.spawn{ command = "x" }
			baton.notify(tostring(id))
			baton.notify(tostring(err))
		`)
		if got := h.notifies(); len(got) != 2 || got[0] != "nil" || got[1] != "no slot" {
			t.Errorf("spawn error = %v, want [nil 'no slot']", got)
		}
	})
}

// TestPurgeReturnsCount checks baton.purge returns the host's count as a number.
func TestPurgeReturnsCount(t *testing.T) {
	h := &fakeHost{purgeN: 5}
	run(t, h, `baton.notify(tostring(baton.purge()))`)
	if got := h.notifies(); len(got) != 1 || got[0] != "5" {
		t.Errorf("purge count = %v, want [5]", got)
	}
}

// TestIdsArg drives idsArg through every shape: a single string, a list of strings,
// a list that drops non-string members, and a non-id type (→ nil ids).
func TestIdsArg(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want []string
	}{
		{"single string", `"solo"`, []string{"solo"}},
		{"list of strings", `{ "a", "b", "c" }`, []string{"a", "b", "c"}},
		{"list drops non-strings", `{ "a", 7, "b", true }`, []string{"a", "b"}},
		{"number arg yields nil", `42`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &fakeHost{}
			run(t, h, `baton.close(`+tt.arg+`)`)
			if got := h.closed; len(got) != 1 || !reflect.DeepEqual(got[0], tt.want) {
				t.Errorf("idsArg(%s) = %v, want %v", tt.arg, got, tt.want)
			}
		})
	}
}

// TestUngroupBadArg checks the (nil, message) failure when ungroup gets neither a
// string nor a table.
func TestUngroupBadArg(t *testing.T) {
	h := &fakeHost{}
	run(t, h, `
		local ok, err = baton.ungroup(42)
		baton.notify(tostring(ok))
		baton.notify(err)
	`)
	if got := h.notifies(); len(got) != 2 || got[0] != "nil" ||
		got[1] != "baton.ungroup takes a group name or a list of ids" {
		t.Errorf("ungroup bad arg = %v", got)
	}
	if len(h.ungroups) != 0 {
		t.Errorf("ungroup should not reach the host on a bad arg: %+v", h.ungroups)
	}
}

// TestBadArgTypeRaises checks the gopher-lua CheckString/CheckInt guards raise a Lua
// error (surfaced as a load error) when a baton.* call gets the wrong type.
func TestBadArgTypeRaises(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"respawn wants string", `baton.respawn({})`},
		{"signal wants name string", `baton.signal("p1", {})`},
		{"move wants int index", `baton.move("p1", "two")`},
		{"group_show wants count int", `baton.group_show("g", "x")`},
		{"rename wants a table", `baton.rename("not a table")`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &fakeHost{}
			p := plugin.New(h)
			defer p.Close()
			path := writeLua(t, tt.src)
			if _, err := p.Load(path, config.Config{}); err == nil {
				t.Errorf("%s should raise a Lua error", tt.src)
			}
		})
	}
}

// TestReadPanels checks baton.panels marshals each proto.Panel into a Lua table with
// the right fields and types, and that panel order/count is preserved.
func TestReadPanels(t *testing.T) {
	h := &fakeHost{panels: []proto.Panel{
		{ID: "p1", Kind: "agent", Title: "claude", State: "running", Group: "feat", Activity: "thinking", Pinned: true},
		{ID: "p2", Kind: "shell", Title: "bash"},
	}}
	run(t, h, `
		local ps = baton.panels()
		baton.notify(tostring(#ps))
		baton.notify(ps[1].id .. "/" .. ps[1].kind .. "/" .. ps[1].title)
		baton.notify(ps[1].state .. "/" .. ps[1].group .. "/" .. ps[1].activity)
		baton.notify(tostring(ps[1].pinned))
		baton.notify(tostring(ps[2].pinned))
	`)
	want := []string{"2", "p1/agent/claude", "running/feat/thinking", "true", "false"}
	if got := h.notifies(); !reflect.DeepEqual(got, want) {
		t.Errorf("panels marshaling = %v, want %v", got, want)
	}
}

// TestReadPanel covers baton.panel(id): a hit returns the matching panel table, a
// miss returns nil.
func TestReadPanel(t *testing.T) {
	h := &fakeHost{panels: []proto.Panel{
		{ID: "p1", Title: "first"},
		{ID: "p2", Title: "second"},
	}}
	run(t, h, `
		baton.notify(baton.panel("p2").title)
		baton.notify(tostring(baton.panel("nope")))
	`)
	if got := h.notifies(); !reflect.DeepEqual(got, []string{"second", "nil"}) {
		t.Errorf("panel lookup = %v, want [second nil]", got)
	}
}

// TestReadGroups checks baton.groups marshals each GroupView into a table with the
// group name and shown count.
func TestReadGroups(t *testing.T) {
	h := &fakeHost{groupViews: []proto.GroupView{
		{Group: "feat", Shown: 2},
		{Group: "bugfix", Shown: 0},
	}}
	run(t, h, `
		local gs = baton.groups()
		baton.notify(tostring(#gs))
		baton.notify(gs[1].group .. "=" .. tostring(gs[1].shown))
		baton.notify(gs[2].group .. "=" .. tostring(gs[2].shown))
	`)
	want := []string{"2", "feat=2", "bugfix=0"}
	if got := h.notifies(); !reflect.DeepEqual(got, want) {
		t.Errorf("groups marshaling = %v, want %v", got, want)
	}
}

// TestLogLevels exercises baton.log at each level — the level switch and the default
// (info) branch — plus an unknown level falling through to info. A bad level type is
// guarded by CheckString and must raise.
func TestLogLevels(t *testing.T) {
	h := &fakeHost{}
	run(t, h, `
		baton.log("debug", "d")
		baton.log("info", "i")
		baton.log("warn", "w")
		baton.log("error", "e")
		baton.log("trace", "fallback-to-info")
	`)
	// log writes to zerolog, not the host; reaching here without a Lua error is the
	// assertion that every level branch ran cleanly.

	h2 := &fakeHost{}
	p := plugin.New(h2)
	defer p.Close()
	path := writeLua(t, `baton.log({}, "msg")`)
	if _, err := p.Load(path, config.Config{}); err == nil {
		t.Error("baton.log with a non-string level should raise")
	}
}

// TestToLuaKinds drives toLua across every value kind by dispatching an event whose
// payload (built by mapToTable→toLua) carries a string, bool, number, nested map,
// list, and a fallback (time-like) value the default branch stringifies.
func TestToLuaKinds(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()
	// The hook reads each field back and notifies its type+value, so the test can
	// assert toLua produced the right Lua kind.
	path := writeLua(t, `
		baton.on("probe", function(e)
			baton.notify(type(e.s) .. ":" .. e.s)
			baton.notify(type(e.b) .. ":" .. tostring(e.b))
			baton.notify(type(e.i) .. ":" .. tostring(e.i))
			baton.notify(type(e.i64) .. ":" .. tostring(e.i64))
			baton.notify(type(e.f) .. ":" .. tostring(e.f))
			baton.notify(type(e.nested) .. ":" .. e.nested.inner)
			baton.notify(type(e.list) .. ":" .. e.list[1] .. e.list[2])
			baton.notify(type(e.nilv))
			baton.notify(type(e.other) .. ":" .. e.other)
		end)
	`)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("load: %v", err)
	}

	p.Dispatch("probe", map[string]any{
		"s":      "hi",
		"b":      true,
		"i":      int(3),
		"i64":    int64(9),
		"f":      float64(2.5),
		"nested": map[string]any{"inner": "deep"},
		"list":   []any{"a", "b"},
		"nilv":   nil,
		"other":  struct{ X int }{X: 1}, // hits the default fmt.Sprintf branch
	})

	waitFor(t, func() bool { return len(h.notifies()) == 9 })
	got := h.notifies()
	want := []string{
		"string:hi",
		"boolean:true",
		"number:3",
		"number:9",
		"number:2.5",
		"table:deep",
		"table:ab",
		"nil",
		"string:{1}",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("toLua kinds = %v, want %v", got, want)
	}
}

// TestFieldIntAbsent covers the fieldInt zero-default branch: a config table without
// replay_kb leaves the base value untouched (replay_kb=0 means "not set").
func TestFieldIntAbsent(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()
	base := config.Config{}
	base.Panel.ReplayKB = 99
	path := writeLua(t, `baton.config{ prefix = "p" }`) // no replay_kb
	res, err := p.Load(path, base)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if res.Config.Panel.ReplayKB != 99 {
		t.Errorf("absent replay_kb should leave base 99, got %d", res.Config.Panel.ReplayKB)
	}
}
