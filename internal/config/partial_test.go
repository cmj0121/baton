package config

import (
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/cmj0121/baton/internal/paths"
)

// lateFailingConfig decodes real values and THEN fails, which is the only file
// shape #77 is about. A syntax error on line one proves nothing: the decoder
// stops with nothing in hand, so every return value is the zero one whether the
// bug is present or not.
//
// The failure is a type error rather than a syntax error for the same reason.
// yaml.v3 accumulates type errors and keeps decoding, so `rank.cwd: fast` takes
// the strict pass down while everything above and below it lands in the struct —
// which is exactly how `workdir: /nowhere` and `promote-at: 2` reached the live
// daemon while its log said defaults (#48, measured).
//
// replay-kb is negative on purpose: it is the one field normalize clamps, so a
// residue that has been through normalize and one that has not are
// distinguishable by reading it.
const lateFailingConfig = `panel:
  workdir: /nowhere
  replay-kb: -5
score:
  dir: /var/lib/nowhere
  promote-at: 2
  rank:
    cwd: fast
`

func writeLateFailingConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := paths.EnsureDir(paths.ConfigFile()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile(), []byte(lateFailingConfig), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLoadOnAFileThatFailedLateReturnsNothingItDecoded is #77.
//
// The precondition is asserted first, against LoadPartial, because without it
// the assertion on Load is vacuous: a file the decoder abandoned immediately
// would also hand back the zero Config, and the test would pass on a build where
// nothing had been fixed.
func TestLoadOnAFileThatFailedLateReturnsNothingItDecoded(t *testing.T) {
	writeLateFailingConfig(t)

	residue, perr := LoadPartial()
	if perr == nil {
		t.Fatal("the file parsed; this test needs one that does not")
	}
	if residue.Panel.Workdir != "/nowhere" || residue.Score.PromoteAt != 2 {
		t.Fatalf("the decoder kept nothing worth losing (%+v); this test is about a residue that exists", residue)
	}

	got, err := Load()
	if err == nil {
		t.Fatal("Load must still report the parse failure")
	}
	if !reflect.DeepEqual(got, Config{}) {
		t.Fatalf("Load returned %+v beside its error, want the defaults a caller that only warns can safely use", got)
	}
}

// TestLoadPartialNormalisesWhatItKept is the quieter half of the same trap.
//
// The residue used to be the one Config in the project that had never been
// through normalize, because normalize ran only on the success path. A caller
// reaching for score.dir on a failed load was therefore reading a struct whose
// neighbouring fields were still whatever the file said, unbounded.
func TestLoadPartialNormalisesWhatItKept(t *testing.T) {
	writeLateFailingConfig(t)

	got, err := LoadPartial()
	if err == nil {
		t.Fatal("the file parsed; this test needs one that does not")
	}
	if got.Panel.ReplayKB != 0 {
		t.Fatalf("replay-kb = %d in the residue, want normalize's clamp on the failure path too", got.Panel.ReplayKB)
	}
	// And the residue really is the residue: the keys loadServerBoot takes from a
	// file nobody could read are still there, which is the whole reason this
	// function is exported.
	if got.Score.Dir != "/var/lib/nowhere" {
		t.Fatalf("score.dir = %q, want the key the daemon boots its store from", got.Score.Dir)
	}
}

// TestLoadPartialAndLoadAgreeOnAFileThatParses: the split is about the failure
// path and nothing else, so a good file must not be able to tell the two apart.
func TestLoadPartialAndLoadAgreeOnAFileThatParses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := paths.EnsureDir(paths.ConfigFile()); err != nil {
		t.Fatal(err)
	}
	good := "prefix: ctrl+a\npanel:\n  workdir: /tmp\nscore:\n  promote-at: 7\n"
	if err := os.WriteFile(paths.ConfigFile(), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}

	whole, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	residue, err := LoadPartial()
	if err != nil {
		t.Fatalf("LoadPartial: %v", err)
	}
	if !reflect.DeepEqual(whole, residue) {
		t.Fatalf("Load and LoadPartial disagree on a file that parses:\n Load: %+v\n Part: %+v", whole, residue)
	}
	if whole.Prefix != "ctrl+a" || whole.Score.PromoteAt != 7 {
		t.Fatalf("the good file never arrived (%+v); the comparison above is between two empty structs", whole)
	}
}

// TestLoadTUIOnAFileThatFailedLateReturnsNothingItDecoded: LoadTUI had #77's
// shape without #77's victim — its one caller reads the value only on the
// success branch — so this pins the shape rather than a live defect. Nothing
// wants a half-decoded theme, which is why there is no partial counterpart.
//
// The precondition cannot go through LoadTUI itself, since the whole point is
// that LoadTUI no longer hands the residue out. It is asserted against the
// decoder directly instead: without it, a file yaml.v3 abandons whole would make
// the assertion below true on any build.
func TestLoadTUIOnAFileThatFailedLateReturnsNothingItDecoded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := paths.EnsureDir(paths.TUIConfigFile()); err != nil {
		t.Fatal(err)
	}
	// The theme decodes; `layouts` is then a scalar where a sequence belongs, so
	// the strict pass fails with the theme already sitting in the struct.
	broken := []byte("theme:\n  brand: \"33\"\nlayouts: nope\n")
	if err := os.WriteFile(paths.TUIConfigFile(), broken, 0o600); err != nil {
		t.Fatal(err)
	}

	var residue TUIConfig
	if err := yaml.Unmarshal(broken, &residue); err == nil {
		t.Fatal("the TUI file parsed; this test needs one that does not")
	}
	if residue.Theme.Brand != "33" {
		t.Fatalf("the decoder kept nothing worth losing (%+v); this test is about a residue that exists", residue)
	}

	got, err := LoadTUI()
	if err == nil {
		t.Fatal("LoadTUI must still report the parse failure")
	}
	if !reflect.DeepEqual(got, TUIConfig{}) {
		t.Fatalf("LoadTUI returned %+v beside its error, want the built-in theme", got)
	}
}
