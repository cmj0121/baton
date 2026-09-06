package proto

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestCommandRoundTrip checks a fully-populated Command survives a JSON round
// trip unchanged — the wire is the only contract between cockpit and server, so a
// dropped or renamed field is a protocol break.
func TestCommandRoundTrip(t *testing.T) {
	in := Command{
		Action: "panel.create",
		Kind:   KindAgent,
		ID:     "p1",
		Path:   "/bin/sh",
		Args:   []string{"-l"},
		Dir:    "/tmp",
		Data:   []byte("hello"),
		Rows:   24,
		Cols:   80,
		IDs:    []string{"p1", "p2"},
		Group:  "work",
		Name:   "renamed",
		Index:  3,
		Signal: "SIGINT",
		Count:  2,
		Git:    "log",
	}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Command
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Action != in.Action || out.Kind != in.Kind || out.ID != in.ID ||
		out.Path != in.Path || out.Dir != in.Dir || out.Rows != in.Rows ||
		out.Cols != in.Cols || out.Group != in.Group || out.Name != in.Name ||
		out.Index != in.Index || out.Signal != in.Signal || out.Count != in.Count ||
		out.Git != in.Git {
		t.Errorf("scalar fields drifted: %+v != %+v", out, in)
	}
	if !bytes.Equal(out.Data, in.Data) {
		t.Errorf("Data = %q, want %q", out.Data, in.Data)
	}
	if strings.Join(out.Args, ",") != strings.Join(in.Args, ",") {
		t.Errorf("Args = %v, want %v", out.Args, in.Args)
	}
	if strings.Join(out.IDs, ",") != strings.Join(in.IDs, ",") {
		t.Errorf("IDs = %v, want %v", out.IDs, in.IDs)
	}
}

// TestCommandOmitsEmpty confirms an empty Command serialises to just its required
// action — the omitempty tags keep idle frames small and unambiguous.
func TestCommandOmitsEmpty(t *testing.T) {
	data, err := json.Marshal(Command{Action: "hello"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(data); got != `{"action":"hello"}` {
		t.Errorf("empty Command = %s, want {\"action\":\"hello\"}", got)
	}
}

// TestServerMsgRoundTrip exercises a panels snapshot with nested Panel and
// GroupView values plus raw config — the densest server→client frame.
func TestServerMsgRoundTrip(t *testing.T) {
	in := ServerMsg{
		Type:    "panels",
		Version: ProtocolVersion,
		Panels: []Panel{
			{ID: "p1", Kind: KindShell, Title: "sh", State: "running"},
			{ID: "p2", Kind: KindAgent, Title: "claude", Group: "work", Pinned: true},
		},
		Groups:   []GroupView{{Group: "work", Shown: 2}},
		Commands: []PluginCommand{{Name: "deploy", Desc: "ship it"}},
		Config:   json.RawMessage(`{"prefix":"ctrl+t"}`),
		CPU:      12.5,
		MemUsed:  1024,
		MemTotal: 4096,
	}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ServerMsg
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Type != in.Type || out.Version != in.Version || len(out.Panels) != 2 ||
		len(out.Groups) != 1 || len(out.Commands) != 1 {
		t.Fatalf("structure drifted: %+v", out)
	}
	if out.Panels[1].Group != "work" || !out.Panels[1].Pinned {
		t.Errorf("nested Panel drifted: %+v", out.Panels[1])
	}
	if out.Groups[0].Shown != 2 {
		t.Errorf("GroupView.Shown = %d, want 2", out.Groups[0].Shown)
	}
	if out.CPU != 12.5 || out.MemUsed != 1024 || out.MemTotal != 4096 {
		t.Errorf("stats drifted: cpu=%v used=%d total=%d", out.CPU, out.MemUsed, out.MemTotal)
	}
	if string(out.Config) != `{"prefix":"ctrl+t"}` {
		t.Errorf("Config = %s, want raw passthrough", out.Config)
	}
}

// TestServerMsgOutputBinary makes sure raw PTY bytes (including a NUL and high
// bytes) survive the output frame — output is the highest-volume message.
func TestServerMsgOutputBinary(t *testing.T) {
	payload := []byte{0x00, 0x1b, '[', '0', 'm', 0xff, '\n'}
	data, err := json.Marshal(ServerMsg{Type: "output", ID: "p1", Data: payload})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ServerMsg
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !bytes.Equal(out.Data, payload) {
		t.Errorf("Data = %v, want %v", out.Data, payload)
	}
}

func TestServerMsgDiffRoundTrip(t *testing.T) {
	in := ServerMsg{
		Type: "diff",
		ID:   "p1",
		Files: []DiffFile{
			{Path: "a.go", Index: "M", Staged: "diff --git a/a.go b/a.go\n+x\n"},
			{Path: "new.go", Work: "?", Unstaged: "new file: new.go\n+y\n"},
		},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ServerMsg
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Files) != 2 || out.Files[0].Path != "a.go" || out.Files[0].Staged != in.Files[0].Staged {
		t.Errorf("diff files did not round-trip: %+v", out.Files)
	}
	if out.Files[1].Work != "?" || out.Files[1].Unstaged != in.Files[1].Unstaged {
		t.Errorf("untracked file did not round-trip: %+v", out.Files[1])
	}
}

// TestConstants pins the negotiated identifiers and a sane timing relationship:
// the client's idle read window must outlast several heartbeats so one dropped
// ping never disconnects a healthy peer.
func TestConstants(t *testing.T) {
	if ProtocolVersion != "baton/1" {
		t.Errorf("ProtocolVersion = %q", ProtocolVersion)
	}
	if KindShell != "shell" || KindAgent != "agent" {
		t.Errorf("panel kinds drifted: %q %q", KindShell, KindAgent)
	}
	if ClientReadTimeout < 3*HeartbeatInterval {
		t.Errorf("ClientReadTimeout %v should be >= 3x HeartbeatInterval %v", ClientReadTimeout, HeartbeatInterval)
	}
	if EventBufferSize <= 0 {
		t.Errorf("EventBufferSize = %d, must be positive", EventBufferSize)
	}
}

// The per-vendor usage list is an additive field, and the thing that makes it
// additive is that absent and empty decode differently. An old daemon sends no
// key at all; a current one always sends at least the presets it could not find.
// If both landed as an empty slice the cockpit could not tell "this daemon does
// not know about vendors" from "this machine has no agent backends", and would
// have to guess which — so this is a wire contract, not a detail.
func TestVendorsAbsentIsNotVendorsEmpty(t *testing.T) {
	// What an older daemon sends: a usage payload with no vendors key.
	var old UsageInfo
	if err := json.Unmarshal([]byte(`{"tokens":42}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Vendors != nil {
		t.Errorf("a payload with no vendors key decoded to %#v, want nil", old.Vendors)
	}

	// What a daemon that scanned and found nothing would send.
	var empty UsageInfo
	if err := json.Unmarshal([]byte(`{"tokens":42,"vendors":[]}`), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.Vendors == nil {
		t.Error("an explicit empty vendors list decoded as nil; it is a different statement")
	}
	if len(empty.Vendors) != 0 {
		t.Errorf("an explicit empty vendors list decoded to %d entries", len(empty.Vendors))
	}
}

// Down-level: an old cockpit reading a new daemon's payload must still get the
// Anthropic quota bars, untouched. This encodes a payload carrying both shapes
// and decodes it into a struct that predates the vendor field.
func TestVendorsDoNotDisturbTheAnthropicLimits(t *testing.T) {
	pct := 61.5
	out := UsageInfo{
		Tokens: 1000,
		Limits: &LimitsInfo{
			FiveHour: &LimitWindow{UsedPercent: pct, ResetsAt: "2026-09-06T18:00:00Z"},
			Source:   "oauth",
		},
		Vendors: []VendorUsage{
			{Vendor: "claude", State: "reading", Tokens: 1000},
			{Vendor: "codex", State: "no-source", Reason: "baton has no usage source for this agent"},
		},
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}

	// The shape an older client compiled against: no Vendors field at all.
	var oldClient struct {
		Tokens int64       `json:"tokens"`
		Limits *LimitsInfo `json:"limits"`
	}
	if err := json.Unmarshal(raw, &oldClient); err != nil {
		t.Fatalf("an old client could not decode a new payload: %v", err)
	}
	if oldClient.Limits == nil || oldClient.Limits.FiveHour == nil {
		t.Fatal("the Anthropic limits did not survive the round trip")
	}
	if oldClient.Limits.FiveHour.UsedPercent != pct {
		t.Errorf("five-hour window = %v, want %v", oldClient.Limits.FiveHour.UsedPercent, pct)
	}
	if oldClient.Tokens != 1000 {
		t.Errorf("tokens = %d, want 1000", oldClient.Tokens)
	}
}

// A vendor with no reading must not encode a zero token count that an operator
// could read as "spent nothing". omitempty is what keeps the number off the wire
// entirely, so the receiving side sees the state and the reason and no figure.
func TestAVendorWithNoReadingCarriesNoNumber(t *testing.T) {
	raw, err := json.Marshal(VendorUsage{
		Vendor: "gemini",
		State:  "absent",
		Reason: "not installed on the fleet's machine",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{`"tokens"`, `"cost_usd"`, `"windows"`} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("a vendor with no reading put %s on the wire: %s", banned, raw)
		}
	}
	if !strings.Contains(string(raw), `"reason"`) {
		t.Errorf("a vendor with no reading carries no reason: %s", raw)
	}
}

// A daemon with nothing to say about vendors must put no vendors key on the wire
// at all — not "vendors":null. Both decode to nil, so this is not about the
// reader; it is the claim that turning the feature off leaves the payload byte
// for byte what it was before the field existed, which is the cheapest possible
// answer to "what does an old cockpit see".
func TestNoVendorListPutsNoVendorKeyOnTheWire(t *testing.T) {
	raw, err := json.Marshal(UsageInfo{Tokens: 42})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "vendors") {
		t.Errorf("a payload with no vendor list mentions vendors: %s", raw)
	}
}
