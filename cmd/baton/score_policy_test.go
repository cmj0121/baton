package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/score"
)

// TestScorePolicyGate pins the rule S1 was filed for: boot and reload decide
// what tunes the score store the SAME way.
//
// They did not. Boot handed openScore the config struct whatever config.Load's
// error said, while the reload path gated SetPolicy on that error being nil, so
// one mistyped weight was enough for a restart to apply the half-parsed file
// while a SIGHUP applied nothing — two live policies from one file, chosen by
// whether the operator reloaded or restarted. The gate is one function now, and
// this is what keeps it one.
func TestScorePolicyGate(t *testing.T) {
	cfg := config.ScoreConfig{
		PromoteAt: 8, UserSignalsAt: 4, WorkingSet: 9,
		Rank: config.RankConfig{Recency: 2, Cwd: 3, Profile: 3, Group: 3},
	}

	got, ok := scorePolicy(cfg, nil)
	if !ok {
		t.Fatal("a config that parsed should choose the policy")
	}
	want := score.Policy{
		PromoteAt: 8, UserSignalsAt: 4, WorkingSet: 9,
		Rank: score.Rank{Recency: 2, Cwd: 3, Profile: 3, Group: 3},
	}
	if got != want {
		t.Fatalf("policy = %+v, want %+v", got, want)
	}

	// The same struct, reached through a file that would not parse: config.Load
	// returns its partially-populated value ALONGSIDE the error, and taking it
	// would be a policy the operator's file never actually asked for.
	if got, ok := scorePolicy(cfg, errors.New("parse config: bad")); ok || got != (score.Policy{}) {
		t.Fatalf("a failed load chose %+v (ok=%v), want no policy at all", got, ok)
	}
}

// TestScorePolicyGateReachesTheStore closes the loop the gate exists for: the
// zero policy a failed load produces is the store's own defaults, not a
// half-parsed file's numbers. At boot there is nothing to keep, so the defaults
// are the fallback; on a reload the running policy stands untouched. Both are
// the one rule — a broken file never chooses.
func TestScorePolicyGateReachesTheStore(t *testing.T) {
	cfg := config.ScoreConfig{PromoteAt: 8, WorkingSet: 9}

	p, _ := scorePolicy(cfg, errors.New("parse config: bad"))
	st, reason := openScore(cfg, p, scoreOpenTimeout)
	if st == nil {
		t.Fatalf("openScore refused: %s", reason)
	}
	t.Cleanup(st.Close)
	if got := st.Policy(); got.PromoteAt == 8 || got.WorkingSet == 9 {
		t.Fatalf("the store booted on %+v, want the package defaults over a file that would not parse", got)
	}

	// And a store already running keeps what it has: SetPolicy is never reached
	// on that branch, which is what the reload path's `ok` guard does.
	before := st.Policy()
	if _, ok := scorePolicy(cfg, errors.New("parse config: bad")); ok {
		t.Fatal("a failed load must not reach SetPolicy")
	}
	if st.Policy() != before {
		t.Fatalf("the running policy moved to %+v, want %+v", st.Policy(), before)
	}

	// The warnings run on both paths and must not panic on the zero policy the
	// gate hands them, which is the shape a failed load always produces.
	warnScorePolicy(config.Config{Score: config.ScoreConfig{
		BadNumbers: []string{"score.rank.cwd"},
	}}, score.Policy{}, st)

	// And with NO store — switched off, or a directory another daemon holds —
	// there is no in-force policy to compare against, so the clamp half must say
	// nothing rather than report every key the operator set as clamped to zero.
	// The key that is not a number is still named: that is a fact about the file.
	warnScorePolicy(config.Config{Score: config.ScoreConfig{
		PromoteAt: 1, BadNumbers: []string{"score.rank.cwd"},
	}}, score.Policy{PromoteAt: 1}, nil)
}

// TestWarnScorePolicySaysWhatWasClamped covers the other half of the same
// obligation (S6): a weight the operator wrote and the store did not honour.
// It runs for its side effects — the assertion is that every branch is reachable
// and none of them panics on the shapes the daemon actually produces.
func TestWarnScorePolicySaysWhatWasClamped(t *testing.T) {
	cfg := config.Config{}
	cfg.Panel.TrackCwd = "off"

	// Asked for below the floor and past the ceiling, plus a threshold and a
	// budget the store raised, plus a cwd weight that cannot ever match.
	want := score.Policy{
		PromoteAt: 1, UserSignalsAt: implausibleUserSignalsAt + 1, WorkingSet: -4,
		Rank: score.Rank{Recency: 0.5, Cwd: 1e300, Profile: 0, Group: 3},
	}
	// What the store makes of that, written out rather than computed: the
	// clamping rules are internal/score's, and this file is checking that the
	// daemon SAYS what they did, not re-deriving them.
	// The store is what says which numbers are actually in force, so the warning
	// is driven by a real one rather than by re-deriving internal/score's rules
	// here — that is the whole reason it takes the store and not a policy.
	st, reason := openScore(config.ScoreConfig{Dir: t.TempDir()}, want, scoreOpenTimeout)
	if st == nil {
		t.Fatalf("openScore refused: %s", reason)
	}
	t.Cleanup(st.Close)
	if got := st.Policy(); got.Rank.Cwd != 1e6 || got.Rank.Recency != 1 {
		t.Fatalf("the store held %+v, want the out-of-range weights clamped", got)
	}
	warnScorePolicy(cfg, want, st)

	// And the quiet case: everything in range, nothing to say.
	warnScorePolicy(config.Config{}, st.Policy(), st)

	// The dead-config half of the same obligation, which the clamped cases cannot
	// reach: score.working-set has no ceiling, so a budget past what the rune
	// backstop can ever spend is honoured in full and warned about instead. The
	// store is opened on it so the branch reads an IN-FORCE number, the way the
	// daemon does, rather than the one the file asked for.
	big := score.Policy{WorkingSet: score.MaxReachableWorkingSet + 1}
	dead, reason := openScore(config.ScoreConfig{Dir: t.TempDir()}, big, scoreOpenTimeout)
	if dead == nil {
		t.Fatalf("openScore refused: %s", reason)
	}
	t.Cleanup(dead.Close)
	if got := dead.Policy().WorkingSet; got != big.WorkingSet {
		t.Fatalf("the store held working-set %d, want %d unclamped: #37 leaves the count to the operator", got, big.WorkingSet)
	}
	warnScorePolicy(config.Config{}, big, dead)
}

// TestAReloadSaysWhichScoreKeysItCannotApply is the gap between what the daemon
// did and what it said it did.
//
// Every other score key reloads for real — promote-at, working-set,
// user-signals-at and all four rank weights take effect on a SIGHUP and announce
// themselves with `score policy changed`. score.dir and score.enabled do not,
// deliberately, and the operator saw only `config reloaded on SIGHUP`: a success
// line, no complaint, and a fleet still using the old directory while the new
// one stays empty.
//
// Both directions per key, because a line that fires on a reload that changed
// nothing is a line an operator learns to scroll past — and this one has to be
// read the once it appears.
func TestAReloadSaysWhichScoreKeysItCannotApply(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		name   string
		booted config.ScoreConfig
		now    config.ScoreConfig
		want   string
	}{
		{
			name:   "the directory moved",
			booted: config.ScoreConfig{Dir: "/srv/fleet-a"},
			now:    config.ScoreConfig{Dir: "/srv/fleet-b"},
			want:   "score.dir changed but a reload cannot apply it",
		},
		{
			name:   "the memory was switched off",
			booted: config.ScoreConfig{Dir: "/srv/fleet-a", Enabled: &on},
			now:    config.ScoreConfig{Dir: "/srv/fleet-a", Enabled: &off},
			want:   "score.enabled changed but a reload cannot apply it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logged := captureBootLog(t)
			warnScoreKeysAReloadCannotApply(tc.booted, tc.now)
			got := logged()
			if !strings.Contains(got, tc.want) {
				t.Errorf("the reload ignored the key and announced success:\n%s", got)
			}
			// And it names both sides, because "cannot apply it" without the two
			// values leaves the operator guessing which one is running.
			if !strings.Contains(got, "in_force") || !strings.Contains(got, "configured") {
				t.Errorf("the line does not say what is running and what was asked for:\n%s", got)
			}
		})
	}

	// The silent direction: a reload of the same file, and a reload that changed
	// only the keys that DO apply.
	for _, tc := range []struct {
		name   string
		booted config.ScoreConfig
		now    config.ScoreConfig
	}{
		{
			name:   "nothing changed",
			booted: config.ScoreConfig{Dir: "/srv/fleet-a", Enabled: &on},
			now:    config.ScoreConfig{Dir: "/srv/fleet-a", Enabled: &on},
		},
		{
			name:   "only the keys a reload does apply changed",
			booted: config.ScoreConfig{Dir: "/srv/fleet-a", PromoteAt: 3},
			now:    config.ScoreConfig{Dir: "/srv/fleet-a", PromoteAt: 9, WorkingSet: 5},
		},
		{
			// Unset is not a change: score.enabled defaults to on and score.dir to
			// the shared default, so a file that never mentioned either says the same
			// thing before and after.
			name:   "neither key is written down at all",
			booted: config.ScoreConfig{},
			now:    config.ScoreConfig{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logged := captureBootLog(t)
			warnScoreKeysAReloadCannotApply(tc.booted, tc.now)
			if got := logged(); got != "" {
				t.Errorf("a reload that asked for nothing this cannot do still complained:\n%s", got)
			}
		})
	}
}

// writeScoreConfig writes $HOME/.baton/config pointing the fleet memory at dir,
// creating the config directory. It is the one writer of that file in this
// package's tests, so a test that boots a daemon on a chosen score.dir and one
// that edits the same key mid-run cannot drift apart on its spelling.
func writeScoreConfig(t *testing.T, home, dir string) {
	t.Helper()
	confDir := filepath.Join(home, ".baton")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "config"),
		[]byte("score:\n  dir: "+dir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestARealSIGHUPSaysTheScoreDirectoryDidNotMove is the wiring, which the
// function-level test above cannot reach: what it compares the reloaded file
// against has to be the config the store was OPENED from, and a call site
// handing it the reloaded config twice would be silent on every edit while
// passing everything above.
//
// So this is the operator's actual sequence — a running daemon, an edited
// config, a real SIGHUP — read off the daemon's own log.
func TestARealSIGHUPSaysTheScoreDirectoryDidNotMove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)
	t.Setenv("BATON_PLUGIN", "")

	write := func(dir string) { writeScoreConfig(t, home, dir) }
	booted := filepath.Join(home, "memory-a")
	write(booted)

	sock := filepath.Join(shortDir(t), "b.sock")
	t.Setenv("BATON_SOCK", sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	logged := captureBootLog(t)
	done := make(chan error, 1)
	go func() { done <- runServerOn(ln, sock, loadServerBoot(sock)) }()
	// The LISTENING line, not the pid file: the pid file is published above the
	// bind, by loadServerBoot, and signal.Notify comes hundreds of lines later, so
	// a HUP sent on the pid file racing that window kills the test process
	// outright.
	if !waitFor(func() bool { return strings.Contains(logged(), "listening") }, 300, 10*time.Millisecond) {
		t.Fatalf("the server never came up:\n%s", logged())
	}

	// The operator moves the memory and reloads, which is where they are entitled
	// to believe the edit took: every other score key really does apply on a HUP.
	write(filepath.Join(home, "memory-b"))
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}
	if !waitFor(func() bool { return strings.Contains(logged(), "config reloaded on SIGHUP") },
		200, 10*time.Millisecond) {
		t.Fatalf("the reload never ran:\n%s", logged())
	}

	_ = ln.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServerOn returned %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runServerOn did not return after the listener closed")
	}

	got := logged()
	if !strings.Contains(got, "score.dir changed but a reload cannot apply it") {
		t.Errorf("the daemon announced a successful reload and went on using %s:\n%s", booted, got)
	}
	if !strings.Contains(got, booted) {
		t.Errorf("the warning does not name the directory still in force:\n%s", got)
	}
}
