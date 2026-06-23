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
