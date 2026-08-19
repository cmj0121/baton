package server_test

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// remoteServer boots a server on a temp socket and returns it with the path.
//
// The directory is a short one of its own rather than t.TempDir(): a unix socket
// path is capped near 104 bytes, and these tests have names long enough to blow
// past it once the framework's per-test directory is prepended.
func remoteServer(t *testing.T) (*server.Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, lerr := net.Listen("unix", sock)
	if lerr != nil {
		t.Fatalf("listen: %v", lerr)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := server.New(ln)
	go func() { _ = srv.Serve() }()
	return srv, sock
}

// recvRemote takes the next status off the client's remote channel.
func recvRemote(t *testing.T, c *client.Client) *proto.RemoteInfo {
	t.Helper()
	select {
	case msg, ok := <-c.Remote:
		if !ok {
			t.Fatal("remote channel closed unexpectedly")
		}
		if msg.Remote == nil {
			t.Fatalf("a %q message carried no remote payload", msg.Type)
		}
		return msg.Remote
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a remote status")
		return nil
	}
}

// dialLocal attaches an ordinary cockpit and drains its hello burst.
func dialLocal(t *testing.T, sock string) *client.Client {
	t.Helper()
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	recv(t, c) // welcome
	recv(t, c) // empty panels
	return c
}

// rawConn speaks the wire directly. The remote gate runs on the FIRST hello, so
// these tests cannot go through client.Dial — which says a plain hello of its
// own before anything else can be sent, and would be admitted before the remote
// one was ever read.
type rawConn struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
}

func dialRaw(t *testing.T, sock string) *rawConn {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &rawConn{conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}
}

func (r *rawConn) send(t *testing.T, cmd proto.Command) {
	t.Helper()
	if err := r.enc.Encode(cmd); err != nil {
		t.Fatalf("send %s: %v", cmd.Action, err)
	}
}

// next returns the next message, skipping the heartbeat.
func (r *rawConn) next(t *testing.T) proto.ServerMsg {
	t.Helper()
	for {
		_ = r.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var msg proto.ServerMsg
		if err := r.dec.Decode(&msg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if msg.Type != "ping" {
			return msg
		}
	}
}

// nextType reads until a message of the wanted type arrives.
func (r *rawConn) nextType(t *testing.T, want string) proto.ServerMsg {
	t.Helper()
	for range 32 {
		if msg := r.next(t); msg.Type == want {
			return msg
		}
	}
	t.Fatalf("no %q message arrived", want)
	return proto.ServerMsg{}
}

// dialRemote attaches a cockpit declaring the remote role, as the ssh bridge
// does, and reports the refusal the fleet answered with (or "" when admitted).
func dialRemote(t *testing.T, sock, passkey, source string) (*rawConn, string) {
	t.Helper()
	r := dialRaw(t, sock)
	r.send(t, proto.Command{Action: "hello", Role: "remote", Passkey: passkey, Source: source})
	msg := r.next(t)
	if msg.Type == "goodbye" {
		return r, msg.Error
	}
	if msg.Type != "welcome" {
		t.Fatalf("a remote hello was answered with %q", msg.Type)
	}
	return r, ""
}

// TestRemoteIsOffByDefault is the whole feature's default posture: a fleet that
// was never switched on refuses a remote attach, and says so.
func TestRemoteIsOffByDefault(t *testing.T) {
	srv, sock := remoteServer(t)
	if srv.RemoteEnabled() {
		t.Fatal("remote access must be off until it is switched on")
	}

	_, refusal := dialRemote(t, sock, "whatever", "cmj@laptop")
	if refusal != "remote access is not enabled on this fleet" {
		t.Fatalf("refusal = %q", refusal)
	}
}

// TestRemoteAttachNeedsTheCurrentPasskey covers the gate proper: the right code
// gets in, a wrong one does not, and a rotated code invalidates the old one.
func TestRemoteAttachNeedsTheCurrentPasskey(t *testing.T) {
	srv, sock := remoteServer(t)
	key, err := srv.EnableRemote()
	if err != nil {
		t.Fatalf("EnableRemote: %v", err)
	}
	if len(key) != 8 {
		t.Fatalf("passkey %q is %d chars, want 8", key, len(key))
	}

	if _, refusal := dialRemote(t, sock, key, "cmj@laptop"); refusal != "" {
		t.Fatalf("the current passkey should be admitted, got %q", refusal)
	}

	if _, refusal := dialRemote(t, sock, "notitatal", "cmj@phone"); refusal != "wrong passkey" {
		t.Fatalf("refusal = %q, want 'wrong passkey'", refusal)
	}

	// Rotating keeps the live connection and invalidates the old code.
	next, err := srv.RotateRemote()
	if err != nil {
		t.Fatalf("RotateRemote: %v", err)
	}
	if next == key {
		t.Fatal("a rotation should mint a different passkey")
	}
	if _, refusal := dialRemote(t, sock, key, "cmj@stale"); refusal == "" {
		t.Fatal("the old passkey should no longer open the fleet")
	}
	if _, refusal := dialRemote(t, sock, next, "cmj@fresh"); refusal != "" {
		t.Fatalf("the rotated passkey should be admitted, got %q", refusal)
	}
}

// TestRemoteAttemptsAreRateLimited proves a wrong passkey is not free: past the
// cap the fleet stops answering the question at all.
func TestRemoteAttemptsAreRateLimited(t *testing.T) {
	srv, sock := remoteServer(t)
	if _, err := srv.EnableRemote(); err != nil {
		t.Fatalf("EnableRemote: %v", err)
	}

	var last string
	for range 6 {
		_, refusal := dialRemote(t, sock, "wrongkey", "prober")
		if refusal == "" {
			t.Fatal("a wrong passkey should never be admitted")
		}
		last = refusal
	}
	if last != "too many failed attempts; wait a minute and try again" {
		t.Fatalf("the sixth attempt said %q, want the rate-limit refusal", last)
	}
}

// TestRemoteRotateNeedsRemoteOn: there is no code to replace when nothing is
// enabled, and minting one would be an enable in disguise.
func TestRemoteRotateNeedsRemoteOn(t *testing.T) {
	srv, _ := remoteServer(t)
	if _, err := srv.RotateRemote(); err == nil {
		t.Fatal("rotating with remote off should fail rather than switch it on")
	}
	if srv.RemoteEnabled() {
		t.Fatal("a failed rotation must not have enabled remote")
	}
}

// TestRemoteStatusListsConnections checks the overlay's data: both connections
// are listed, each sees itself marked, and only the local one is told the code.
func TestRemoteStatusListsConnections(t *testing.T) {
	srv, sock := remoteServer(t)
	key, err := srv.EnableRemote()
	if err != nil {
		t.Fatalf("EnableRemote: %v", err)
	}

	local := dialLocal(t, sock)
	far, refusal := dialRemote(t, sock, key, "cmj@laptop.lan")
	if refusal != "" {
		t.Fatalf("remote attach refused: %s", refusal)
	}

	if err := local.Send(proto.Command{Action: "remote.status"}); err != nil {
		t.Fatalf("remote.status: %v", err)
	}
	info := lastRemote(t, local)
	if !info.Enabled || !info.Local {
		t.Fatalf("the local cockpit should see enabled+local, got %+v", info)
	}
	if info.Passkey != key {
		t.Fatalf("a local cockpit should be told the passkey, got %q", info.Passkey)
	}
	if len(info.Conns) != 2 {
		t.Fatalf("want 2 connections, got %d: %+v", len(info.Conns), info.Conns)
	}
	var sawSelf, sawFar bool
	for _, cn := range info.Conns {
		switch cn.Source {
		case "local":
			sawSelf = cn.Self && cn.Role == "cockpit" && !cn.Remote
		case "cmj@laptop.lan":
			sawFar = !cn.Self && cn.Role == "remote" && cn.Remote
		}
		if cn.ID == "" || cn.Since == "" {
			t.Fatalf("connection %+v is missing an id or an attach time", cn)
		}
	}
	if !sawSelf || !sawFar {
		t.Fatalf("the list should mark this cockpit and name the remote one: %+v", info.Conns)
	}

	// The remote cockpit sees the same list, but never the passkey.
	far.send(t, proto.Command{Action: "remote.status"})
	farInfo := far.nextType(t, "remote").Remote
	if farInfo == nil {
		t.Fatal("the remote reply carried no payload")
	}
	if farInfo.Local {
		t.Fatal("a remote cockpit must not be marked local")
	}
	if farInfo.Passkey != "" {
		t.Fatalf("a remote cockpit must never be told the passkey, got %q", farInfo.Passkey)
	}
	if len(farInfo.Conns) != 2 {
		t.Fatalf("the remote cockpit should see both connections, got %d", len(farInfo.Conns))
	}
}

// TestRemoteControlIsLocalOnly is the feature's one asymmetry: a remote cockpit
// may list and kick, but the passkey and the switch are the owner's to press on
// the fleet's own machine.
func TestRemoteControlIsLocalOnly(t *testing.T) {
	srv, sock := remoteServer(t)
	key, err := srv.EnableRemote()
	if err != nil {
		t.Fatalf("EnableRemote: %v", err)
	}
	far, refusal := dialRemote(t, sock, key, "cmj@laptop.lan")
	if refusal != "" {
		t.Fatalf("remote attach refused: %s", refusal)
	}

	for _, action := range []string{"remote.rotate", "remote.disable", "remote.enable"} {
		far.send(t, proto.Command{Action: action})
		if msg := far.nextType(t, "error"); msg.Error == "" {
			t.Fatalf("%s over a remote attach should be refused with a reason", action)
		}
	}
	if !srv.RemoteEnabled() {
		t.Fatal("a refused remote.disable must not have switched remote off")
	}
}

// TestRemoteKickDropsTheConnectionWithAReason: the kicked cockpit is told why
// rather than just finding its socket gone.
func TestRemoteKickDropsTheConnectionWithAReason(t *testing.T) {
	srv, sock := remoteServer(t)
	key, err := srv.EnableRemote()
	if err != nil {
		t.Fatalf("EnableRemote: %v", err)
	}
	local := dialLocal(t, sock)
	far, refusal := dialRemote(t, sock, key, "cmj@laptop.lan")
	if refusal != "" {
		t.Fatalf("remote attach refused: %s", refusal)
	}

	if err := local.Send(proto.Command{Action: "remote.status"}); err != nil {
		t.Fatalf("remote.status: %v", err)
	}
	var target string
	for _, cn := range lastRemote(t, local).Conns {
		if cn.Source == "cmj@laptop.lan" {
			target = cn.ID
		}
	}
	if target == "" {
		t.Fatal("the remote connection was not listed")
	}

	if err := local.Send(proto.Command{Action: "remote.kick", Conn: target}); err != nil {
		t.Fatalf("remote.kick: %v", err)
	}
	if got := far.nextType(t, "goodbye"); got.Error == "" {
		t.Fatal("a kicked cockpit should be told why")
	}

	// An unknown id is an error, not a silent success.
	if err := local.Send(proto.Command{Action: "remote.kick", Conn: "c999"}); err != nil {
		t.Fatalf("remote.kick unknown: %v", err)
	}
	if msg := recvType(t, local, "error"); msg.Error == "" {
		t.Fatal("kicking an unknown connection should report it")
	}
}

// TestDisableRemoteDropsRemoteCockpitsOnly: switching off is a full revocation
// for the far side and a no-op for the cockpit that pressed it.
func TestDisableRemoteDropsRemoteCockpitsOnly(t *testing.T) {
	srv, sock := remoteServer(t)
	key, err := srv.EnableRemote()
	if err != nil {
		t.Fatalf("EnableRemote: %v", err)
	}
	local := dialLocal(t, sock)
	far, refusal := dialRemote(t, sock, key, "cmj@laptop.lan")
	if refusal != "" {
		t.Fatalf("remote attach refused: %s", refusal)
	}

	srv.DisableRemote()
	if got := far.nextType(t, "goodbye"); got.Error != "remote access was disabled on the fleet" {
		t.Fatalf("the dropped cockpit was told %q", got.Error)
	}
	if srv.RemoteEnabled() {
		t.Fatal("remote should be off after DisableRemote")
	}

	// The local cockpit is untouched and can still drive the fleet.
	if err := local.Send(proto.Command{Action: "panel.list"}); err != nil {
		t.Fatalf("panel.list: %v", err)
	}
	if got := recvType(t, local, "panels"); got.Type != "panels" {
		t.Fatalf("the local cockpit should still be attached, got %q", got.Type)
	}
}

// TestSettingsRemoteIsAppliedAsATransition: the config switch takes effect on
// boot and on a change, but a reload that repeats the same value must not undo
// a C-t r taken since.
func TestSettingsRemoteIsAppliedAsATransition(t *testing.T) {
	srv, _ := remoteServer(t)

	srv.Reload(server.Settings{Remote: true})
	if !srv.RemoteEnabled() {
		t.Fatal("settings.remote: true should switch remote on")
	}

	// The operator switched it off in the cockpit; a reload with the same file
	// must leave that decision alone.
	srv.DisableRemote()
	srv.Reload(server.Settings{Remote: true})
	if srv.RemoteEnabled() {
		t.Fatal("an unchanged settings.remote must not undo a cockpit decision")
	}

	// A real change in the file is acted on.
	srv.Reload(server.Settings{Remote: false})
	srv.Reload(server.Settings{Remote: true})
	if !srv.RemoteEnabled() {
		t.Fatal("a changed settings.remote should be acted on")
	}
}

// TestConductorCannotReachRemote keeps the operator surface shut to the agent
// driving the fleet.
func TestConductorCannotReachRemote(t *testing.T) {
	_, sock := remoteServer(t)
	c := dialLocal(t, sock)
	if err := c.Send(proto.Command{Action: "hello", Role: "conductor", Self: "p1"}); err != nil {
		t.Fatalf("hello conductor: %v", err)
	}
	recv(t, c) // welcome
	recv(t, c) // panels

	for _, action := range []string{"remote.status", "remote.enable", "remote.rotate", "remote.disable", "remote.kick"} {
		if err := c.Send(proto.Command{Action: action}); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if msg := recvType(t, c, "error"); msg.Error != "conductor role: remote access is an operator surface" {
			t.Fatalf("%s was answered %q", action, msg.Error)
		}
	}
}

// lastRemote drains the remote channel and returns the freshest status. The
// list is pushed on every attach and detach, so a test that asked for one may
// find several queued ahead of it.
func lastRemote(t *testing.T, c *client.Client) *proto.RemoteInfo {
	t.Helper()
	info := recvRemote(t, c)
	for {
		select {
		case msg, ok := <-c.Remote:
			if !ok {
				return info
			}
			if msg.Remote != nil {
				info = msg.Remote
			}
		case <-time.After(200 * time.Millisecond):
			return info
		}
	}
}
