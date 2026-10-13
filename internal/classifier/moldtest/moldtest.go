// Package moldtest stands in for mold in tests: a fake that speaks its wire
// shape — one JSON line in, one JSON line out, on a unix socket — and counts
// the dials, so a test can tell "asked and said no" from "never asked".
//
// It is a normal package rather than a _test.go file because two packages'
// suites share it, and a _test.go file cannot be imported.
package moldtest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Sock returns a fresh short socket path and removes it when t ends. It lives
// under os.MkdirTemp rather than t.TempDir(): the unix path cap is ~104 bytes,
// and a test name of any length plus the tempdir prefix overflows it before
// the test has done anything.
func Sock(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "m.sock")
}

// Fake is a mold serving on one path. Each connection's request line is
// answered with what reply returns for it; a nil return closes the connection
// without writing.
type Fake struct {
	ln    net.Listener
	reply func(req map[string]any) []byte
	done  chan struct{}
	once  sync.Once

	mu    sync.Mutex
	dials int
}

// Serve starts a fake on path and closes it when t ends. A nil reply is a
// stall: the fake reads the request and never answers, holding the connection
// until it is closed — the mold that accepts and then hangs.
func Serve(t testing.TB, path string, reply func(req map[string]any) []byte) *Fake {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &Fake{ln: ln, reply: reply, done: make(chan struct{})}
	t.Cleanup(f.Close)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.dials++
			f.mu.Unlock()
			go f.answer(t, conn)
		}
	}()
	return f
}

func (f *Fake) answer(t testing.TB, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}
	var req map[string]any
	if err := json.Unmarshal(line, &req); err != nil {
		// Not reported through t: this runs on the answer goroutine, which
		// may outlive the test. A closed connection is what the client sees,
		// and the client's error is what the test asserts on.
		return
	}
	if f.reply == nil {
		<-f.done
		return
	}
	if out := f.reply(req); out != nil {
		_, _ = conn.Write(append(out, '\n'))
	}
}

// Dials is how many connections the fake has accepted.
func (f *Fake) Dials() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dials
}

// Close stops the listener and releases any stalled connection. It is safe to
// call more than once, so a test may stop the fake early and leave Cleanup to
// call it again.
func (f *Fake) Close() {
	f.once.Do(func() {
		_ = f.ln.Close()
		close(f.done)
	})
}

// AskReply is the line a trained mold sends back for {"op":"ask"}.
func AskReply(key string, conf float64, scope string) []byte {
	return []byte(fmt.Sprintf(`{"scope":%q,"answers":[{"key":%q,"confidence":%g}]}`, scope, key, conf))
}

// StatusReply is the line mold sends back for {"op":"status"}.
func StatusReply(pid int, version, bundle string, keys int, device string) []byte {
	return []byte(fmt.Sprintf(`{"pid":%d,"version":%q,"bundle":%q,"keys":%d,"device":%q}`, pid, version, bundle, keys, device))
}
