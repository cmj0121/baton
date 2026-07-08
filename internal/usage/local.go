package usage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LocalProvider reads Claude Code's session transcripts and aggregates today's
// token usage. Every Claude Code run — baton's own agent panels included — appends
// a JSONL transcript under $HOME/.claude/projects/<project>/<session>.jsonl, one
// line per message, with the assistant messages carrying a `usage` block.
type LocalProvider struct {
	dir string           // the .../projects root scanned for transcripts
	now func() time.Time // injectable clock (tests pin "today")
}

// NewLocalProvider builds a local source rooted at the user's Claude Code project
// transcripts. CLAUDE_CONFIG_DIR overrides the ~/.claude location, matching Claude
// Code's own env override.
func NewLocalProvider() *LocalProvider {
	return &LocalProvider{dir: claudeProjectsDir(), now: time.Now}
}

// Source implements Provider.
func (p *LocalProvider) Source() string { return "local" }

// usageKey is the cheap substring gate: only lines that mention a usage block are
// worth JSON-parsing, and most transcript lines (user turns, tool results) do not.
var usageKey = []byte(`"usage"`)

// Fetch scans the transcripts for today's assistant messages and sums their token
// usage, pricing each message by its own model. Files not touched since local
// midnight are skipped whole — an append-only transcript last written yesterday
// cannot hold a message from today — which keeps a fleet of hundreds of sessions
// down to reading only the day's active few.
func (p *LocalProvider) Fetch(ctx context.Context) (Snapshot, error) {
	start := startOfDay(p.now())
	snap := Snapshot{Since: start, Source: "local"}
	seen := make(map[string]struct{})

	err := filepath.WalkDir(p.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable dir/file is skipped, not fatal to the whole scan
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if info, ierr := d.Info(); ierr != nil || info.ModTime().Before(start) {
			return nil // no message from today can live in a file last written before it
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		scanTranscript(path, start, seen, &snap)
		return nil
	})
	// A missing projects dir (Claude Code never run here) is not an error — it just
	// means zero usage. WalkDir surfaces it via the root callback, which we ignore.
	if err != nil && !os.IsNotExist(err) {
		return snap, err
	}
	return snap, nil
}

// scanTranscript folds one transcript file's today usage into snap. It reads line
// by line with an unbounded reader (a single line can carry a pasted image and
// blow past bufio.Scanner's token cap), and only parses lines that mention usage.
func scanTranscript(path string, start time.Time, seen map[string]struct{}, snap *Snapshot) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && bytes.Contains(line, usageKey) {
			foldEntry(line, start, seen, snap)
		}
		if err != nil {
			return // io.EOF or a read error: either way, done with this file
		}
	}
}

// transcriptEntry is the slice of a transcript line we read: the timestamp, the
// dedup keys, and the assistant message's model + usage.
type transcriptEntry struct {
	Timestamp string `json:"timestamp"`
	RequestID string `json:"requestId"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheCreation            *struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

// foldEntry parses one line and, if it is a today-dated assistant message not
// already counted, adds its tokens and cost to snap. Duplicate lines (a resumed
// session re-writes earlier turns) are keyed out by message id + request id.
func foldEntry(line []byte, start time.Time, seen map[string]struct{}, snap *Snapshot) {
	var e transcriptEntry
	if json.Unmarshal(line, &e) != nil || e.Message.Usage == nil {
		return
	}
	ts, err := time.Parse(time.RFC3339, e.Timestamp)
	if err != nil || ts.Before(start) {
		return
	}
	if e.Message.ID != "" || e.RequestID != "" {
		key := e.Message.ID + "|" + e.RequestID
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
	}

	u := e.Message.Usage
	snap.Input += u.InputTokens
	snap.Output += u.OutputTokens
	snap.CacheRead += u.CacheReadInputTokens
	snap.CacheWrite += u.CacheCreationInputTokens

	tu := tokenUsage{Uncached: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens}
	if u.CacheCreation != nil {
		tu.CacheWrite5m = u.CacheCreation.Ephemeral5m
		tu.CacheWrite1h = u.CacheCreation.Ephemeral1h
	} else {
		// No tier breakdown: price the whole cache write at the 5-minute rate, the
		// common default, rather than dropping it.
		tu.CacheWrite5m = u.CacheCreationInputTokens
	}
	snap.CostUSD += costUSD(e.Message.Model, tu)
}

// claudeProjectsDir locates Claude Code's transcript root: $CLAUDE_CONFIG_DIR/projects
// when set (Claude Code's own override), else ~/.claude/projects.
func claudeProjectsDir() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return filepath.Join(v, "projects")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".claude", "projects")
	}
	return filepath.Join(".claude", "projects")
}
