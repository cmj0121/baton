package score

import (
	"errors"
	"strings"
	"testing"
)

// This file covers #58's half of the refine classification: the store says
// whose failure a refused correction was, so the daemon does not have to guess
// from the message. See ErrRefine.

// TestEveryRefineRefusalOfTheCallerCarriesTheSentinel walks all eight refusals
// the three corrections make on the caller's request. Every one of them is
// reachable by a conductor with a typo, so every one of them has to be
// distinguishable from a mount that died — which is the whole point of the
// sentinel, and the reason this is a table rather than one case.
//
// The message is asserted beside the sentinel because ErrRefine's text IS the
// prefix these eight already carried: the wrapping was chosen so the conductor
// is answered with exactly the bytes it was answered with before, and a rename
// of the sentinel that quietly rewords every refusal on the wire is what this
// half catches.
func TestEveryRefineRefusalOfTheCallerCarriesTheSentinel(t *testing.T) {
	for _, tc := range []struct {
		name string
		// call runs the correction on a store holding keep and gone.
		call func(s *Store, keep, gone Entry) error
		want string
	}{
		{
			name: "an id naming no entry",
			call: func(s *Store, _, _ Entry) error { return s.Lower("nosuch") },
			want: `score refine: no entry "nosuch"`,
		},
		{
			name: "a merge from an id naming no entry",
			call: func(s *Store, keep, _ Entry) error { return s.Merge(keep.Id, "nosuch") },
			want: `score refine: no entry "nosuch"`,
		},
		{
			name: "a merge of an entry into itself",
			call: func(s *Store, keep, _ Entry) error { return s.Merge(keep.Id, keep.Id) },
			want: "score refine: an entry cannot be merged into itself",
		},
		{
			name: "a reword to nothing",
			call: func(s *Store, keep, _ Entry) error { return s.Reword(keep.Id, "  \t ") },
			want: "score refine: reword needs the new wording",
		},
		{
			name: "a reword past the entry cap",
			call: func(s *Store, keep, _ Entry) error {
				return s.Reword(keep.Id, strings.Repeat("x", maxEntryRunes+1))
			},
			want: "score refine: wording is",
		},
		{
			name: "a reword that changes nothing",
			call: func(s *Store, keep, _ Entry) error { return s.Reword(keep.Id, keep.Text) },
			want: "score refine: the wording is unchanged",
		},
		{
			name: "a reword onto what another entry already says",
			call: func(s *Store, keep, gone Entry) error { return s.Reword(keep.Id, gone.Text) },
			want: "already says that",
		},
		{
			name: "a lower on the bottom rung",
			call: func(s *Store, keep, _ Entry) error { return s.Lower(keep.Id) },
			want: "is already on the bottom rung",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openStore(t, t.TempDir())
			keep := submit(t, s, "the agent asks before it deletes")
			gone := submit(t, s, "it runs the tests before it pushes")

			err := tc.call(s, keep, gone)
			if err == nil {
				t.Fatalf("%s was accepted, so there is no refusal to classify", tc.name)
			}
			if !errors.Is(err, ErrRefine) {
				t.Errorf("%v does not carry ErrRefine, so the daemon reports it as a broken store", err)
			}
			if got := err.Error(); !strings.Contains(got, tc.want) {
				t.Errorf("the conductor is answered %q, want it to still contain %q", got, tc.want)
			}
		})
	}
}

// TestARefineTheDiskRefusedCarriesNoSentinel is the other side, and it is the
// case that says why the write latch cannot be the daemon's only voice.
//
// The DIRECTORY is unwritable, which leaves the events log — an existing file —
// appendable and stops only the score.md rewrite, since the store rewrites that
// file through a sibling temp. So the reword's durable append LANDS, the
// operator's file is what could not be written, and Store.WriteFailing is false
// throughout: appendEvents covers the log and says so in its own comment. A
// disk refusal that no latch holds still has to reach the operator, and the
// only thing that can tell it from a mistyped id is the absence of ErrRefine.
func TestARefineTheDiskRefusedCarriesNoSentinel(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	e := submit(t, s, "the agent asks before it deletes")

	restore := unwritable(t, dir)
	err := s.Reword(e.Id, "the agent asks first")
	if err == nil {
		t.Skip("the directory this test made unwritable is still writable; this test needs an unprivileged user")
	}
	if errors.Is(err, ErrRefine) {
		t.Errorf("%v is the DISK refusing, but it carries the caller's sentinel", err)
	}
	if s.WriteFailing() {
		t.Error("the write latch caught a score.md rewrite; it covers the log only, " +
			"so the door-side line is what reports this one")
	}

	// And the same call succeeds once the mount comes back, so the refusal above
	// was the filesystem rather than anything the store latched on the way past.
	restore()
	if err := s.Reword(e.Id, "the agent asks first"); err != nil {
		t.Fatalf("the reword still fails after the directory came back: %v", err)
	}
}
