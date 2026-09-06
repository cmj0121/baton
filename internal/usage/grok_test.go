package usage

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// grokLine is one updates.jsonl line in the shape grok actually writes, reduced
// to the fields the reader reads. The nesting is kept verbatim — params.update
// .usage, and _meta.eventId beside it — because the nesting is the thing the
// decoder has to get right.
func grokLine(ts int64, event, prompt string, in, out, cachedRead, cacheCreate, ticks int64) string {
	return `{"timestamp":` + itoa(ts) + `,"method":"_x.ai/session/update","params":{"update":` +
		`{"sessionUpdate":"turn_completed","prompt_id":"` + prompt + `","usage":{"inputTokens":` + itoa(in) +
		`,"outputTokens":` + itoa(out) + `,"cachedReadTokens":` + itoa(cachedRead) +
		`,"cacheCreationTokens":` + itoa(cacheCreate) + `,"costUsdTicks":` + itoa(ticks) +
		`}}},"_meta":{"eventId":"` + event + `"}}`
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A grok turn's tokens land in the four buckets the snapshot counts, with the
// cached reads taken out of the prompt total rather than counted twice.
func TestGrokDecodeSplitsCachedReadsOutOfInput(t *testing.T) {
	// inputTokens is the whole prompt: 100000, of which 80000 were cache reads and
	// 5000 were written to the cache. 15000 is what was actually uncached.
	line := grokLine(1788136427, "ev1", "p1", 100000, 2000, 80000, 5000, 1_0000_000_000)
	r, ok := decodeGrok([]byte(line))
	if !ok {
		t.Fatal("a turn_completed line with a usage block did not decode")
	}
	if r.input != 15000 {
		t.Errorf("uncached input = %d, want 15000 (100000 prompt - 80000 cached - 5000 written)", r.input)
	}
	if r.output != 2000 || r.cacheRead != 80000 || r.cacheWrite != 5000 {
		t.Errorf("output/cacheRead/cacheWrite = %d/%d/%d, want 2000/80000/5000", r.output, r.cacheRead, r.cacheWrite)
	}
	if r.ts.Unix() != 1788136427 {
		t.Errorf("timestamp = %v, want the Unix second 1788136427", r.ts.Unix())
	}
}

// The cost is grok's own figure, converted by the tick scale and by nothing else.
// A test that only checked "cost > 0" would pass against any scale, so this pins
// the number: 1e10 ticks is one dollar.
func TestGrokCostIsTheVendorsOwnFigure(t *testing.T) {
	// 43_898_600_000 ticks is $4.38986 at 1e10 ticks to the dollar.
	r, ok := decodeGrok([]byte(grokLine(1788136427, "ev1", "p1", 500000, 10000, 400000, 0, 43_898_600_000)))
	if !ok {
		t.Fatal("line did not decode")
	}
	if want := 4.38986; !nearly(r.cost, want) {
		t.Errorf("cost = %v, want %v — the tick scale is 1e10 ticks to the dollar", r.cost, want)
	}
}

// A price table would be the wrong answer for grok and this holds the reader to
// not having one: the same tokens at a different stated price must give a
// different cost. If the reader ever started pricing tokens itself, these two
// would come out equal.
func TestGrokCostFollowsTheStatedPriceNotTheTokens(t *testing.T) {
	cheap, ok1 := decodeGrok([]byte(grokLine(1788136427, "ev1", "p1", 500000, 10000, 400000, 0, 1_000_000_000)))
	dear, ok2 := decodeGrok([]byte(grokLine(1788136427, "ev2", "p2", 500000, 10000, 400000, 0, 9_000_000_000)))
	if !ok1 || !ok2 {
		t.Fatal("lines did not decode")
	}
	if cheap.input != dear.input || cheap.output != dear.output {
		t.Fatal("the two lines were meant to carry identical token counts")
	}
	if cheap.cost >= dear.cost {
		t.Errorf("identical tokens at 1e9 and 9e9 ticks gave costs %v and %v; the reader is pricing tokens itself",
			cheap.cost, dear.cost)
	}
}

// Only turn_completed carries a settled total. Any other update kind that happens
// to mention a cost must not be counted, or a turn is counted twice: once while
// it streams and again when it finishes.
func TestGrokIgnoresUpdatesThatAreNotACompletedTurn(t *testing.T) {
	line := `{"timestamp":1788136427,"params":{"update":{"sessionUpdate":"agent_message_chunk",` +
		`"prompt_id":"p1","usage":{"inputTokens":10,"outputTokens":1,"costUsdTicks":500}}},` +
		`"_meta":{"eventId":"ev1"}}`
	if _, ok := decodeGrok([]byte(line)); ok {
		t.Error("a non-turn_completed update decoded; a streaming chunk would be counted twice")
	}
}

// A line with no usage block at all is not a usage record.
func TestGrokIgnoresLinesWithoutUsage(t *testing.T) {
	line := `{"timestamp":1788136427,"params":{"update":{"sessionUpdate":"turn_completed","prompt_id":"p1"}},` +
		`"_meta":{"eventId":"ev1"},"note":"costUsdTicks appears only in this string"}`
	if _, ok := decodeGrok([]byte(line)); ok {
		t.Error("a line with no usage block decoded as a usage record")
	}
}

// A timestamp grok did not write cannot be placed in a window. Zero is the
// absence of the field, not midnight in 1970 — counted as the latter it would
// fall below every scan floor and vanish, which is the same outcome by accident;
// refusing it says so on purpose.
func TestGrokRefusesAnAbsentTimestamp(t *testing.T) {
	if _, ok := decodeGrok([]byte(grokLine(0, "ev1", "p1", 10, 1, 0, 0, 100))); ok {
		t.Error("a line with no timestamp decoded")
	}
}

// grok's own arithmetic disagreeing with itself must not subtract from the
// window's tokens.
func TestGrokClampsNegativeUncachedInput(t *testing.T) {
	r, ok := decodeGrok([]byte(grokLine(1788136427, "ev1", "p1", 100, 1, 900, 0, 100)))
	if !ok {
		t.Fatal("line did not decode")
	}
	if r.input != 0 {
		t.Errorf("uncached input = %d, want 0; a negative count would subtract from the total", r.input)
	}
}

// Two lines with no ids at all must not collapse onto one another. The engine
// treats an empty key as "never a duplicate", so the decoder must not manufacture
// a key out of two absences.
func TestGrokLinesWithNoIdsAreNotAllTheSameRecord(t *testing.T) {
	line := `{"timestamp":1788136427,"params":{"update":{"sessionUpdate":"turn_completed",` +
		`"usage":{"inputTokens":10,"outputTokens":1,"costUsdTicks":100}}}}`
	r, ok := decodeGrok([]byte(line))
	if !ok {
		t.Fatal("line did not decode")
	}
	if r.key != "" {
		t.Errorf("key = %q for a line with neither eventId nor prompt_id; want \"\" so it is never a duplicate", r.key)
	}
}

// The end-to-end read: a grok session tree on disk becomes a snapshot. This is
// the test that would catch a wrong root, a wrong filename, or a walk that never
// reaches the depth grok nests its sessions at.
func TestGrokProviderReadsASessionTree(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	sess := filepath.Join(root, "sessions", "%2FUsers%2Fme%2Fproj", "01a0552f-6b32-7181-913b-b0b0a7068520")
	if err := os.MkdirAll(sess, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(sess, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ts := now.Add(-30 * time.Minute).Unix()
	write("updates.jsonl",
		grokLine(ts, "ev1", "p1", 1000, 100, 400, 0, 10_000_000_000)+"\n"+
			grokLine(ts+60, "ev2", "p2", 2000, 200, 800, 0, 20_000_000_000)+"\n")
	// The sibling files describe the same two turns. A reader that took every
	// .jsonl in the tree would count them again. These are the exact names grok
	// writes beside updates.jsonl, so the fixture is the claim it is checking.
	write("chat_history.jsonl", grokLine(ts, "ev1", "p1", 1000, 100, 400, 0, 10_000_000_000)+"\n")
	write("events.jsonl", grokLine(ts+60, "ev2", "p2", 2000, 200, 800, 0, 20_000_000_000)+"\n")
	write("rewind_points.jsonl", grokLine(ts, "ev1", "p1", 1000, 100, 400, 0, 10_000_000_000)+"\n")
	write("prompt_context.json", grokLine(ts, "ev1", "p1", 1000, 100, 400, 0, 10_000_000_000)+"\n")

	p := NewGrokProvider(5 * time.Hour)
	p.dir = filepath.Join(root, "sessions")
	p.now = func() time.Time { return now }

	snap, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snap.Source != "grok" {
		t.Errorf("Source = %q, want %q", snap.Source, "grok")
	}
	// Uncached 600 + 1200, output 100 + 200, cache reads 400 + 800 = 3300. Read a
	// second time out of the narrative files it would come to 6600.
	if got := snap.TotalTokens(); got != 3300 {
		t.Errorf("total tokens = %d, want 3300 (the narrative files must not be counted again)", got)
	}
	if want := 3.0; !nearly(snap.CostUSD, want) {
		t.Errorf("cost = %v, want %v", snap.CostUSD, want)
	}
}

// The `only` filter is what stops the narrative files being counted. Removing it
// must break the total above, and this pins the mechanism rather than the result.
func TestGrokFormatReadsOnlyTheAccountingFile(t *testing.T) {
	f := grokFormat()
	if f.only != "updates.jsonl" {
		t.Fatalf("only = %q, want updates.jsonl", f.only)
	}
	if !f.carries("/x/y/updates.jsonl") {
		t.Error("updates.jsonl is not carried; the accounting record would be skipped")
	}
	for _, other := range []string{"/x/y/chat_history.jsonl", "/x/y/events.jsonl", "/x/y/rewind_points.jsonl"} {
		if f.carries(other) {
			t.Errorf("%s is carried; its turns would be counted a second time", other)
		}
	}
}

// GROK_HOME moves the root, the way CLAUDE_CONFIG_DIR does for the Claude reader.
func TestGrokHomeOverridesTheRoot(t *testing.T) {
	t.Setenv("GROK_HOME", "/tmp/elsewhere")
	if got, want := grokSessionsDir(), filepath.Join("/tmp/elsewhere", "sessions"); got != want {
		t.Errorf("grokSessionsDir() = %q, want %q", got, want)
	}
}

func nearly(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
