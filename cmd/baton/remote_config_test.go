package main

import (
	"os"
	"testing"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/paths"
)

// TestRemoteConfigOnAFileThatWillNotParseDialsWithTheDefaults is #77 on the near
// side of a remote attach, which the issue does not name.
//
// `baton --remote` warned "dialling with the defaults" and then dialled with
// config.Load's half-decoded struct — the same false line #48 removed from the
// daemon, still standing here. settings.remote-command is the one key
// attachRemoteWith reads from this config, and it is what ssh is asked to run on
// the far side: a file that failed to parse after that line chose the far-side
// command, under a warning saying it had not.
//
// The precondition goes through config.LoadPartial, because the assertion is
// about a residue and a file the decoder abandons whole would satisfy it on any
// build.
func TestRemoteConfigOnAFileThatWillNotParseDialsWithTheDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := paths.EnsureDir(paths.ConfigFile()); err != nil {
		t.Fatal(err)
	}
	broken := "settings:\n" +
		"  remote-command: /nowhere/baton\n" +
		"score:\n" +
		"  rank:\n" +
		"    cwd: fast\n"
	if err := os.WriteFile(paths.ConfigFile(), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}

	residue, err := config.LoadPartial()
	if err == nil {
		t.Fatal("the config parsed; this test needs one that does not")
	}
	if residue.Settings.RemoteCommand != "/nowhere/baton" {
		t.Fatalf("the decoder kept no remote-command (%+v); this test is about a residue that exists", residue.Settings)
	}

	if got := remoteConfig(); got.Settings.RemoteCommand != "" {
		t.Fatalf("--remote would ask ssh to run %q, chosen by a file nobody could read", got.Settings.RemoteCommand)
	}
}

// TestRemoteConfigCarriesTheFileWhenItParses keeps the guard above from being
// satisfied by a remoteConfig that reads nothing at all.
func TestRemoteConfigCarriesTheFileWhenItParses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := paths.EnsureDir(paths.ConfigFile()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile(), []byte("settings:\n  remote-command: /opt/baton\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := remoteConfig(); got.Settings.RemoteCommand != "/opt/baton" {
		t.Fatalf("remote-command = %q, want the one the operator wrote", got.Settings.RemoteCommand)
	}
}
