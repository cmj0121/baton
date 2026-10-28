package usage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// codexMetaLine is a rollout's first line, reduced to what the origin reads.
func codexMetaLine(id, cwd string) string {
	return `{"timestamp":"2026-10-28T02:39:51.836Z","type":"session_meta","payload":{"session_id":"` + id +
		`","id":"` + id + `","cwd":"` + cwd + `","base_instructions":"you are codex"}}`
}

// codexRecordLine is one token_usage_record line in the shape codex writes, the
// running totals beside usage included, because not summing those is part of
// what the decoder has to get right. Half the output is reasoning, as codex counts
// it — inside output_tokens — so a decoder that added it again would be caught.
func codexRecordLine(ts time.Time, resp string, in, cached, cacheWrite, out int64) string {
	usage := `{"input_tokens":` + itoa(in) + `,"cached_input_tokens":` + itoa(cached) +
		`,"cache_write_input_tokens":` + itoa(cacheWrite) + `,"output_tokens":` + itoa(out) +
		`,"reasoning_output_tokens":` + itoa(out/2) + `,"total_tokens":` + itoa(in+out) + `}`
	big := `{"input_tokens":9000000,"cached_input_tokens":0,"cache_write_input_tokens":0,` +
		`"output_tokens":9000000,"reasoning_output_tokens":0,"total_tokens":18000000}`
	return `{"timestamp":"` + ts.UTC().Format(time.RFC3339Nano) + `","ordinal":16,"type":"token_usage_record",` +
		`"payload":{"thread_id":"t","turn_id":"u","response_id":"` + resp + `","usage":` + usage +
		`,"turn_token_usage":` + big + `,"thread_token_usage":` + big + `}}`
}

// codexTokenCountLine is the event_msg twin of a record: the same response's
// spend, which must not be counted a second time.
func codexTokenCountLine(ts time.Time, in, cached, out int64) string {
	usage := `{"input_tokens":` + itoa(in) + `,"cached_input_tokens":` + itoa(cached) +
		`,"cache_write_input_tokens":0,"output_tokens":` + itoa(out) + `,"reasoning_output_tokens":0,"total_tokens":` +
		itoa(in+out) + `}`
	return `{"timestamp":"` + ts.UTC().Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"token_count",` +
		`"info":{"total_token_usage":` + usage + `,"last_token_usage":` + usage + `,"model_context_window":475000},` +
		`"rate_limits":{"limit_id":"codex","primary":{"used_percent":1.0,"window_minutes":10080,"resets_at":1793759366}}}}`
}

// writeRollout lays a rollout out where codex does: sessions/YYYY/MM/DD.
func writeRollout(t *testing.T, home string, day time.Time, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A record's tokens land in grok's buckets: cached reads and cache writes out of
// the prompt total, output as stated, and the running totals left alone.
func TestCodexDecodeSplitsCachedInputOut(t *testing.T) {
	ts := time.Date(2026, 10, 28, 3, 28, 32, 375e6, time.UTC)
	r, ok := decodeCodex([]byte(codexRecordLine(ts, "resp_1", 16121, 12032, 89, 60)))
	if !ok {
		t.Fatal("a token_usage_record line did not decode")
	}
	if r.input != 4000 || r.cacheRead != 12032 || r.cacheWrite != 89 || r.output != 60 {
		t.Errorf("input/cacheRead/cacheWrite/output = %d/%d/%d/%d, want 4000/12032/89/60 (reasoning is inside output)",
			r.input, r.cacheRead, r.cacheWrite, r.output)
	}
	if !r.ts.Equal(ts) || r.key != "resp_1" || r.cost != 0 {
		t.Errorf("ts/key/cost = %v/%q/%v, want %v/resp_1/0", r.ts, r.key, r.cost, ts)
	}
}

// Codex's arithmetic disagreeing with itself must not subtract from the window.
func TestCodexClampsNegativeUncachedInput(t *testing.T) {
	r, ok := decodeCodex([]byte(codexRecordLine(time.Now(), "r", 10, 900, 0, 1)))
	if !ok || r.input != 0 {
		t.Errorf("uncached input = %d (ok=%v), want 0", r.input, ok)
	}
}

// Every other kind of line is not a usage record — the token_count twin above all,
// which carries the same spend.
func TestCodexIgnoresOtherLines(t *testing.T) {
	now := time.Now()
	for name, line := range map[string]string{
		"token_count":  codexTokenCountLine(now, 100, 0, 1),
		"session_meta": codexMetaLine("id", "/p"),
		"no usage":     `{"timestamp":"2026-10-28T03:28:32Z","type":"token_usage_record","payload":{"response_id":"r"}}`,
		"bad time":     `{"timestamp":"yesterday","type":"token_usage_record","payload":{"usage":{"input_tokens":1}}}`,
		"not json":     `{"type":"token_usage_record"`,
	} {
		if _, ok := decodeCodex([]byte(line)); ok {
			t.Errorf("%s decoded as a usage record", name)
		}
	}
}

// The gate lets the record through and keeps out a response_item that merely
// quotes one — the big lines are the ones it exists to skip.
func TestCodexGateIsTheLinesOwnType(t *testing.T) {
	f := codexFormat()
	rec := codexRecordLine(time.Now(), "r", 1, 0, 0, 1)
	quoted := `{"type":"response_item","payload":{"type":"message","content":"` +
		`{\"type\":\"token_usage_record\",\"payload\":{}}"}}`
	if !containsGate(rec, f.gate) {
		t.Error("the gate drops a real token_usage_record line")
	}
	if containsGate(quoted, f.gate) || containsGate(codexTokenCountLine(time.Now(), 1, 0, 1), f.gate) {
		t.Error("the gate passes a line whose own type is not token_usage_record")
	}
}

func containsGate(line string, gate []byte) bool { return bytes.Contains([]byte(line), gate) }

// The origin is the stated cwd and the rollout id, never a date segment; a
// rollout with no readable header is unattributed and still its own session.
func TestCodexOriginIsTheStatedCwd(t *testing.T) {
	home := t.TempDir()
	day := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	good := writeRollout(t, home, day, "rollout-2026-10-28T10-29-36-abc.jsonl", codexMetaLine("abc", "/p/a"))
	if p, s := codexOrigin(filepath.Join(home, "sessions"), good); p != "/p/a" || s != "abc" {
		t.Errorf("origin = %q, %q, want /p/a, abc", p, s)
	}
	bare := writeRollout(t, home, day, "rollout-x.jsonl", codexRecordLine(day, "r", 1, 0, 0, 1))
	if p, s := codexOrigin(filepath.Join(home, "sessions"), bare); p != "" || s != "rollout-x" {
		t.Errorf("headerless origin = %q, %q, want \"\", rollout-x", p, s)
	}
}

// The acceptance read: a rollout under a temp CODEX_HOME reports a reading whose
// tokens are the in-window records' sum. The record before the window, the
// token_count twins, and a forked copy of the same responses add nothing, and the
// spend is credited to the stated cwd rather than to the year directory.
func TestCodexReportReadsARollout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	now := time.Now()
	old, a, b := now.Add(-7*time.Hour), now.Add(-30*time.Minute), now.Add(-10*time.Minute)

	lines := []string{
		codexMetaLine("s1", "/p/a"),
		codexRecordLine(old, "resp_old", 5000, 0, 0, 500), // a window that ended hours ago
		codexRecordLine(a, "resp_a", 1000, 400, 0, 100),
		codexTokenCountLine(a, 1000, 400, 100),
		codexRecordLine(b, "resp_b", 2000, 800, 50, 200),
		codexTokenCountLine(b, 2000, 800, 200),
	}
	writeRollout(t, home, a, "rollout-a-s1.jsonl", lines...)
	// A fork replays the parent's records verbatim, ids and timestamps included.
	writeRollout(t, home, b, "rollout-b-s2.jsonl", append([]string{codexMetaLine("s2", "/p/a")}, lines[1:]...)...)

	r := Report(context.Background(), VendorCandidate{Name: "codex"}, 5*time.Hour, now)
	if r.State != VendorReading || r.Source != "codex" {
		t.Fatalf("state/source = %q/%q (%s), want reading/codex", r.State, r.Source, r.Reason)
	}
	// 1000+100 and 2000+200 tokens, cached included; once, not twice, not with
	// the 5500 from before the window.
	if r.Tokens != 3300 {
		t.Errorf("tokens = %d, want 3300", r.Tokens)
	}
	if r.CostUSD != 0 {
		t.Errorf("cost = %v, want 0; codex states no price", r.CostUSD)
	}
	if len(r.Projects) != 1 || r.Projects["/p/a"].Tokens != 3300 {
		t.Errorf("projects = %v, want {/p/a: 3300}", r.Projects)
	}
	if h := r.ProjectHints["/p/a"]; h.Path != "/p/a" || !h.Exact {
		t.Errorf("hint = %+v, want /p/a exact", h)
	}
}
