package client

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/remote"
)

// fakeSSH puts a script named `ssh` first on PATH, so DialSSH runs it instead of
// the real client. body is the shell it runs; $@ is the argument list baton
// built, which the tests also use to assert the command line.
func fakeSSH(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func addr(t *testing.T, s string) remote.Address {
	t.Helper()
	a, err := remote.ParseAddress(s)
	if err != nil {
		t.Fatalf("ParseAddress(%q): %v", s, err)
	}
	return a
}

func TestSSHArgsKeepTheDefaultPortImplicit(t *testing.T) {
	got := sshArgs(addr(t, "cmj@laptop.lan"), SSHOptions{})
	want := []string{"cmj@laptop.lan", DefaultRemoteCommand}
	if !slices.Equal(got, want) {
		t.Fatalf("sshArgs = %q, want %q", got, want)
	}
}

func TestSSHArgsPassAPortAndACommandOverride(t *testing.T) {
	got := sshArgs(addr(t, "laptop.lan:2222"), SSHOptions{Command: "/opt/bin/baton --stdio"})
	want := []string{"-p", "2222", "laptop.lan", "/opt/bin/baton --stdio"}
	if !slices.Equal(got, want) {
		t.Fatalf("sshArgs = %q, want %q", got, want)
	}

	// A blank override is not an empty command — it falls back to the default.
	got = sshArgs(addr(t, "laptop.lan"), SSHOptions{Command: "   "})
	if got[len(got)-1] != DefaultRemoteCommand {
		t.Fatalf("a blank command override should fall back, got %q", got)
	}
}

func TestSSHArgsCarryExtraOptions(t *testing.T) {
	got := sshArgs(addr(t, "laptop.lan"), SSHOptions{SSHArgs: []string{"-o", "BatchMode=yes"}})
	want := []string{"-o", "BatchMode=yes", "laptop.lan", DefaultRemoteCommand}
	if !slices.Equal(got, want) {
		t.Fatalf("sshArgs = %q, want %q", got, want)
	}
}

// TestDialSSHAttachesOnAWelcome is the happy path: the far side answers, the
// client comes back attached, and the footer label is the address dialled.
func TestDialSSHAttachesOnAWelcome(t *testing.T) {
	fakeSSH(t, `
printf '{"type":"welcome","version":"baton/1"}\n'
cat >/dev/null
`)
	c, err := DialSSH(addr(t, "cmj@laptop.lan"), SSHOptions{Passkey: "K7m2QxP9", Source: "cmj@desk"})
	if err != nil {
		t.Fatalf("DialSSH: %v", err)
	}
	defer func() { _ = c.Close() }()

	if got := c.Endpoint(); got != "cmj@laptop.lan" {
		t.Fatalf("Endpoint() = %q, want the address dialled", got)
	}
	select {
	case msg := <-c.Events:
		if msg.Type != "welcome" {
			t.Fatalf("first event = %q, want welcome", msg.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the welcome never reached the event channel")
	}
	c.Quiet() // exercised here because the cockpit always calls it before taking the screen
}

// TestDialSSHReportsTheFleetsRefusal: a wrong passkey comes back as the fleet's
// own words, not as a cockpit that opens and dies.
func TestDialSSHReportsTheFleetsRefusal(t *testing.T) {
	fakeSSH(t, `
printf '{"type":"goodbye","error":"wrong passkey"}\n'
cat >/dev/null
`)
	if _, err := DialSSH(addr(t, "laptop.lan"), SSHOptions{Passkey: "nope"}); err == nil {
		t.Fatal("a refused attach should be an error")
	} else if !strings.Contains(err.Error(), "wrong passkey") {
		t.Fatalf("error = %v, want the fleet's refusal", err)
	}
}

// TestDialSSHQuotesSSHWhenItDiesSilently: the transport failing has to say what
// ssh said, because that is where the real reason lives.
func TestDialSSHQuotesSSHWhenItDiesSilently(t *testing.T) {
	fakeSSH(t, `
echo 'ssh: connect to host laptop.lan port 22: No route to host' >&2
exit 255
`)
	_, err := DialSSH(addr(t, "laptop.lan"), SSHOptions{Passkey: "K7m2QxP9"})
	if err == nil {
		t.Fatal("a dead ssh should be an error")
	}
	if !strings.Contains(err.Error(), "No route to host") {
		t.Fatalf("error = %v, want ssh's own message", err)
	}
}

// TestDialSSHHintsAtThePathWhenBatonIsMissing: the single most likely first
// failure deserves the fix in the message.
func TestDialSSHHintsAtThePathWhenBatonIsMissing(t *testing.T) {
	fakeSSH(t, `
echo 'sh: 1: baton: command not found' >&2
exit 127
`)
	_, err := DialSSH(addr(t, "laptop.lan"), SSHOptions{Passkey: "K7m2QxP9"})
	if err == nil {
		t.Fatal("a missing far-side baton should be an error")
	}
	if !strings.Contains(err.Error(), "remote-command") {
		t.Fatalf("error = %v, want the PATH hint", err)
	}
}

func TestStartSSHFailsWhenThereIsNoSSH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := startSSH(addr(t, "laptop.lan"), SSHOptions{}); err == nil {
		t.Fatal("startSSH should fail when ssh is not on PATH")
	}
}

func TestStderrTapKeepsTheLastLineAndCanGoQuiet(t *testing.T) {
	tap := &stderrTap{}
	if got := tap.tail(); got != "" {
		t.Fatalf("an empty tap tails %q", got)
	}
	_, _ = tap.Write([]byte("banner\nssh: Permission denied\n\n"))
	if got := tap.tail(); got != "ssh: Permission denied" {
		t.Fatalf("tail() = %q", got)
	}

	// The buffer is bounded, so a chatty session cannot grow it without limit.
	_, _ = tap.Write([]byte(strings.Repeat("x", maxStderrTap*2)))
	if len(tap.buf) > maxStderrTap {
		t.Fatalf("the tap held %d bytes, past the %d cap", len(tap.buf), maxStderrTap)
	}

	tap.passthrough = true
	tap.quiet()
	if tap.passthrough {
		t.Fatal("quiet() should stop the pass-through")
	}
}

// TestQuietOnALocalClientIsANoOp: Quiet is called on every attach, and a local
// one has no ssh child to silence.
func TestQuietOnALocalClientIsANoOp(t *testing.T) {
	c := &Client{conn: nopConn{}}
	c.Quiet()
}

type nopConn struct{}

func (nopConn) Read([]byte) (int, error)         { return 0, nil }
func (nopConn) Write(p []byte) (int, error)      { return len(p), nil }
func (nopConn) Close() error                     { return nil }
func (nopConn) SetReadDeadline(time.Time) error  { return nil }
func (nopConn) SetWriteDeadline(time.Time) error { return nil }
