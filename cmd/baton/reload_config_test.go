package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/proto"
)

// fleetConn is a bare control connection: commands in, messages out, in the
// order the daemon handled them.
//
// The ORDER is what this test needs and what a raw connection gives it. A daemon
// handles one connection's commands on one goroutine, in sequence, so a
// config.get sent after a server.reload is answered from the state that reload
// left behind — which is how a reload's result is read rather than slept for. A
// reload that applies nothing broadcasts nothing, so there is no push to wait on
// instead.
//
// It is raw rather than internal/client because that client sends a config.get
// of its own at dial, and a successful reload's PushConfig lands on the same
// channel as a config.get's reply. Pairing each request with its own answer
// would mean counting unsolicited messages whose number differs between the
// test that reloads successfully and the test that does not. await takes the
// next reply of a type instead, and needs no such bookkeeping.
type fleetConn struct {
	t   *testing.T
	c   net.Conn
	enc *json.Encoder
	dec *json.Decoder
}

func dialFleet(t *testing.T, sock string) *fleetConn {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	f := &fleetConn{t: t, c: c, enc: json.NewEncoder(c), dec: json.NewDecoder(c)}
	f.send(proto.Command{Action: "hello"})
	return f
}

func (f *fleetConn) send(cmd proto.Command) {
	f.t.Helper()
	if err := f.enc.Encode(cmd); err != nil {
		f.t.Fatalf("send %s: %v", cmd.Action, err)
	}
}

// await sends cmd and returns the first reply of the given type, skipping the
// unrelated pushes a live fleet interleaves (panels, telemetry, pings).
func (f *fleetConn) await(cmd proto.Command, typ string) proto.ServerMsg {
	f.t.Helper()
	f.send(cmd)
	_ = f.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var msg proto.ServerMsg
		if err := f.dec.Decode(&msg); err != nil {
			f.t.Fatalf("waiting for a %q reply to %s: %v", typ, cmd.Action, err)
		}
		if msg.Type == typ {
			return msg
		}
	}
}

// liveSettings is what the daemon is ACTUALLY running on right now, read back
// off its own socket rather than inferred from what it logged.
//
// The distinction matters more here than anywhere: the defect this pins was a
// reload that replaced every subsystem's settings and said so with one warning
// line — the same line it logs when it correctly replaces nothing. A test that
// watched the log would have passed against the bug.
type liveSettings struct {
	config string // the merged effective config served to every cockpit (SetClientConfig)
	agents string // the agent backends detected from that config (SetAgents)
	score  string // the score policy in force in the live store (Store.SetPolicy)
}

func (f *fleetConn) settings() liveSettings {
	f.t.Helper()
	cfg := f.await(proto.Command{Action: "config.get"}, "config")
	agents, err := json.Marshal(cfg.Agents)
	if err != nil {
		f.t.Fatalf("marshal the agent list: %v", err)
	}
	st := f.await(proto.Command{Action: "score.status"}, "score")
	return liveSettings{config: string(cfg.Config), agents: string(agents), score: string(st.Score)}
}

// bootFleet starts a daemon on the config already written under home and
// returns its socket, ready to take commands. Closing the listener and checking
// what runServerOn returned is registered as cleanup, so a test that boots one
// says nothing about shutting it down.
func bootFleet(t *testing.T, home string) string {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	t.Setenv("BATON_PLUGIN", "")

	sock := filepath.Join(shortDir(t), "b.sock")
	t.Setenv("BATON_SOCK", sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- runServerOn(ln, sock, loadServerBoot(sock)) }()
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("runServerOn returned %v, want nil", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("runServerOn did not return after the listener closed")
		}
	})
	waitServing(t, sock)
	return sock
}

// chosenConfig and changedConfig are two configs an operator plainly chose,
// naming the same three subsystems with different values. Nothing in either
// matches a built-in default, so neither "the reload changed nothing" nor "the
// reload applied it" can pass by accident.
const chosenConfig = `panel:
  workdir: %s
  agents:
    tester:
      command: /bin/echo
score:
  promote-at: 8
  user-signals-at: 5
  working-set: 7
`

const changedConfig = `panel:
  workdir: %s
  agents:
    other:
      command: /bin/echo
score:
  promote-at: 4
  user-signals-at: 3
  working-set: 5
`

// brokenConfig will not parse: `rank.cwd` is a word where a number belongs, and
// yaml.v3 fails the decode over it.
//
// What makes it the right shape for this test is everything ABOVE that line. A
// failed Unmarshal does not hand back the zero value — it hands back what it
// decoded before it gave up — so this file's workdir, its empty agent map and
// its promote-at are all sitting in the struct config.LoadPartial returns
// alongside the error, which is what applyConfig reads (for the mistyped key's
// name, and nothing else). They are exactly what the daemon used to go on and
// apply.
const brokenConfig = `panel:
  workdir: /nowhere
  agents: {}
score:
  promote-at: 2
  rank:
    cwd: fast
`

// TestAReloadOnAConfigThatWillNotParseAppliesNothing is issue #48.
//
// applyConfig warned on a failed config.Load and then carried on, so one typo
// in ~/.baton/config was enough for a SIGHUP to hand srv.Reload, SetAgents and
// SetClientConfig a half-decoded struct — the operator's whole configuration
// replaced by one nobody wrote, under a single warning line.
//
// The assertion is on what the subsystems are RUNNING, read back over the
// socket. Three of them, because the shape of the bug was that one knob had
// remembered to defend itself and the rest had not: the score policy was gated
// on the load error and survived, while the config served to every cockpit and
// the agent list did not.
func TestAReloadOnAConfigThatWillNotParseAppliesNothing(t *testing.T) {
	home := t.TempDir()
	chosen := filepath.Join(home, "work")
	if err := os.MkdirAll(chosen, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, home, fmt.Sprintf(chosenConfig, chosen))

	f := dialFleet(t, bootFleet(t, home))
	before := f.settings()

	// The good config really did take. Without this the rest is vacuous: a daemon
	// that ran on defaults throughout would also "change nothing".
	if !strings.Contains(before.config, chosen) {
		t.Fatalf("the chosen workdir never reached the served config: %s", before.config)
	}
	if !strings.Contains(before.agents, `"tester"`) {
		t.Fatalf("the chosen agent profile never reached the agent list: %s", before.agents)
	}
	if !strings.Contains(before.score, `"promote_at":8`) {
		t.Fatalf("the chosen score threshold never reached the store: %s", before.score)
	}

	// The operator mistypes one weight and reloads. This is the same closure a
	// SIGHUP runs — runServerOn wires both to it — so what it applies here is
	// what a HUP applies.
	writeConfig(t, home, brokenConfig)
	f.send(proto.Command{Action: "server.reload"})

	after := f.settings()
	if after.config != before.config {
		t.Errorf("a config that would not parse replaced the served config.\n before: %s\n  after: %s",
			before.config, after.config)
	}
	if after.agents != before.agents {
		t.Errorf("a config that would not parse replaced the agent list.\n before: %s\n  after: %s",
			before.agents, after.agents)
	}
	if after.score != before.score {
		t.Errorf("a config that would not parse retuned the score store.\n before: %s\n  after: %s",
			before.score, after.score)
	}
}

// TestABootOnAConfigThatWillNotParseComesUpOnTheDefaults is the NEVER-HAD-ONE
// half of the same rule, and the half that keeps the fix from being "a broken
// config means the daemon applies nothing, ever".
//
// The first pass runs before Serve with nothing yet applied. Short-circuiting
// it the way a reload is short-circuited leaves the daemon serving an empty
// config and a NULL agent list — a cockpit that attaches is offered no agent to
// spawn at all, which is worse than the defect being fixed.
//
// So the assertion is that the daemon comes up USABLE: the built-in agent
// catalogue is there, and the score store is on the package defaults rather
// than on the half-decoded numbers sitting in the struct Load returned beside
// its error.
func TestABootOnAConfigThatWillNotParseComesUpOnTheDefaults(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, brokenConfig)

	got := dialFleet(t, bootFleet(t, home)).settings()
	if !strings.Contains(got.agents, `"claude"`) {
		t.Errorf("a daemon booted on an unreadable config offers no agents to spawn: %s", got.agents)
	}
	if got.config == "" {
		t.Error("a daemon booted on an unreadable config serves no config at all to a cockpit")
	}
	// The defaults, not the file's own promote-at: 2 — which yaml.v3 had already
	// decoded when it gave up on the weight below it.
	if !strings.Contains(got.score, `"promote_at":3`) {
		t.Errorf("the store came up on the half-decoded file rather than the defaults: %s", got.score)
	}
	// And the workdir it never managed to read is not in force either.
	if strings.Contains(got.config, "/nowhere") {
		t.Errorf("a half-decoded workdir reached the served config: %s", got.config)
	}
}

// TestAReloadOnAConfigThatParsesStillApplies is the other direction, and the
// half that keeps the fix from being "never reload anything".
//
// Refusing a broken file is only correct if a good one still lands, so this
// edits the same three subsystems through a file that parses and requires all
// three to move. Deleting the early return's condition — refusing every reload
// rather than every failed one — passes the test above and fails this one.
func TestAReloadOnAConfigThatParsesStillApplies(t *testing.T) {
	home := t.TempDir()
	first := filepath.Join(home, "work")
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, home, fmt.Sprintf(chosenConfig, first))

	f := dialFleet(t, bootFleet(t, home))

	second := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, home, fmt.Sprintf(changedConfig, second))
	f.send(proto.Command{Action: "server.reload"})

	// Each of these says the new value arrived AND the old one left, so no
	// baseline snapshot is needed: chosenConfig and changedConfig share no value.
	after := f.settings()
	if !strings.Contains(after.config, second) || strings.Contains(after.config, first) {
		t.Errorf("the edited workdir did not reach the served config: %s", after.config)
	}
	if !strings.Contains(after.agents, `"other"`) || strings.Contains(after.agents, `"tester"`) {
		t.Errorf("the edited agent profile did not reach the agent list: %s", after.agents)
	}
	if !strings.Contains(after.score, `"promote_at":4`) {
		t.Errorf("the edited score threshold did not reach the store: %s", after.score)
	}
}
