package usage

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cmj0121/baton/internal/paths"
)

// A Claude Code panel does not keep the session baton launched it with. /clear and
// /resume both move it onto another session id from inside, where baton cannot see,
// and every per-session figure keyed on the launched id — the opening cost, the
// panel's share of the window — goes quiet from then on.
//
// The status line is the one thing that hears about it: Claude Code hands it the
// session it is on now, on every render. So the sink drops that id in a small file
// named after the LAUNCHED session, and the daemon, which knows which launch each
// panel is, reads it back on its next usage poll. The file name is the launched id
// because that is unique per spawn and per fleet: a respawn gets a new name rather
// than inheriting the old run's last word, and two fleets never share one.

// maxLiveRead bounds a read of a live-session file. The file holds one UUID; a
// larger one was not written by the sink, and reading it whole buys nothing.
const maxLiveRead = 256

// ParseSessionID returns the session id a status-line payload says the panel is
// on, and whether it carries one of the id's shape.
func ParseSessionID(payload []byte) (string, bool) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(payload, &p) != nil || !sessionIDShape(p.SessionID) {
		return "", false
	}
	return p.SessionID, true
}

// WriteLiveSession records sid as the session the panel launched under path's
// name is on now. It writes only when the file does not already say so: the sink
// runs on every render, and the id changes once per /clear.
func WriteLiveSession(path, sid string) error {
	if prev, ok := ReadLiveSession(path); ok && prev == sid {
		return nil
	}
	return replaceFile(path, []byte(sid+"\n"))
}

// ReadLiveSession returns the session id recorded at path, and whether there is
// one of the id's shape. Anything else in the file reads as nothing: it names
// the transcript the opening cost is read from.
func ReadLiveSession(path string) (string, bool) {
	f, err := paths.OpenRegular(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxLiveRead))
	if err != nil {
		return "", false
	}
	sid := string(bytes.TrimSpace(b))
	if !sessionIDShape(sid) {
		return "", false
	}
	return sid, true
}

// SweepLiveSessions removes every record in dir whose name is not in keep: the
// launches no panel is running any more. A sink's temporary is left alone — it
// may be one rename from landing, and replaceFile sweeps its own leftovers — and
// a directory that is not there has nothing to sweep.
func SweepLiveSessions(dir string, keep map[string]bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if keep[name] || strings.HasPrefix(name, tempPrefix) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}
