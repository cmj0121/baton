package tui

import (
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// TestRefreshAttentionRisingEdge fires the notification once when a panel enters
// attention, stays quiet while it waits, and fires again after it resolves and
// needs you anew.
func TestRefreshAttentionRisingEdge(t *testing.T) {
	m := model{fleet: []panel.Panel{{ID: "1", Title: "claude", State: panel.Running}}}

	// Rising edge: the panel starts needing you.
	m.fleet[0].State = panel.Attention
	m.refreshAttention()
	if m.status != "◆ claude needs you" {
		t.Fatalf("entering attention should notify, status = %q", m.status)
	}

	// It keeps waiting: a later refresh must not re-fire over a cleared status.
	m.status = "dashboard"
	m.refreshAttention()
	if m.status != "dashboard" {
		t.Fatalf("a still-waiting panel should not re-notify, status = %q", m.status)
	}

	// It resolves, then needs you again: the edge fires afresh.
	m.fleet[0].State = panel.Running
	m.refreshAttention()
	m.fleet[0].State = panel.Attention
	m.refreshAttention()
	if m.status != "◆ claude needs you" {
		t.Fatalf("re-entering attention should notify again, status = %q", m.status)
	}
}

// TestBellOnRisingEdge rings once when a panel enters attention and then stays
// silent until the next rising edge — even when an error status hides the text.
func TestBellOnRisingEdge(t *testing.T) {
	m := model{bellEnabled: true, fleet: []panel.Panel{{ID: "1", Title: "claude", State: panel.Running}}}

	m.fleet[0].State = panel.Attention
	m.refreshAttention()
	if !m.bellPending {
		t.Fatal("entering attention should arm the bell")
	}
	if m.takeBell() == nil {
		t.Fatal("takeBell should return the bell command when armed")
	}
	if m.bellPending {
		t.Fatal("takeBell should clear the pending flag")
	}
	if m.takeBell() != nil {
		t.Fatal("a drained bell must not ring again")
	}

	// Still waiting: no new edge, no new bell.
	m.refreshAttention()
	if m.bellPending {
		t.Fatal("a still-waiting panel should not re-arm the bell")
	}

	// An error status hides the text pop but the bell still rings on the edge.
	m.fleet[0].State = panel.Running
	m.refreshAttention()
	m.status = "error: boom"
	m.fleet[0].State = panel.Attention
	m.refreshAttention()
	if !m.bellPending {
		t.Fatal("the bell should arm on a rising edge even under an error status")
	}
}

// TestBellDisabledStaysSilent proves the config toggle gates the audible nudge
// while leaving the visual notification (status pop) intact.
func TestBellDisabledStaysSilent(t *testing.T) {
	m := model{bellEnabled: false, fleet: []panel.Panel{{ID: "1", Title: "claude", State: panel.Attention}}}
	m.refreshAttention()
	if m.bellPending {
		t.Fatal("a disabled bell must not arm")
	}
	if m.status != "◆ claude needs you" {
		t.Fatalf("the visual notification should still fire, status = %q", m.status)
	}
}

// TestKeyMapTogglesBell flips the bell setting from its key-map row and persists
// it, leaving the confirm-close toggle (the row above) untouched.
func TestKeyMapTogglesBell(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // toggling persists to $HOME/.baton/config
	m := model{mode: modeKeyMap, fleet: sampleFleet(), confirmClose: true, bellEnabled: true,
		binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}

	// The bell row sits just after the confirm-close row in the settings block.
	m.cursor = len(m.keymap()) + 1 + settingBell
	m = press(m, "enter")
	if m.bellEnabled {
		t.Fatal("enter on the bell row should toggle it off")
	}
	if !m.confirmClose {
		t.Fatal("toggling the bell must not disturb confirm-close")
	}
	if loadPrefs().bellEnabled {
		t.Fatal("the bell toggle should persist to the config as off")
	}
}

// TestRefreshAttentionMultiple counts panels when several need you at once.
func TestRefreshAttentionMultiple(t *testing.T) {
	m := model{fleet: []panel.Panel{
		{ID: "1", Title: "a", State: panel.Attention},
		{ID: "2", Title: "b", State: panel.Attention},
	}}
	m.refreshAttention()
	if !strings.Contains(m.status, "2 panels need your attention") {
		t.Fatalf("status = %q, want a 2-panel notification", m.status)
	}
}

// TestRefreshAttentionKeepsError never buries an error under a notification.
func TestRefreshAttentionKeepsError(t *testing.T) {
	m := model{status: "error: boom", fleet: []panel.Panel{{ID: "1", Title: "x", State: panel.Attention}}}
	m.refreshAttention()
	if m.status != "error: boom" {
		t.Fatalf("an error status should survive the notification, status = %q", m.status)
	}
}

// TestApplyTelemetryNotifies proves the live Monitor tick path raises the footer
// notification when it moves a panel into attention.
func TestApplyTelemetryNotifies(t *testing.T) {
	m := model{fleet: []panel.Panel{{ID: "1", Title: "claude", State: panel.Running}}}
	m.applyTelemetry(proto.ServerMsg{Type: "telemetry", Panels: []proto.Panel{
		{ID: "1", State: "attention", Activity: "needs you"},
	}})
	if !strings.Contains(m.status, "claude") || !strings.Contains(m.status, "needs you") {
		t.Fatalf("a telemetry tick into attention should notify, status = %q", m.status)
	}
}

// TestAttentionBadgeInEveryFooter checks the persistent badge rides every view's
// status bar — dashboard, zoom, and group split — so a waiting panel is never
// hidden by the view you happen to be in.
func TestAttentionBadgeInEveryFooter(t *testing.T) {
	mk := func() model {
		return model{
			width: 120, height: 30, endpoint: "local", status: "dashboard",
			fleet:     []panel.Panel{{ID: "1", Title: "claude", State: panel.Attention}},
			groupName: "api", zoomTitle: "claude",
			binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t",
		}
	}
	views := map[string]string{
		"dashboard": mk().footer(),
		"zoom":      mk().zoomFooter(),
		"group":     mk().groupZoomFooter(),
	}
	for name, foot := range views {
		if !strings.Contains(foot, "needs you") {
			t.Errorf("%s footer should carry the attention badge, got:\n%s", name, foot)
		}
	}

	// A calm fleet shows no badge.
	calm := model{width: 120, height: 30, endpoint: "local", status: "dashboard"}
	if strings.Contains(calm.footer(), "needs you") {
		t.Fatal("a calm fleet should show no attention badge")
	}
}

// TestBackendOutageAlert proves a dropped connection flags the backend down,
// stays up (no quit), and shows a red alert cap in every view's footer — cleared
// when a fresh welcome arrives.
func TestBackendOutageAlert(t *testing.T) {
	m := model{width: 120, height: 30, endpoint: "local", status: "dashboard",
		groupName: "api", zoomTitle: "sh", binds: append([]binding(nil), bindings...), prefixKey: "ctrl+t"}

	// No alert while the backend is live.
	if strings.Contains(m.footer(), "BACKEND DOWN") {
		t.Fatal("a live backend should show no outage cap")
	}

	next, _ := m.Update(connClosedMsg{})
	m = next.(model)
	if m.quitting || !m.backendDown {
		t.Fatalf("a dropped connection should alert, not quit: quitting=%v down=%v", m.quitting, m.backendDown)
	}
	for name, foot := range map[string]string{"dashboard": m.footer(), "zoom": m.zoomFooter(), "group": m.groupZoomFooter()} {
		if !strings.Contains(foot, "BACKEND DOWN") {
			t.Errorf("%s footer should carry the outage alert", name)
		}
	}

	// A fresh welcome clears the alert.
	m.applyEvent(proto.ServerMsg{Type: "welcome", Version: proto.ProtocolVersion, ServerVer: "9.9"})
	if m.backendDown {
		t.Fatal("a welcome should clear the outage flag")
	}
	if m.serverVer != "9.9" {
		t.Fatalf("welcome should record the backend version, got %q", m.serverVer)
	}
}

// TestVersionLine shows the frontend build, the backend once known, and the
// protocol.
func TestVersionLine(t *testing.T) {
	m := model{appVersion: "1.2.3"}
	if got := m.versionLine(); !strings.Contains(got, "baton 1.2.3") || !strings.Contains(got, "protocol "+proto.ProtocolVersion) {
		t.Fatalf("version line = %q", got)
	}
	if strings.Contains(m.versionLine(), "backend") {
		t.Fatal("no backend version should show before the welcome")
	}
	m.serverVer = "4.5.6"
	if !strings.Contains(m.versionLine(), "backend 4.5.6") {
		t.Fatalf("version line should include the backend once known, got %q", m.versionLine())
	}
}
