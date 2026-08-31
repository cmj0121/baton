//go:build unix

package score

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// rlimitChildEnv marks the re-executed copy of the test binary that is allowed
// to lower its own RLIMIT_FSIZE. Set in the child's environment only.
const rlimitChildEnv = "BATON_SCORE_RLIMIT_CHILD"

// forkRlimitTest re-runs the calling test in a child copy of this binary and
// fails if the child does, returning true in the parent and false in the child.
//
// DO NOT SIMPLIFY THIS AWAY. RLIMIT_FSIZE is a property of the PROCESS, not of
// the goroutine or the file, so lowering it in-process caps every write the test
// binary makes — including the testlog.txt the testing framework writes behind
// our backs whenever `go test` runs with caching enabled and the code under test
// opens a file. That write lands inside the lowered window and fails the whole
// package with "file too large" even though every test passed: invisible in a
// package-only run, reproducible only under `go test ./...`. Re-executing keeps
// the low limit inside a process that owns nothing but the one test, and leaves
// the parent's bookkeeping writes alone.
//
// The child is handed no -test.testlogfile of its own, so it has no framework
// write to trip over; a skip inside the child (an rlimit that cannot be read or
// lowered) is carried back so the parent never reports a pass that never ran.
func forkRlimitTest(t *testing.T) bool {
	t.Helper()
	if os.Getenv(rlimitChildEnv) != "" {
		return false
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v=true")
	cmd.Env = append(os.Environ(), rlimitChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	switch {
	case err != nil:
		t.Fatalf("rlimit child failed: %v\n%s", err, out)
	case strings.Contains(string(out), "--- SKIP"):
		t.Skipf("rlimit child skipped:\n%s", out)
	}
	return true
}

// capFileSize caps how large a file this process may write, and restores the
// previous limit when the test ends. Only ever reached inside a forkRlimitTest
// child — read the warning there before moving it back into the parent.
//
// A partial write is the hard part to reproduce honestly: the store only cares
// about the case where the kernel accepts a PREFIX of the bytes, which a
// permission error or a closed descriptor never produces. RLIMIT_FSIZE does
// exactly that, and SIGXFSZ is ignored for the duration so the process gets the
// error rather than the default kill.
//
// The limit has to be chosen carefully, above the file's current length and
// below the payload's. Above, because the unwind shrinks the file back to that
// length and ftruncate is checked against the same limit — a bound under the
// file size would block the very repair under test, which no real ENOSPC does.
// Below, because the two kernels cut in different places: linux stops at the
// byte that would cross the limit, darwin caps the length of the call itself,
// and only a payload longer than the bound is short on both.
func capFileSize(t *testing.T, limit uint64) {
	t.Helper()
	var prev syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &prev); err != nil {
		t.Skipf("RLIMIT_FSIZE unavailable: %v", err)
	}
	signal.Ignore(syscall.SIGXFSZ)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: limit, Max: prev.Max}); err != nil {
		signal.Reset(syscall.SIGXFSZ)
		t.Skipf("cannot lower RLIMIT_FSIZE: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Setrlimit(syscall.RLIMIT_FSIZE, &prev)
		signal.Reset(syscall.SIGXFSZ)
	})
}

// TestBatchedAppendIsAllOrNothing is the disk-full case batching opened. A
// batch that runs out of room lands a prefix of its events, and a prefix is
// worse here than nothing: the pass reports failure and reverts memory, so the
// log is left insisting on changes the store believes never happened, and the
// next boot settles that disagreement by re-admitting those entries as the
// operator's — downgrading the provenance invariant I6 rests on, over a full
// disk.
//
// The body runs in a re-executed child process; see forkRlimitTest.
func TestBatchedAppendIsAllOrNothing(t *testing.T) {
	if forkRlimitTest(t) {
		return
	}
	dir := t.TempDir()
	s := openStore(t, dir)

	// Thirty long bullets, each a distinct wording so none of them folds into
	// another: one admit batch of roughly eleven kilobytes, against a log that
	// does not exist yet.
	var md strings.Builder
	for i := range 30 {
		md.WriteString("- " + strconv.Itoa(i) + strings.Repeat("x", 250) + "\n")
	}
	writeMD(t, dir, md.String())
	before := s.Render(Context{})

	capFileSize(t, 1000)
	d, err := s.Reconcile()
	if err == nil {
		t.Fatal("Reconcile succeeded with no room to log what it admitted")
	}

	// Nothing of the batch survives: no prefix, no torn record, no file at all
	// where there was none.
	if data, rerr := os.ReadFile(filepath.Join(dir, scoreEvents)); rerr == nil && len(data) != 0 {
		t.Fatalf("the failed batch left %d bytes in the log:\n%s", len(data), data)
	}
	// Memory agrees with the log, and the operator's file is untouched — the id
	// write-back only happens once the events are durable.
	if s.Len() != len(before) {
		t.Fatalf("entries = %d, want the pass reverted to %d", s.Len(), len(before))
	}
	if d != (Delta{}) {
		t.Fatalf("pass = %+v, want nothing counted for a batch that never landed", d)
	}
	if got := readFile(t, dir, scoreMD); got != md.String() {
		t.Error("score.md was rewritten for a batch that never landed")
	}
}

// TestAppendRollbackRestoresTheLogExactly checks the byte-level unwind against
// a log that already holds records: the file must come back to precisely what
// it was, still ending on a newline, so the next append is a clean record and
// not a continuation of a half-written one.
//
// The body runs in a re-executed child process; see forkRlimitTest.
func TestAppendRollbackRestoresTheLogExactly(t *testing.T) {
	if forkRlimitTest(t) {
		return
	}
	dir := t.TempDir()
	s := openStore(t, dir)
	submit(t, s, "the survivor")
	before := readFile(t, dir, scoreEvents)

	capFileSize(t, uint64(len(before))+50)
	long := `{"schema":1,"event":"folded","id":"abc123","at":"2026-08-30T00:00:00Z","source":"agent","text":"` +
		strings.Repeat("y", 900) + `"}`
	if err := appendDurable(s.eventsPath, []byte(long+"\n")); err == nil {
		t.Fatal("appendDurable succeeded past the size limit")
	}

	after := readFile(t, dir, scoreEvents)
	if after != before {
		t.Fatalf("rollback left the log changed (%d bytes, was %d)", len(after), len(before))
	}
	if after[len(after)-1] != '\n' {
		t.Fatalf("the log no longer ends on a newline: %q", after[len(after)-40:])
	}
	if strings.Contains(after, "yyy") {
		t.Fatal("a fragment of the failed append survived in the log")
	}
}
