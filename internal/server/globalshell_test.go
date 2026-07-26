package server_test

import (
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/client"
	"github.com/cmj0121/baton/internal/paths"
	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/server"
)

// TestGlobalShellPanelSpawn checks the global shell: it is the singleton, runs as
// a plain shell in $HOME (not the requested dir), carries a "shell · <id>" title,
// and — unlike the conductor — gets no scoped-role identity env.
func TestGlobalShellPanelSpawn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	sock := filepath.Join(t.TempDir(), "baton.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	srv := server.New(ln)
	go func() { _ = srv.Serve() }()

	c, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	recv(t, c) // welcome
	recv(t, c) // empty panels

	// Spawn the global shell. The requested dir (/tmp) must be overridden by $HOME.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell", Dir: "/tmp", GlobalShell: true}); err != nil {
		t.Fatalf("create global shell: %v", err)
	}
	snap := recv(t, c)
	var gid string
	var found proto.Panel
	for _, p := range snap.Panels {
		if p.GlobalShell {
			gid, found = p.ID, p
		}
	}
	if gid == "" {
		t.Fatalf("no global shell in snapshot %+v", snap.Panels)
	}
	if found.Kind != "shell" {
		t.Fatalf("global shell kind = %q, want shell", found.Kind)
	}
	if !strings.HasPrefix(found.Title, "shell · ") {
		t.Fatalf("global shell title = %q, want a shell label", found.Title)
	}

	// It opens in $HOME, never the requested /tmp.
	if dir := srv.PanelDir(gid); dir != home {
		t.Fatalf("global shell dir = %q, want $HOME %q", dir, home)
	}

	// No identity env: it drives nothing, so the scoped role is never injected.
	for _, e := range srv.PanelEnv(gid) {
		if strings.HasPrefix(e, paths.EnvRole+"=") || strings.HasPrefix(e, paths.EnvSocket+"=") {
			t.Fatalf("global shell must carry no scoped-role env, got %q", e)
		}
	}

	// Singleton: a second global-shell create is refused.
	if err := c.Send(proto.Command{Action: "panel.create", Kind: "shell", GlobalShell: true}); err != nil {
		t.Fatalf("second global shell: %v", err)
	}
	if got := recv(t, c); got.Type != "error" || !strings.Contains(got.Error, "already exists") {
		t.Fatalf("second global shell should be refused, got %+v", got)
	}
}
