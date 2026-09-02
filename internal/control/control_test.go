package control_test

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/cmj0121/baton/internal/control"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

func startServer(t *testing.T) string {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = server.New(ln).Serve() }()
	return sock
}

// TestControlRoundtrips drives the fleet through the control client: spawn,
// list, group, send input, and close all resolve synchronously.
func TestControlRoundtrips(t *testing.T) {
	sock := startServer(t)

	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	id, err := c.Spawn(proto.Command{Action: "panel.create", Kind: proto.KindShell})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if id == "" {
		t.Fatal("spawn returned an empty id")
	}

	panels, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(panels) != 1 || panels[0].ID != id {
		t.Fatalf("list should show the spawned panel, got %+v", panels)
	}

	if err := c.Do(proto.Command{Action: "panel.group", Group: "work", IDs: []string{id}}); err != nil {
		t.Fatalf("group: %v", err)
	}
	if panels, _ = c.List(); panels[0].Group != "work" {
		t.Fatalf("panel should be grouped, got %+v", panels[0])
	}

	// Prompt injection into the panel resolves without error.
	if err := c.Do(proto.Command{Action: "panel.input", ID: id, Data: []byte("echo hi\n")}); err != nil {
		t.Fatalf("send input: %v", err)
	}

	if err := c.Do(proto.Command{Action: "panel.close", IDs: []string{id}}); err != nil {
		t.Fatalf("close: %v", err)
	}
	if panels, _ = c.List(); len(panels) != 0 {
		t.Fatalf("fleet should be empty after close, got %+v", panels)
	}
}

// TestControlHelpers exercises the semantic wrappers shared with the MCP tools:
// SpawnPanel (agent and shell), ListJSON, and SendText (submit on/off).
func TestControlHelpers(t *testing.T) {
	sock := startServer(t)

	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Agent spawn (with args) and a plain shell spawn.
	agentID, err := c.SpawnPanel("/bin/cat", []string{"-u"}, t.TempDir())
	if err != nil {
		t.Fatalf("spawn agent: %v", err)
	}
	shellID, err := c.SpawnPanel("", nil, "")
	if err != nil {
		t.Fatalf("spawn shell: %v", err)
	}

	js, err := c.ListJSON()
	if err != nil {
		t.Fatalf("list json: %v", err)
	}
	if !strings.Contains(js, agentID) || !strings.Contains(js, shellID) {
		t.Fatalf("ListJSON should mention both panels, got:\n%s", js)
	}

	if err := c.SendText(agentID, "submitted", true); err != nil {
		t.Fatalf("send submit: %v", err)
	}
	if err := c.SendText(agentID, "no newline", false); err != nil {
		t.Fatalf("send no-submit: %v", err)
	}

	// Dispatch records a brief and is accepted; an unknown panel surfaces the
	// server's error through the client.
	if err := c.Dispatch(agentID, "land the fix"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := c.Dispatch("999", "nope"); err == nil {
		t.Fatal("dispatch to an unknown panel should error")
	}

	// Group dispatch reaches a real work item, and errors on an unknown one.
	if err := c.Do(proto.Command{Action: "panel.group", Group: "team", IDs: []string{agentID}}); err != nil {
		t.Fatalf("group: %v", err)
	}
	if err := c.DispatchGroup("team", "ship it"); err != nil {
		t.Fatalf("dispatch-group: %v", err)
	}
	if err := c.DispatchGroup("ghost", "nope"); err == nil {
		t.Fatal("dispatch-group to an unknown group should error")
	}

	// Enqueue a backlog task and read it back through the list.
	if err := c.Enqueue("queued work", "team"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	tasks, err := c.Tasks()
	if err != nil {
		t.Fatalf("tasks: %v", err)
	}
	var queuedID string
	for _, tk := range tasks {
		if tk.Prompt == "queued work" {
			queuedID = tk.ID
		}
	}
	if queuedID == "" {
		t.Fatalf("the enqueued task should appear in the backlog, got %+v", tasks)
	}

	// The same backlog renders as indented JSON for the ctl/MCP surfaces.
	js, err = c.TasksJSON()
	if err != nil {
		t.Fatalf("tasks json: %v", err)
	}
	if !strings.Contains(js, queuedID) {
		t.Fatalf("TasksJSON should mention the queued task, got:\n%s", js)
	}

	// Cancel that one task by id, then enqueue another and drain the whole
	// backlog — both leave the queue empty.
	if err := c.CancelTask(queuedID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := c.Enqueue("more work", ""); err != nil {
		t.Fatalf("enqueue again: %v", err)
	}
	if err := c.DrainQueue(); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if tasks, err = c.Tasks(); err != nil {
		t.Fatalf("tasks after drain: %v", err)
	}
	for _, tk := range tasks {
		// Drain clears only the unassigned backlog; tasks already bound to a
		// panel are left to finish.
		if tk.Panel == "" && tk.Status == "queued" {
			t.Fatalf("drain should clear the unassigned backlog, got %+v", tasks)
		}
	}
}

// TestControlDialErrors covers the connect-failure path: dialling a socket that
// no server is listening on surfaces a clear error rather than a nil client.
func TestControlDialErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.sock")
	if _, err := control.DialSocket(missing, "", "", ""); err == nil {
		t.Fatal("dialling an absent socket should fail")
	}

	// Cancelling an unknown task id is rejected by the server and surfaced.
	sock := startServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.CancelTask("t999"); err == nil {
		t.Fatal("cancelling an unknown task should error")
	}

	// Once the connection is closed, the write paths fail fast rather than block:
	// every wrapper surfaces the encode error.
	_ = c.Close()
	if err := c.Enqueue("after close", ""); err == nil {
		t.Fatal("enqueue on a closed client should error")
	}
	if _, err := c.Tasks(); err == nil {
		t.Fatal("tasks on a closed client should error")
	}
	if _, err := c.TasksJSON(); err == nil {
		t.Fatal("tasks-json on a closed client should error")
	}
}

// TestControlQueueOps exercises the backlog wrappers that reorder or spawn:
// EnqueueSpawn, PromoteTask, and DemoteTask, over a real server.
func TestControlQueueOps(t *testing.T) {
	sock := startServer(t)

	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	// EnqueueSpawn records a spawn-on-demand backlog task that carries the
	// command shape and the close-on-done flag.
	if err := c.EnqueueSpawn("do the thing", "team", "/bin/cat", []string{"-u"}, t.TempDir(), true); err != nil {
		t.Fatalf("enqueue-spawn: %v", err)
	}
	// A second plain task so there are two entries to reorder.
	if err := c.Enqueue("second", "team"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	tasks, err := c.Tasks()
	if err != nil {
		t.Fatalf("tasks: %v", err)
	}
	var spawnID, secondID string
	for _, tk := range tasks {
		switch tk.Prompt {
		case "do the thing":
			spawnID = tk.ID
		case "second":
			secondID = tk.ID
		}
	}
	if spawnID == "" || secondID == "" {
		t.Fatalf("both enqueued tasks should appear, got %+v", tasks)
	}

	// Promote the tail task to the head, then demote it back — both are accepted
	// by the server and reorder the visible backlog.
	if err := c.PromoteTask(secondID); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if tasks, err = c.Tasks(); err != nil {
		t.Fatalf("tasks after promote: %v", err)
	}
	if len(tasks) < 2 || tasks[0].ID != secondID {
		t.Fatalf("promoted task should lead the backlog, got %+v", tasks)
	}
	if err := c.DemoteTask(secondID); err != nil {
		t.Fatalf("demote: %v", err)
	}
	if tasks, err = c.Tasks(); err != nil {
		t.Fatalf("tasks after demote: %v", err)
	}
	if len(tasks) < 2 || tasks[len(tasks)-1].ID != secondID {
		t.Fatalf("demoted task should trail the backlog, got %+v", tasks)
	}

	// Unknown ids are rejected by the server and surfaced through the wrappers.
	if err := c.PromoteTask("nope"); err == nil {
		t.Fatal("promoting an unknown task should error")
	}
	if err := c.DemoteTask("nope"); err == nil {
		t.Fatal("demoting an unknown task should error")
	}
}

// TestControlClosedClient covers the write-failure fast paths of the remaining
// wrappers: once the connection is closed every send-first method surfaces the
// encode error instead of blocking or panicking.
func TestControlClosedClient(t *testing.T) {
	sock := startServer(t)
	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()

	// Spawn calls List first, so its initial send fails on the closed conn.
	if _, err := c.Spawn(proto.Command{Action: "panel.create", Kind: proto.KindShell}); err == nil {
		t.Fatal("spawn on a closed client should error")
	}
	// ListJSON wraps List; the underlying send fails.
	if _, err := c.ListJSON(); err == nil {
		t.Fatal("list-json on a closed client should error")
	}
	if _, err := c.SpawnPanel("/bin/cat", nil, ""); err == nil {
		t.Fatal("spawn-panel on a closed client should error")
	}
	if err := c.EnqueueSpawn("x", "", "/bin/cat", nil, "", false); err == nil {
		t.Fatal("enqueue-spawn on a closed client should error")
	}
	if err := c.PromoteTask("x"); err == nil {
		t.Fatal("promote on a closed client should error")
	}
	if err := c.DemoteTask("x"); err == nil {
		t.Fatal("demote on a closed client should error")
	}
	if err := c.DrainQueue(); err == nil {
		t.Fatal("drain on a closed client should error")
	}
}

// TestControlDialHandshakeFails covers DialSocket's post-connect failure path:
// the dial succeeds but the peer closes without ever sending the panels
// snapshot, so readUntilPanels drains to EOF and Dial returns an error.
func TestControlDialHandshakeFails(t *testing.T) {
	sock := filepath.Join(shortDir(t), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// Accept one connection and hang up immediately, never sending a snapshot.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}()

	if _, err := control.DialSocket(sock, "", "", ""); err == nil {
		t.Fatal("dial against a peer that never sends panels should fail")
	}
}

// TestHelloDeclaresAnActorOutsideAPanel is the client half of the daemon's
// per-actor rate caps: inside a panel the panel id IS the identity, and outside
// one every client used to declare the empty string and share a single slot with
// every other client outside a panel.
//
// It reads the hello off the wire rather than trusting the Client, because the
// frame is the whole interface here — the server has nothing else to key on.
//
// What it covers is the SECOND rule and the long-lived shape. A client that has
// a panel must not also declare an actor: two identities for one caller is a
// second slot it could spend from, and the panel id is the one the daemon can
// actually account for. A client that outlives its connections declares the
// process, which is the thing that survives its dials.
//
// The per-command shape is not here, and that is deliberate. Asserting it in
// this process would mean computing the expected value with the same call the
// code makes, which passes for any rule at all; see
// TestTheOutOfPanelIdentityHoldsAcrossInvocations, which runs the real client
// twice instead.
func TestHelloDeclaresAnActorOutsideAPanel(t *testing.T) {
	for _, tc := range []struct {
		name, self, want string
		dial             func(sock, self string) (*control.Client, error)
	}{
		{name: "a per-command client inside panel 7", self: "7", want: "",
			dial: func(sock, self string) (*control.Client, error) {
				t.Setenv(paths.EnvSocket, sock)
				t.Setenv(paths.EnvRole, "")
				t.Setenv(paths.EnvPanelID, self)
				return control.Dial()
			}},
		// The long-lived shape: `baton mcp` serves many tool calls from one
		// process, each over a fresh dial, so it is itself rather than the session
		// that started it — which is usually the operator's own terminal.
		{name: "a long-lived client outside a panel", want: "pid:" + strconv.Itoa(os.Getpid()),
			dial: func(sock, self string) (*control.Client, error) {
				t.Setenv(paths.EnvSocket, sock)
				t.Setenv(paths.EnvRole, "")
				t.Setenv(paths.EnvPanelID, self)
				return control.DialAsProcess()
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sock, actors := helloActors(t, 1)

			c, err := tc.dial(sock, tc.self)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer func() { _ = c.Close() }()

			switch got := <-actors; {
			case tc.want == "" && got != "":
				t.Errorf("a client inside panel %q also declared actor %q; the panel id is already "+
					"the identity, and a second one is just another slot to spend from", tc.self, got)
			case got != tc.want:
				t.Errorf("declared actor %q, want %q. A client that outlives its connections is "+
					"ITSELF: keyed on its session it would share a slot with the shell that started "+
					"it, which is usually the operator's own terminal", got, tc.want)
			}
		})
	}
}

// The child process TestTheOutOfPanelIdentityHoldsAcrossInvocations drives:
// dialHelperMode is what tells it to be a client rather than to skip; the
// daemon stand-in it dials arrives in BATON_SOCK, which is the variable the
// real client reads.
const (
	dialHelperMode = "BATON_TEST_DIAL_HELPER"
	dialHelper     = "TestDialHelper"
)

// TestDialHelper is not a test. It is the client under test, run as a whole
// process because that is the shape `baton ctl` has — and run twice, from two
// different parents, because that is the only way to ask whether the identity it
// declares survives the process that declared it.
//
// It prints the parent it ran under, so the test can say the two really were
// different before it says the two actors were the same.
func TestDialHelper(t *testing.T) {
	if os.Getenv(dialHelperMode) == "" {
		t.Skip("the child half of TestTheOutOfPanelIdentityHoldsAcrossInvocations")
	}
	// Through Dial and BATON_SOCK, exactly as `baton ctl` reaches a daemon: the
	// identity rule under test is Dial's, so a dial that was handed an actor
	// would assert nothing about it.
	c, err := control.Dial()
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = c.Close()
	fmt.Printf("ppid=%d\n", os.Getppid())
}

// TestTheOutOfPanelIdentityHoldsAcrossInvocations is the property a per-command
// client's actor has to have, asserted in a way that can fail.
//
// The version this replaces computed its expected value with the same
// syscall.Getsid(0) the code calls, so it passed for ANY identity rule the code
// might hold — the parent-process one that was falsified on this branch
// included. Nothing in the suite asserted the property the change was about.
//
// The property is that two invocations from ONE session declare the SAME actor.
// `baton ctl` is a process per command, so the identity has to outlive the
// process; `out=$(baton ctl …)` forks a subshell, so anything keyed inside the
// process — its pid, its parent — is fresh on every iteration of a loop written
// that way, and the cap it selects a slot in would be dead on exactly the shape
// it exists for. The two clients here really do have different parents, and the
// test says so before it says their actors match.
//
// The second half is the LIMIT, asserted rather than left to prose. A launcher
// that starts each command in a session of its own — cron, `ssh host baton ctl
// …`, systemd-run, an agent runtime — rotates the identity exactly as the parent
// would have. That is the self-rotation this identity is already documented as
// accepting, not a defect; what would be a defect is claiming a stability the
// code does not have. See control.sessionActor for who it actually leaves
// uncovered.
func TestTheOutOfPanelIdentityHoldsAcrossInvocations(t *testing.T) {
	run := func(t *testing.T, sock string, ownSession bool) int {
		t.Helper()
		// Through a shell that BACKGROUNDS it, and that is what makes the
		// assertion non-vacuous: `&` forks, so the client's parent is this
		// invocation's own sh and the next invocation's is a different one —
		// exactly as `out=$(baton ctl …)` hands a loop a fresh subshell every
		// iteration. Started straight from the test binary the two clients would
		// share one parent, and a parent-keyed identity would look stable.
		cmd := exec.Command("/bin/sh", "-c", `"$0" "$@" & wait`,
			os.Args[0], "-test.run=^"+dialHelper+"$")
		// The panel and role are cleared rather than inherited: this test is about a
		// client OUTSIDE a panel, and a developer running the suite from inside one
		// of their own baton panels has BATON_PANEL_ID in their shell — which the
		// child would otherwise declare as its self, and a client with a self
		// declares no actor at all.
		cmd.Env = append(os.Environ(), dialHelperMode+"=1", paths.EnvSocket+"="+sock,
			paths.EnvPanelID+"=", paths.EnvRole+"=")
		if ownSession {
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run the client: %v\n%s", err, out)
		}
		for line := range strings.SplitSeq(string(out), "\n") {
			if ppid, ok := strings.CutPrefix(strings.TrimSpace(line), "ppid="); ok {
				n, err := strconv.Atoi(ppid)
				if err != nil {
					t.Fatalf("the client reported parent %q: %v", ppid, err)
				}
				return n
			}
		}
		t.Fatalf("the client never reported its parent:\n%s", out)
		return 0
	}

	t.Run("two invocations from one session are one actor", func(t *testing.T) {
		sock, actors := helloActors(t, 2)
		first, second := run(t, sock, false), run(t, sock, false)
		if first == second {
			t.Fatalf("both clients ran under parent %d, so a parent-keyed identity would pass this "+
				"too and it asserts nothing", first)
		}

		one, two := <-actors, <-actors
		if !strings.HasPrefix(one, "sid:") {
			t.Errorf("a client outside a panel declared actor %q; empty is the shared slot every "+
				"such client used to spend from, and the prefix is what keeps it out of the bare "+
				"panel numbers the same caps are keyed on", one)
		}
		if one != two {
			t.Errorf("two invocations from one session declared %q and %q. A per-command client "+
				"keyed on anything inside its own process is a fresh identity for every "+
				"`out=$(baton ctl …)`, and the cap would be dead on the loop it exists for", one, two)
		}
	})

	t.Run("a launcher with a session per command rotates it", func(t *testing.T) {
		sock, actors := helloActors(t, 2)
		run(t, sock, true)
		run(t, sock, true)

		if one, two := <-actors, <-actors; one == two {
			t.Errorf("two commands each started in their own session both declared %q. That is more "+
				"than sessionActor claims, and the docs beside it are written around the limit — if "+
				"the identity got wider, they are the thing to fix", one)
		}
	})
}

// shortDir is a temp directory with a short path. A unix socket path is capped
// near 104 bytes, and t.TempDir() puts the test's own name in the path — which
// on the tests in this file is most of the budget. One helper owns the rule, so
// the next socket a test binds does not re-derive it in a comment of its own.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// helloActors is a daemon stand-in: it answers just enough handshake for a dial
// to return, and reports the actor each of the next n clients declared, in the
// order they arrived.
func helloActors(t *testing.T, n int) (sock string, actors <-chan string) {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(shortDir(t), "s.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	out := make(chan string, n)
	go func() {
		for range n {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var cmd proto.Command
			if err := json.NewDecoder(conn).Decode(&cmd); err != nil {
				_ = conn.Close()
				return
			}
			out <- cmd.Actor
			// Enough of a handshake for a dial to return.
			_ = json.NewEncoder(conn).Encode(proto.ServerMsg{Type: "panels"})
			_ = conn.Close()
		}
	}()
	return ln.Addr().String(), out
}

// TestControlConductorFenced confirms the env-driven conductor identity reaches
// the server: a control client that inherits BATON_ROLE/BATON_PANEL_ID is fenced
// off from acting on its own panel.
func TestControlConductorFenced(t *testing.T) {
	sock := startServer(t)

	admin, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial admin: %v", err)
	}
	defer func() { _ = admin.Close() }()
	selfID, err := admin.Spawn(proto.Command{Action: "panel.create", Kind: proto.KindShell})
	if err != nil {
		t.Fatalf("spawn self: %v", err)
	}

	t.Setenv(paths.EnvSocket, sock)
	t.Setenv(paths.EnvRole, "conductor")
	t.Setenv(paths.EnvPanelID, selfID)

	cond, err := control.Dial()
	if err != nil {
		t.Fatalf("dial conductor: %v", err)
	}
	defer func() { _ = cond.Close() }()

	err = cond.Do(proto.Command{Action: "panel.close", IDs: []string{selfID}})
	if err == nil || !strings.Contains(err.Error(), "own panel") {
		t.Fatalf("conductor self-close should be refused, got %v", err)
	}
}

// TestAttentionRoundtrip drives the issue's second gap end to end over a
// real socket: an agent says it needs a human with a reason, the fleet snapshot
// shows it saying so, and the agent takes it back again.
func TestAttentionRoundtrip(t *testing.T) {
	sock := startServer(t)

	c, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	id, err := c.SpawnPanel("", nil, "")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	if err := c.DeclareAttention(id, "which migration do I run first?"); err != nil {
		t.Fatalf("declare: %v", err)
	}
	p := panelByID(t, c, id)
	if p.State != "attention" || p.Reason != "which migration do I run first?" {
		t.Fatalf("a declaration should reach the fleet with its reason, got state=%q reason=%q", p.State, p.Reason)
	}

	if err := c.ResolveAttention(id); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p = panelByID(t, c, id); p.State == "attention" || p.Reason != "" {
		t.Fatalf("a resolve should withdraw both the state and the reason, got state=%q reason=%q", p.State, p.Reason)
	}
	// Standing down twice is not an error: an agent should not have to know
	// whether its hand is still up.
	if err := c.ResolveAttention(id); err != nil {
		t.Fatalf("a second resolve should be a no-op, got %v", err)
	}

	// The two ways to say nothing useful, both refused by the server and both
	// surfaced through the client.
	if err := c.DeclareAttention(id, ""); err == nil {
		t.Fatal("a declaration with no reason should be refused")
	}
	if err := c.DeclareAttention("999", "hello?"); err == nil {
		t.Fatal("a declaration on an unknown panel should be refused")
	}
}

// TestAttentionSelf covers the form an agent actually uses:
// no id at all. The panel id baton injected into the process is declared on
// hello, so `baton ctl attention --why "…"` inside a panel addresses itself
// without ever having to learn which panel it is.
func TestAttentionSelf(t *testing.T) {
	sock := startServer(t)

	admin, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial admin: %v", err)
	}
	defer func() { _ = admin.Close() }()
	selfID, err := admin.SpawnPanel("", nil, "")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	t.Setenv(paths.EnvSocket, sock)
	t.Setenv(paths.EnvRole, "")
	t.Setenv(paths.EnvPanelID, selfID)
	inside, err := control.Dial()
	if err != nil {
		t.Fatalf("dial from inside the panel: %v", err)
	}
	defer func() { _ = inside.Close() }()

	if err := inside.DeclareAttention("", "the brief is ambiguous"); err != nil {
		t.Fatalf("id-less declare: %v", err)
	}
	if p := panelByID(t, admin, selfID); p.State != "attention" || p.Reason != "the brief is ambiguous" {
		t.Fatalf("an id-less declaration should target the caller's own panel, got %+v", p)
	}
	if err := inside.ResolveAttention(""); err != nil {
		t.Fatalf("id-less resolve: %v", err)
	}
	if p := panelByID(t, admin, selfID); p.State == "attention" {
		t.Fatalf("an id-less resolve should stand the same panel down, got %+v", p)
	}

	// A connection that declared no identity and named no panel is told so,
	// rather than quietly addressing nothing.
	anon, err := control.DialSocket(sock, "", "", "")
	if err != nil {
		t.Fatalf("dial anon: %v", err)
	}
	defer func() { _ = anon.Close() }()
	if err := anon.DeclareAttention("", "who am I?"); err == nil || !strings.Contains(err.Error(), "no panel id") {
		t.Fatalf("an unaddressed declaration should say so, got %v", err)
	}
}

// panelByID reads one panel out of the current fleet snapshot.
func panelByID(t *testing.T, c *control.Client, id string) proto.Panel {
	t.Helper()
	panels, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, p := range panels {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("panel %q is not in the fleet", id)
	return proto.Panel{}
}
