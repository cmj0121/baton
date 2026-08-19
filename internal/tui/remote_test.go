package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmj0121/baton/internal/proto"
)

// remoteEpoch is the fixed "now" the duration column is measured from, so
// "2h 14m" is an exact number rather than something the wall clock drifts under.
var remoteEpoch = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// remoteModel is a cockpit sitting on the overlay with a given status. There is
// no live client, so the actions are exercised for their state and their status
// line; the server side of each is covered in internal/server.
func remoteModel(info *proto.RemoteInfo) model {
	m := baseModel()
	m.now = remoteEpoch
	m.mode = modeRemote
	m.remoteFrom = modeDashboard
	m.applyRemote(info)
	return m
}

// enabledStatus is a fleet with remote on, seen from the local cockpit: itself
// and one attachment from another machine.
func enabledStatus() *proto.RemoteInfo {
	return &proto.RemoteInfo{
		Enabled: true,
		Local:   true,
		Passkey: "K7m2QxP9",
		Conns: []proto.RemoteConn{
			{ID: "c1", Source: "local", Role: "cockpit", Since: remoteEpoch.Add(-2*time.Hour - 14*time.Minute).Format(time.RFC3339), Self: true},
			{ID: "c2", Source: "cmj@laptop.lan", Role: "remote", Since: remoteEpoch.Add(-6 * time.Minute).Format(time.RFC3339), Remote: true},
		},
	}
}

func TestOpenAndCloseRemoteRestoreTheView(t *testing.T) {
	m := baseModel()
	next, _ := m.openRemote(modeGroupZoom)
	m = next.(model)
	if m.mode != modeRemote || m.remoteFrom != modeGroupZoom {
		t.Fatalf("openRemote should enter modeRemote from the caller's view, got %v/%v", m.mode, m.remoteFrom)
	}

	next, _ = m.closeRemote()
	if got := next.(model).mode; got != modeGroupZoom {
		t.Fatalf("closeRemote returned to %v, want the group view", got)
	}
}

func TestRemoteViewShowsThePasskeyAndTheConnections(t *testing.T) {
	out := remoteModel(enabledStatus()).remoteView()
	for _, want := range []string{spaced("REMOTE"), "enabled", "K7m2QxP9", "cmj@laptop.lan", "2h 14m", "6m", "cockpit", "remote"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the overlay should show %q:\n%s", want, out)
		}
	}
}

func TestRemoteViewHidesThePasskeyFromARemoteCockpit(t *testing.T) {
	info := enabledStatus()
	info.Local = false
	info.Passkey = "" // the server never sends it to a remote connection
	out := remoteModel(info).remoteView()

	if strings.Contains(out, "K7m2QxP9") {
		t.Fatalf("a remote cockpit must never be shown the passkey:\n%s", out)
	}
	if !strings.Contains(out, "fleet's own machine") {
		t.Fatalf("the overlay should say where the passkey is read:\n%s", out)
	}
	// It is not offered the keys the server would refuse anyway.
	if strings.Contains(out, "new passkey") || strings.Contains(out, "disable") {
		t.Fatalf("a remote cockpit should not be offered the passkey keys:\n%s", out)
	}
	if !strings.Contains(out, "kick") {
		t.Fatalf("a remote cockpit may still kick:\n%s", out)
	}
}

func TestRemoteViewOffTellsYouHowToTurnItOn(t *testing.T) {
	out := remoteModel(&proto.RemoteInfo{Local: true}).remoteView()
	if !strings.Contains(out, "disabled") || !strings.Contains(out, "enable remote") {
		t.Fatalf("a fleet with remote off should show the switch:\n%s", out)
	}
	if !strings.Contains(out, "no connections") {
		t.Fatalf("an empty list should say so:\n%s", out)
	}
}

func TestRemoteViewBeforeTheFirstStatus(t *testing.T) {
	m := baseModel()
	m.mode = modeRemote
	out := m.remoteView()
	if !strings.Contains(out, "asking the fleet") {
		t.Fatalf("the overlay should say it is waiting:\n%s", out)
	}
}

func TestRemoteKeysMoveTheCursorAndClampIt(t *testing.T) {
	m := remoteModel(enabledStatus())

	next, _ := m.handleRemoteKey("down")
	m = next.(model)
	if m.remoteSel != 1 {
		t.Fatalf("down should move to row 1, got %d", m.remoteSel)
	}
	next, _ = m.handleRemoteKey("down")
	m = next.(model)
	if m.remoteSel != 1 {
		t.Fatalf("down at the last row should stay, got %d", m.remoteSel)
	}
	next, _ = m.handleRemoteKey("up")
	m = next.(model)
	next, _ = m.handleRemoteKey("up")
	if got := next.(model).remoteSel; got != 0 {
		t.Fatalf("up at the top should stay at 0, got %d", got)
	}
}

func TestRemoteKeysAreRefusedOverARemoteAttach(t *testing.T) {
	info := enabledStatus()
	info.Local, info.Passkey = false, ""
	m := remoteModel(info)

	for _, key := range []string{"n", "E", "e"} {
		next, _ := m.handleRemoteKey(key)
		got := next.(model).status
		if !strings.Contains(got, "own machine") {
			t.Fatalf("%q over a remote attach said %q, want the local-only refusal", key, got)
		}
	}
}

func TestRemoteControlKeysNeedRemoteOn(t *testing.T) {
	m := remoteModel(&proto.RemoteInfo{Local: true})
	for _, key := range []string{"n", "E"} {
		next, _ := m.handleRemoteKey(key)
		if got := next.(model).status; !strings.Contains(got, "not enabled") {
			t.Fatalf("%q with remote off said %q", key, got)
		}
	}

	// e is the one that works here, and is a no-op once it is already on.
	next, _ := m.handleRemoteKey("e")
	if got := next.(model).status; !strings.Contains(got, "enabling") {
		t.Fatalf("e with remote off said %q", got)
	}
	m = remoteModel(enabledStatus())
	next, _ = m.handleRemoteKey("e")
	if got := next.(model).status; !strings.Contains(got, "already enabled") {
		t.Fatalf("e with remote on said %q", got)
	}
}

func TestRemoteKickAndRotateAndDisableSetTheStatus(t *testing.T) {
	m := remoteModel(enabledStatus())

	next, _ := m.handleRemoteKey("x")
	if got := next.(model).status; !strings.Contains(got, "kicking local") {
		t.Fatalf("x said %q", got)
	}
	next, _ = m.handleRemoteKey("n")
	if got := next.(model).status; !strings.Contains(got, "rotating") {
		t.Fatalf("n said %q", got)
	}
	next, _ = m.handleRemoteKey("E")
	if got := next.(model).status; !strings.Contains(got, "disabling") {
		t.Fatalf("E said %q", got)
	}
	next, _ = m.handleRemoteKey("r")
	if got := next.(model).status; !strings.Contains(got, "refreshed") {
		t.Fatalf("r said %q", got)
	}

	// esc leaves.
	next, _ = m.handleRemoteKey("esc")
	if got := next.(model).mode; got != modeDashboard {
		t.Fatalf("esc left the overlay in %v", got)
	}
}

func TestRemoteKickWithNothingSelectedIsANoOp(t *testing.T) {
	m := remoteModel(&proto.RemoteInfo{Enabled: true, Local: true})
	next, _ := m.handleRemoteKey("k")
	if got := next.(model).status; got == "kicking " {
		t.Fatal("kicking an empty list should do nothing")
	}
}

func TestApplyRemoteKeepsTheCursorOnARowThatExists(t *testing.T) {
	m := remoteModel(enabledStatus())
	m.remoteSel = 1

	shrunk := enabledStatus()
	shrunk.Conns = shrunk.Conns[:1]
	m.applyRemote(shrunk)
	if m.remoteSel != 0 {
		t.Fatalf("the cursor should follow the shorter list, got %d", m.remoteSel)
	}

	m.applyRemote(nil)
	if m.remoteSel != 0 || m.remoteInfo != nil {
		t.Fatal("a nil status should reset the overlay")
	}
	if _, ok := m.remoteSelected(); ok {
		t.Fatal("nothing is selected without a status")
	}
}

func TestRemoteSinceFormatsTheAttachDuration(t *testing.T) {
	now := remoteEpoch
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{2*time.Hour + 14*time.Minute, "2h 14m"},
		{6 * time.Minute, "6m"},
		{30 * time.Second, "30s"},
	} {
		if got := remoteSince(now.Add(-tc.ago).Format(time.RFC3339), now); got != tc.want {
			t.Fatalf("remoteSince(-%v) = %q, want %q", tc.ago, got, tc.want)
		}
	}
	if got := remoteSince("not a time", now); got != "—" {
		t.Fatalf("an unparsable instant = %q, want a dash rather than a wrong number", got)
	}
	// A clock skew between two machines must not render a negative age.
	if got := remoteSince(now.Add(time.Hour).Format(time.RFC3339), now); got != "0s" {
		t.Fatalf("a future instant = %q, want 0s", got)
	}
}

func TestRemoteSourceLabel(t *testing.T) {
	for _, tc := range []struct{ user, host, want string }{
		{"cmj", "laptop.lan", "cmj@laptop.lan"},
		{"", "laptop.lan", "laptop.lan"},
		{"cmj", "", "cmj"},
		{"", "", "remote"},
		{"  ", "  ", "remote"},
	} {
		if got := RemoteSourceLabel(tc.user, tc.host); got != tc.want {
			t.Fatalf("RemoteSourceLabel(%q,%q) = %q, want %q", tc.user, tc.host, got, tc.want)
		}
	}
}

// TestRemoteIsReachedFromThePrefixInEveryView pins the binding down: it is an
// escape, so it fires after the prefix and never on a bare key.
func TestRemoteIsReachedFromThePrefixInEveryView(t *testing.T) {
	m := baseModel()
	b, ok := m.lookupEscape(keyRemote)
	if !ok || b.act != actRemote {
		t.Fatalf("C-t %s should resolve to the remote overlay, got %+v ok=%v", keyRemote, b, ok)
	}
	// And it shadows NOTHING: an escape wins over a command under the prefix, so a
	// key that is also a command would cost that command its prefix form. This is
	// why the overlay is not on `r` — C-t r stays respawn.
	if cmd, ok := m.lookupCmd(keyRemote); ok {
		t.Fatalf("%s is also the command key for %+v; an escape must not shadow one", keyRemote, cmd)
	}
	if cmd, ok := m.lookupCmd(keyRespawn); !ok || cmd.act != actRespawn {
		t.Fatalf("C-t %s must still respawn, got %+v ok=%v", keyRespawn, cmd, ok)
	}
	if !isEscape(actRemote) {
		t.Fatal("actRemote should be an escape")
	}

	// Through the real dispatch: C-t @ opens it from the dashboard.
	m.prefix = true
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(keyRemote)})
	if got := next.(model).mode; got != modeRemote {
		t.Fatalf("C-t %s left the cockpit in %v", keyRemote, got)
	}
}

func TestGoodbyeTellsTheCockpitWhyItWent(t *testing.T) {
	m := baseModel()
	m.applyEvent(proto.ServerMsg{Type: "goodbye", Error: "this connection was kicked from local"})
	if !m.backendDown {
		t.Fatal("a goodbye should mark the backend gone")
	}
	if !strings.Contains(m.status, "kicked from local") {
		t.Fatalf("status = %q, want the reason", m.status)
	}
}

// TestRemoteUsesTheOverlayKeys checks the overlay moves on j/k again. It spent
// k on the kick, which cost this one screen the movement keys every other
// overlay has and put a verb that hangs up on somebody next to the up arrow.
func TestRemoteUsesTheOverlayKeys(t *testing.T) {
	m := remoteModel(enabledStatus())
	if m.remoteConnCount() < 2 {
		t.Skip("needs at least two connections to move between")
	}

	next, _ := m.handleRemoteKey("j")
	if got := next.(model).remoteSel; got != 1 {
		t.Errorf("j should move down, sel = %d", got)
	}
	next, _ = next.(model).handleRemoteKey("k")
	if got := next.(model).remoteSel; got != 0 {
		t.Errorf("k should move back up, sel = %d", got)
	}
	next, _ = next.(model).handleRemoteKey("G")
	if got := next.(model).remoteSel; got != m.remoteConnCount()-1 {
		t.Errorf("G should jump to the last connection, sel = %d", got)
	}
}
