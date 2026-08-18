package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogFileDefault(t *testing.T) {
	if got := LogFile(); !strings.HasSuffix(got, "/.baton/baton.log") {
		t.Errorf("LogFile() = %q, want it to end with /.baton/baton.log", got)
	}
}

func TestPidFilePairsWithSocket(t *testing.T) {
	cases := map[string]string{
		"/run/baton/baton-42.sock": "/run/baton/baton-42.pid",
		"/tmp/x.sock":              "/tmp/x.pid",
		"/tmp/nosuffix":            "/tmp/nosuffix.pid",
	}
	for sock, want := range cases {
		if got := PidFile(sock); got != want {
			t.Errorf("PidFile(%q) = %q, want %q", sock, got, want)
		}
	}
}

// TestExpand covers the config-path resolution: the tilde forms, a relative
// path, and the empty value every reader treats as "unset".
func TestExpand(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if got := Expand("  "); got != "" {
		t.Errorf(`Expand("  ") = %q; want ""`, got)
	}
	if got := Expand("~"); got != home {
		t.Errorf(`Expand("~") = %q; want %q`, got, home)
	}
	if got, want := Expand("~/.baton/logs"), filepath.Join(home, ".baton/logs"); got != want {
		t.Errorf(`Expand("~/.baton/logs") = %q; want %q`, got, want)
	}
	if got := Expand("/tmp/baton-logs"); got != "/tmp/baton-logs" {
		t.Errorf("Expand of an absolute path rewrote it: %q", got)
	}
	if got := Expand("logs"); !filepath.IsAbs(got) {
		t.Errorf(`Expand("logs") = %q; want an absolute path`, got)
	}
}
