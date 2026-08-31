package score

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"unicode"
)

// idRe is the shape of a store-issued id: short lowercase hex.
var idRe = regexp.MustCompile(`^[0-9a-f]{6}$`)

// readFile is a test helper that fails instead of returning an error.
func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func TestOpenSeedsHeaderOnFirstRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "score")

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// The seed teaches the format without ever becoming memory: score.md has
	// content, the store has no entries at all.
	if s.Len() != 0 {
		t.Fatalf("seeded store holds %d entries, want 0 — the header is not memory", s.Len())
	}
	md := readFile(t, dir, scoreMD)
	if !strings.Contains(md, "- [e7f3a2]") {
		t.Errorf("the header should show the entry format:\n%s", md)
	}
	for _, line := range strings.Split(md, "\n") {
		if _, _, ok := parseLine(line); ok {
			t.Errorf("seeded line %q parses as an entry, want documentation only", line)
		}
	}

	// The seeded file is 0600 in a 0700 directory; the other two are written by
	// the first mutation, and inherit the same mode.
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("dir perm = %o, want group/other bits clear", perm)
	}
	submit(t, s, "the first real note")
	for _, name := range []string{scoreMD, scoreJSON, scoreEvents} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s perm = %o, want 0600", name, perm)
		}
	}
	if e := s.Render(Context{})[0]; !idRe.MatchString(e.Id) || e.Tier != 1 {
		t.Errorf("submitted entry = %+v, want a short hex id at tier 1", e)
	}
}

func TestOpenExistingNeverReseeds(t *testing.T) {
	tests := []struct {
		name string
		md   string
		want int // entries after Open
	}{
		{"empty score.md", "", 0},
		{"one real entry", "- [abc123] keep the build green\n", 1},
		{"operator prose skipped", "# notes\n\n- [abc123] keep the build green\nremember the milk\n", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, scoreMD), []byte(tt.md), 0o600); err != nil {
				t.Fatalf("write score.md: %v", err)
			}

			s, err := Open(dir)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			entries := s.Render(Context{})
			if len(entries) != tt.want {
				t.Fatalf("entries = %d, want %d", len(entries), tt.want)
			}
			if got := readFile(t, dir, scoreMD); got != tt.md {
				t.Errorf("Open rewrote score.md:\n got: %q\nwant: %q", got, tt.md)
			}
		})
	}
}

func TestSubmitAppendsAllThreeFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	prov := Provenance{SourcePanel: "p1", SourceProfile: "dev", SourceCwd: "/work", Source: "agent"}
	e, _, err := s.Submit("  ship it\nby friday  ", prov)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if e.Text != "ship it by friday" {
		t.Errorf("text = %q, want newline flattened and trimmed", e.Text)
	}
	if e.Tier != 1 || e.Provenance != prov {
		t.Errorf("entry = %+v, want tier 1 with the provenance kept", e)
	}
	if !idRe.MatchString(e.Id) {
		t.Errorf("id %q is not short hex", e.Id)
	}

	// score.md gained the line.
	if md := readFile(t, dir, scoreMD); !strings.Contains(md, "- ["+e.Id+"] ship it by friday\n") {
		t.Errorf("score.md missing submitted line:\n%s", md)
	}

	// The log gained a submitted event for the id.
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(readFile(t, dir, scoreEvents)), "\n") {
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		if ev.Schema != Schema {
			t.Errorf("event schema = %d, want %d", ev.Schema, Schema)
		}
		if ev.At.IsZero() {
			t.Errorf("event %q has no timestamp", line)
		}
		if ev.Event == EventSubmitted && ev.Id == e.Id {
			found = true
		}
	}
	if !found {
		t.Error("no submitted event logged for the new entry")
	}

	// The snapshot holds the entry, and no temp file lingers.
	var snap snapshot
	if err := json.Unmarshal([]byte(readFile(t, dir, scoreJSON)), &snap); err != nil {
		t.Fatalf("snapshot is not valid JSON: %v", err)
	}
	if snap.Schema != Schema {
		t.Errorf("snapshot schema = %d, want %d", snap.Schema, Schema)
	}
	// The folding key is derived and deliberately not persisted, so the decoded
	// entry has none; everything score.json is FOR must match exactly.
	want := e
	want.norm = ""
	if got := snap.Entries[e.Id]; !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot entry = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, scoreJSON+".tmp")); !os.IsNotExist(err) {
		t.Errorf("snapshot temp file left behind (stat err = %v)", err)
	}
}

// TestSubmitNeutralisesControlSequences follows a terminal payload the whole way
// — submit, the bytes on disk, and the injectable block — and checks no control
// byte survives any leg. RenderBlock's output is what a dispatch writes to a
// panel's pty, so a control byte there is a control byte in every agent's
// terminal.
func TestSubmitNeutralisesControlSequences(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// OSC 52 clipboard write, a CSI colour + cursor move, a bidi override, a
	// zero-width space, and a bare CR that would rewrite the line in place.
	const payload = "\x1b]52;c;cGF5bG9hZA==\x07keep \x1b[1;31mthis\x1b[2J\r\u202etext\u200b visible"

	e, _, err := s.Submit(payload, Provenance{Source: "agent", SourcePanel: "p1"})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if e.Text != "]52;c;cGF5bG9hZA==keep [1;31mthis[2J text visible" {
		t.Fatalf("stored text = %q, want the escapes stripped to inert prose", e.Text)
	}

	block := s.RenderBlock(Context{Panel: "p2"})
	if !strings.Contains(block, e.Text) {
		t.Fatalf("block does not carry the entry:\n%s", block)
	}
	for _, tt := range []struct {
		name string
		text string
	}{
		{"entry", e.Text},
		{scoreMD, readFile(t, dir, scoreMD)},
		{scoreJSON, readFile(t, dir, scoreJSON)},
		{"block", block},
	} {
		for _, r := range tt.text {
			// Newlines are the store's own line discipline, not the agent's.
			if r == '\n' {
				continue
			}
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar {
				t.Errorf("%s still carries %U:\n%s", tt.name, r, tt.text)
				break
			}
		}
	}
}

// TestSubmitRefusesOverLong checks the length policy: refuse, do not truncate,
// and measure the sanitised text so a padding of escapes cannot inflate the
// count.
func TestSubmitRefusesOverLong(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	before := readFile(t, dir, scoreMD)

	if _, _, err := s.Submit(strings.Repeat("é", maxEntryRunes), Provenance{Source: "user"}); err != nil {
		t.Fatalf("a submission exactly at the limit must be accepted: %v", err)
	}
	// Padding that the sanitiser removes must not push a legal note over the
	// limit: the cap is measured after scrubbing, not before.
	padded := strings.Repeat("a", maxEntryRunes) + strings.Repeat("\x1b", 50)
	if _, _, err := s.Submit(padded, Provenance{Source: "agent"}); err != nil {
		t.Fatalf("escape padding must not inflate the rune count: %v", err)
	}
	if _, _, err := s.Submit(strings.Repeat("z", maxEntryRunes+1), Provenance{Source: "agent"}); err == nil {
		t.Fatal("an over-long submission succeeded, want a refusal")
	}

	// The refusal is total: nothing truncated into any of the three files.
	if md := readFile(t, dir, scoreMD); strings.Contains(md, "zz") {
		t.Errorf("score.md gained a truncated entry:\n%s", md)
	}
	if n := s.Len(); n != 2 { // the two accepted submissions, not the refused one
		t.Errorf("entries = %d, want 2 — the refused submission must not be stored", n)
	}
	if !strings.HasPrefix(readFile(t, dir, scoreMD), before) {
		t.Error("the refusal disturbed the existing score.md")
	}
}

// TestLoadSanitisesOperatorEdits closes the store's other door. score.md is a
// designed input channel, so text arriving through it — an operator's edit, or
// anything that reached the file another way — must be as inert as a submission.
// R1 gives Reconcile a real caller that re-reads the file at runtime, and Render
// feeds a panel's pty, so a raw escape here would be a raw escape there.
func TestLoadSanitisesOperatorEdits(t *testing.T) {
	dir := t.TempDir()
	md := "- [abc123] wipe \x1b[2Jthe screen \x1b]52;c;cGF5bG9hZA==\x07and the clipboard\n"
	if err := os.WriteFile(filepath.Join(dir, scoreMD), []byte(md), 0o600); err != nil {
		t.Fatalf("write score.md: %v", err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	block := s.RenderBlock(Context{Panel: "p1"})
	for _, r := range block {
		if r == '\n' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == unicode.ReplacementChar {
			t.Fatalf("a planted score.md line reached the block with %U:\n%s", r, block)
		}
	}
	if !strings.Contains(block, "wipe [2Jthe screen ]52;c;cGF5bG9hZA==and the clipboard") {
		t.Errorf("the line should survive as inert prose:\n%s", block)
	}

	// Reconcile is the runtime re-read, and must scrub on that path too.
	reconcile(t, s)
	if got := s.Render(Context{})[0].Text; strings.ContainsRune(got, 0x1b) {
		t.Errorf("Reconcile reloaded a raw escape: %q", got)
	}
}

func TestSubmitRejectsEmpty(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Blank, then a payload of nothing but scrubbed classes: ESC, BEL, U+200B
	// ZERO WIDTH SPACE, U+202E RIGHT-TO-LEFT OVERRIDE, U+FFFD REPLACEMENT.
	for _, text := range []string{"  \n ", "\x1b\x07\u200b\u202e\ufffd"} {
		if _, _, err := s.Submit(text, Provenance{Source: "user"}); err == nil {
			t.Errorf("Submit(%q) succeeded, want error — it sanitises to nothing", text)
		}
	}
}

// TestSeedNeverReachesABrief is the whole point of seeding comment lines: a
// fresh install must inject nothing into a real agent's brief, and must keep
// injecting nothing when the snapshot — a disposable cache by this package's own
// doctrine — is deleted underneath it. An earlier shape seeded flagged entries,
// and that flag lived only in score.json, so this is the exact path that
// resurrected them.
func TestSeedNeverReachesABrief(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if md := readFile(t, dir, scoreMD); md == "" {
		t.Fatal("score.md should carry the seeded header")
	}
	if got := s.Render(Context{}); got != nil {
		t.Errorf("Render = %+v, want nil — the seed is documentation, not memory", got)
	}
	if got := s.RenderBlock(Context{Panel: "p1"}); got != "" {
		t.Errorf("RenderBlock = %q, want empty", got)
	}

	// Reopen with no snapshot at all: still nothing to inject.
	if err := os.Remove(filepath.Join(dir, scoreJSON)); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove snapshot: %v", err)
	}
	s.Close() // one writer per directory; hand the claim over
	re, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen without a snapshot: %v", err)
	}
	if re.Len() != 0 {
		t.Errorf("Len after dropping the snapshot = %d, want 0", re.Len())
	}
	if got := re.RenderBlock(Context{Panel: "p1"}); got != "" {
		t.Fatalf("a lost snapshot resurrected the seed into a brief: %q", got)
	}

	submit(t, re, "real memory")
	got := re.RenderBlock(Context{Panel: "p1"})
	if want := "── Score ──\n- real memory [noted]\n───────────\n"; got != want {
		t.Fatalf("RenderBlock:\n got: %q\nwant: %q", got, want)
	}
}

func TestSubmitSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	e := submit(t, s, "persisted")

	s.Close()
	re, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	entries := re.Render(Context{})
	if len(entries) != 1 { // the seeded header lines are not entries
		t.Fatalf("entries after reopen = %d, want 1", len(entries))
	}
	if got := entries[0]; !reflect.DeepEqual(got, e) {
		t.Errorf("reopened entry = %+v, want %+v", got, e)
	}
	if re.Len() != 1 {
		t.Errorf("Len after reopen = %d, want 1", re.Len())
	}
}

func TestSubmitConcurrent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, _, err := s.Submit(fmt.Sprintf("note %d", i), Provenance{Source: "agent"}); err != nil {
				t.Errorf("Submit %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	got := len(s.Render(Context{Group: "g"}))
	if got != renderLimit {
		t.Fatalf("Render returned %d entries, want the render limit %d", got, renderLimit)
	}
	reconcile(t, s)
}

func TestRender(t *testing.T) {
	tests := []struct {
		name    string
		submits int // the seeded header contributes no entries
		want    int
	}{
		{"seed only", 0, 0},
		{"under the limit", 3, 3},
		{"capped at the limit", 10, renderLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			for i := 0; i < tt.submits; i++ {
				if _, _, err := s.Submit(fmt.Sprintf("note %d", i), Provenance{Source: "user"}); err != nil {
					t.Fatalf("Submit: %v", err)
				}
			}
			if got := len(s.Render(Context{})); got != tt.want {
				t.Fatalf("Render = %d entries, want %d", got, tt.want)
			}
		})
	}
}

func TestRenderEmptyAndDisabled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, scoreMD), nil, 0o600); err != nil {
		t.Fatalf("write score.md: %v", err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := s.Render(Context{}); got != nil {
		t.Errorf("empty store Render = %v, want nil", got)
	}
	if got := s.RenderBlock(Context{}); got != "" {
		t.Errorf("empty store RenderBlock = %q, want empty", got)
	}

	var disabled *Store
	if got := disabled.Render(Context{}); got != nil {
		t.Errorf("disabled Render = %v, want nil", got)
	}
	if got := disabled.RenderBlock(Context{}); got != "" {
		t.Errorf("disabled RenderBlock = %q, want empty", got)
	}
	if _, err := disabled.Reconcile(); err != nil {
		t.Errorf("disabled Reconcile = %v, want nil", err)
	}
	if _, _, err := disabled.Submit("x", Provenance{}); err == nil {
		t.Error("disabled Submit succeeded, want refusal")
	}
	if err := disabled.Reinforce("abc123", "user"); err == nil {
		t.Error("disabled Reinforce succeeded, want refusal")
	}
}

// TestRenderBlockWording checks the tier wording of the injected block. The
// entries are planted straight into the store rather than through the files:
// no tier above 1 can be reached through the API until R3 promotes one, and
// score.json is a cache R1 never reads back, so the files cannot express this
// state at all. The seam under test is the rendering, not how a tier is earned.
func TestRenderBlockWording(t *testing.T) {
	s := &Store{entries: []Entry{
		{Id: "aaaaaa", Text: "first", Tier: 1},
		{Id: "bbbbbb", Text: "second", Tier: 2},
		{Id: "cccccc", Text: "third", Tier: 3},
		{Id: "dddddd", Text: "off the scale", Tier: 9},
	}}

	got := s.RenderBlock(Context{Panel: "p1"})
	want := "── Score ──\n" +
		"- first [noted]\n" +
		"- second [note and take care]\n" +
		"- third [important]\n" +
		"- off the scale [noted]\n" +
		"───────────\n"
	if got != want {
		t.Fatalf("RenderBlock:\n got: %q\nwant: %q", got, want)
	}
}

func TestReinforce(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	e := submit(t, s, "reinforce me")

	if err := s.Reinforce(e.Id, "agent"); err != nil {
		t.Fatalf("Reinforce: %v", err)
	}
	if err := s.Reinforce(e.Id, "user"); err != nil {
		t.Fatalf("Reinforce: %v", err)
	}
	if err := s.Reinforce("ffffff", "user"); err == nil {
		t.Error("Reinforce of unknown id succeeded, want error")
	}

	// The counter is replayed from the log, tier untouched (no promotion yet).
	s.Close()
	re, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	entries := re.Render(Context{})
	got := entries[len(entries)-1]
	if got.Reinforcements != 2 || got.Tier != 1 {
		t.Fatalf("after reinforce: %+v, want 2 reinforcements at tier 1", got)
	}

	// And the log recorded who reinforced, in the #38 vocabulary: an agent
	// reinforcement is a fold, an operator one a user-signal.
	events := readFile(t, dir, scoreEvents)
	if !strings.Contains(events, `"event":"folded"`) || !strings.Contains(events, `"source":"agent"`) {
		t.Errorf("log missing the folded event from the agent:\n%s", events)
	}
	if !strings.Contains(events, `"event":"user-signal"`) || !strings.Contains(events, `"source":"user"`) {
		t.Errorf("log missing the user-signal event from the operator:\n%s", events)
	}
	if strings.Contains(events, `"event":"reinforced"`) {
		t.Errorf("log used an event name outside the #38 vocabulary:\n%s", events)
	}
}

func TestRefineIsStubbed(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = s.Refine("retire", "abc123", "")
	if err == nil || !strings.Contains(err.Error(), "R6") {
		t.Fatalf("Refine err = %v, want the R6 stub error", err)
	}
}

func TestReconcilePicksUpOperatorEdits(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Operator appends a line by hand.
	md := readFile(t, dir, scoreMD) + "- [abc123] handwritten wisdom\n"
	if err := os.WriteFile(filepath.Join(dir, scoreMD), []byte(md), 0o600); err != nil {
		t.Fatalf("write score.md: %v", err)
	}

	reconcile(t, s)
	entries := s.Render(Context{})
	if len(entries) != 1 { // the handwritten line; the header is not an entry
		t.Fatalf("entries after reconcile = %d, want 1", len(entries))
	}
	if got := entries[0]; got.Id != "abc123" || got.Text != "handwritten wisdom" || got.Tier != 1 {
		t.Fatalf("reconciled entry = %+v", got)
	}
	// S0 reconcile must not rewrite anything.
	if got := readFile(t, dir, scoreMD); got != md {
		t.Errorf("Reconcile rewrote score.md:\n got: %q\nwant: %q", got, md)
	}

	// The whole FILE going missing is not an edit — see projectLocked. The store
	// writes it back rather than reading it as "delete everything"; emptying the
	// file, which TestReconcileDeletedLine covers, is the statement that retires.
	if err := os.Remove(filepath.Join(dir, scoreMD)); err != nil {
		t.Fatalf("remove score.md: %v", err)
	}
	reconcile(t, s)
	if got := s.Render(Context{}); len(got) != 1 || got[0].Id != "abc123" {
		t.Errorf("entries after deleting score.md = %v, want the entry re-projected", got)
	}
	if got := readFile(t, dir, scoreMD); !strings.Contains(got, "- [abc123] handwritten wisdom") {
		t.Errorf("score.md was not re-projected:\n%s", got)
	}
}

func TestOpenToleratesCorruptSnapshot(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{"garbage", "{not json at all"},
		{"newer schema", `{"schema":99,"entries":{"abc123":{"id":"abc123","tier":3}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, scoreMD), []byte("- [abc123] survives\n"), 0o600); err != nil {
				t.Fatalf("write score.md: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, scoreJSON), []byte(tt.json), 0o600); err != nil {
				t.Fatalf("write score.json: %v", err)
			}

			s, err := Open(dir)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			entries := s.Render(Context{})
			if len(entries) != 1 || entries[0].Text != "survives" || entries[0].Tier != 1 {
				t.Fatalf("entries = %+v, want the score.md entry at default tier 1", entries)
			}

			// The next mutation heals the snapshot.
			if _, _, err := s.Submit("heal", Provenance{Source: "user"}); err != nil {
				t.Fatalf("Submit: %v", err)
			}
			var snap snapshot
			if err := json.Unmarshal([]byte(readFile(t, dir, scoreJSON)), &snap); err != nil {
				t.Fatalf("snapshot still corrupt after Submit: %v", err)
			}
		})
	}
}

func TestTornEventLogTailTolerated(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, scoreMD), []byte("- [abc123] present\n"), 0o600); err != nil {
		t.Fatalf("write score.md: %v", err)
	}
	torn := `{"schema":1,"event":"submitted","id":"abc123","at":"2026-08-30T00:00:00Z","text":"present","provenance":{"source":"user"}}` + "\n" +
		`{"schema":1,"event":"subm` // crash mid-append, no newline
	if err := os.WriteFile(filepath.Join(dir, scoreEvents), []byte(torn), 0o600); err != nil {
		t.Fatalf("write events: %v", err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open with torn log tail: %v", err)
	}
	submit(t, s, "after the tear")

	// The new event landed on its own line; only the torn line stays broken.
	lines := strings.Split(strings.TrimSuffix(readFile(t, dir, scoreEvents), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("event log has %d lines, want 3:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	var bad int
	for _, line := range lines {
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			bad++
		}
	}
	if bad != 1 {
		t.Fatalf("%d unparsable event lines, want exactly the torn one", bad)
	}
}

func TestParseLine(t *testing.T) {
	tests := []struct {
		line     string
		id, text string
		ok       bool
	}{
		{"- [e7f3a2] keep it simple", "e7f3a2", "keep it simple", true},
		{"  - [e7f3a2] indented is fine", "e7f3a2", "indented is fine", true},
		{"- [e7f3a2]", "e7f3a2", "", true},
		{"", "", "", false},
		{"# heading", "", "", false},
		{"- plain bullet", "", "", false},
		{"- [] no id", "", "", false},
		{"- [has space] text", "", "", false},
		{"- [unterminated text", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			id, text, ok := parseLine(tt.line)
			if id != tt.id || text != tt.text || ok != tt.ok {
				t.Fatalf("parseLine(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.line, id, text, ok, tt.id, tt.text, tt.ok)
			}
		})
	}
}

func TestWriteFileAtomicReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	if err := writeFileAtomic(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeFileAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("second write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("read back = %q, %v; want \"new\"", data, err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind (stat err = %v)", err)
	}
}
