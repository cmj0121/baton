package main

import (
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

// TestScorePolicyTranslatesTheFileAndNothingElse: scorePolicy is the one
// translation between the config's shape and the store's, so boot and reload
// cannot spell the same file into two different live policies.
//
// It used to be the GATE as well, taking config.Load's error and choosing
// nothing when it was set — a correct rule, installed for one knob on
// infrastructure every other knob shares (#48). The gate now lives at the two
// seams that actually have to answer it, and what is left here has no opinion
// about whether the file parsed, because neither caller reaches it with a file
// that did not.
func TestScorePolicyTranslatesTheFileAndNothingElse(t *testing.T) {
	cfg := config.ScoreConfig{
		PromoteAt: 8, UserSignalsAt: 4, WorkingSet: 9,
		Rank: config.RankConfig{Recency: 2, Cwd: 3, Profile: 3, Group: 3},
	}
	want := score.Policy{
		PromoteAt: 8, UserSignalsAt: 4, WorkingSet: 9,
		Rank: score.Rank{Recency: 2, Cwd: 3, Profile: 3, Group: 3},
	}
	if got := scorePolicy(cfg); got != want {
		t.Fatalf("policy = %+v, want %+v", got, want)
	}
	if got := scorePolicy(config.ScoreConfig{}); got != (score.Policy{}) {
		t.Fatalf("a file that set nothing chose %+v, want nothing at all", got)
	}
}

// TestBootOnAFileThatWillNotParseTakesTheDefaultPolicy is the NEVER-HAD-ONE half
// of the failed-load rule, at the seam that holds it.
//
// A file that will not parse chooses no policy anywhere. At boot there is no
// running policy to keep, so the store is built on the package's own defaults —
// which is the one place the daemon still starts on defaults after #48, and the
// reason applyConfig's half is written differently rather than shared.
//
// The file matters: config.LoadPartial hands back what it decoded BEFORE it
// gave up, so `promote-at: 8` and `working-set: 9` are sitting in the struct it
// returns alongside the error — and this seam calls LoadPartial rather than
// Load precisely because it wants score.dir and score.enabled out of it (#77).
// Deleting the boot gate makes the store come up on them.
func TestBootOnAFileThatWillNotParseTakesTheDefaultPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", home)

	writeConfig(t, home, "score:\n  promote-at: 8\n  working-set: 9\n  rank:\n    cwd: fast\n")

	sock := filepath.Join(shortDir(t), "b.sock")
	t.Setenv("BATON_SOCK", sock)
	boot := loadServerBoot(sock)
	t.Cleanup(boot.release)
	if boot.scoreStore == nil {
		t.Fatalf("the store did not open: %s", boot.scoreReason)
	}
	if got := boot.scoreStore.Policy(); got.PromoteAt == 8 || got.WorkingSet == 9 {
		t.Fatalf("the store booted on %+v, want the package defaults over a file that would not parse", got)
	}
}

// TestWarnScorePolicySurvivesTheShapesAFailedLoadProduces: the warnings run on
// every path and must not panic on the zero policy, or on a config with no
// store behind it.
func TestWarnScorePolicySurvivesTheShapesAFailedLoadProduces(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // an unset score.dir resolves under $HOME; never the real one
	cfg := config.ScoreConfig{PromoteAt: 8, WorkingSet: 9}
	st, reason := openScore(cfg, scorePolicy(cfg), scoreOpenTimeout)
	if st == nil {
		t.Fatalf("openScore refused: %s", reason)
	}
	t.Cleanup(st.Close)

	warnScorePolicy(config.Config{Score: config.ScoreConfig{
		BadNumbers: []string{"score.rank.cwd"},
	}}, score.Policy{}, st)

	// And with NO store — switched off, or a directory another daemon holds —
	// there is no in-force policy to compare against, so the clamp half must say
	// nothing rather than report every key the operator set as clamped to zero.
	// The key that is not a number is still named: that is a fact about the file,
	// which is why applyConfig can say it on a load that applied nothing at all.
	warnScorePolicy(config.Config{Score: config.ScoreConfig{
		PromoteAt: 1, BadNumbers: []string{"score.rank.cwd"},
	}}, score.Policy{PromoteAt: 1}, nil)
	warnBadScoreNumbers(config.ScoreConfig{BadNumbers: []string{"score.working-set"}})
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

// writeConfig writes $HOME/.baton/config, creating the directory. It is the one
// writer of that file in this package's tests.
func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".baton")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeScoreConfig writes $HOME/.baton/config pointing the fleet memory at dir,
// creating the config directory. It is the one place this package's tests spell
// the score.dir key, so a test that boots a daemon on a chosen directory and one
// that edits the same key mid-run cannot drift apart on it.
//
// The file itself is written by writeConfig, which is the one writer of
// $HOME/.baton/config: two functions independently spelling the directory, the
// filename and the mode is the drift this comment used to claim was impossible.
func writeScoreConfig(t *testing.T, home, dir string) {
	t.Helper()
	writeConfig(t, home, "score:\n  dir: "+dir+"\n")
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
