package score

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain fails the package if a test leaves a store open.
//
// An open Store runs compactLoop, and that goroutine holds the receiver, so a
// test that never closes one keeps it alive for the whole binary. Twenty did,
// which cost nothing anyone could see until #88 gave each store a flat 2 MiB
// bitset and the suite's live floor went from 0.4 MiB to 40.4 -- at which point
// it broke a memory assertion in a neighbouring test by raising the collector's
// allowance, which is a confusing way to learn about a leak.
//
// The check is on goroutines rather than on heap size because that is the thing
// with a right answer: after the last test, no compactLoop should be running.
// A number of megabytes would need a threshold, and a threshold nobody can
// derive gets raised until it means nothing.
func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		if n, stacks := liveCompactLoops(); n > 0 {
			fmt.Fprintf(os.Stderr,
				"\n%d store(s) were left open by this package's tests.\n"+
					"An open Store's compactLoop holds it alive for the whole binary; "+
					"open through openStore, which closes on cleanup.\n\n%s\n", n, stacks)
			code = 1
		}
	}
	os.Exit(code)
}

// liveCompactLoops counts the store background loops still running, and returns
// their stacks so a failure names where they were started rather than only how
// many there are.
//
// It retries because a store closed in the last test's cleanup may not have
// finished unwinding when m.Run returns; Close waits for the loop, but a leaked
// one never arrives, so a short poll separates "still stopping" from "never
// asked to stop".
func liveCompactLoops() (int, string) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		buf := make([]byte, 1<<20)
		stacks := string(buf[:runtime.Stack(buf, true)])
		n, keep := 0, []string{}
		for _, g := range strings.Split(stacks, "\n\n") {
			if strings.Contains(g, "score.(*Store).compactLoop") {
				n++
				keep = append(keep, g)
			}
		}
		if n == 0 || time.Now().After(deadline) {
			return n, strings.Join(keep, "\n\n")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
