package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	liveA = "5203df8f-33a0-4684-ab31-86d209ec751e"
	liveB = "7640fc26-dab9-49e3-a41f-ef2e14a7cbde"
)

// The session id is spliced into a transcript path by Opening, so a payload whose
// id is not of the id's shape carries none.
func TestParseSessionID(t *testing.T) {
	for name, tc := range map[string]struct {
		payload string
		want    string
		ok      bool
	}{
		"present":   {`{"session_id":"` + liveA + `","model":{}}`, liveA, true},
		"absent":    {`{"model":{}}`, "", false},
		"empty":     {`{"session_id":""}`, "", false},
		"traversal": {`{"session_id":"../../etc/passwd"}`, "", false},
		"not json":  {`not json`, "", false},
	} {
		got, ok := ParseSessionID([]byte(tc.payload))
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: ParseSessionID = %q, %v; want %q, %v", name, got, ok, tc.want, tc.ok)
		}
	}
}

// A record reads back as written, and a changed session replaces it.
func TestLiveSessionRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live", "launch")
	if _, ok := ReadLiveSession(path); ok {
		t.Fatal("a missing record read as a session")
	}
	for _, sid := range []string{liveA, liveB} {
		if err := WriteLiveSession(path, sid); err != nil {
			t.Fatal(err)
		}
		if got, ok := ReadLiveSession(path); !ok || got != sid {
			t.Fatalf("ReadLiveSession = %q, %v; want %q", got, ok, sid)
		}
	}
}

// The sink runs on every render, so a record that already says so is not written
// again. The file's mtime is set back by hand: a rewrite would bring it forward.
func TestWriteLiveSessionOnlyOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launch")
	if err := WriteLiveSession(path, liveA); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	if err := WriteLiveSession(path, liveA); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || !fi.ModTime().Equal(past) {
		t.Fatalf("an unchanged session was written again (mtime %v, want %v)", fi.ModTime(), past)
	}
	if err := WriteLiveSession(path, liveB); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.ModTime().Equal(past) {
		t.Error("a changed session was not written")
	}
}

// Anything but one id in the file reads as nothing: it names a transcript path.
func TestReadLiveSessionRejectsJunk(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"traversal": "../../secret\n",
		"two ids":   liveA + " " + liveB,
		"empty":     "",
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, ok := ReadLiveSession(path); ok {
			t.Errorf("%s: read %q as a session", name, got)
		}
	}
}

// The sweep removes records of launches no panel runs, and leaves the kept ones
// and a sink's in-flight temporary alone.
func TestSweepLiveSessions(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"kept", "gone", tempPrefix + "123" + tempSuffix} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(liveA), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	SweepLiveSessions(dir, map[string]bool{"kept": true})

	for name, want := range map[string]bool{"kept": true, "gone": false, tempPrefix + "123" + tempSuffix: true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if exists := err == nil; exists != want {
			t.Errorf("%s: exists = %v, want %v", name, exists, want)
		}
	}
	SweepLiveSessions(filepath.Join(dir, "absent"), nil) // must not panic or create it
}
