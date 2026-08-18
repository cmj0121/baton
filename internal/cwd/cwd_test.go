package cwd

import "testing"

// TestParseTrack: the four modes round-trip, and anything else falls back to auto
// rather than to off — failing to learn a directory costs a convenience, so the
// forgiving direction is also the useful one.
func TestParseTrack(t *testing.T) {
	cases := map[string]struct {
		want Track
		ok   bool
	}{
		"auto": {Auto, true}, "osc7": {OSC7, true}, "proc": {Proc, true}, "off": {Off, true},
		" OSC7 ": {OSC7, true}, "": {Auto, false}, "nonsense": {Auto, false},
	}
	for in, c := range cases {
		got, ok := ParseTrack(in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseTrack(%q) = %q/%v, want %q/%v", in, got, ok, c.want, c.ok)
		}
	}
}

// TestParseRestore: likewise, with shells-only as the fallback.
func TestParseRestore(t *testing.T) {
	cases := map[string]struct {
		want Restore
		ok   bool
	}{
		"shells": {Shells, true}, "all": {All, true}, "off": {NoRestore, true},
		" All ": {All, true}, "": {Shells, false}, "true": {Shells, false},
	}
	for in, c := range cases {
		got, ok := ParseRestore(in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseRestore(%q) = %q/%v, want %q/%v", in, got, ok, c.want, c.ok)
		}
	}
}

// TestTrackSources: each mode reads exactly the sources it names, so "proc" makes
// no use of a shell report and "osc7" costs no syscalls.
func TestTrackSources(t *testing.T) {
	cases := []struct {
		t             Track
		report, table bool
	}{
		{Auto, true, true},
		{OSC7, true, false},
		{Proc, false, true},
		{Off, false, false},
	}
	for _, c := range cases {
		if c.t.ReadsReport() != c.report || c.t.ReadsProcess() != c.table {
			t.Errorf("%q reads report=%v table=%v, want %v/%v",
				c.t, c.t.ReadsReport(), c.t.ReadsProcess(), c.report, c.table)
		}
	}
}

// TestRestores: the default separates the two cases deliberately — a shell is
// wherever you left it, an agent's task was set where it was launched.
func TestRestores(t *testing.T) {
	cases := []struct {
		r            Restore
		shell, agent bool
	}{
		{Shells, true, false},
		{All, true, true},
		{NoRestore, false, false},
	}
	for _, c := range cases {
		if c.r.Restores(false) != c.shell || c.r.Restores(true) != c.agent {
			t.Errorf("%q restores shell=%v agent=%v, want %v/%v",
				c.r, c.r.Restores(false), c.r.Restores(true), c.shell, c.agent)
		}
	}
}
