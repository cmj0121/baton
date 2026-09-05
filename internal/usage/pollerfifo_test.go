package usage

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Both reads in this file already had a SIZE cap and no KIND check, which is the
// pairing TestAFifoSettingsFileDoesNotStallTheSpawn names for the settings file.
// The consequence here is worse than one lost reading: both run on the usage
// poller's single goroutine, so a pipe at either name parks the footer and the
// quota bars together, for the daemon's life rather than for a tick.

// TestAFifoSinkFileDoesNotStallThePoller covers the file a sink writes and the
// daemon reads. The second half is the assertion: a FIFO must be SKIPPED, so the
// call comes back with "nothing to show" rather than not coming back.
func TestAFifoSinkFileDoesNotStallThePoller(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-limits.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}

	type result struct {
		l  Limits
		ok bool
	}
	got := make(chan result, 1)
	go func() {
		l, ok := ReadLimits(path)
		got <- result{l, ok}
	}()
	select {
	case r := <-got:
		if r.ok {
			t.Fatalf("ReadLimits reported a reading from a FIFO: %+v", r.l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadLimits did not return — a FIFO at the sink path parks the usage poller for good")
	}
}

// TestAFifoTranscriptDoesNotStallTheScan covers the transcript walk. The walk
// takes anything under the projects tree whose name ends .jsonl, and a FIFO can
// carry that name as easily as a file can.
//
// The real transcript beside it is what makes this an assertion rather than a
// smoke test: the pipe has to be skipped and the scan has to go on to finish the
// rest, so a total of zero would pass for the wrong reason.
func TestAFifoTranscriptDoesNotStallTheScan(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(proj, "aaa.jsonl"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// Named to sort after the pipe, so the walk reaches it only if the pipe let go.
	real := filepath.Join(proj, "zzz.jsonl")
	line := `{"timestamp":"` + time.Now().UTC().Format(time.RFC3339) +
		`","message":{"usage":{"input_tokens":10,"output_tokens":5}}}` + "\n"
	if err := os.WriteFile(real, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &LocalProvider{dir: dir, window: time.Hour, now: time.Now}
	got := make(chan Snapshot, 1)
	go func() {
		snap, _ := p.Fetch(context.Background())
		got <- snap
	}()
	select {
	case snap := <-got:
		if snap.Input != 10 || snap.Output != 5 {
			t.Fatalf("scan = in %d out %d, want the pipe skipped and the real transcript still counted",
				snap.Input, snap.Output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Fetch did not return — a FIFO named like a transcript parks the usage poller for good")
	}
}
