package paths

import (
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
