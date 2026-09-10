package task

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestTerminal checks which statuses are end states.
func TestTerminal(t *testing.T) {
	for st, want := range map[Status]bool{
		Queued:     false,
		Dispatched: false,
		Running:    false,
		Done:       true,
		Failed:     true,
	} {
		if got := st.Terminal(); got != want {
			t.Errorf("%q.Terminal() = %v, want %v", st, got, want)
		}
	}
}

// TestCanAdvance covers the transition table: the lifecycle moves forward only,
// any non-terminal status can fail, and a terminal status is sticky.
func TestCanAdvance(t *testing.T) {
	cases := []struct {
		from, to Status
		want     bool
	}{
		{Queued, Dispatched, true},
		{Queued, Running, true},
		{Queued, Failed, true},
		{Dispatched, Running, true},
		{Dispatched, Done, false}, // must run before done
		{Running, Done, true},
		{Running, Failed, true},
		{Running, Dispatched, false}, // no going back
		{Done, Running, false},       // terminal is sticky
		{Failed, Queued, false},
		{Queued, Queued, false}, // not a forward move
	}
	for _, c := range cases {
		if got := CanAdvance(c.from, c.to); got != c.want {
			t.Errorf("CanAdvance(%q, %q) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

// TestAuthorReadsBackFromEveryShapeOnDisk drives the migration with the files
// themselves. Every one of these is a backlog entry someone could be holding
// right now — the store's own doc invites hand-editing them — so the claim that
// an upgrade reads them correctly is made against the bytes, not against a
// paragraph about the bytes.
//
// The fourth case is the only one this build could not have written: a file
// carrying both keys is settled by the author, because that is the newer fact
// and the plugin key is only ever a fallback for its absence.
func TestAuthorReadsBackFromEveryShapeOnDisk(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		want Author
	}{
		{
			"this build's own file",
			`{"id":"t1","prompt":"x","status":"queued","author":"user","attempts":1}`,
			AuthorUser,
		},
		{
			"before the field, queued by a plugin",
			`{"id":"t1","prompt":"x","status":"queued","plugin":true,"attempts":1}`,
			AuthorPlugin,
		},
		{
			"before the field, queued by anything else",
			`{"id":"t1","prompt":"x","status":"queued","attempts":1}`,
			AuthorUnknown,
		},
		{
			"both keys, which the author settles",
			`{"id":"t1","prompt":"x","status":"queued","author":"agent","plugin":true,"attempts":1}`,
			AuthorAgent,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got Task
			if err := json.Unmarshal([]byte(tc.file), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.Author != tc.want {
				t.Fatalf("author = %q, want %q", got.Author, tc.want)
			}
			// The migration decodes through a second type, which is exactly the
			// shape that can drop every other field without anyone noticing.
			if got.ID != "t1" || got.Prompt != "x" || got.Status != Queued || got.Attempts != 1 {
				t.Fatalf("the rest of the task did not survive the decode: %+v", got)
			}
		})
	}
}

// TestATaskSurvivesItsOwnRoundTrip is the same worry from the other end: the
// custom UnmarshalJSON decodes into a stand-in type, and a field left off that
// type is lost silently — the task still loads, it just forgets something. So
// this compares a fully populated task against itself rather than checking the
// handful of fields the case above happens to name.
func TestATaskSurvivesItsOwnRoundTrip(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	in := Task{
		ID: "t7", Prompt: "ship it", Status: Running, Panel: "p3", Group: "auth",
		Result: "note", Priority: 2, Attempts: 3,
		Spawn:      &SpawnSpec{Command: "claude", Args: []string{"-p"}, Dir: "/w", CloseOnDone: true},
		Author:     AuthorUser,
		UserSignal: true,
		Created:    now, Updated: now.Add(time.Minute),
	}
	blob, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Task
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, back) {
		t.Fatalf("round-trip lost something:\n got %+v\nwant %+v", back, in)
	}
}

// TestThePluginKeyIsReadAndNeverWritten holds the half of the compatibility
// story that is easy to lose: the plugin key is a migration on READ, and writing
// it back would put a second spelling of the author on disk for the two to drift
// apart later. The cost is stated where the field is — a task queued by this
// build and read by one predating Author is filtered once — and it is paid
// deliberately here rather than by a key nobody meant to keep writing.
func TestThePluginKeyIsReadAndNeverWritten(t *testing.T) {
	blob, err := json.Marshal(Task{ID: "t1", Prompt: "x", Status: Queued, Author: AuthorPlugin})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(blob, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := keys["plugin"]; ok {
		t.Fatalf("the plugin key was written back: %s", blob)
	}
	if string(keys["author"]) != `"plugin"` {
		t.Fatalf("author key = %s, want %q", keys["author"], AuthorPlugin)
	}
}

// TestAnAuthorIsNeverAWrittenZero checks the one thing omitempty has to do for
// the migration to mean anything: AuthorUnknown must not reach the file as a
// key. A file that names an author of "" and one that names none at all would
// otherwise be different bytes saying the same thing, and the plugin key's
// promotion is conditioned on the author being absent.
func TestAnAuthorIsNeverAWrittenZero(t *testing.T) {
	blob, err := json.Marshal(Task{ID: "t1", Prompt: "x", Status: Queued})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(blob, &keys); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := keys["author"]; ok {
		t.Fatalf("an unknown author reached the file: %s", blob)
	}
}
