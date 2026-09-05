package ptymgr

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestPaneIsDeadBeforeTheChildIsReaped pins the ordering pump states in prose
// and nothing else held: markDead runs BEFORE cmd.Wait.
//
// Why it matters: Wait reaps, and reaping frees the pid for the OS to hand to
// somebody else. Signal and remove decide whether to kill by reading the pane
// as live, so a pane that still reads live after its pid has been reaped is a
// kill aimed at whatever now owns that number — an unrelated process of this
// user's, signalled by baton. Setting the flag first means a reader that sees
// live has, at that instant, a pid that is certainly still ours.
//
// The window has to be forced open, because in the ordinary life of a panel it
// is microseconds wide. A real PTY cannot do it: the child holds the slave as
// its CONTROLLING TERMINAL, so the master reaches EOF only when the child dies,
// and Wait then returns at once. So the pump is driven directly with a pipe
// standing in for the master — the pipe is EOF the moment the test says so —
// and a child that is still alive, which parks Wait for as long as the
// assertion needs. Everything under test (pump, markDead, livePane, the real
// cmd.Wait) is the production code.
func TestPaneIsDeadBeforeTheChildIsReaped(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	// A child that outlives the "master"'s EOF, so Wait is still blocked while
	// the assertion runs. It is killed below rather than waited out.
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	m := New()
	reaped := make(chan int, 1)
	m.OnClose(func(_ string, code int) { reaped <- code })

	p := &pane{f: r, pid: cmd.Process.Pid}
	m.mu.Lock()
	m.ptys["p"] = p
	m.pumps++ // pump's deferred pumpDone balances this
	m.mu.Unlock()
	go m.pump("p", p, cmd)

	_ = w.Close() // the "master" hits EOF; the child is untouched and still running

	// livePane is the guard's only consumer, so assert through it rather than
	// through the field it reads.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, live := m.livePane("p"); !live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pane still reads live two seconds after EOF — markDead is running after cmd.Wait, " +
				"so a Signal or a remove in this window would target a pid the OS may already have reused")
		}
		time.Sleep(time.Millisecond)
	}

	// The other half of the ordering, and the half that makes the first half
	// meaningful: the child has NOT been reaped yet. Without this the test would
	// also pass on a pump that reaped first and marked dead immediately after.
	select {
	case code := <-reaped:
		t.Fatalf("cmd.Wait already returned (exit %d) — the child was reaped before the pane was marked dead", code)
	default:
	}

	_ = cmd.Process.Kill()
	select {
	case <-reaped:
	case <-time.After(5 * time.Second):
		t.Fatal("pump never finished after the child was killed")
	}
}

// TestManagerLockIsNotHeldAcrossTheCallbacks pins the one edge that keeps the
// lock graph acyclic.
//
// The server takes its fleet lock and then calls into the Manager (Snapshot,
// Tail, Pids), so the order is s.mu -> m.mu. The Manager's callbacks run the
// other way: onOutput and onClose are the server's routeOutput and onPanelExit,
// which take s.mu. If the pump held m.mu across either of them the two orders
// would both exist and the daemon would deadlock — every panel's output and
// every command, at once.
//
// Nothing enforces that today except where the calls happen to sit in pump.
// This is the test that notices if one moves: the callbacks re-enter the
// Manager, so a pump holding m.mu across them self-deadlocks on a mutex that is
// not reentrant, and the bound below reports it instead of hanging the suite.
func TestManagerLockIsNotHeldAcrossTheCallbacks(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	m := New()

	reentered := make(chan struct{}, 1)
	m.OnOutput(func(id string, _ []byte) {
		m.Snapshot(id) // takes m.mu, as the server's own path does through s.mu
		select {
		case reentered <- struct{}{}:
		default:
		}
	})
	done := make(chan struct{})
	m.OnClose(func(id string, _ int) {
		m.Pids() // takes m.mu
		close(done)
	})

	// The child exits on its own, so there is deliberately no deferred Stop:
	// Stop takes m.mu, and on the failure this test exists to catch that lock is
	// held forever by the deadlocked pump. Cleaning up would then hang the whole
	// binary and report as a timeout panic blamed on whatever ran last, instead
	// of as this test failing with its reason.
	if err := m.StartCmd("p", Spec{Command: "/bin/sh", Args: []string{"-c", "echo hello; exit 0"}}); err != nil {
		t.Fatalf("StartCmd: %v", err)
	}

	select {
	case <-reentered:
	case <-time.After(5 * time.Second):
		t.Fatal("onOutput never returned — the pump is holding the manager lock across it, " +
			"which is the m.mu -> s.mu edge that closes the deadlock cycle with the server")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("onClose never returned — the pump is holding the manager lock across it")
	}
}
