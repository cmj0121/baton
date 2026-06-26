package plugin_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/plugin"
	"github.com/cmj0121/baton/internal/proto"
)

// spawnRec is one recorded baton.spawn the fake host saw.
type spawnRec struct {
	kind, command, dir, group string
	args                      []string
}

// groupRec is one recorded ids+name pair (baton.group / baton.ungroup).
type groupRec struct {
	ids  []string
	name string
}

// renameRec is one recorded baton.rename.
type renameRec struct {
	id, group, name string
}

// moveRec is one recorded baton.move.
type moveRec struct {
	ids   []string
	index int
}

// pinRec is one recorded baton.pin / baton.unpin.
type pinRec struct {
	ids    []string
	pinned bool
}

// signalRec is one recorded baton.signal.
type signalRec struct {
	ids  []string
	name string
}

// groupShowRec is one recorded baton.group_show.
type groupShowRec struct {
	name  string
	count int
}

// fakeHost is a stand-in for *server.Server: it records what the baton.* calls
// drive, so a test can assert the Lua surface lands on the host. Every method
// records its received arguments; the err* / ret* fields let a test steer the
// return so it can also assert the failure (nil, msg) and read marshaling paths.
type fakeHost struct {
	mu sync.Mutex

	spawned          []spawnRec
	closed           [][]string
	respawned        []string
	purgeCalls       int
	signals          []signalRec
	groups           []groupRec
	ungroups         []groupRec
	renames          []renameRec
	moves            []moveRec
	pins             []pinRec
	groupShows       []groupShowRec
	dispatched       []dispatchRec
	dispatchedGroups []dispatchRec
	enqueued         []dispatchRec
	notified         []string
	footer           string

	// return-value control.
	panels       []proto.Panel
	groupViews   []proto.GroupView
	purgeN       int
	spawnID      string
	spawnErr     error
	closeErr     error
	respawnErr   error
	signalErr    error
	groupErr     error
	ungroupErr   error
	renameErr    error
	moveErr      error
	pinErr       error
	groupShowErr error
	dispatchErr  error
}

type dispatchRec struct {
	id     string
	prompt string
}

func (h *fakeHost) Spawn(kind, command string, args []string, dir, group string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.spawned = append(h.spawned, spawnRec{kind, command, dir, group, args})
	id := h.spawnID
	if id == "" {
		id = "p1"
	}
	return id, h.spawnErr
}
func (h *fakeHost) Dispatch(id, prompt string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dispatched = append(h.dispatched, dispatchRec{id, prompt})
	return h.dispatchErr
}
func (h *fakeHost) DispatchGroup(group, prompt string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dispatchedGroups = append(h.dispatchedGroups, dispatchRec{group, prompt})
	return len(h.dispatchedGroups), h.dispatchErr
}
func (h *fakeHost) Enqueue(prompt, group string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enqueued = append(h.enqueued, dispatchRec{group, prompt})
	return "t1", h.dispatchErr
}
func (h *fakeHost) Close(ids []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = append(h.closed, ids)
	return h.closeErr
}
func (h *fakeHost) Respawn(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.respawned = append(h.respawned, id)
	return h.respawnErr
}
func (h *fakeHost) Purge() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.purgeCalls++
	return h.purgeN
}
func (h *fakeHost) Group(ids []string, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.groups = append(h.groups, groupRec{ids, name})
	return h.groupErr
}
func (h *fakeHost) Ungroup(ids []string, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ungroups = append(h.ungroups, groupRec{ids, name})
	return h.ungroupErr
}
func (h *fakeHost) Rename(id, group, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.renames = append(h.renames, renameRec{id, group, name})
	return h.renameErr
}
func (h *fakeHost) Move(ids []string, index int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.moves = append(h.moves, moveRec{ids, index})
	return h.moveErr
}
func (h *fakeHost) SetPinned(ids []string, pinned bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pins = append(h.pins, pinRec{ids, pinned})
	return h.pinErr
}
func (h *fakeHost) GroupShow(name string, count int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.groupShows = append(h.groupShows, groupShowRec{name, count})
	return h.groupShowErr
}
func (h *fakeHost) GroupInfos() []proto.GroupView {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.groupViews
}
func (h *fakeHost) Signal(ids []string, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.signals = append(h.signals, signalRec{ids, name})
	return h.signalErr
}
func (h *fakeHost) Notify(msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notified = append(h.notified, msg)
}
func (h *fakeHost) SetFooter(text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.footer = text
}
func (h *fakeHost) PanelInfos() []proto.Panel {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.panels
}

func (h *fakeHost) notifies() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.notified...)
}

func (h *fakeHost) footerText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.footer
}

// writeLua writes src to a temp .lua file and returns its path.
func writeLua(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plug-in.lua")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write lua: %v", err)
	}
	return path
}

// waitFor polls cond up to a second, so a test can wait on the async event worker
// without sleeping a fixed time.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within the deadline")
}

// TestLoadRegistersEverything runs a file that exercises every pillar and checks
// the merged config, the command list, the output-gate, and a load-time fleet call.
func TestLoadRegistersEverything(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `
		baton.config{ prefix = "ctrl+a", allow_name_conflict = true, replay_kb = 64, default_agent = "aider" }
		baton.agent{ name = "aider", command = "aider", args = { "--yes" } }
		baton.bind("D", "diff")
		baton.command{ name = "hi", desc = "say hi", run = function() baton.notify("hi") end }
		baton.on("panel.attention", function(pan) baton.notify("attn:"..pan.title) end)
		baton.on("panel.output", function(pan) end)
		baton.spawn{ kind = "agent", command = "claude", args = { "-x" }, dir = "/tmp" }
	`)

	res, err := p.Load(path, config.Config{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if res.Config.Prefix != "ctrl+a" {
		t.Errorf("prefix = %q, want ctrl+a", res.Config.Prefix)
	}
	if res.Config.Settings.AllowNameConflict == nil || !*res.Config.Settings.AllowNameConflict {
		t.Errorf("allow_name_conflict not applied: %+v", res.Config.Settings.AllowNameConflict)
	}
	if res.Config.Panel.ReplayKB != 64 {
		t.Errorf("replay_kb = %d, want 64", res.Config.Panel.ReplayKB)
	}
	if a, ok := res.Config.Panel.Agents["aider"]; !ok || a.Command != "aider" || len(a.Args) != 1 {
		t.Errorf("agent aider not registered: %+v", res.Config.Panel.Agents)
	}
	if res.Config.Keys["diff"] != "D" {
		t.Errorf("bind diff = %q, want D", res.Config.Keys["diff"])
	}
	if len(res.Commands) != 1 || res.Commands[0].Name != "hi" || res.Commands[0].Desc != "say hi" {
		t.Errorf("commands = %+v, want one 'hi'", res.Commands)
	}
	if !res.WantOutput {
		t.Error("WantOutput should be true when a panel.output handler is registered")
	}

	h.mu.Lock()
	spawned := h.spawned
	h.mu.Unlock()
	if len(spawned) != 1 || spawned[0].command != "claude" || spawned[0].dir != "/tmp" || len(spawned[0].args) != 1 {
		t.Errorf("load-time spawn not delivered to host: %+v", spawned)
	}
}

// TestDispatchVerb checks baton.dispatch(id, prompt) lands on the host's Dispatch,
// and that baton.spawn{prompt=…} dispatches the brief to the freshly spawned panel.
func TestDispatchVerb(t *testing.T) {
	h := &fakeHost{spawnID: "p7"}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `
		baton.dispatch("p3", "review the PR")
		baton.spawn{ kind = "agent", command = "claude", prompt = "write the tests" }
	`)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("load: %v", err)
	}

	h.mu.Lock()
	got := h.dispatched
	h.mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("want 2 dispatches (explicit + spawn prompt), got %+v", got)
	}
	if got[0] != (dispatchRec{"p3", "review the PR"}) {
		t.Errorf("explicit dispatch = %+v", got[0])
	}
	if got[1] != (dispatchRec{"p7", "write the tests"}) {
		t.Errorf("spawn-prompt dispatch = %+v (want it to target the new panel id)", got[1])
	}
}

// TestDispatchGroupVerb checks baton.dispatch_group(group, prompt) lands on the
// host's DispatchGroup and returns the reached count.
func TestDispatchGroupVerb(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `
		n = baton.dispatch_group("api", "refactor")
		if n ~= 1 then error("expected count 1, got "..tostring(n)) end
	`)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	h.mu.Lock()
	got := h.dispatchedGroups
	h.mu.Unlock()
	if len(got) != 1 || got[0] != (dispatchRec{"api", "refactor"}) {
		t.Fatalf("dispatch_group not delivered to host: %+v", got)
	}
}

// TestEnqueueVerb checks baton.enqueue{prompt,group} lands on the host's Enqueue
// and returns the new task id.
func TestEnqueueVerb(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `
		id = baton.enqueue{ prompt = "ship it", group = "api" }
		if id ~= "t1" then error("expected task id t1, got "..tostring(id)) end
	`)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	h.mu.Lock()
	got := h.enqueued
	h.mu.Unlock()
	if len(got) != 1 || got[0] != (dispatchRec{"api", "ship it"}) {
		t.Fatalf("enqueue not delivered to host: %+v", got)
	}
}

// TestSpawnPromptSurvivesDispatchError checks that a failing prompt dispatch does
// not strand the spawn: the panel id is still returned and the panel kept.
func TestSpawnPromptSurvivesDispatchError(t *testing.T) {
	h := &fakeHost{spawnID: "p9", dispatchErr: errors.New("busy")}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `
		id = baton.spawn{ kind = "agent", command = "claude", prompt = "go" }
		if id ~= "p9" then error("spawn should still return the id, got "..tostring(id)) end
	`)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("load: %v", err)
	}
}

// TestHookFires checks a dispatched event reaches its Lua handler, which calls back
// into the host with the event payload.
func TestHookFires(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `baton.on("panel.attention", function(pan) baton.notify("attn:"..pan.title) end)`)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("load: %v", err)
	}

	p.Dispatch("panel.attention", map[string]any{"title": "claude·api"})
	waitFor(t, func() bool { return len(h.notifies()) == 1 })
	if got := h.notifies()[0]; got != "attn:claude·api" {
		t.Errorf("hook notified %q, want attn:claude·api", got)
	}
}

// TestRunCommand runs a registered command and rejects an unknown one.
func TestRunCommand(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `baton.command{ name = "go", run = function() baton.notify("went") end }`)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("load: %v", err)
	}

	if err := p.RunCommand("go"); err != nil {
		t.Fatalf("run go: %v", err)
	}
	if n := h.notifies(); len(n) != 1 || n[0] != "went" {
		t.Errorf("command did not run: %+v", n)
	}
	if err := p.RunCommand("missing"); err == nil {
		t.Error("running an unknown command should error")
	}
}

// TestReloadIsFresh checks a second load rebuilds the world: the prior command and
// hooks are gone, replaced by the new file's.
func TestReloadIsFresh(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	first := writeLua(t, `baton.command{ name = "old", run = function() end }`)
	if _, err := p.Load(first, config.Config{}); err != nil {
		t.Fatalf("load first: %v", err)
	}
	second := writeLua(t, `baton.command{ name = "new", run = function() end }`)
	res, err := p.Load(second, config.Config{})
	if err != nil {
		t.Fatalf("load second: %v", err)
	}
	if len(res.Commands) != 1 || res.Commands[0].Name != "new" {
		t.Errorf("reload should leave only 'new', got %+v", res.Commands)
	}
	if err := p.RunCommand("old"); err == nil {
		t.Error("the old command should be gone after a reload")
	}
}

// TestMissingFileIsCleanNoop checks a missing plugin returns no error and keeps the
// YAML base config untouched.
func TestMissingFileIsCleanNoop(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	base := config.Config{Prefix: "ctrl+t"}
	res, err := p.Load(filepath.Join(t.TempDir(), "nope.lua"), base)
	if err != nil {
		t.Fatalf("missing file should be a clean no-op, got %v", err)
	}
	if res.Config.Prefix != "ctrl+t" {
		t.Errorf("base config should survive, prefix = %q", res.Config.Prefix)
	}
	if len(res.Commands) != 0 || res.WantOutput {
		t.Errorf("missing file should register nothing: %+v want=%v", res.Commands, res.WantOutput)
	}
}

// TestTokenFooterExample loads the shipped example plugin, feeds it agent output
// carrying a token count, and checks it sets the footer — validating both
// baton.footer and that the example file stays correct.
func TestTokenFooterExample(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	res, err := p.Load("../../examples/token-footer.lua", config.Config{})
	if err != nil {
		t.Fatalf("load example: %v", err)
	}
	if !res.WantOutput {
		t.Fatal("the token example registers panel.output, so WantOutput must be true")
	}

	// Two panels report token counts; the footer shows their sum.
	p.Dispatch("panel.output", map[string]any{"id": "1", "data": "\x1b[32mused 12,345 tokens\x1b[0m"})
	p.Dispatch("panel.output", map[string]any{"id": "2", "data": "100 tokens so far"})
	waitFor(t, func() bool { return h.footerText() == "⊙ 12445 tok" })

	// When a panel exits, its tally drops from the total.
	p.Dispatch("panel.exit", map[string]any{"id": "1"})
	waitFor(t, func() bool { return h.footerText() == "⊙ 100 tok" })
}

// TestLoadErrorIsNonFatal checks a Lua syntax error returns an error but still yields
// a usable result (with the base config), never panicking.
func TestLoadErrorIsNonFatal(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `this is not lua )(`)
	res, err := p.Load(path, config.Config{Prefix: "ctrl+t"})
	if err == nil {
		t.Fatal("a broken plugin should surface an error")
	}
	if res.Config.Prefix != "ctrl+t" {
		t.Errorf("base config should still come back on error, prefix = %q", res.Config.Prefix)
	}
}

// TestFilterTaskRewriteAndVeto exercises the synchronous task.pre hook: a hook can
// pass a brief through, rewrite the prompt (by string or table), or veto the task
// (by false or a drop table). Hooks chain, and the first veto stops the chain.
func TestFilterTaskRewriteAndVeto(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `
		baton.on("task.pre", function(t)
			if t.prompt == "drop me" then return false end       -- veto
			if t.prompt == "tag me" then return "[build] "..t.prompt end  -- string rewrite
			if t.prompt == "table me" then return { prompt = "rewritten" } end -- table rewrite
			if t.prompt == "table drop" then return { drop = true } end       -- table veto
		end)
	`)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("load: %v", err)
	}

	cases := []struct {
		in        string
		wantOut   string
		wantAllow bool
	}{
		{"keep me", "keep me", true},
		{"tag me", "[build] tag me", true},
		{"table me", "rewritten", true},
		{"drop me", "", false},
		{"table drop", "", false},
	}
	for _, c := range cases {
		out, allow := p.FilterTask(c.in, "build")
		if allow != c.wantAllow || (allow && out != c.wantOut) {
			t.Errorf("FilterTask(%q) = (%q, %v), want (%q, %v)", c.in, out, allow, c.wantOut, c.wantAllow)
		}
	}
}

// TestFilterTaskChains threads a brief through two hooks: the second sees the first
// one's rewrite, proving the chain accumulates.
func TestFilterTaskChains(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `
		baton.on("task.pre", function(t) return t.prompt.." [a]" end)
		baton.on("task.pre", function(t) return t.prompt.." [b]" end)
	`)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	out, allow := p.FilterTask("go", "")
	if !allow || out != "go [a] [b]" {
		t.Fatalf("chained filter = (%q, %v), want (\"go [a] [b]\", true)", out, allow)
	}
}

// TestFilterTaskFailsOpen confirms the fail-open contract: a throwing hook is
// skipped (the brief survives), and no hook at all passes through.
func TestFilterTaskFailsOpen(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	// No plugin loaded → no task.pre hook → pass-through.
	if out, allow := p.FilterTask("untouched", ""); out != "untouched" || !allow {
		t.Fatalf("no-hook filter = (%q, %v), want (\"untouched\", true)", out, allow)
	}

	path := writeLua(t, `baton.on("task.pre", function(t) error("boom") end)`)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("load: %v", err)
	}
	if out, allow := p.FilterTask("survive", ""); out != "survive" || !allow {
		t.Fatalf("a throwing hook should fail open, got (%q, %v)", out, allow)
	}
}

// TestFilterTaskAfterClose checks the quit path: once the worker is closed, the
// filter fails open rather than hanging.
func TestFilterTaskAfterClose(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	p.Close()

	if out, allow := p.FilterTask("late", "g"); out != "late" || !allow {
		t.Fatalf("filter after close should fail open, got (%q, %v)", out, allow)
	}
}

// TestFilterTaskConcurrent hammers FilterTask from many goroutines while the event
// worker also fires hooks, proving the synchronous filter serializes safely onto the
// single worker thread (run under -race). Each call carries its own result channel,
// so there is no shared state to tear.
func TestFilterTaskConcurrent(t *testing.T) {
	h := &fakeHost{}
	p := plugin.New(h)
	defer p.Close()

	path := writeLua(t, `
		baton.on("task.pre", function(t) return t.prompt.."!" end)
		baton.on("panel.attention", function(pan) end)
	`)
	if _, err := p.Load(path, config.Config{}); err != nil {
		t.Fatalf("load: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if out, allow := p.FilterTask("go", ""); !allow || out != "go!" {
					t.Errorf("concurrent filter = (%q, %v)", out, allow)
					return
				}
				p.Dispatch("panel.attention", map[string]any{"title": "x"})
			}
		}()
	}
	wg.Wait()
}
