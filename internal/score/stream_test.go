package score

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This file is the STREAMING replay's. Round 3 capped what Open would read at
// maxScoreFileBytes, which stopped #56's OOM-kill and bought a refusal in its
// place; round 9 measured what the refusal cost — a log one byte past the cap
// fails Open forever, because the boot compaction that would have shrunk it sits
// below the replay that fails. Folding one record at a time makes replay's memory
// what the log DESCRIBES rather than what it weighs, so the compaction is
// reachable and the store shrinks itself.
//
// Nothing here may be traded for that. scanRecordsFrom must classify every line
// exactly as the whole-file split did, and scanRecordsBuffered below is that
// split, kept verbatim so the claim is a comparison rather than a description.

// scanRecordsBuffered is the pre-streaming scanRecords, VERBATIM. It exists only
// so the differential test has something to differ against; nothing in the
// package calls it.
func scanRecordsBuffered(data []byte, note func(ev *event)) (torn int) {
	var ev event
	for _, line := range bytes.Split(data, newline) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		ev = event{}
		if json.Unmarshal(line, &ev) != nil || ev.Id == "" || ev.Event == "" {
			torn++
			continue
		}
		note(&ev)
	}
	return torn
}

// variedLog is a log that exercises every record type replay branches on, in an
// order that makes the branches interact: a retire-then-restore, a merge, an
// edit after a fold, a lower after a raise, a compacted record carrying counts
// and aliases the log never accumulated, an id that is retired and never
// restored, and the post-crash artifacts scattered through the middle rather
// than only at the tail.
func variedLog() []byte {
	var b strings.Builder
	rec := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	rec(`{"schema":1,"event":"submitted","id":"aaaaaaaa","at":"2026-01-01T00:00:00Z","text":"the first thing worth keeping","source":"user","provenance":{"source":"user"}}`)
	rec(`{"schema":1,"event":"submitted","id":"bbbbbbbb","at":"2026-01-01T00:01:00Z","text":"the second thing worth keeping","source":"agent","provenance":{"source":"agent","source_profile":"claude"}}`)
	b.WriteString("\n")    // a blank line
	b.WriteString("   \n") // a whitespace-only line
	rec(`{"schema":1,"event":"folded","id":"aaaaaaaa","at":"2026-01-01T00:02:00Z","source":"user","text":"the first thing worth keeping","removed_line":true}`)
	rec(`{"schema":1,"event":"folded","id":"aaaaaaaa","at":"2026-01-01T00:03:00Z","source":"agent"}`)
	rec(`not json at all`)
	rec(`{"schema":1,"event":"raised","id":"aaaaaaaa","at":"2026-01-01T00:04:00Z","tier":2}`)
	rec(`{"schema":1,"event":"user-signal","id":"bbbbbbbb","at":"2026-01-01T00:05:00Z","source":"user"}`)
	rec(`{"schema":1,"id":"cccccccc","at":"2026-01-01T00:06:00Z"}`) // no event name
	rec(`{"schema":1,"event":"submitted","at":"2026-01-01T00:06:30Z","text":"no id"}`)
	rec(`{"schema":1,"event":"edited","id":"bbbbbbbb","at":"2026-01-01T00:07:00Z","text":"the second thing, reworded"}`)
	rec(`{"schema":1,"event":"submitted","id":"cccccccc","at":"2026-01-01T00:08:00Z","text":"a third thing that will be retired","source":"agent","provenance":{"source":"agent","source_profile":"codex"}}`)
	rec(`{"schema":1,"event":"retired","id":"cccccccc","at":"2026-01-01T00:09:00Z"}`)
	// Retire then restore: the id must not be added to the order twice.
	rec(`{"schema":1,"event":"submitted","id":"cccccccc","at":"2026-01-01T00:10:00Z","text":"the third thing, said again","source":"user","provenance":{"source":"user"}}`)
	rec(`{"schema":1,"event":"merged","id":"aaaaaaaa","at":"2026-01-01T00:11:00Z","text":"a wording absorbed from elsewhere"}`)
	rec(`{"schema":1,"event":"raised","id":"bbbbbbbb","at":"2026-01-01T00:12:00Z","tier":3}`)
	rec(`{"schema":1,"event":"lowered","id":"bbbbbbbb","at":"2026-01-01T00:13:00Z","tier":2}`)
	rec(`{"schema":1,"event":"submitted","id":"dddddddd","at":"2026-01-01T00:14:00Z","text":"a fourth thing, retired for good","source":"agent","provenance":{"source":"agent"}}`)
	rec(`{"schema":1,"event":"retired","id":"dddddddd","at":"2026-01-01T00:15:00Z"}`)
	b.WriteString(strings.Repeat("\x00", 300) + "\n") // a NUL block the filesystem never wrote back
	rec(`{"schema":1,"event":"compacted","id":"eeeeeeee","at":"2026-01-01T00:16:00Z","text":"a fifth thing, arriving whole","source":"user","provenance":{"source":"user"},"tier":3,"reinforcements":7,"user_signals":4,"aliases":["an older wording of the fifth thing","an older wording still"]}`)
	rec(`{"schema":1,"event":"folded","id":"eeeeeeee","at":"2026-01-01T00:17:00Z","source":"user","text":"an older wording of the fifth thing","removed_line":true}`)
	b.WriteString("\xff\xfe\x01rubbish\x7f\n")
	rec(`{"schema":1,"event":"lowered","id":"eeeeeeee","at":"2026-01-01T00:18:00Z","tier":2}`)
	b.WriteString(`{"schema":1,"event":"user-sig`) // crash mid-append, no trailing newline
	return []byte(b.String())
}

// TestStreamingScanMatchesTheBufferedScanRecordForRecord is the differential at
// the only place the two implementations differ. Everything downstream of
// scanRecordsFrom is the same code reached through the same closure, so an
// identical sequence of note calls and an identical torn count IS an identical
// store — and this compares the sequence element by element rather than counting
// it, so a record reordered, dropped or repeated is caught where a total would
// hide it.
func TestStreamingScanMatchesTheBufferedScanRecordForRecord(t *testing.T) {
	data := variedLog()

	collect := func(scan func([]byte, func(*event)) int) ([]event, int) {
		var got []event
		torn := scan(data, func(ev *event) { got = append(got, *ev) })
		return got, torn
	}
	want, wantTorn := collect(scanRecordsBuffered)
	got, gotTorn := collect(scanRecords)

	if wantTorn == 0 {
		t.Fatal("the corpus grew a hole: it must contain lines that fail the record rule")
	}
	if gotTorn != wantTorn {
		t.Errorf("torn = %d, want %d", gotTorn, wantTorn)
	}
	if len(got) != len(want) {
		t.Fatalf("records = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if a, b := show(got[i]), show(want[i]); a != b {
			t.Errorf("record %d:\n got %s\nwant %s", i, a, b)
		}
	}
}

// show renders one event whole, pointer field included, so a differential
// mismatch names the field rather than an address.
func show(ev event) string {
	b, err := json.Marshal(ev)
	if err != nil {
		return fmt.Sprintf("unmarshalable: %+v", ev)
	}
	return string(b)
}

// TestStreamingScanMatchesTheBufferedScanOnCrashArtifacts is the same comparison
// over the shapes a power loss actually leaves, each one alone rather than buried
// in a corpus — so a classification that only agrees on average is caught.
func TestStreamingScanMatchesTheBufferedScanOnCrashArtifacts(t *testing.T) {
	intact := `{"schema":1,"event":"submitted","id":"abc123","at":"2026-08-30T00:00:00Z","text":"survives the tear","source":"user","provenance":{"source":"user"}}`

	for _, tc := range []struct {
		name string
		log  string
	}{
		{"nothing at all", ""},
		{"one intact record with no trailing newline", intact},
		{"one intact record with a trailing newline", intact + "\n"},
		{"blank lines only", "\n\n\n\n"},
		{"a NUL-filled block the filesystem never wrote back", intact + "\n" + strings.Repeat("\x00", 512)},
		{"a whole file of NULs", strings.Repeat("\x00", 4096)},
		{"a record cut off and then NUL-padded", intact + "\n" + `{"schema":1,"event":"sub` + strings.Repeat("\x00", 64)},
		{"arbitrary binary garbage", intact + "\n\xff\xfe\x00\x01\x02rubbish\x7f"},
		{"a bare newline and nothing after it", intact + "\n\n"},
		{"a record cut in half", intact + "\n" + intact[:len(intact)/2]},
		{"a crash mid-append", intact + "\n" + `{"schema":1,"event":"user-sig`},
		{"a record with no id", intact + "\n" + `{"schema":1,"event":"folded","at":"2026-08-30T00:00:00Z"}`},
		{"a record with no event name", intact + "\n" + `{"schema":1,"id":"abc123","at":"2026-08-30T00:00:00Z"}`},
		{"leading blank lines before the first record", "\n\n" + intact + "\n"},
		{"a windows line ending", intact + "\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.log)
			var want, got []event
			wantTorn := scanRecordsBuffered(data, func(ev *event) { want = append(want, *ev) })
			gotTorn := scanRecords(data, func(ev *event) { got = append(got, *ev) })
			if gotTorn != wantTorn {
				t.Errorf("torn = %d, want %d", gotTorn, wantTorn)
			}
			if len(got) != len(want) {
				t.Fatalf("records = %d, want %d", len(got), len(want))
			}
			for i := range want {
				if a, b := show(got[i]), show(want[i]); a != b {
					t.Errorf("record %d:\n got %s\nwant %s", i, a, b)
				}
			}
		})
	}
}

// fingerprint is everything a caller can see of a replayed store, in one string:
// the entries in ORDER with every field replay sets, plus the health counters and
// the burned set. It is what the differential compares, and what pins the store
// against a future change to the scan.
func fingerprint(s *Store) string {
	var b strings.Builder
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		fmt.Fprintf(&b, "entry id=%s text=%q tier=%d prov=%+v reinf=%d usersig=%d aliases=%q lastAt=%d\n",
			e.Id, e.Text, e.Tier, e.Provenance, e.Reinforcements, e.UserSignals, e.Aliases, s.lastAt[e.Id])
	}
	burned := make([]string, 0, len(s.burned))
	for id := range s.burned {
		burned = append(burned, id)
	}
	slicesSort(burned)
	fmt.Fprintf(&b, "burned=%q\n", burned)
	owed := make([]string, 0, len(s.owed))
	for id, texts := range s.owed {
		owed = append(owed, fmt.Sprintf("%s=%q", id, texts))
	}
	slicesSort(owed)
	fmt.Fprintf(&b, "owed=%q\nseq=%d\nhealth=%+v\n", owed, s.seq, s.health)
	return b.String()
}

func slicesSort(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// TestStreamingReplayLeavesTheStoreWhereTheBufferedOneDid pins the whole store,
// field by field, against the state the buffered replay produced. The expected
// string was captured by running this same fingerprint on the pre-streaming
// implementation over this same log.
//
// The scan is the ONLY thing that changed, so this is the differential read from
// the far end: the record-for-record comparison above proves the two scans hand
// replayLocked the same events, and this proves those events still land in the
// same store.
func TestStreamingReplayLeavesTheStoreWhereTheBufferedOneDid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, scoreEvents), variedLog(), 0o600); err != nil {
		t.Fatal(err)
	}
	// No score.md, so the boot's reconcile pass projects the replayed entries
	// rather than folding a file's opinions in over them: what is measured here is
	// the replay.
	s := newStore(dir, Policy{})
	if err := s.replayLocked(); err != nil {
		t.Fatalf("replay: %v", err)
	}

	const want = `entry id=aaaaaaaa text="the first thing worth keeping" tier=2 prov={SourcePanel: SourceProfile: SourceCwd: SourceGroup: Source:user} reinf=2 usersig=1 aliases=["a wording absorbed from elsewhere"] lastAt=4
entry id=bbbbbbbb text="the second thing, reworded" tier=2 prov={SourcePanel: SourceProfile:claude SourceCwd: SourceGroup: Source:agent} reinf=1 usersig=1 aliases=["the second thing worth keeping"] lastAt=6
entry id=cccccccc text="the third thing, said again" tier=1 prov={SourcePanel: SourceProfile: SourceCwd: SourceGroup: Source:user} reinf=0 usersig=0 aliases=[] lastAt=10
entry id=eeeeeeee text="a fifth thing, arriving whole" tier=2 prov={SourcePanel: SourceProfile: SourceCwd: SourceGroup: Source:user} reinf=8 usersig=5 aliases=["an older wording of the fifth thing" "an older wording still"] lastAt=17
burned=["aaaaaaaa" "bbbbbbbb" "cccccccc" "dddddddd" "eeeeeeee"]
owed=["aaaaaaaa=[\"the first thing worth keeping\"]" "eeeeeeee=[\"an older wording of the fifth thing\"]"]
seq=18
health={Oversized:0 TornEvents:6 CompactionFailures:0 CompactionError: LogBefore:0 LogAfter:0 Compactions:0 WriteFailing:false SwallowedRepeats:0 UnreportedFolds:0 AliasEvictions:0 Compacted:0 BareAdmits:0 RejectedTiers:0}
`
	if got := fingerprint(s); got != want {
		t.Errorf("the streaming replay moved the store.\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestAnOverLongRecordIsTornAndTheScanCarriesOn is the failure streaming
// introduces and the buffered form did not have: a line longer than the scan will
// assemble.
//
// It is TORN, and the argument is in maxRecordBytes. What this pins is the half
// that decides whether the classification is safe — the records AROUND it survive
// and are replayed. A scanner that stopped at an over-long line (which is what
// bufio.Scanner does) would silently drop every record after it, which is the
// state loss this package must never have.
func TestAnOverLongRecordIsTornAndTheScanCarriesOn(t *testing.T) {
	before := `{"schema":1,"event":"submitted","id":"aaaaaaaa","at":"2026-01-01T00:00:00Z","text":"said before the damage","source":"user","provenance":{"source":"user"}}`
	after := `{"schema":1,"event":"submitted","id":"bbbbbbbb","at":"2026-01-01T00:02:00Z","text":"said after the damage","source":"user","provenance":{"source":"user"}}`

	for _, tc := range []struct {
		name string
		huge string
	}{
		{"a run of NULs with no newline in it", strings.Repeat("\x00", maxRecordBytes+1)},
		{"a syntactically valid record past the cap", `{"schema":1,"event":"submitted","id":"cccccccc","text":"` + strings.Repeat("x", maxRecordBytes) + `"}`},
		{"garbage exactly one byte over", strings.Repeat("x", maxRecordBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := before + "\n" + tc.huge + "\n" + after + "\n"
			var got []string
			torn, err := scanRecordsFrom(strings.NewReader(log), func(ev *event) { got = append(got, ev.Id) })
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if torn != 1 {
				t.Errorf("torn = %d, want the over-long line counted exactly once", torn)
			}
			if want := []string{"aaaaaaaa", "bbbbbbbb"}; !equalStrings(got, want) {
				t.Errorf("records = %v, want %v — the scan must carry on past the damage", got, want)
			}
		})
	}
}

// And the same at the very end of the file, where a crash actually leaves damage:
// an over-long final line with no newline after it must still be counted once and
// must not swallow the records before it.
func TestAnOverLongFinalRecordIsTornExactlyOnce(t *testing.T) {
	intact := `{"schema":1,"event":"submitted","id":"aaaaaaaa","at":"2026-01-01T00:00:00Z","text":"survives the damage","source":"user","provenance":{"source":"user"}}`
	log := intact + "\n" + strings.Repeat("\x00", maxRecordBytes+9)

	var got []string
	torn, err := scanRecordsFrom(strings.NewReader(log), func(ev *event) { got = append(got, ev.Id) })
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if torn != 1 {
		t.Errorf("torn = %d, want 1", torn)
	}
	if !equalStrings(got, []string{"aaaaaaaa"}) {
		t.Errorf("records = %v, want the intact record before the damage", got)
	}
}

// A record that straddles the read window is the ordinary case of the assembly
// path — nothing this package writes reaches scanRecordBufBytes, but a hand-edited
// log can, and it is a RECORD rather than damage. It must decode.
func TestARecordLongerThanTheReadWindowStillDecodes(t *testing.T) {
	long := strings.Repeat("a wording far longer than any this package writes, ", 4000)
	if len(long) <= scanRecordBufBytes {
		t.Fatalf("the fixture is %d bytes, which does not straddle the %d-byte window", len(long), scanRecordBufBytes)
	}
	rec, err := json.Marshal(event{Schema: 1, Event: EventSubmitted, Id: "aaaaaaaa", Text: long})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec) >= maxRecordBytes {
		t.Fatalf("the fixture is %d bytes, which is past maxRecordBytes and would be torn", len(rec))
	}

	var got []event
	torn, err := scanRecordsFrom(bytes.NewReader(append(rec, '\n')), func(ev *event) { got = append(got, *ev) })
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if torn != 0 || len(got) != 1 || got[0].Text != long {
		t.Fatalf("torn=%d records=%d, want the straddling record decoded whole", torn, len(got))
	}
}

// TestTheScanReportsAReadFailureRatherThanTruncatingSilently: a reader that dies
// mid-log must not look like a log that ended. Replay returns the error and Open
// discards the store, which is what it did when the read was a single ReadAll.
func TestTheScanReportsAReadFailureRatherThanTruncatingSilently(t *testing.T) {
	intact := `{"schema":1,"event":"submitted","id":"aaaaaaaa","at":"2026-01-01T00:00:00Z","text":"before the failure","source":"user"}` + "\n"
	r := &failAfter{data: []byte(intact + intact), at: len(intact) + 10}

	var got []string
	_, err := scanRecordsFrom(r, func(ev *event) { got = append(got, ev.Id) })
	if err == nil {
		t.Fatal("a read that failed mid-log must not be reported as a log that ended")
	}
	if !strings.Contains(err.Error(), "disk went away") {
		t.Errorf("err = %v, want the reader's own words", err)
	}
}

// failAfter hands out at bytes and then fails, which is what a disk that goes
// away mid-read looks like to a scan.
type failAfter struct {
	data []byte
	at   int
	n    int
}

func (f *failAfter) Read(p []byte) (int, error) {
	if f.n >= f.at {
		return 0, fmt.Errorf("disk went away")
	}
	n := copy(p, f.data[f.n:min(f.at, len(f.data))])
	f.n += n
	return n, nil
}

// TestAnOversizedEventLogHealsItself is the round's whole point, and the test
// round 3's TestOversizedEventLogIsRefusedNotRead used to be.
//
// A log far past the old read cap opens, the boot compaction below the replay
// runs, and the file is small again — with every entry's provenance, tier and
// counts carried through it, because compaction is the mechanism that already
// does that. Then the second boot is cheap, which is the half that says the heal
// STUCK: round 9's measurement was two boots over one unchanged file.
func TestAnOversizedEventLogHealsItself(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a log past the old 64 MiB read cap")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, scoreEvents)
	writeBigLog(t, logPath, maxScoreFileBytes+(1<<20))

	grew := sizeOnDisk(t, logPath)
	if grew <= maxScoreFileBytes {
		t.Fatalf("the fixture is %d bytes, which the old cap would not have refused", grew)
	}

	s, err := Open(dir, Policy{})
	if err != nil {
		t.Fatalf("a %d-byte event log must open, not be refused: %v", grew, err)
	}
	if s.Len() != bigLogEntries {
		t.Errorf("entries = %d, want %d — the whole log replayed", s.Len(), bigLogEntries)
	}
	h := s.Health()
	if h.Compactions != 1 {
		t.Errorf("compactions = %d, want the boot to have rewritten the log", h.Compactions)
	}
	if h.TornEvents != 0 {
		t.Errorf("torn = %d, want a clean log read cleanly", h.TornEvents)
	}
	// Provenance, tiers and counts survive, which is the reason compaction is the
	// repair and renaming the log aside is not.
	s.mu.Lock()
	for _, e := range s.entries {
		if e.Provenance.Source != SourceAgent || e.Provenance.SourceProfile != "claude" {
			t.Errorf("entry %s lost its provenance: %+v", e.Id, e.Provenance)
		}
		if e.Reinforcements == 0 {
			t.Errorf("entry %s lost its reinforcement count", e.Id)
		}
	}
	s.mu.Unlock()
	s.Close()

	shrunk := sizeOnDisk(t, logPath)
	if shrunk >= grew {
		t.Fatalf("the log is %d bytes after the boot and was %d before: it did not heal", shrunk, grew)
	}
	if shrunk > compactAtBytes {
		t.Errorf("the log is still %d bytes, past the size the store rewrites at", shrunk)
	}

	// The second boot is the one round 9 measured as identical to the first.
	s2, err := Open(dir, Policy{})
	if err != nil {
		t.Fatalf("second boot: %v", err)
	}
	defer s2.Close()
	if s2.Len() != bigLogEntries {
		t.Errorf("entries after the heal = %d, want %d", s2.Len(), bigLogEntries)
	}
	if h := s2.Health(); h.Compactions != 0 {
		t.Errorf("compactions on the second boot = %d, want none — the log is already small", h.Compactions)
	}
}

// TestReplayHoldsTheLogARecordAtATime is the memory claim, measured rather than
// described: a log many times larger than anything the scan may hold must not
// take the heap with it. Before streaming this same log cost live heap
// proportional to the FILE — 134 MiB on 60 MiB — which is what made #56's boot
// unhealable.
//
// The bound is deliberately loose. What it has to catch is a replay that went
// back to holding the file, and that is an order of magnitude away from anything
// timing noise or a GC that has not run yet can produce.
func TestReplayHoldsTheLogARecordAtATime(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a multi-megabyte log")
	}
	const size = 48 << 20
	dir := t.TempDir()
	writeBigLog(t, dir+string(filepath.Separator)+scoreEvents, size)

	s := newStore(dir, Policy{})
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := s.replayLocked(); err != nil {
		t.Fatalf("replay: %v", err)
	}
	runtime.ReadMemStats(&after)
	held := after.HeapAlloc - min(before.HeapAlloc, after.HeapAlloc)

	if held > size/4 {
		t.Errorf("replay is holding %d bytes of a %d-byte log; it must hold records, not the file", held, size)
	}
	if s.Len() != bigLogEntries {
		t.Fatalf("entries = %d, want %d", s.Len(), bigLogEntries)
	}
}

// bigLogEntries is how many distinct entries writeBigLog's log describes — a
// handful, which is what #37 calls the working set. The log is large because it
// is HISTORY, which is exactly the shape the old whole-file read could not tell
// apart from a large store.
const bigLogEntries = 8

// writeBigLog writes an event log of at least size bytes describing
// bigLogEntries entries: one submission each, then repeats folded onto them.
func writeBigLog(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	var written int64
	for i := range bigLogEntries {
		n, err := fmt.Fprintf(w, `{"schema":1,"event":"submitted","id":"id%06d","at":"2026-01-01T00:00:00Z","text":"the fleet keeps rediscovering thing number %d","source":"agent","provenance":{"source":"agent","source_profile":"claude","source_cwd":"/home/someone/work"}}`+"\n", i, i)
		if err != nil {
			t.Fatal(err)
		}
		written += int64(n)
	}
	for n := 0; written < size; n++ {
		c, err := fmt.Fprintf(w, `{"schema":1,"event":"folded","id":"id%06d","at":"2026-01-01T00:00:01Z","source":"agent"}`+"\n", n%bigLogEntries)
		if err != nil {
			t.Fatal(err)
		}
		written += int64(c)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func sizeOnDisk(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
