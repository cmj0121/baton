package usage

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Transcript lines in the shape Claude Code writes them at a session's start.
const (
	openAttachment = `{"type":"attachment","isSidechain":false,"attachment":{"type":"skill_listing","content":"` +
		`a long listing of skills"}}`
	openMeta = `{"type":"user","isSidechain":false,"isMeta":true,"message":{"role":"user",` +
		`"content":"<local-command-caveat>Caveat: generated while running local commands</local-command-caveat>"}}`
	openFirstTurn = `{"type":"assistant","isSidechain":false,"message":{"id":"m1","role":"assistant",` +
		`"usage":{"input_tokens":2,"cache_creation_input_tokens":20000,"cache_read_input_tokens":24000,"output_tokens":9}}}`
	openSecondTurn = `{"type":"assistant","isSidechain":false,"message":{"id":"m2","role":"assistant",` +
		`"usage":{"input_tokens":5,"cache_creation_input_tokens":900,"cache_read_input_tokens":44002,"output_tokens":9}}}`
)

// typedLine is a user line carrying text as a plain string.
func typedLine(text string) string {
	return `{"type":"user","isSidechain":false,"message":{"role":"user","content":"` + text + `"}}`
}

func TestOpeningOf(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  int64
		ok    bool
	}{
		{
			name:  "attachments stay in, the first turn is read",
			lines: []string{openAttachment, openFirstTurn, openSecondTurn},
			want:  44002, ok: true,
		},
		{
			name:  "the typed prompt is taken out",
			lines: []string{openAttachment, typedLine(strings.Repeat("abcd", 100)), openFirstTurn},
			want:  44002 - 100, ok: true,
		},
		{
			name:  "a CJK prompt counts a token a character",
			lines: []string{typedLine("修正這個問題"), openFirstTurn},
			want:  44002 - 6, ok: true,
		},
		{
			name: "a text-block prompt is taken out, other blocks are not",
			lines: []string{
				`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` +
					strings.Repeat("x", 40) + `"},{"type":"image","source":{"data":"` + strings.Repeat("y", 400) + `"}}]}}`,
				openFirstTurn,
			},
			want: 44002 - 10, ok: true,
		},
		{
			name:  "a meta line is Claude Code's, not typed",
			lines: []string{openMeta, openFirstTurn},
			want:  44002, ok: true,
		},
		{
			name: "a sidechain turn is a subagent's, not the opening",
			lines: []string{
				`{"type":"assistant","isSidechain":true,"message":{"usage":{"input_tokens":7000}}}`,
				openFirstTurn,
			},
			want: 44002, ok: true,
		},
		{
			name: "a turn stating no input is skipped",
			lines: []string{
				`{"type":"assistant","message":{"model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0}}}`,
				openFirstTurn,
			},
			want: 44002, ok: true,
		},
		{
			name:  "a prompt larger than the turn floors at zero",
			lines: []string{typedLine(strings.Repeat("z", 400)), `{"type":"assistant","message":{"usage":{"input_tokens":10}}}`},
			want:  0, ok: true,
		},
		{
			name:  "garbage lines are passed over",
			lines: []string{`not json`, `{"type":`, openFirstTurn},
			want:  44002, ok: true,
		},
		{
			name:  "no assistant turn yet",
			lines: []string{openAttachment, typedLine("hello")},
			ok:    false,
		},
		{
			name: "empty transcript",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := openingOf(strings.NewReader(strings.Join(tt.lines, "\n") + "\n"))
			if got != tt.want || ok != tt.ok {
				t.Fatalf("openingOf = (%d, %v), want (%d, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

// TestOpeningOfSkipsAnOversizedLine drops a line past the per-line cap and still
// finds the turn after it.
func TestOpeningOfSkipsAnOversizedLine(t *testing.T) {
	huge := `{"type":"user","message":{"content":"` + strings.Repeat("h", maxTranscriptLine) + `"}}`
	got, ok := openingOf(strings.NewReader(huge + "\n" + openFirstTurn + "\n"))
	if !ok || got != 44002 {
		t.Fatalf("openingOf = (%d, %v), want (44002, true) — the oversized prompt is not counted either way", got, ok)
	}
}

func TestOpening(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "-Users-me-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "0b1c2d3e-0000-4000-8000-000000000001"
	body := strings.Join([]string{openAttachment, typedLine("abcd"), openFirstTurn}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, ok := Opening(root, id); !ok || got != 44001 {
		t.Errorf("Opening = (%d, %v), want (44001, true)", got, ok)
	}
	for _, bad := range []string{"", "0b1c2d3e-0000-4000-8000-000000000002", "../" + id, "*"} {
		if got, ok := Opening(root, bad); ok {
			t.Errorf("Opening(%q) = (%d, true), want no reading", bad, got)
		}
	}
}

// TestOpeningSkipsAFIFO is the same guard the usage scan has: a pipe named like a
// transcript must not park the caller in open(2).
func TestOpeningSkipsAFIFO(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo.jsonl"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if _, ok := Opening(root, "fifo"); ok {
		t.Fatal("Opening read a FIFO")
	}
}

func TestEstimateTokens(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"記憶", 2},
		{"fix 記憶", 3},
	} {
		if got := estimateTokens(tt.in); got != tt.want {
			t.Errorf("estimateTokens(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// TestOpeningRootIsNotAPattern: a root whose path holds glob metacharacters is
// still a plain directory.
func TestOpeningRootIsNotAPattern(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home[1]")
	dir := filepath.Join(root, "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err) // a file beside the project directories is passed over
	}
	const id = "0b1c2d3e-0000-4000-8000-000000000003"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(openFirstTurn+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := Opening(root, id); !ok || got != 44002 {
		t.Fatalf("Opening = (%d, %v), want (44002, true)", got, ok)
	}
	if _, ok := Opening(filepath.Join(root, "missing"), id); ok {
		t.Fatal("Opening read from a root that does not exist")
	}
}
