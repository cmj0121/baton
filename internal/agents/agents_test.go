package agents_test

import (
	"errors"
	"reflect"
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
