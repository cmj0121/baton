package mcp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/proto"
)

// peerNames is the worker's half of peer interaction, in table order.
var peerNames = []string{"baton_peers", "baton_peek", "baton_tell"}

// peerFleet is a daemon with one peers-on group, "work", holding the caller
// (me), a second agent (mate) and a shell, plus an agent in another group that
// none of them may reach. /bin/cat stands in for an agent CLI: it holds its pty
// open and echoes what it is typed, which is how a test reads a send back.
type peerFleet struct {
	sock                      string
	me, mate, shell, outsider string
	cockpit                   *control.Client
}

func startPeerFleet(t *testing.T) peerFleet {
	t.Helper()
	f := peerFleet{sock: startServer(t)}
	c, err := control.DialSocket(f.sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	f.cockpit = c

	spawn := func(agent, group string) string {
		id, err := c.SpawnPanel(agent, nil, "")
		if err != nil {
			t.Fatalf("spawn: %v", err)
		}
		if err := c.Do(proto.Command{Action: "panel.group", Group: group, IDs: []string{id}}); err != nil {
			t.Fatalf("group %s: %v", id, err)
		}
		return id
	}
	f.me = spawn("/bin/cat", "work")
	f.mate = spawn("/bin/cat", "work")
	f.shell = spawn("", "work")
	f.outsider = spawn("/bin/cat", "elsewhere")
	if err := c.GroupPeers("work", true); err != nil {
		t.Fatalf("peers on: %v", err)
	}
	return f
}

// worker runs reqs through the WORKER table, every dial declaring panel self —
// what `baton mcp --score-only` does inside the panel baton spawned it in.
func (f peerFleet) worker(t *testing.T, self string, reqs ...string) []testResp {
	t.Helper()
	s := NewWorker("9.9.9")
	s.dial = func() (*control.Client, error) { return control.DialSocket(f.sock, "", self, "") }
	return serveReqs(t, s, reqs...)
}

func call(name string, arguments map[string]any) string {
	req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": arguments},
	})
	return string(req)
}

// one runs a single tool call as self and returns its text and whether the
// result was flagged as an error.
func (f peerFleet) one(t *testing.T, self, name string, arguments map[string]any) (text string, isErr bool) {
	t.Helper()
	resps := f.worker(t, self, call(name, arguments))
	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("%s: want one tool result, got %+v", name, resps)
	}
	return contentText(t, resps[0].Result), resps[0].Result["isError"] == true
}

// peekUntil polls baton_peek until target's output holds want, and returns it.
func (f peerFleet) peekUntil(t *testing.T, target, want string) string {
	t.Helper()
	var last string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		text, isErr := f.one(t, f.me, "baton_peek", map[string]any{"id": target})
		if isErr {
			t.Fatalf("baton_peek %s was refused: %s", target, text)
		}
		if last = text; strings.Contains(last, want) {
			return last
		}
	}
	t.Fatalf("panel %s never showed %q; baton_peek gives %q", target, want, last)
	return ""
}

// TestWorkerToolList pins D11: a worker's tools/list is the memory's write tool
// and the three peer tools, always — it does not depend on a daemon, a panel or
// a flag, none of which this test has.
func TestWorkerToolList(t *testing.T) {
	resps := serveReqs(t, NewWorker("9.9.9"), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if len(resps) != 1 {
		t.Fatalf("want one response, got %+v", resps)
	}
	listed, _ := resps[0].Result["tools"].([]any)
	byName := map[string]map[string]any{}
	var names []string
	for _, tl := range listed {
		m := tl.(map[string]any)
		names = append(names, m["name"].(string))
		byName[m["name"].(string)] = m
	}
	if want := append([]string{"score_submit"}, peerNames...); !slices.Equal(names, want) {
		t.Fatalf("worker tools/list = %v, want %v", names, want)
	}

	// The descriptions are the only instruction a worker gets about these tools.
	for _, name := range peerNames {
		desc := byName[name]["description"].(string)
		for _, must := range []string{"operator has switched peer interaction on for your group", "the reason says why"} {
			if !strings.Contains(desc, must) {
				t.Errorf("%s must say %q, its description is: %s", name, must, desc)
			}
		}
	}
	if desc := byName["baton_tell"]["description"].(string); !strings.Contains(desc, "press Enter") || !strings.Contains(desc, "one line") {
		t.Errorf("baton_tell must say it types one line and presses Enter: %s", desc)
	}

	required := func(name string) []string {
		schema := byName[name]["inputSchema"].(map[string]any)
		var out []string
		for _, r := range schema["required"].([]any) {
			out = append(out, r.(string))
		}
		return out
	}
	if _, has := byName["baton_peers"]["inputSchema"].(map[string]any)["required"]; has {
		t.Error("baton_peers takes no arguments")
	}
	if got := required("baton_peek"); !slices.Equal(got, []string{"id"}) {
		t.Errorf("baton_peek requires %v, want [id]", got)
	}
	if got := required("baton_tell"); !slices.Equal(got, []string{"id", "text"}) {
		t.Errorf("baton_tell requires %v, want [id text]", got)
	}
}

// TestConductorTableUnchanged holds the other half of D11: the conductor's
// table is what it was before the peer tools existed. The list is spelled out so
// that adding to it is an edit here, and ToolNames — what the conductor's
// briefing is checked against — is held to the same list.
func TestConductorTableUnchanged(t *testing.T) {
	want := []string{
		"baton_list", "baton_spawn", "baton_send", "baton_attention", "baton_resolve",
		"baton_dispatch", "baton_enqueue", "baton_queue", "baton_reorder", "baton_dispatch_group",
		"baton_group", "baton_rename", "baton_pin", "baton_unpin", "baton_signal", "baton_close",
		"score_submit", "score_merge", "score_reword", "score_lower",
	}
	if got := toolNames(New("9.9.9").tools); !slices.Equal(got, want) {
		t.Errorf("conductor table = %v\nwant %v", got, want)
	}
	if got := ToolNames(); !slices.Equal(got, want) {
		t.Errorf("ToolNames = %v\nwant %v", got, want)
	}
}

// TestPeerToolsRoundTrip drives the three tools against real panels: list the
// group, type into a shell and an agent, read both back as plain text.
func TestPeerToolsRoundTrip(t *testing.T) {
	f := startPeerFleet(t)

	text, isErr := f.one(t, f.me, "baton_peers", nil)
	if isErr {
		t.Fatalf("baton_peers was refused: %s", text)
	}
	var peers []map[string]any
	if err := json.Unmarshal([]byte(text), &peers); err != nil {
		t.Fatalf("baton_peers should answer JSON, got %q: %v", text, err)
	}
	var ids, kinds []string
	for _, p := range peers {
		ids = append(ids, p["id"].(string))
		kinds = append(kinds, p["kind"].(string))
		if p["title"] == "" || p["state"] == nil {
			t.Errorf("a peer should carry its title and state, got %v", p)
		}
	}
	// Neither the caller nor the panel in the other group: this is the peer
	// list, not the fleet every connection is also broadcast.
	if !slices.Equal(ids, []string{f.mate, f.shell}) || !slices.Equal(kinds, []string{"agent", "shell"}) {
		t.Fatalf("baton_peers = %v %v, want %v [agent shell]", ids, kinds, []string{f.mate, f.shell})
	}

	// A shell runs what it is told: the arithmetic only appears evaluated if the
	// line was typed AND submitted.
	text, isErr = f.one(t, f.me, "baton_tell", map[string]any{"id": f.shell, "text": "echo peer-$((40+2))"})
	if isErr || !strings.Contains(text, "("+f.shell+")") || !strings.HasPrefix(text, "sent ") {
		t.Fatalf("baton_tell should hand back the daemon's notice naming the shell, got %q (error=%v)", text, isErr)
	}
	f.peekUntil(t, f.shell, "peer-42")

	// The shell now prints a coloured line. What comes back holds the text and
	// none of the sequence: ESC stripped alone would leave "[31mred-ok[0m".
	if text, isErr = f.one(t, f.me, "baton_tell", map[string]any{"id": f.shell, "text": `printf '\033[31mred-%s\033[0m\n' ok`}); isErr {
		t.Fatalf("baton_tell: %s", text)
	}
	out := f.peekUntil(t, f.shell, "red-ok")
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("baton_peek must not hand a model an escape byte: %q", out)
	}
	if !slices.Contains(strings.Split(out, "\n"), "red-ok") {
		t.Errorf("the coloured line should read as the bare text \"red-ok\", got %q", out)
	}

	// An agent is told who is typing.
	meTitle := ""
	fleet, err := f.cockpit.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, p := range fleet {
		if p.ID == f.me {
			meTitle = p.Title
		}
	}
	if text, isErr = f.one(t, f.me, "baton_tell", map[string]any{"id": f.mate, "text": "hello-mate"}); isErr {
		t.Fatalf("baton_tell: %s", text)
	}
	f.peekUntil(t, f.mate, "[from "+meTitle+" ("+f.me+")] hello-mate")
}

// TestPeekSize: the size a caller names reaches the daemon, and a caller that
// names none gets the default rather than the daemon's much smaller one.
func TestPeekSize(t *testing.T) {
	f := startPeerFleet(t)
	// 3000 bytes of output, more than the daemon's own default window and less
	// than the tool's.
	if text, isErr := f.one(t, f.me, "baton_tell", map[string]any{"id": f.shell, "text": `i=0; while [ $i -lt 100 ]; do echo "line-$i-xxxxxxxxxxxxxxxxxxxx"; i=$((i+1)); done; echo fin`}); isErr {
		t.Fatalf("baton_tell: %s", text)
	}
	all := f.peekUntil(t, f.shell, "\nfin")
	if !strings.Contains(all, "line-10-") {
		t.Errorf("the default size should reach back ~4096 bytes, got %d bytes: %q", len(all), all)
	}
	small, isErr := f.one(t, f.me, "baton_peek", map[string]any{"id": f.shell, "bytes": 64})
	if isErr || len(small) > 64 || !strings.Contains(small, "fin") {
		t.Errorf("bytes=64 should answer at most the last 64 bytes, got %d (error=%v): %q", len(small), isErr, small)
	}
}

// TestPeerRefusalsAreToolErrors: every refusal reaches the model as a tool
// error whose text is the daemon's reason, intact.
func TestPeerRefusalsAreToolErrors(t *testing.T) {
	f := startPeerFleet(t)

	refused := func(when, self, name string, arguments map[string]any, want string) {
		t.Helper()
		text, isErr := f.one(t, self, name, arguments)
		if !isErr || !strings.HasPrefix(text, "peer: ") || !strings.Contains(text, want) {
			t.Errorf("%s %s: want a tool error with the daemon's reason naming %q, got %q (error=%v)", name, when, want, text, isErr)
		}
	}

	tell := map[string]any{"id": f.outsider, "text": "x"}
	refused("out of group", f.me, "baton_peek", map[string]any{"id": f.outsider}, "is not in your group")
	refused("out of group", f.me, "baton_tell", tell, "is not in your group")
	refused("at itself", f.me, "baton_peek", map[string]any{"id": f.me}, "your own panel")
	refused("at itself", f.me, "baton_tell", map[string]any{"id": f.me, "text": "x"}, "your own panel")

	// A server started outside a panel declares none, and is told so.
	refused("outside a panel", "", "baton_peers", nil, "declared no panel")

	// The operator turns the flag off: the same three calls that worked are
	// refused, and say what to ask for.
	if err := f.cockpit.GroupPeers("work", false); err != nil {
		t.Fatalf("peers off: %v", err)
	}
	const off = `off for group "work"`
	refused("with peers off", f.me, "baton_peers", nil, off)
	refused("with peers off", f.me, "baton_peek", map[string]any{"id": f.mate}, off)
	refused("with peers off", f.me, "baton_tell", map[string]any{"id": f.mate, "text": "x"}, off)

	// The tool's own argument guards are tool errors too, and never reach the
	// daemon — these would otherwise read as its "peers off".
	for name, arguments := range map[string]map[string]any{
		"baton_peek": {},
		"baton_tell": {"id": f.mate},
	} {
		if text, isErr := f.one(t, f.me, name, arguments); !isErr || !strings.Contains(text, "required") {
			t.Errorf("%s with a missing argument: want a tool error saying what is required, got %q (error=%v)", name, text, isErr)
		}
	}
	if text, isErr := f.one(t, f.me, "baton_tell", map[string]any{"text": "x"}); !isErr || !strings.Contains(text, "required") {
		t.Errorf("baton_tell with no id: want a tool error, got %q (error=%v)", text, isErr)
	}
}

// TestWorkerDeclaresItsPanel is the link the whole feature hangs on: the peer
// verbs are authorised on the panel a connection declared, and a worker's MCP
// server learns which panel that is from the environment baton spawned it in.
// This goes through the production dial — no override — so it is the env that
// is under test.
func TestWorkerDeclaresItsPanel(t *testing.T) {
	f := startPeerFleet(t)
	t.Setenv("BATON_SOCK", f.sock)
	t.Setenv("BATON_ROLE", "")

	list := func() (string, bool) {
		resps := serveReqs(t, NewWorker("9.9.9"), call("baton_peers", nil))
		return contentText(t, resps[0].Result), resps[0].Result["isError"] == true
	}

	t.Setenv("BATON_PANEL_ID", f.me)
	if text, isErr := list(); isErr || !strings.Contains(text, `"id": "`+f.mate+`"`) {
		t.Fatalf("inside panel %s the worker server should list its group-mates, got %q (error=%v)", f.me, text, isErr)
	}

	t.Setenv("BATON_PANEL_ID", "")
	if text, isErr := list(); !isErr || !strings.Contains(text, "declared no panel") {
		t.Fatalf("outside a panel the call should be refused for want of one, got %q (error=%v)", text, isErr)
	}
}

// TestPlainTail is the scrub a peeked tail goes through, case by case.
func TestPlainTail(t *testing.T) {
	for name, tc := range map[string]struct{ raw, want string }{
		"plain text is untouched":         {"one\ntwo", "one\ntwo"},
		"a colour sequence goes whole":    {"\x1b[1;31mred\x1b[0m", "red"},
		"an OSC title goes whole":         {"\x1b]0;evil title\x07shown", "shown"},
		"an OSC 52 clipboard write goes":  {"a\x1b]52;c;ZXZpbA==\x1b\\b", "ab"},
		"cursor movement goes":            {"\x1b[2J\x1b[Hhome", "home"},
		"CRLF becomes a line break":       {"one\r\ntwo\r\n", "one\ntwo"},
		"lone controls are dropped":       {"a\x07b\x00c\x08d", "abcd"},
		"a C1 introducer is dropped":      {"a\u009b31mb", "a31mb"},
		"bidi and zero-width are dropped": {"a\u202eb\u200bc", "abc"},
		"an undecodable byte is dropped":  {"a\xffb", "ab"},
		// The tail is cut by size. 面 is e9 9d a2: lose its first byte and what
		// leads is 0x9d, which an escape parser reads as an 8-bit OSC introducer.
		"a head cut inside a character":    {"\x9d\xa2abc", "abc"},
		"a CJK character cut at the head":  {"\x9d\xa2板 ok\n$ ", "板 ok\n$"},
		"a CJK character cut at the end":   {"ok 面\xe6\x9d", "ok 面"},
		"a stray C1 byte mid-buffer":       {"a\x9dbc\x07d\nnext", "abcd\nnext"},
		"a stray DCS byte keeps the rest":  {"a\x90bc", "abc"},
		"sequences beside a cut character": {"\xa2\x1b[32mgreen\x1b[0m", "green"},
		"tabs are kept":                    {"a\tb", "a\tb"},
		"non-ASCII is kept":                {"面板 ✓ é", "面板 ✓ é"},
		"line ends are trimmed":            {"one   \ntwo\t", "one\ntwo"},
		"outer blank lines are trimmed":    {"\n\none\n\ntwo\n\n", "one\n\ntwo"},
		"nothing is nothing":               {"", ""},
		"only sequences is nothing":        {"\x1b[2J\x1b[H\r\n", ""},
	} {
		if got := plainTail([]byte(tc.raw)); got != tc.want {
			t.Errorf("%s: plainTail(%q) = %q, want %q", name, tc.raw, got, tc.want)
		}
	}
}

// TestPeekOfASilentPanel: an empty tail is said in words, not handed back as an
// empty result a model could read as a failure.
func TestPeekOfASilentPanel(t *testing.T) {
	f := startPeerFleet(t)
	// /bin/cat has printed nothing and nobody has typed into it.
	text, isErr := f.one(t, f.me, "baton_peek", map[string]any{"id": f.mate})
	if isErr || text != "(panel "+f.mate+" has no output to show)" {
		t.Errorf("a silent panel should peek as a sentence, got %q (error=%v)", text, isErr)
	}
}
