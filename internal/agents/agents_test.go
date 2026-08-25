package agents_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/agents"
)

// onlyOnPath returns a PATH resolver that finds exactly the named commands, so a
// test can describe a machine rather than depend on the one it runs on.
func onlyOnPath(names ...string) func(string) (string, error) {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return func(cmd string) (string, error) {
		if set[cmd] {
			return "/usr/bin/" + cmd, nil
		}
		return "", errors.New("not found")
	}
}

// TestPresetsAreCopied proves the catalogue hands out copies: one fleet's merge
// must not be able to rewrite the defaults the next one starts from.
func TestPresetsAreCopied(t *testing.T) {
	first := agents.Presets()
	if len(first) == 0 {
		t.Fatal("the built-in catalogue is empty")
	}
	first[0].Command = "clobbered"
	if agents.Presets()[0].Command == "clobbered" {
		t.Fatal("Presets handed out the shared slice")
	}
	if _, ok := agents.Find(agents.Presets(), agents.Default); !ok {
		t.Fatalf("the default backend %q is not in the catalogue", agents.Default)
	}
}

// TestMergeOverridesAndAppends pins the layering: a user profile replaces the
// preset of the same name outright, a new name is appended, and the preset order
// is left alone because it is the order a picker reads in.
func TestMergeOverridesAndAppends(t *testing.T) {
	got := agents.Merge([]agents.Backend{
		{Name: "claude", Command: "claude-next", Args: []string{"--fast"}},
		{Name: "zeta", Command: "zeta"},
		{Name: "my-agent", Command: "/opt/bin/my-agent"},
		{Name: "broken"}, // no command: a typo, not a backend
	})

	if b, ok := agents.Find(got, "claude"); !ok || b.Command != "claude-next" || !reflect.DeepEqual(b.Args, []string{"--fast"}) {
		t.Fatalf("a user profile should replace the preset outright, got %+v ok=%v", b, ok)
	}
	if got[0].Name != agents.Default {
		t.Fatalf("the preset order should survive a merge, got %q first", got[0].Name)
	}
	if _, ok := agents.Find(got, "broken"); ok {
		t.Fatal("a profile with no command should not become a backend")
	}

	// The appended names are sorted, so a reload cannot reshuffle the tail of the
	// list on map iteration order.
	tail := got[len(agents.Presets()):]
	if len(tail) != 2 || tail[0].Name != "my-agent" || tail[1].Name != "zeta" {
		t.Fatalf("appended names should be sorted, got %+v", tail)
	}
}

// TestDetectKeepsWhatThisMachineHas is the whole promise of the list: an entry
// survives when its command is there, and vanishes when it is not.
func TestDetectKeepsWhatThisMachineHas(t *testing.T) {
	defer agents.SetLookPath(onlyOnPath("claude", "aider"))()

	got := agents.Detect(agents.Merge(nil))
	if len(got) != 2 || got[0].Name != "claude" || got[1].Name != "aider" {
		t.Fatalf("detect should keep only the installed backends, in catalogue order, got %+v", got)
	}
}

// TestDetectKeepsIsolatedProfiles covers the one entry the host cannot answer
// for: an isolated profile runs its command inside an image, so an absence on
// this PATH says nothing about it and must not hide a backend that works.
func TestDetectKeepsIsolatedProfiles(t *testing.T) {
	defer agents.SetLookPath(onlyOnPath())() // nothing installed at all

	got := agents.Detect([]agents.Backend{
		{Name: "boxed", Command: "claude", Isolated: true},
		{Name: "host", Command: "claude"},
	})
	if len(got) != 1 || got[0].Name != "boxed" {
		t.Fatalf("only the isolated profile should survive an empty PATH, got %+v", got)
	}
}

// TestScanReportsMissesInsteadOfDroppingThem is the difference between Scan and
// Detect, and the whole point of the change: the catalogue comes back whole, with
// a verdict per entry, so a caller can say "baton knows six of these and found
// one" instead of "there is one".
func TestScanReportsMissesInsteadOfDroppingThem(t *testing.T) {
	defer agents.SetLookPath(onlyOnPath("claude"))()

	got := agents.Scan(agents.Merge(nil))
	if len(got) != len(agents.Presets()) {
		t.Fatalf("scan should keep every candidate, got %d of %d", len(got), len(agents.Presets()))
	}
	for _, s := range got {
		if want := s.Name != "claude"; s.Missing != want {
			t.Errorf("%s: Missing = %v, want %v", s.Name, s.Missing, want)
		}
	}

	// Detect is still the filtered view its callers spawn from.
	if d := agents.Detect(agents.Merge(nil)); len(d) != 1 || d[0].Name != "claude" {
		t.Fatalf("detect should still drop the misses, got %+v", d)
	}
}

// TestScanTrustsIsolatedProfiles mirrors Detect's rule: a command that runs inside
// an image is never missing because of this host's PATH, which has no opinion
// worth having about it.
func TestScanTrustsIsolatedProfiles(t *testing.T) {
	defer agents.SetLookPath(onlyOnPath())()

	got := agents.Scan([]agents.Backend{{Name: "boxed", Command: "claude", Isolated: true}})
	if len(got) != 1 || got[0].Missing {
		t.Fatalf("an isolated profile must not be marked missing on an empty PATH, got %+v", got)
	}
}

// TestEveryPresetSaysWhereToGetIt guards the reason the field was added. A preset
// with no homepage tells someone who does not recognise the name nothing at all,
// which is the state this change existed to leave behind — so a new entry cannot
// be added without one.
func TestEveryPresetSaysWhereToGetIt(t *testing.T) {
	for _, b := range agents.Presets() {
		if b.Homepage == "" {
			t.Errorf("preset %q carries no homepage", b.Name)
		}
		if !strings.HasPrefix(b.Homepage, "https://") {
			t.Errorf("preset %q homepage %q is not https", b.Name, b.Homepage)
		}
	}
}

// TestGrokIsSpawnedAsGrok pins the one preset whose documentation and whose binary
// disagree. xAI's announcement reads as though the command is "grok-build" — that
// is the product and the repo — while @xai-official/grok declares
// bin {"grok": "bin/grok"}. PATH is what Detect asks about, so the bin entry wins.
func TestGrokIsSpawnedAsGrok(t *testing.T) {
	b, ok := agents.Find(agents.Presets(), "grok")
	if !ok {
		t.Fatal("grok is not in the catalogue")
	}
	if b.Command != "grok" {
		t.Errorf("grok command = %q, want %q — check the package's bin entry, not its README", b.Command, "grok")
	}
}

// TestMergeDropsThePresetHomepage covers the layering's edge: a user profile
// replaces a preset outright. Carrying the preset's URL over would explain a
// missing /opt/bin/our-claude by pointing at Anthropic, which is a different
// program that happens to share a name.
func TestMergeDropsThePresetHomepage(t *testing.T) {
	got := agents.Merge([]agents.Backend{{Name: "claude", Command: "/opt/bin/our-claude"}})
	b, ok := agents.Find(got, "claude")
	if !ok {
		t.Fatal("the overridden profile vanished")
	}
	if b.Homepage != "" {
		t.Errorf("an overridden preset kept the preset homepage %q", b.Homepage)
	}
}
