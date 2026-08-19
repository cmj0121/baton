package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/remote"
	"github.com/cmj0121/baton/internal/server"
	"github.com/cmj0121/baton/internal/tui"
)

// shortDir is a temp directory with a short path. A unix socket path is capped
// near 104 bytes, which t.TempDir() plus a long test name can exceed.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// liveFleet boots a server on a socket in dir and returns the path.
func liveFleet(t *testing.T, dir, name string) string {
	t.Helper()
	sock := filepath.Join(dir, name)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = server.New(ln).Serve() }()
	return sock
}

func TestAliveWithin(t *testing.T) {
	dir := shortDir(t)
	sock := liveFleet(t, dir, "s.sock")
	if !aliveWithin(sock, time.Second) {
		t.Fatal("a listening socket should answer")
	}
	if aliveWithin(filepath.Join(dir, "nope.sock"), 200*time.Millisecond) {
		t.Fatal("a socket that does not exist must not report alive")
	}
}

// TestBridgeSocketHonoursAnExplicitChoice: BATON_SOCK is how a person points the
// bridge at one particular fleet, and it wins over the discovery below it.
func TestBridgeSocketHonoursAnExplicitChoice(t *testing.T) {
	dir := shortDir(t)
	sock := liveFleet(t, dir, "explicit.sock")
	t.Setenv(paths.EnvSocket, sock)

	got, err := bridgeSocket(0, filepath.Join(dir, "baton.log"), "")
	if err != nil {
		t.Fatalf("bridgeSocket: %v", err)
	}
	if got != sock {
		t.Fatalf("bridgeSocket = %q, want the explicit socket", got)
	}
}

// TestBridgeSocketFindsTheRunningFleet is the reason the bridge cannot just call
// paths.Socket(): sshd runs it in a session of its own, so the per-session path
// names a socket nothing has ever bound.
func TestBridgeSocketFindsTheRunningFleet(t *testing.T) {
	dir := shortDir(t)
	t.Setenv("XDG_RUNTIME_DIR", dir)
	if err := os.MkdirAll(filepath.Join(dir, "baton"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv(paths.EnvSocket, "")

	// A stale socket file from a crashed daemon, and a live one beside it.
	stale := filepath.Join(dir, "baton", "baton-1.sock")
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatalf("write stale socket: %v", err)
	}
	live := liveFleet(t, filepath.Join(dir, "baton"), "baton-2.sock")

	got, err := bridgeSocket(0, filepath.Join(dir, "baton.log"), "")
	if err != nil {
		t.Fatalf("bridgeSocket: %v", err)
	}
	if got != live {
		t.Fatalf("bridgeSocket = %q, want the socket that answers (%q)", got, live)
	}
}

// TestRunStdioBridgesTheProtocol is the far side end to end: bytes written to
// the ssh pipe reach the fleet, and the fleet's answer comes back out.
func TestRunStdioBridgesTheProtocol(t *testing.T) {
	dir := shortDir(t)
	sock := liveFleet(t, dir, "bridge.sock")
	t.Setenv(paths.EnvSocket, sock)

	// Stand in for the ssh pipe: what the test writes is the bridge's input, and
	// what the bridge writes out is what the test reads.
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- bridge(inR, outW, sock) }()

	if err := json.NewEncoder(inW).Encode(proto.Command{Action: "hello"}); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	// Read until the welcome: a status push can already be queued to this
	// connection by the time its hello is answered, and the bridge is a pipe — it
	// does not reorder what the fleet chose to send.
	_ = outR.SetReadDeadline(time.Now().Add(10 * time.Second))
	dec := json.NewDecoder(outR)
	var welcomed bool
	for range 8 {
		var msg proto.ServerMsg
		if err := dec.Decode(&msg); err != nil {
			t.Fatalf("read from the bridge: %v", err)
		}
		if msg.Type == "welcome" {
			welcomed = true
			break
		}
	}
	if !welcomed {
		t.Fatal("the fleet's welcome never came back through the bridge")
	}

	// Closing the pipe is the ssh session ending; the bridge returns rather than
	// hanging on a stream nobody is feeding.
	_ = inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("bridge: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the bridge did not return when the pipe closed")
	}
	_ = outW.Close()
}

func TestRunStdioReportsAFleetItCannotReach(t *testing.T) {
	dir := shortDir(t)
	t.Setenv(paths.EnvSocket, filepath.Join(dir, "nothing.sock"))
	// startDaemon re-execs the test binary, which is not a daemon; it fails to
	// come up and the bridge says so rather than hanging.
	if err := runStdio(0, filepath.Join(dir, "baton.log"), ""); err == nil {
		t.Fatal("bridging to a fleet that cannot be reached should be an error")
	}
}

func TestLocalSourceLabelNamesThisMachine(t *testing.T) {
	t.Setenv("USER", "cmj")
	host, _ := os.Hostname()
	if got, want := localSourceLabel(), "cmj@"+host; got != want {
		t.Fatalf("localSourceLabel() = %q, want %q", got, want)
	}
}

// fakeSSH puts a script named `ssh` first on PATH so a dial runs it instead of
// the real client.
func fakeSSH(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// scriptedPrompt answers the connection form from a list, recording what the
// loop fed back into it — which is where the "keep the address, say what went
// wrong" behaviour actually shows up.
type scriptedPrompt struct {
	answers []tui.RemoteTarget
	seen    []string // the problem each call was opened with
	address []string // the address each call was seeded with
}

func (p *scriptedPrompt) next(address, problem string) (tui.RemoteTarget, bool, error) {
	p.seen = append(p.seen, problem)
	p.address = append(p.address, address)
	if len(p.answers) == 0 {
		return tui.RemoteTarget{}, false, nil // out of answers: the person quit
	}
	a := p.answers[0]
	p.answers = p.answers[1:]
	return a, true, nil
}

func TestAttachRemoteQuitsWhenTheFormIsCancelled(t *testing.T) {
	p := &scriptedPrompt{}
	if err := attachRemoteWith(config.Config{}, p.next, nil); err != nil {
		t.Fatalf("cancelling the form should not be an error, got %v", err)
	}
}

func TestAttachRemoteReturnsAFormFailure(t *testing.T) {
	boom := func(string, string) (tui.RemoteTarget, bool, error) {
		return tui.RemoteTarget{}, false, errors.New("no terminal")
	}
	if err := attachRemoteWith(config.Config{}, boom, nil); err == nil {
		t.Fatal("a form that cannot run should surface its error")
	}
}

// TestAttachRemoteRetriesWithTheAddressKept walks the loop: a malformed address
// and then a refused passkey both come back into the form, keeping what was
// typed, and the third answer attaches.
func TestAttachRemoteRetriesWithTheAddressKept(t *testing.T) {
	fakeSSH(t, `
if [ "$1" = "laptop.lan" ]; then
	printf '{"type":"goodbye","error":"wrong passkey"}\n'
else
	printf '{"type":"welcome","version":"baton/1"}\n'
fi
cat >/dev/null
`)
	p := &scriptedPrompt{answers: []tui.RemoteTarget{
		{Address: "host:", Passkey: "K7m2QxP9"},    // not an address
		{Address: "laptop.lan", Passkey: "wrong"},  // refused by the fleet
		{Address: "desk.lan", Passkey: "K7m2QxP9"}, // in
	}}

	var attached string
	cockpit := func(c *client.Client, a remote.Address) error {
		attached = a.String()
		_ = c.Close()
		return nil
	}
	if err := attachRemoteWith(config.Config{}, p.next, cockpit); err != nil {
		t.Fatalf("attachRemoteWith: %v", err)
	}

	if attached != "desk.lan" {
		t.Fatalf("attached to %q, want desk.lan", attached)
	}
	if len(p.seen) != 3 {
		t.Fatalf("the form was opened %d times, want 3", len(p.seen))
	}
	if p.seen[0] != "" {
		t.Fatalf("the first prompt should carry no complaint, got %q", p.seen[0])
	}
	if !strings.Contains(p.seen[1], "trailing colon") {
		t.Fatalf("the retry should say the address was wrong, got %q", p.seen[1])
	}
	if !strings.Contains(p.seen[2], "wrong passkey") {
		t.Fatalf("the retry should carry the fleet's refusal, got %q", p.seen[2])
	}
	if p.address[2] != "laptop.lan" {
		t.Fatalf("the retry should keep the address typed, got %q", p.address[2])
	}
}

// TestAttachRemoteUsesTheConfiguredFarSideCommand: `ssh host cmd` runs a
// non-interactive shell, so the override is the fix for a baton that is not on
// the remote PATH — it has to actually reach the command line.
func TestAttachRemoteUsesTheConfiguredFarSideCommand(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "argv")
	fakeSSH(t, `
echo "$@" > `+record+`
printf '{"type":"welcome","version":"baton/1"}\n'
cat >/dev/null
`)
	cfg := config.Config{}
	cfg.Settings.RemoteCommand = "/opt/bin/baton --stdio"

	p := &scriptedPrompt{answers: []tui.RemoteTarget{{Address: "desk.lan:2222", Passkey: "K7m2QxP9"}}}
	cockpit := func(c *client.Client, _ remote.Address) error { _ = c.Close(); return nil }
	if err := attachRemoteWith(cfg, p.next, cockpit); err != nil {
		t.Fatalf("attachRemoteWith: %v", err)
	}

	argv, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	if got := strings.TrimSpace(string(argv)); got != "-p 2222 desk.lan /opt/bin/baton --stdio" {
		t.Fatalf("ssh was run as %q", got)
	}
}
