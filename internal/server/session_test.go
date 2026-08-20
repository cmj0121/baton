package server

import (
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/ptymgr"
	"github.com/cmj0121/baton/internal/usage"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestNewSessionIDIsUniqueV4: the flag requires a valid UUID, and re-using one is
// a hard error at launch — so every mint has to be well-formed and distinct.
func TestNewSessionIDIsUniqueV4(t *testing.T) {
	seen := make(map[string]struct{}, 128)
	for range 128 {
		id := newSessionID()
		if !uuidV4.MatchString(id) {
			t.Fatalf("newSessionID() = %q, want an RFC 4122 v4 UUID", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("newSessionID() repeated %q", id)
		}
		seen[id] = struct{}{}
	}
}

// TestWithSessionIDInjects: a Claude Code panel is launched with a session of its
// own, appended after the profile's own arguments, and the caller's spec is left
// untouched — replaying an id on respawn would fail the launch.
func TestWithSessionIDInjects(t *testing.T) {
	orig := ptymgr.Spec{Command: "/usr/local/bin/claude", Args: []string{"--effort", "high"}}
	got, id := withSessionID(orig)

	if !uuidV4.MatchString(id) {
		t.Fatalf("id = %q, want a v4 UUID", id)
	}
	want := []string{"--effort", "high", "--session-id", id}
	if !slices.Equal(got.Args, want) {
		t.Fatalf("args = %v, want %v", got.Args, want)
	}
	if !slices.Equal(orig.Args, []string{"--effort", "high"}) {
		t.Fatalf("the caller's spec was mutated: %v", orig.Args)
	}
}

// TestWithSessionIDSkips: injection is limited to Claude Code, and stands aside
// whenever the user's own arguments already decide the session's identity —
// a second --session-id would conflict, and resuming means re-entering an id on
// purpose.
func TestWithSessionIDSkips(t *testing.T) {
	cases := map[string]ptymgr.Spec{
		"a shell":              {Command: "/bin/bash"},
		"another agent":        {Command: "codex", Args: []string{"--yolo"}},
		"already pinned":       {Command: "claude", Args: []string{"--session-id", "fixed"}},
		"pinned with equals":   {Command: "claude", Args: []string{"--session-id=fixed"}},
		"resuming a session":   {Command: "claude", Args: []string{"--resume", "abc"}},
		"continuing the last":  {Command: "claude", Args: []string{"-c"}},
		"forking on resume":    {Command: "claude", Args: []string{"--fork-session"}},
		"short resume flag -r": {Command: "claude", Args: []string{"-r"}},
	}
	for name, spec := range cases {
		got, id := withSessionID(spec)
		if id != "" {
			t.Errorf("%s: minted %q, want no session id", name, id)
		}
		if !slices.Equal(got.Args, spec.Args) {
			t.Errorf("%s: args changed to %v", name, got.Args)
		}
	}
}

// TestIsClaudeCommand: the match is on the binary's name, so a path, a
// version-suffixed build and a Windows .exe all count, while a command that
// merely mentions claude does not.
func TestIsClaudeCommand(t *testing.T) {
	cases := map[string]bool{
		"claude":                   true,
		"/opt/homebrew/bin/claude": true,
		"claude.exe":               true,
		"claude-nightly":           true,
		"CLAUDE":                   true,
		"bash":                     false,
		"my-claude-wrapper":        false,
		"":                         false,
	}
	for cmd, want := range cases {
		if got := isClaudeCommand(cmd); got != want {
			t.Errorf("isClaudeCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// TestUsageInfoLockedAttributesPanels: the per-session breakdown is resolved into
// a per-panel one, summing every session a panel has run under (each respawn adds
// one), and a panel whose sessions the window never saw is left out entirely —
// absent means "not known", never "spent nothing".
func TestUsageInfoLockedAttributesPanels(t *testing.T) {
	s := &Server{
		sessions: map[string][]string{
			"p1":    {"sess-a", "sess-b"}, // respawned once
			"p2":    {"sess-c"},
			"quiet": {"sess-never-seen"},
		},
		usageWarn:  0.75,
		usageAlarm: 0.9,
	}
	since := time.Date(2026, 7, 8, 6, 0, 0, 0, time.UTC)
	snap := usage.Snapshot{
		Input: 300, CostUSD: 1.5, Source: "local", Resets: true,
		Since: since, Until: since.Add(5 * time.Hour),
		Sessions: map[string]usage.SessionUsage{
			"sess-a": {Tokens: 100, CostUSD: 0.5},
			"sess-b": {Tokens: 150, CostUSD: 0.7},
			"sess-c": {Tokens: 50, CostUSD: 0.3},
		},
	}

	info := s.usageInfoLocked(snap)
	if info == nil {
		t.Fatal("a non-empty snapshot should produce a payload")
	}
	if got := info.Panels["p1"].Tokens; got != 250 {
		t.Errorf("p1 = %d tokens, want 250 (both of its sessions)", got)
	}
	if got := info.Panels["p2"].Tokens; got != 50 {
		t.Errorf("p2 = %d tokens, want 50", got)
	}
	if _, listed := info.Panels["quiet"]; listed {
		t.Error("a panel with nothing in the window must be absent, not zero")
	}
	if !info.Resets || info.Until != since.Add(5*time.Hour).Format(time.RFC3339) {
		t.Errorf("window = %q resets=%v, want the snapshot's reset", info.Until, info.Resets)
	}
	if info.WarnAt != 0.75 || info.AlarmAt != 0.9 {
		t.Errorf("presentation settings did not ride along: %+v", info)
	}
}

// TestUsageInfoLockedEmpty: nothing to report produces no payload at all, so the
// footer stays clean rather than showing a zeroed window.
func TestUsageInfoLockedEmpty(t *testing.T) {
	s := &Server{sessions: map[string][]string{}}
	if info := s.usageInfoLocked(usage.Snapshot{Source: "local"}); info != nil {
		t.Fatalf("an empty snapshot should produce no payload, got %+v", info)
	}
}

// TestSameUsageInfo: an unchanged poll must not wake every attached frontend, but
// a window that rolled forward while the totals sat still must.
func TestSameUsageInfo(t *testing.T) {
	a := &Server{sessions: map[string][]string{"p1": {"s1"}}}
	since := time.Date(2026, 7, 8, 6, 0, 0, 0, time.UTC)
	snap := usage.Snapshot{
		Input: 10, Source: "local", Resets: true, Since: since, Until: since.Add(time.Hour),
		Sessions: map[string]usage.SessionUsage{"s1": {Tokens: 10}},
	}
	first := a.usageInfoLocked(snap)
	if !sameUsageInfo(first, a.usageInfoLocked(snap)) {
		t.Error("an identical snapshot should compare equal")
	}

	rolled := snap
	rolled.Since = since.Add(30 * time.Minute)
	rolled.Until = rolled.Since.Add(time.Hour)
	if sameUsageInfo(first, a.usageInfoLocked(rolled)) {
		t.Error("a window that moved under unchanged totals must count as changed")
	}

	if !sameUsageInfo(nil, nil) {
		t.Error("two absent payloads are the same")
	}
	if sameUsageInfo(first, nil) || sameUsageInfo(nil, first) {
		t.Error("present and absent payloads differ")
	}
}
