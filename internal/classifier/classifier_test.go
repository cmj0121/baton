package classifier

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/classifier/moldtest"
)

// This suite stands in for mold with a fake that speaks its wire shape — one
// JSON line in, one JSON line out, on a unix socket — and is written against
// the two things the Monitor depends on rather than for coverage: that an
// answer only counts when it is OUR answer (scope and key, D9), and that a
// broken socket costs one log line and no further dials (the latch).
//
// The fake, and the short socket path it needs, are moldtest's.

// logCounter collects what the client logs so a test can count the lines.
type logCounter struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCounter) logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *logCounter) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func newClient(path string, lg *logCounter) *Client {
	return New(Config{Socket: path, Scope: "baton-attention", MinConfidence: 0.5, Timeout: 500 * time.Millisecond}, lg.logf)
}

// TestAskRoundTripsAndGatesOnConfidence: the request carries mold's field names
// (op, text, scope, top) and NOT min_confidence — the floor is baton's, so the
// raw score comes back — and the floor is applied here, inclusively.
func TestAskRoundTripsAndGatesOnConfidence(t *testing.T) {
	path := moldtest.Sock(t)
	var got map[string]any
	var mu sync.Mutex
	conf := 0.91
	moldtest.Serve(t, path, func(req map[string]any) []byte {
		mu.Lock()
		got = req
		mu.Unlock()
		return moldtest.AskReply("attention", conf, "baton-attention")
	})
	lg := &logCounter{}
	c := newClient(path, lg)

	a, err := c.Ask(context.Background(), "Password:")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !a.Attention || a.Confidence != 0.91 {
		t.Fatalf("got %+v, want attention at 0.91", a)
	}
	func() {
		mu.Lock()
		defer mu.Unlock()
		if got["op"] != "ask" || got["text"] != "Password:" || got["scope"] != "baton-attention" || got["top"] != float64(1) {
			t.Errorf("request on the wire: %v", got)
		}
		if _, sent := got["min_confidence"]; sent {
			t.Errorf("min_confidence was sent; the gate is baton's, so classifier status can show the raw score")
		}
	}()

	for _, tc := range []struct {
		conf float64
		want bool
	}{{0.5, true}, {0.49, false}, {0.0, false}} {
		mu.Lock()
		conf = tc.conf
		mu.Unlock()
		a, err := c.Ask(context.Background(), "x")
		if err != nil {
			t.Fatalf("ask at %g: %v", tc.conf, err)
		}
		if a.Attention != tc.want || a.Confidence != tc.conf {
			t.Errorf("at %g: got %+v, want attention=%v", tc.conf, a, tc.want)
		}
	}
	if lg.count() != 0 {
		t.Errorf("a healthy socket logged %d lines: %q", lg.count(), lg.lines)
	}
}

// TestAWrongScopeOrKeyIsANoNotAnError: mold resolves scopes by meaning, so a
// bundle without ours answers from another one with a confident score for a
// question we never asked (D9). Neither case is a socket failure — the latch
// stays open and the next ask still dials — but neither counts.
func TestAWrongScopeOrKeyIsANoNotAnError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		scope, key string
	}{
		{"scope differs", "billing", "attention"},
		{"key is not attention", "baton-attention", "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := moldtest.Sock(t)
			f := moldtest.Serve(t, path, func(map[string]any) []byte { return moldtest.AskReply(tc.key, 0.98, tc.scope) })
			lg := &logCounter{}
			c := newClient(path, lg)
			for i := range 3 {
				a, err := c.Ask(context.Background(), "x")
				if err != nil {
					t.Fatalf("ask %d: %v", i, err)
				}
				if a.Attention {
					t.Fatalf("ask %d: %+v counted as attention", i, a)
				}
				if a.Confidence != 0.98 {
					t.Errorf("ask %d: raw score %g was not carried back", i, a.Confidence)
				}
			}
			if c.Absent() {
				t.Error("a live socket answering the wrong thing tripped the latch")
			}
			if f.Dials() != 3 {
				t.Errorf("dialed %d times for 3 asks; a wrong answer must not stop the asking", f.Dials())
			}
		})
	}
}

// TestAWrongScopeIsSaidOnce: it recurs every tick until the operator trains
// the scope, so one line.
func TestAWrongScopeIsSaidOnce(t *testing.T) {
	path := moldtest.Sock(t)
	moldtest.Serve(t, path, func(map[string]any) []byte { return moldtest.AskReply("attention", 0.9, "billing") })
	lg := &logCounter{}
	c := newClient(path, lg)
	for range 3 {
		if _, err := c.Ask(context.Background(), "x"); err != nil {
			t.Fatalf("ask: %v", err)
		}
	}
	if lg.count() != 1 || !strings.Contains(lg.lines[0], `"billing"`) {
		t.Fatalf("logged %d lines, want one naming the scope: %q", lg.count(), lg.lines)
	}
}

// TestEachFailureKindTripsTheLatchOnce: a missing socket, a server that
// accepts and never answers, and one that answers garbage each return an error
// from the ask that met them, log exactly one line naming the socket, and turn
// every later ask into ErrAbsent without a dial. This is D9's "a dead
// classifier must not log every tick", one row per kind, because a latch that
// caught dial errors and let a timeout through would pass a single-kind test.
func TestEachFailureKindTripsTheLatchOnce(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, path string) *moldtest.Fake
		want  string // a fragment the first error must carry
	}{
		{"missing socket", func(*testing.T, string) *moldtest.Fake { return nil }, "no such file"},
		{"timeout", func(t *testing.T, path string) *moldtest.Fake {
			return moldtest.Serve(t, path, nil)
		}, "timeout"},
		{"malformed reply", func(t *testing.T, path string) *moldtest.Fake {
			return moldtest.Serve(t, path, func(map[string]any) []byte { return []byte(`{"answers": [`) })
		}, "malformed"},
		{"mold declines", func(t *testing.T, path string) *moldtest.Fake {
			return moldtest.Serve(t, path, func(map[string]any) []byte { return []byte(`{"error":"bundle mismatch"}`) })
		}, "bundle mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := moldtest.Sock(t)
			f := tc.setup(t, path)
			lg := &logCounter{}
			c := newClient(path, lg)
			if c.Absent() {
				t.Fatal("absent before the first ask")
			}

			_, err := c.Ask(context.Background(), "x")
			if err == nil {
				t.Fatal("the first ask succeeded")
			}
			if errors.Is(err, ErrAbsent) {
				t.Fatalf("the first ask returned ErrAbsent rather than what went wrong: %v", err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Errorf("first error %q does not say %q", err, tc.want)
			}
			if !c.Absent() {
				t.Fatal("not absent after the failure")
			}
			if lg.count() != 1 || !strings.Contains(lg.lines[0], path) {
				t.Fatalf("logged %d lines, want one naming the socket: %q", lg.count(), lg.lines)
			}

			before := 0
			if f != nil {
				before = f.Dials()
			}
			for i := range 3 {
				_, err := c.Ask(context.Background(), "x")
				if !errors.Is(err, ErrAbsent) {
					t.Errorf("ask %d after the latch: %v, want ErrAbsent", i, err)
				}
			}
			if lg.count() != 1 {
				t.Errorf("the latch logged again: %d lines %q", lg.count(), lg.lines)
			}
			if f != nil && f.Dials() != before {
				t.Errorf("the latch dialed: %d -> %d", before, f.Dials())
			}
		})
	}
}

// TestStatusIsTheWayBackFromAbsent: an absent client does not dial on an ask,
// so nothing an ask meets can re-arm it. A Status always dials, latch or not,
// and a success clears it — which is how `baton ctl classifier status` doubles
// as the operator's re-arm after `mold serve` is restarted (a reload builds a
// fresh client, the other way back). A Status that fails trips the latch like
// an ask would. The round is run twice, the second time from a mold that was
// up and stopped, so the latch is shown to close and open again on one client.
func TestStatusIsTheWayBackFromAbsent(t *testing.T) {
	path := moldtest.Sock(t)
	lg := &logCounter{}
	c := newClient(path, lg)

	if _, err := c.Ask(context.Background(), "x"); err == nil || !c.Absent() {
		t.Fatalf("a missing socket did not trip the latch: err=%v absent=%v", err, c.Absent())
	}
	if _, err := c.Status(context.Background()); err == nil {
		t.Fatal("status on a missing socket succeeded")
	}
	if lg.count() != 1 {
		t.Errorf("a failed status after the latch logged again: %q", lg.lines)
	}

	reply := func(req map[string]any) []byte {
		if req["op"] == "status" {
			return moldtest.StatusReply(4772, "0.3.1", "/b/bundle", 12, "cpu")
		}
		return moldtest.AskReply("attention", 0.7, "baton-attention")
	}
	for round := 1; round <= 2; round++ {
		// mold comes up. The latch does not notice on its own: an ask is still
		// refused without a dial.
		f := moldtest.Serve(t, path, reply)
		if _, err := c.Ask(context.Background(), "x"); !errors.Is(err, ErrAbsent) || f.Dials() != 0 {
			t.Fatalf("round %d: the latch let an ask through: err=%v dials=%d", round, err, f.Dials())
		}

		st, err := c.Status(context.Background())
		if err != nil {
			t.Fatalf("round %d: status: %v", round, err)
		}
		want := Status{PID: 4772, Version: "0.3.1", Bundle: "/b/bundle", Keys: 12, Device: "cpu"}
		if st != want {
			t.Errorf("round %d: status = %+v, want %+v", round, st, want)
		}
		if c.Absent() {
			t.Fatalf("round %d: a successful status did not clear the latch", round)
		}
		if a, err := c.Ask(context.Background(), "x"); err != nil || !a.Attention {
			t.Fatalf("round %d: ask after status: %+v, %v", round, a, err)
		}

		// mold stops: the next ask meets a dead socket and trips the latch again,
		// with a fresh log line, since the latch had been cleared.
		f.Close()
		if _, err := c.Ask(context.Background(), "x"); err == nil || errors.Is(err, ErrAbsent) || !c.Absent() {
			t.Fatalf("round %d: an ask at a stopped mold did not trip the latch: err=%v absent=%v", round, err, c.Absent())
		}
		if lg.count() != round+1 {
			t.Errorf("round %d: logged %d lines, want %d: %q", round, lg.count(), round+1, lg.lines)
		}
	}
}

// TestNilLogfAndConfigAreHonoured: New accepts no logger, and Config hands
// back what it was given for the status verb to print.
func TestNilLogfAndConfigAreHonoured(t *testing.T) {
	cfg := Config{Socket: moldtest.Sock(t), Scope: "s", MinConfidence: 0.25, Timeout: time.Second}
	c := New(cfg, nil)
	if c.Config() != cfg {
		t.Errorf("Config() = %+v, want %+v", c.Config(), cfg)
	}
	if _, err := c.Ask(context.Background(), "x"); err == nil {
		t.Fatal("a missing socket answered")
	}
	if !c.Absent() {
		t.Error("the latch did not trip without a logger")
	}
}
