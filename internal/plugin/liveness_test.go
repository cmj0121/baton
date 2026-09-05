package plugin_test

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/plugin"
)

// within fails if fn has not returned within a second. The bound IS the
// assertion in this file: every failure here is a call that never returns, and
// a return value cannot observe that — the test would hang instead, surfacing
// as a timeout panic blamed on whatever ran last.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s did not return within a second — the Lua worker is wedged", what)
	}
}

// TestFifoPluginPathDoesNotWedgeTheWorker is the liveness case for the plugin
// load, and it belongs here rather than only in paths because the WORKER is what
// the block costs.
//
// The Lua VM is single-threaded and every synchronous entry funnels onto it, so
// an open that never returns does not merely fail the load — it retires the
// worker for the process's life. All three of the worker's entry points go with
// it, and the test drives each: Load's caller waits on a done channel that is
// never closed, every FilterTask burns its whole FilterTimeout before failing
// open, and Close waits on a worker that never reaches its quit case.
//
// What makes it more than a plugin bug is WHERE the load runs. The boot pass
// calls it after the listener is bound and before Serve, so a FIFO at
// $HOME/.baton/plug-in.lua leaves a daemon with a live socket that never calls
// Accept — which from a cockpit is indistinguishable from a fleet that hangs.
func TestFifoPluginPathDoesNotWedgeTheWorker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plug-in.lua")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}

	h := &fakeHost{}
	p := plugin.New(h)

	within(t, "Load on a FIFO plugin path", func() {
		if _, err := p.Load(path, config.Config{}); err == nil {
			t.Error("load of a FIFO should be refused, got nil error")
		}
	})

	// The worker survived the refusal, rather than merely having been abandoned:
	// the next filter is SERVED, and the elapsed time says so. A wedged worker
	// also returns the brief unchanged — fail-open is the timeout's behaviour too
	// — so the value alone proves nothing and the duration is the real assertion.
	start := time.Now()
	within(t, "FilterTask after a refused FIFO load", func() {
		if out, allow := p.FilterTask(plugin.Brief{Prompt: "after"}); !allow || out.Prompt != "after" {
			t.Errorf("filter = (%q, %v), want the brief through", out.Prompt, allow)
		}
	})
	if d := time.Since(start); d >= plugin.FilterTimeout {
		t.Errorf("filter took %v (>= FilterTimeout %v) — it timed out rather than being served", d, plugin.FilterTimeout)
	}

	within(t, "Close after a refused FIFO load", p.Close)
}
