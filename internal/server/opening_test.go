package server

import (
	"maps"
	"sync"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
	"github.com/cmj0121/baton/internal/usage"
)

// fakeOpening is an openingRead that answers from a table and counts its reads.
type fakeOpening struct {
	mu    sync.Mutex
	table map[string]int64
	reads map[string]int
}

func (f *fakeOpening) read(sid string) (int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reads == nil {
		f.reads = make(map[string]int)
	}
	f.reads[sid]++
	n, ok := f.table[sid]
	return n, ok
}

func newOpeningServer(f *fakeOpening, sessions map[string][]string) *Server {
	srv := newUsageServer(stubUsage{snap: usage.Snapshot{Input: 1000}})
	srv.sessions = sessions
	srv.openingRead = f.read
	return srv
}

// TestOpeningPerPanel: every panel's current session reaches the wire under the
// panel's id, and a panel whose session has no first turn yet is absent.
func TestOpeningPerPanel(t *testing.T) {
	f := &fakeOpening{table: map[string]int64{"s-a": 41_000, "s-b": 38_500}}
	srv := newOpeningServer(f, map[string][]string{"p1": {"s-a"}, "p2": {"s-b"}, "p3": {"s-c"}})
	srv.refreshUsage()

	want := map[string]int64{"p1": 41_000, "p2": 38_500}
	if got := srv.usageMsg().UsageInfo.Opening; !maps.Equal(got, want) {
		t.Fatalf("Opening = %v, want %v", got, want)
	}
}

// TestOpeningReadOncePerSession: a held reading is never read again, and a
// session with none is asked again on the next poll until it has one.
func TestOpeningReadOncePerSession(t *testing.T) {
	f := &fakeOpening{table: map[string]int64{"s-a": 41_000}}
	srv := newOpeningServer(f, map[string][]string{"p1": {"s-a"}, "p2": {"s-b"}})
	srv.refreshUsage()
	srv.refreshUsage()
	f.mu.Lock()
	f.table["s-b"] = 12_000 // the first turn lands
	f.mu.Unlock()
	srv.refreshUsage()
	srv.refreshUsage()

	if f.reads["s-a"] != 1 {
		t.Errorf("s-a read %d times, want once — the figure is fixed for the session", f.reads["s-a"])
	}
	if f.reads["s-b"] != 3 {
		t.Errorf("s-b read %d times, want 3 — twice with no turn, once to take the reading", f.reads["s-b"])
	}
	if got := srv.usageMsg().UsageInfo.Opening["p2"]; got != 12_000 {
		t.Errorf("p2 opening = %d, want 12000", got)
	}
}

// TestOpeningFollowsTheCurrentSession: a respawn onto a new session hides the old
// reading at once — the panel is no longer paying it — and drops it from the cache.
func TestOpeningFollowsTheCurrentSession(t *testing.T) {
	f := &fakeOpening{table: map[string]int64{"s-old": 41_000}}
	srv := newOpeningServer(f, map[string][]string{"p1": {"s-old"}})
	srv.refreshUsage()
	if got := srv.usageMsg().UsageInfo.Opening["p1"]; got != 41_000 {
		t.Fatalf("precondition: p1 opening = %d, want 41000", got)
	}

	srv.mu.Lock()
	srv.sessions["p1"] = append(srv.sessions["p1"], "s-new")
	srv.mu.Unlock()
	srv.refreshUsage()

	if _, shown := srv.usageMsg().UsageInfo.Opening["p1"]; shown {
		t.Error("p1 still shows the old session's opening after a respawn")
	}
	if _, held := srv.opening["s-old"]; held {
		t.Error("the old session's reading is still cached")
	}
}

// TestOpeningDroppedWithThePanel: a closed panel's reading leaves the wire and the
// cache on the next poll.
func TestOpeningDroppedWithThePanel(t *testing.T) {
	f := &fakeOpening{table: map[string]int64{"s-a": 41_000}}
	srv := newOpeningServer(f, map[string][]string{"p1": {"s-a"}})
	srv.refreshUsage()

	srv.mu.Lock()
	delete(srv.sessions, "p1")
	srv.mu.Unlock()
	srv.refreshUsage()

	if got := srv.usageMsg().UsageInfo.Opening; got != nil {
		t.Errorf("Opening = %v after the panel closed, want nil", got)
	}
	if len(srv.opening) != 0 {
		t.Errorf("cache = %v, want empty", srv.opening)
	}
}

// TestOpeningChangeIsNews: a reading landing with every other figure unchanged
// still counts as a change, or the footer would wait for an unrelated number.
func TestOpeningChangeIsNews(t *testing.T) {
	a := &proto.UsageInfo{Tokens: 5}
	b := &proto.UsageInfo{Tokens: 5, Opening: map[string]int64{"p1": 41_000}}
	if sameUsageInfo(a, b) {
		t.Fatal("sameUsageInfo ignores a new opening reading")
	}
	c := &proto.UsageInfo{Tokens: 5, Opening: map[string]int64{"p1": 41_000}}
	if !sameUsageInfo(b, c) {
		t.Fatal("sameUsageInfo reports equal readings as a change")
	}
}

func TestAttachOpening(t *testing.T) {
	if got := attachOpening(nil, nil); got != nil {
		t.Errorf("attachOpening(nil, nil) = %+v, want nil", got)
	}
	held := &proto.UsageInfo{Tokens: 5, Opening: map[string]int64{"p1": 1}}
	if got := attachOpening(held, nil); got.Opening != nil || got.Tokens != 5 {
		t.Errorf("attachOpening(held, nil) = %+v, want the readings cleared and the rest kept", got)
	}
	if held.Opening == nil {
		t.Error("attachOpening mutated the payload it was handed")
	}
	if got := attachOpening(nil, map[string]int64{"p1": 2}); got == nil || got.Opening["p1"] != 2 {
		t.Errorf("attachOpening(nil, readings) = %+v, want a payload carrying them", got)
	}
}

// TestRefreshOpeningDefaultReader: with no reader wired, the server reads Claude
// Code's own transcripts — and a session with none reads as nothing, not zero.
func TestRefreshOpeningDefaultReader(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	srv := newUsageServer(stubUsage{})
	srv.sessions = map[string][]string{"p1": {"0b1c2d3e-0000-4000-8000-000000000001"}}
	srv.refreshOpening()
	if len(srv.opening) != 0 {
		t.Fatalf("opening = %v, want nothing read from an empty transcript root", srv.opening)
	}
}
