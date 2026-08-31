package config

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/paths"
)

// TestScoreDefaults covers the accessors' defaulting: an absent (or partial)
// score section must land on "enabled, $HOME/.baton", an explicit false must
// stick, and a hand-written "~" path must expand to the test's fake home.
func TestScoreDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	off, on := false, true
	tests := []struct {
		name    string
		cfg     ScoreConfig
		enabled bool
		dir     string
	}{
		{"absent section", ScoreConfig{}, true, filepath.Join(home, ".baton")},
		{"explicit false", ScoreConfig{Enabled: &off}, false, filepath.Join(home, ".baton")},
		{"explicit true", ScoreConfig{Enabled: &on}, true, filepath.Join(home, ".baton")},
		{"tilde dir expands", ScoreConfig{Dir: "~/x"}, true, filepath.Join(home, "x")},
		{"absolute dir kept", ScoreConfig{Dir: "/var/lib/baton"}, true, "/var/lib/baton"},
		{"blank dir is unset", ScoreConfig{Dir: "   "}, true, filepath.Join(home, ".baton")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsEnabled(); got != tc.enabled {
				t.Errorf("IsEnabled() = %v; want %v", got, tc.enabled)
			}
			if got := tc.cfg.Directory(); got != tc.dir {
				t.Errorf("Directory() = %q; want %q", got, tc.dir)
			}
		})
	}
}

// TestScoreLoad parses a full score block through the real Load path, so the
// YAML keys and the section's spot on the Config root are both exercised.
func TestScoreLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := paths.EnsureDir(paths.ConfigFile()); err != nil {
		t.Fatal(err)
	}
	yaml := "score:\n  dir: ~/scores\n  enabled: false\n  promote-at: 5\n  user-signals-at: 4\n" +
		"  working-set: 3\n  rank:\n    recency: 4\n    cwd: 2.5\n    profile: 1\n    group: 8\n"
	if err := os.WriteFile(paths.ConfigFile(), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Score.IsEnabled() {
		t.Error("enabled: false should disable the subsystem")
	}
	if want := filepath.Join(home, "scores"); got.Score.Directory() != want {
		t.Errorf("Directory() = %q; want %q", got.Score.Directory(), want)
	}
	// The recurrence threshold reaches the store as it stands; the floor and the
	// default belong to score.Policy.clamp, not to the parser.
	if got.Score.PromoteAt != 5 {
		t.Errorf("promote_at = %d; want 5", got.Score.PromoteAt)
	}
	// The user-signal threshold rides the same way, under the hyphenated spelling
	// every other key in this file uses.
	if got.Score.UserSignalsAt != 4 {
		t.Errorf("user-signals-at = %d; want 4", got.Score.UserSignalsAt)
	}
	// So do the ranking knobs, hyphenated like every other key in this file. A
	// weight of exactly 1 has to survive the parse unchanged, because that is how
	// an operator switches a dimension off — and an omitempty that ate it would
	// silently restore the default instead.
	if got.Score.WorkingSet != 3 {
		t.Errorf("working-set = %d; want 3", got.Score.WorkingSet)
	}
	if want := (RankConfig{Recency: 4, Cwd: 2.5, Profile: 1, Group: 8}); got.Score.Rank != want {
		t.Errorf("rank = %+v; want %+v", got.Score.Rank, want)
	}
}

// TestScoreRoundTrip saves a config carrying a score section and loads it back,
// proving the section survives the same Save/Load cycle every other section
// rides — an explicit false included, which omitempty must not eat.
func TestScoreRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	off := false
	want := Config{Score: ScoreConfig{
		Dir: "~/scores", PromoteAt: 4, UserSignalsAt: 3, WorkingSet: 5,
		Rank: RankConfig{Recency: 3, Cwd: 1, Profile: 2, Group: 1.5}, Enabled: &off,
	}}
	if err := want.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Score.Dir != "~/scores" {
		t.Errorf("dir should round-trip verbatim, got %q", got.Score.Dir)
	}
	if got.Score.Enabled == nil || *got.Score.Enabled {
		t.Errorf("enabled should round-trip as false, got %+v", got.Score.Enabled)
	}
	if got.Score.PromoteAt != 4 {
		t.Errorf("promote_at should round-trip, got %d", got.Score.PromoteAt)
	}
	if got.Score.UserSignalsAt != 3 {
		t.Errorf("user-signals-at should round-trip, got %d", got.Score.UserSignalsAt)
	}
}

// TestScoreLegacyPromoteAtIsCaught covers the key that is NOT in force. The YAML
// decoder is not strict, so a file left saying `promote_at:` while this branch
// was in testing parses without complaint, contributes nothing, and leaves the
// store on a threshold nobody chose. Nothing shipped under the underscore, so no
// shim is owed — but the daemon has to be able to say the key is being ignored,
// and it can only do that if the parse notices it.
func TestScoreLegacyPromoteAtIsCaught(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := paths.EnsureDir(paths.ConfigFile()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ConfigFile(), []byte("score:\n  promote_at: 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !got.Score.StalePromoteAt {
		t.Fatal("the underscore spelling went unnoticed")
	}
	// Noticed, never obeyed: the threshold in force is still the default.
	if got.Score.PromoteAt != 0 {
		t.Fatalf("promote-at = %d; want the stale key to contribute nothing", got.Score.PromoteAt)
	}
	// And it never travels back into the file a Save rewrites.
	if err := got.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(paths.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "promote_at") {
		t.Fatalf("a saved config carried the stale key forward:\n%s", data)
	}
}

// TestScoreBadNumbersNamesTheKey is the warning path for the mistake R3's four
// float knobs make easy. A mistyped number fails the WHOLE decode, so the daemon
// keeps its running score policy on a reload and boots on the package defaults,
// and the only line it would otherwise log names neither the section nor the
// key. The second pass has to find them without the parse that failed.
func TestScoreBadNumbersNamesTheKey(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []string
		ok   bool // the strict parse survives
	}{
		{
			name: "a mistyped weight",
			yaml: "score:\n  rank:\n    recency: fast\n    cwd: 2\n",
			want: []string{"score.rank.recency"},
		},
		{
			name: "several at once, in a stable order",
			yaml: "score:\n  working-set: lots\n  promote-at: yes\n  rank:\n    group: \"2\"\n    cwd: soon\n",
			want: []string{"score.promote-at", "score.rank.cwd", "score.rank.group", "score.working-set"},
		},
		{
			name: "a key with no value is unset, not bad",
			yaml: "score:\n  working-set:\n  rank:\n    recency:\n",
			ok:   true,
		},
		{
			name: "numbers of both shapes are fine",
			yaml: "score:\n  working-set: 3\n  rank:\n    recency: 2.5\n    cwd: 4\n",
			ok:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if err := paths.EnsureDir(paths.ConfigFile()); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.ConfigFile(), []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := Load()
			if tc.ok != (err == nil) {
				t.Fatalf("Load error = %v, want ok=%v", err, tc.ok)
			}
			if !slices.Equal(got.Score.BadNumbers, tc.want) {
				t.Fatalf("BadNumbers = %v, want %v", got.Score.BadNumbers, tc.want)
			}
			// The keys that ARE numbers stay unreported whatever else the file got
			// wrong: the warning is per key, so it can be acted on.
			if !tc.ok && slices.Contains(got.Score.BadNumbers, "score.rank.profile") {
				t.Fatalf("BadNumbers named a key the file never set: %v", got.Score.BadNumbers)
			}
		})
	}
}

// TestScoreBadNumbersSurvivesAFailedParse is the property the whole second pass
// exists for: the pass that NAMES the key must run even though the pass that
// would have read the value did not.
func TestScoreBadNumbersSurvivesAFailedParse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := paths.EnsureDir(paths.ConfigFile()); err != nil {
		t.Fatal(err)
	}
	// A good key beside the bad one, and the stale spelling too, so the pass is
	// shown to keep working on a file the strict decode gave up on.
	yaml := "score:\n  dir: ~/scores\n  promote_at: 5\n  rank:\n    cwd: nope\n"
	if err := os.WriteFile(paths.ConfigFile(), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err == nil {
		t.Fatal("a mistyped weight should fail the parse")
	}
	if !slices.Equal(got.Score.BadNumbers, []string{"score.rank.cwd"}) {
		t.Fatalf("BadNumbers = %v, want the mistyped key named", got.Score.BadNumbers)
	}
	if !got.Score.StalePromoteAt {
		t.Fatal("the stale-key check stopped working on a file that would not parse")
	}
}

// numericScoreKeys walks ScoreConfig's own yaml tags for every field a number
// belongs in, including the nested rank block, and returns the dotted key names
// a config file spells them with.
//
// Reflection rather than a written list, because a written list is the thing
// this test exists to make unnecessary.
//
// Every kind is accounted for, and one it has never met FAILS rather than being
// skipped: a walk that quietly ignores what it does not recognise is a coverage
// test that reports success on a key it never looked at — which is the same
// silence badNumbers exists to end. Pointers are followed because *int is as
// numeric as int; Enabled is the *bool that makes the deref load-bearing today.
func numericScoreKeys(t *testing.T, prefix string, rt reflect.Type) []string {
	t.Helper()
	var out []string
	for i := range rt.NumField() {
		f := rt.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch ft.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			out = append(out, prefix+tag)
		case reflect.Struct:
			out = append(out, numericScoreKeys(t, prefix+tag+".", ft)...)
		case reflect.String, reflect.Bool:
			// Nothing an operator can mistype AS a number; `loose` never watches it.
		default:
			t.Fatalf("%s%s is a %s, which this walk cannot classify: teach it whether that kind can hold a number before adding the key",
				prefix, tag, ft.Kind())
		}
	}
	return out
}

// TestBadNumbersCoversEveryNumericKey closes the gap between two places that
// spell the same keys: ScoreConfig's yaml tags, and the `loose` struct in
// config.Load that finds the ones an operator mistyped. Nothing links them, so a
// numeric key added to ScoreConfig alone would parse, would fail the strict pass
// when mistyped, and would be named by no warning at all — which is the failure
// badNumbers exists to prevent, arriving through the door it does not watch.
//
// The test writes a file that mistypes every one of them and asserts each is
// reported, so a new key fails here until `loose` learns it.
func TestBadNumbersCoversEveryNumericKey(t *testing.T) {
	keys := numericScoreKeys(t, "score.", reflect.TypeOf(ScoreConfig{}))
	if len(keys) < 6 {
		t.Fatalf("found only %v; ScoreConfig should carry promote-at, working-set and four weights", keys)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := paths.EnsureDir(paths.ConfigFile()); err != nil {
		t.Fatal(err)
	}
	// Rebuild the file from the discovered keys, so a key this test has never
	// heard of still gets written and still has to be reported.
	nested := map[string][]string{} // parent mapping -> leaf keys under it
	var b strings.Builder
	b.WriteString("score:\n")
	for _, k := range keys {
		rest := strings.TrimPrefix(k, "score.")
		parent, leaf, deep := strings.Cut(rest, ".")
		if !deep {
			fmt.Fprintf(&b, "  %s: notanumber\n", rest)
			continue
		}
		nested[parent] = append(nested[parent], leaf)
	}
	for _, parent := range slices.Sorted(maps.Keys(nested)) {
		fmt.Fprintf(&b, "  %s:\n", parent)
		for _, leaf := range nested[parent] {
			fmt.Fprintf(&b, "    %s: notanumber\n", leaf)
		}
	}
	if err := os.WriteFile(paths.ConfigFile(), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err == nil {
		t.Fatalf("a file of mistyped numbers should fail the strict parse:\n%s", b.String())
	}
	want := slices.Clone(keys)
	slices.Sort(want)
	if !slices.Equal(got.Score.BadNumbers, want) {
		t.Fatalf("BadNumbers = %v, want every numeric key named: %v\nfile:\n%s",
			got.Score.BadNumbers, want, b.String())
	}
}
