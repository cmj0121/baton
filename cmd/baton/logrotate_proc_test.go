package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// baton.log has more than one writer at a time — every `baton` process opens it,
// and the daemon holds it open for weeks — so the questions #64 raises are about
// what SEVERAL processes see across a rotation. None of them can be answered
// in-process, so this file runs real ones: the test binary re-executes itself
// with logHelperEnv set and does the other half of each demonstration.
const (
	logHelperEnv  = "BATON_TEST_LOGHELPER"
	logHelperDir  = "BATON_TEST_LOGHELPER_DIR"
	logHelperCap  = "BATON_TEST_LOGHELPER_CAP"
	logHelperID   = "BATON_TEST_LOGHELPER_ID"
	helperTimeout = 20 * time.Second
)

// TestLogRotateHelperProcess is the other side of every test in this file. It is
// a no-op in an ordinary run and the whole of the process when the parent
// re-executes the binary with logHelperEnv set.
func TestLogRotateHelperProcess(t *testing.T) {
	mode := os.Getenv(logHelperEnv)
	if mode == "" {
		t.Skip("not a helper process")
	}
	dir := os.Getenv(logHelperDir)
	path := filepath.Join(dir, "baton.log")
	maxBytes, err := strconv.ParseInt(os.Getenv(logHelperCap), 10, 64)
	if err != nil {
		t.Fatalf("helper cap: %v", err)
	}

	switch mode {
	case "appender":
		helperAppend(t, dir, path)
	case "roller":
		helperRoll(t, dir, path, maxBytes)
	case "stdio":
		helperStdio(t, path, maxBytes)
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
	os.Exit(0)
}

// startHelper re-executes this test binary in the given helper mode and returns
// the running command plus the buffer its output lands in.
func startHelper(t *testing.T, mode, dir string, maxBytes int64, id int) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLogRotateHelperProcess$", "-test.timeout=60s")
	cmd.Env = append(os.Environ(),
		logHelperEnv+"="+mode,
		logHelperDir+"="+dir,
		logHelperCap+"="+strconv.FormatInt(maxBytes, 10),
		logHelperID+"="+strconv.Itoa(id),
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s helper: %v", mode, err)
	}
	return cmd, &out
}

// waitHelper joins the helper and fails the test with everything it said.
func waitHelper(t *testing.T, cmd *exec.Cmd, out *bytes.Buffer) {
	t.Helper()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper exited with %v:\n%s", err, out.String())
	}
}

// touch drops a marker file the other process is waiting on.
func touch(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
		t.Fatalf("mark %s: %v", name, err)
	}
}

// awaitMark blocks until the marker exists. The handshake is a file rather than
// a sleep because every claim in this file is about ORDER — what a descriptor
// opened before a rotation sees after it — and a sleep would make each of them a
// statement about timing instead.
func awaitMark(t *testing.T, dir, name string) {
	t.Helper()
	deadline := time.Now().Add(helperTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", name)
}

// appendLines writes numbered lines straight to an open descriptor, which is
// what a second baton process appending to the log does.
func appendLines(t *testing.T, f *os.File, prefix string, n int) {
	t.Helper()
	for i := range n {
		if _, err := fmt.Fprintf(f, "%s%05d\n", prefix, i); err != nil {
			t.Fatalf("append %s%05d: %v", prefix, i, err)
		}
	}
}

// missingLines is which of the n numbered lines under prefix are not in text.
func missingLines(text, prefix string, n int) []string {
	var missing []string
	for i := range n {
		want := fmt.Sprintf("%s%05d\n", prefix, i)
		if !strings.Contains(text, want) {
			missing = append(missing, strings.TrimSpace(want))
		}
	}
	return missing
}

// helperAppend is the second process in TestASecondProcessLosesNothingAcrossARotation:
// it opens the log ONCE, writes on both sides of a rotation it does not perform,
// and only then reopens.
func helperAppend(t *testing.T, dir, path string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open the log: %v", err)
	}
	appendLines(t, f, "A", 100)
	touch(t, dir, "wrote-before")
	awaitMark(t, dir, "rotated")
	appendLines(t, f, "B", 100) // the same descriptor, after the rename
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	g, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("reopen the log: %v", err)
	}
	appendLines(t, g, "C", 100) // its next open, which is the new generation
	if err := g.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// helperRoll is one of the racing rotators: it opens a rotator on a log that is
// already past the cap, which rotates at open.
func helperRoll(t *testing.T, dir, path string, maxBytes int64) {
	touch(t, dir, "started-"+os.Getenv(logHelperID))
	awaitMark(t, dir, "go")
	if _, err := openLogRotator(path, maxBytes); err != nil {
		t.Fatalf("openLogRotator: %v", err)
	}
	touch(t, dir, "rolled-"+os.Getenv(logHelperID))
}

// helperStdio is the daemon's shape: it mirrors its std streams onto the log and
// then drives it through two rotations, writing to stderr in each generation.
func helperStdio(t *testing.T, path string, maxBytes int64) {
	sink, err := openLogRotator(path, maxBytes)
	if err != nil {
		t.Fatalf("openLogRotator: %v", err)
	}
	if merr := sink.mirrorStdio(); merr != nil {
		t.Fatalf("mirrorStdio: %v", merr)
	}
	fill := bytes.Repeat([]byte("x"), int(maxBytes))
	for gen := range 3 {
		fmt.Fprintf(os.Stderr, "STDERR-GEN%d\n", gen)
		if gen < 2 {
			if _, werr := sink.Write(fill); werr != nil {
				t.Fatalf("fill generation %d: %v", gen, werr)
			}
		}
	}
}

// TestASecondProcessLosesNothingAcrossARotation is the acceptance criterion that
// rules out every rotation that unlinks: another baton is appending through a
// descriptor it opened before the rename, and none of its lines may vanish.
//
// The three phases are what make the answer specific rather than merely
// reassuring. A lines are written before the rotation and B lines after it
// THROUGH THE SAME DESCRIPTOR, so both must be in the rotated file — that is the
// whole of "the old descriptor writes into the rotated file". C lines follow a
// close and a reopen, so they must be in the new one — that is "it picks up the
// new one at its next open". A design that removed the previous generation
// before renaming, as panellog's roll does, loses the B lines to an inode with
// no name and fails here.
func TestASecondProcessLosesNothingAcrossARotation(t *testing.T) {
	const cap64 = 4096
	dir := t.TempDir()
	path := filepath.Join(dir, "baton.log")

	r, err := openLogRotator(path, cap64)
	if err != nil {
		t.Fatalf("openLogRotator: %v", err)
	}
	cmd, out := startHelper(t, "appender", dir, cap64, 0)

	awaitMark(t, dir, "wrote-before")
	// Cross the cap from this process, which is the rotation the helper is not
	// party to and does not know about.
	if _, err := r.Write(bytes.Repeat([]byte("r"), cap64+1)); err != nil {
		t.Fatalf("cross the cap: %v", err)
	}
	if readAll(t, path+rotatedLogSuffix) == "" {
		t.Fatal("this process did not rotate; the helper would be proving nothing")
	}
	touch(t, dir, "rotated")
	waitHelper(t, cmd, out)

	live, rotated := readAll(t, path), readAll(t, path+rotatedLogSuffix)
	if got := missingLines(rotated, "A", 100); len(got) > 0 {
		t.Errorf("%d lines written before the rotation are not in %s: %v", len(got), path+rotatedLogSuffix, got[:1])
	}
	if got := missingLines(rotated, "B", 100); len(got) > 0 {
		t.Errorf("%d lines written AFTER the rotation through the descriptor the other process already "+
			"held are in no file: %v — they went to an unlinked inode", len(got), got[:1])
	}
	if got := missingLines(live, "C", 100); len(got) > 0 {
		t.Errorf("%d lines written after the other process reopened are not in %s: %v",
			len(got), path, got[:1])
	}
	if strings.Contains(live, "B00000\n") {
		t.Error("a line written through the pre-rotation descriptor landed in the NEW generation; " +
			"the rename did not do what this test assumes")
	}
}

// TestTwoProcessesRotatingAtOnceProduceOneRotation is the acceptance criterion
// the cross-process lock exists for.
//
// Every helper opens a rotator on a log that is already over the cap, so every
// one of them wants to rotate. Without the claim — or with the claim but without
// re-reading the SIZE under it — the second to arrive renames the winner's fresh
// empty file over the winner's rotation, and the seeded content is gone from
// both generations. The assertion is that the rotated file is byte-for-byte what
// was seeded.
func TestTwoProcessesRotatingAtOnceProduceOneRotation(t *testing.T) {
	const (
		cap64   = 4096
		racers  = 6
		seedNum = 600
	)
	dir := t.TempDir()
	path := filepath.Join(dir, "baton.log")

	var seed strings.Builder
	for i := range seedNum {
		fmt.Fprintf(&seed, "S%05d\n", i)
	}
	if int64(seed.Len()) < cap64 {
		t.Fatalf("the fixture is %d bytes, under the %d cap: nothing would rotate", seed.Len(), cap64)
	}
	if err := os.WriteFile(path, []byte(seed.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// Hold the claim while every racer arrives. Without this the first to rotate
	// is usually finished before the rest have opened the file, and they find a
	// small log and want nothing — which is a real outcome but not the one under
	// test. Held, all of them open the OVER-CAP file and park inside roll, which
	// is the instant two rotators actually collide.
	release, err := lockRotation(path + rotateLockSuffix)
	if err != nil {
		t.Fatalf("lockRotation: %v", err)
	}

	cmds := make([]*exec.Cmd, racers)
	outs := make([]*bytes.Buffer, racers)
	for i := range racers {
		cmds[i], outs[i] = startHelper(t, "roller", dir, cap64, i)
	}
	for i := range racers {
		awaitMark(t, dir, "started-"+strconv.Itoa(i))
	}
	touch(t, dir, "go")

	// They are now inside roll, waiting on the claim. What says so is the file
	// they have not made: a rotation that ignored the claim would already be on
	// disk. This is also the assertion for the claim itself.
	time.Sleep(250 * time.Millisecond)
	if _, serr := os.Stat(path + rotatedLogSuffix); serr == nil {
		release()
		t.Fatal("a rotation happened while another process held the rotation claim")
	}

	release()
	for i := range racers {
		waitHelper(t, cmds[i], outs[i])
	}

	if got := readAll(t, path+rotatedLogSuffix); got != seed.String() {
		t.Errorf("the rotated generation holds %d of the %d bytes that were seeded; %d rotators "+
			"produced more than one rotation and one of them replaced the others' work",
			len(got), seed.Len(), racers)
	}
	if got := readAll(t, path); got != "" {
		t.Errorf("the live log holds %d bytes; nothing wrote to it, so it should be the empty file "+
			"the one rotation made", len(got))
	}
	if got := readAll(t, path+rotatedLogSuffix+rotatedLogSuffix); got != "" {
		t.Errorf("a second rotated generation exists; the bound is one")
	}
}

// TestTheDaemonsStdioFollowsTheRotations is what makes mirrorStdio worth its
// two lines: the daemon's std streams are the log file its parent opened, and a
// descriptor nobody reopens is pointing at an unlinked inode after the SECOND
// rotation — which is where a panic would go.
//
// Two rotations is the whole point. One is survivable without any of this: the
// inherited descriptor still names ".1". It is the second, which replaces that
// file, that turns the daemon's last words into bytes with no path.
func TestTheDaemonsStdioFollowsTheRotations(t *testing.T) {
	const cap64 = 4096
	dir := t.TempDir()
	path := filepath.Join(dir, "baton.log")

	cmd, out := startHelper(t, "stdio", dir, cap64, 0)
	waitHelper(t, cmd, out)

	live, rotated := readAll(t, path), readAll(t, path+rotatedLogSuffix)
	if !strings.Contains(live, "STDERR-GEN2") {
		t.Errorf("what the process wrote to stderr after two rotations is not in %s "+
			"(the rotated generation holds %d bytes): the descriptor was left on an unlinked inode",
			path, len(rotated))
	}
	if !strings.Contains(rotated, "STDERR-GEN1") {
		t.Errorf("what the process wrote to stderr between the two rotations is not in %s",
			path+rotatedLogSuffix)
	}
	// GEN0 is not looked for: two rotations have been through since, and the
	// generation holding it is the one the bound drops.
}

// TestManyProcessesAppendingWhileOneRotates is the steady state #64 describes:
// one long-lived writer crossing the cap over and over while short-lived ones
// come and go, which is the daemon and the CLI. Nothing here may fail a write.
func TestManyProcessesAppendingWhileOneRotates(t *testing.T) {
	const (
		cap64   = 2048
		clients = 4
	)
	dir := t.TempDir()
	path := filepath.Join(dir, "baton.log")

	r, err := openLogRotator(path, cap64)
	if err != nil {
		t.Fatalf("openLogRotator: %v", err)
	}

	var wg sync.WaitGroup
	for c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each round is a whole `baton ctl`-shaped life: open, write, close.
			for range 40 {
				f, oerr := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
				if oerr != nil {
					t.Errorf("client %d open: %v", c, oerr)
					return
				}
				if _, werr := fmt.Fprintf(f, "client-%d\n", c); werr != nil {
					t.Errorf("client %d write: %v", c, werr)
				}
				_ = f.Close()
			}
		}()
	}
	for range 60 {
		if _, werr := r.Write(bytes.Repeat([]byte("d"), cap64/4)); werr != nil {
			t.Fatalf("daemon write: %v", werr)
		}
	}
	wg.Wait()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() >= cap64*2 {
		t.Errorf("the live log is %d bytes against a %d cap; the appenders pushed it past its bound "+
			"without anyone noticing", fi.Size(), cap64)
	}
}
