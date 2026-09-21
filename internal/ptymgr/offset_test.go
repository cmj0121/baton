package ptymgr

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAppendRingReturnsTheRunningOffset pins the offset a chunk is stamped with:
// the count of every byte the pane has ever produced, through the chunk. It must
// keep counting across the ring's trim, because the server compares a live
// chunk's offset against the one its attach replay ended at — an offset that
// restarted with the ring would make every chunk after a trim look already sent.
func TestAppendRingReturnsTheRunningOffset(t *testing.T) {
	m := New()
	m.ringCap = 64 // white-box: small cap so the trim runs many times
	p := &pane{}

	var want int64
	for i := 0; i < 100; i++ {
		chunk := bytes.Repeat([]byte{'x'}, 1+i%7)
		want += int64(len(chunk))
		if got := m.appendRing(p, chunk); got != want {
			t.Fatalf("write %d: appendRing returned offset %d, want %d", i, got, want)
		}
	}
	m.ptys["p"] = p
	if got := m.SnapshotAt("p").End; got != want {
		t.Fatalf("SnapshotAt End = %d, want the running offset %d", got, want)
	}
}

// TestOutputCallbackCarriesTheChunkEnd checks the pump hands the sink the same
// running offset: every chunk arrives stamped with the count of all bytes seen
// through it. The output is paced so it spans several reads — with one chunk,
// its length and its end offset are the same number and a sink handed the
// wrong one could not tell.
func TestOutputCallbackCarriesTheChunkEnd(t *testing.T) {
	m := New()

	var mu sync.Mutex
	var seen int64
	var chunks int
	var bad []string
	m.OnOutput(func(_ string, data []byte, end int64) {
		mu.Lock()
		defer mu.Unlock()
		seen += int64(len(data))
		chunks++
		if end != seen {
			bad = append(bad, fmt.Sprintf("chunk %d ended at %d after %d bytes", chunks, end, seen))
		}
	})
	script := "printf a; sleep 0.1; printf bb; sleep 0.1; printf ccc; sleep 0.1; printf dddd; sleep 5"
	if err := m.StartCmd("p", Spec{Command: "/bin/sh", Args: []string{"-c", script}}); err != nil {
		t.Fatalf("StartCmd: %v", err)
	}
	t.Cleanup(func() { m.Stop("p") })

	const total = 10 // a + bb + ccc + dddd
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got, n, wrong := seen, chunks, bad
		mu.Unlock()
		if got >= total {
			if n < 2 {
				t.Fatalf("the output arrived in %d read(s); the test needs several to mean anything", n)
			}
			if len(wrong) > 0 {
				t.Fatalf("the sink was handed the wrong end offsets: %v", wrong)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sink saw %d of %d bytes", got, total)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSnapshotAtReportsThePaintedSize pins the size a replay is tagged with: the
// one the PTY was last set to, and the birth size for a panel nobody has sized.
// A replay tagged 0x0 would tell the cockpit nothing about the width the ring
// tail was painted at.
func TestSnapshotAtReportsThePaintedSize(t *testing.T) {
	m := New()
	m.ptys["born"] = &pane{ring: []byte("a")}
	m.ptys["sized"] = &pane{ring: []byte("b"), rows: 40, cols: 132}

	if r := m.SnapshotAt("born"); r.Rows != birthRows || r.Cols != birthCols {
		t.Errorf("never-sized panel replays at %dx%d, want the birth size %dx%d", r.Rows, r.Cols, birthRows, birthCols)
	}
	if r := m.SnapshotAt("sized"); r.Rows != 40 || r.Cols != 132 {
		t.Errorf("sized panel replays at %dx%d, want 40x132", r.Rows, r.Cols)
	}
	if r := m.SnapshotAt("nope"); r.Data != nil || r.End != 0 {
		t.Errorf("unknown panel should replay nothing, got %+v", r)
	}
}

// TestSnapshotAtStartsCleanOnlyAfterEviction covers where a replay begins. Once
// the ring has evicted its oldest bytes the window can open inside a UTF-8 rune
// or halfway through a line, and a fresh emulator would draw that fragment as
// garbage; so the replay starts at the first escape sequence or line break. A
// ring that never wrapped starts at the panel's real first byte and is kept
// whole, and the advance is bounded so a long line with no break costs at most
// a few hundred bytes of history rather than all of it.
func TestSnapshotAtStartsCleanOnlyAfterEviction(t *testing.T) {
	m := New()
	m.ringCap = minRingCap // white-box: the window is the last minRingCap bytes

	// Whole ring: nothing evicted, so even a mid-line start is the real start.
	m.ptys["whole"] = &pane{ring: []byte("tail of text\x1b[Hscreen"), written: 21}
	if got := string(m.SnapshotAt("whole").Data); got != "tail of text\x1b[Hscreen" {
		t.Errorf("an unwrapped ring must replay whole, got %q", got)
	}

	// Wrapped, opening on the continuation bytes of "é" and a torn line.
	wrapped := append([]byte{0xa9, 0xa9}, []byte("torn line\x1b[Hscreen")...)
	m.ptys["wrapped"] = &pane{ring: wrapped, written: 1 << 20}
	if got := string(m.SnapshotAt("wrapped").Data); got != "\x1b[Hscreen" {
		t.Errorf("a wrapped ring should replay from the first escape, got %q", got)
	}

	// Wrapped, with a line break before any escape: start on the next line.
	m.ptys["newline"] = &pane{ring: []byte("torn\nnext line"), written: 1 << 20}
	if got := string(m.SnapshotAt("newline").Data); got != "next line" {
		t.Errorf("a wrapped ring should replay from after the first line break, got %q", got)
	}

	// Wrapped, with no break inside the bound: only the torn rune is dropped.
	long := append([]byte{0x80}, bytes.Repeat([]byte{'y'}, 300)...)
	long = append(long, "\x1b[H"...)
	m.ptys["long"] = &pane{ring: long, written: 1 << 20}
	if got := m.SnapshotAt("long").Data; !bytes.Equal(got, long[1:]) {
		t.Errorf("the advance must stop at the bound, got %d bytes want %d", len(got), len(long)-1)
	}
}

// waitRing polls a panel's replay until it holds mark.
func waitRing(t *testing.T, m *Manager, id, mark string) Replay {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r := m.SnapshotAt(id); strings.Contains(string(r.Data), mark) {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%q never reached panel %s's ring", mark, id)
	return Replay{}
}

// TestOffsetsKeepRisingAcrossARespawn pins that the offset is never reused for
// an id. A respawned panel gets a fresh pane under the same id, and a client
// that replayed the dead one holds that pane's end offset; were the new pane to
// count from zero again, every chunk it produced below that mark would look
// already replayed and be skipped — a live panel with a frozen screen.
//
// The new pane's own ring is still whole, not wrapped: that check counts what
// THIS pane wrote, so its first line is not trimmed away as a torn fragment.
func TestOffsetsKeepRisingAcrossARespawn(t *testing.T) {
	m := New()
	m.OnOutput(func(string, []byte, int64) {})
	t.Cleanup(func() { m.Stop("p") })

	if err := m.StartCmd("p", Spec{Command: "/bin/sh", Args: []string{"-c", "printf OLDOLDOLDOLD; sleep 5"}}); err != nil {
		t.Fatalf("StartCmd: %v", err)
	}
	old := waitRing(t, m, "p", "OLDOLDOLDOLD")
	m.Stop("p")

	if err := m.StartCmd("p", Spec{Command: "/bin/sh", Args: []string{"-c", "printf 'ab\\ncd'; sleep 5"}}); err != nil {
		t.Fatalf("StartCmd: %v", err)
	}
	fresh := waitRing(t, m, "p", "cd")
	if fresh.End <= old.End {
		t.Errorf("the respawned pane ends at offset %d, not past the dead one's %d", fresh.End, old.End)
	}
	if !strings.HasPrefix(string(fresh.Data), "ab") {
		t.Errorf("the respawned pane's unwrapped ring lost its start: %q", fresh.Data)
	}
}
