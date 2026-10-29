package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/agentplugin"
)

// TestPluginVersionsMatchBinary: the public plugin carries its version twice —
// in its own plugin.json and in the marketplace entry that lists it — and
// Claude Code decides whether an installed copy is stale by those strings, not
// by the skills' content. A release that bumps const version and forgets
// either file ships skills an operator's /plugin update never fetches, or a
// marketplace that advertises a version its plugin.json denies. So all three
// must agree; a release bump touches main.go and both JSON files together.
//
// plugin.json is read from the embedded FS, the copy the binary itself ships;
// marketplace.json is embedded by nothing, so it is read from the repo root,
// which `go test` reaches from this package's directory.
func TestPluginVersionsMatchBinary(t *testing.T) {
	want := strings.TrimPrefix(version, "v")

	raw, err := agentplugin.FS.ReadFile(".claude-plugin/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var plugin struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &plugin); err != nil {
		t.Fatalf("agentplugin/.claude-plugin/plugin.json is not JSON: %v", err)
	}
	if plugin.Version != want {
		t.Errorf("agentplugin/.claude-plugin/plugin.json version = %q, want %q (const version)", plugin.Version, want)
	}

	raw, err = os.ReadFile(filepath.Join("..", "..", ".claude-plugin", "marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	var market struct {
		Plugins []struct {
			Name    string `json:"name"`
			Source  string `json:"source"`
			Version string `json:"version"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &market); err != nil {
		t.Fatalf(".claude-plugin/marketplace.json is not JSON: %v", err)
	}
	// The entry is found by plugin.json's name because the marketplace resolves
	// an install by that pairing; an entry under another name is one no
	// `/plugin install baton@…` would ever reach.
	found := false
	for _, p := range market.Plugins {
		if p.Name != plugin.Name {
			continue
		}
		found = true
		if p.Source != "./agentplugin" {
			t.Errorf("marketplace entry %q source = %q, want ./agentplugin", p.Name, p.Source)
		}
		if p.Version != want {
			t.Errorf("marketplace entry %q version = %q, want %q (const version)", p.Name, p.Version, want)
		}
	}
	if !found {
		t.Errorf("marketplace.json lists no plugin named %q (plugin.json's name)", plugin.Name)
	}
}
