package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cmj0121/baton/internal/paths"
)

// The codex reader. Codex writes one rollout per session under
// sessions/YYYY/MM/DD/rollout-<stamp>-<id>.jsonl, and the rollout states the
// spend of every model response twice: once as an event_msg/token_count line —
// the figure the TUI shows, last_token_usage beside a running total — and once as
// a token_usage_record line carrying the same numbers under the response's own
// id. On this machine's rollouts the two agree line for line, so which to read is
// decided by what else each one carries, and the record carries the id.
//
// The id is the reason. A forked or resumed rollout copies the parent's history
// into a new file, timestamps and all, and the same response's spend then sits
// in two files. Claude Code's transcripts have the same property and the engine's
// dedup is the answer to it, but the dedup needs a key, and token_count has none
// that is the response's own: its nearest stand-in, a timestamp and a running
// total, is two coincidences agreeing rather than an identity.
//
// The other thing codex does differently is the tree. Grok and Claude Code file a
// session under its project; codex files it under the date it began, so the first
// directory under the root is a year. The project is stated instead, as the cwd
// on the rollout's first line, and the format's origin hook reads it from there.

// codexUsageKey is the substring gate, and it is spelled out to the quote marks on
// purpose. The record is a small minority of a rollout's lines, and the lines it
// is not are the big ones: response_item lines carry whole tool outputs and
// prompts. A bare "token_usage_record" would match any of those that merely
// mention it, where the quoted key-and-value can only appear unescaped — that is,
// as the line's own type rather than inside a string — in compact JSON that has
// one.
var codexUsageKey = []byte(`"type":"token_usage_record"`)

// codexHome is codex's config root: $CODEX_HOME when set, else ~/.codex. The
// override is codex's own, and honouring it here is what keeps the reader on the
// tree the signed-in CLI is actually writing.
func codexHome() string {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".codex")
	}
	return ".codex"
}

// codexSessionsDir locates codex's rollout root under codexHome.
func codexSessionsDir() string {
	return filepath.Join(codexHome(), "sessions")
}

// codexFormat is the codex rollout reader.
//
// project is the identity because the origin hook already made the project key
// the stated cwd: there is no encoded name to read back, the key is the path.
func codexFormat() vendorFormat {
	return vendorFormat{
		source:  "codex",
		root:    codexSessionsDir(),
		gate:    codexUsageKey,
		decode:  decodeCodex,
		project: func(dir string) string { return dir },
		origin:  codexOrigin,
	}
}

// NewCodexProvider builds the codex reader over the user's rollouts. window is
// the same window length the other local readers take; codex's own rate-limit
// windows are a separate reading and do not come from here.
func NewCodexProvider(window time.Duration) *LocalProvider {
	return newFormatProvider(codexFormat(), window)
}

// codexMeta is the slice of a rollout's first line the origin wants.
type codexMeta struct {
	Type    string `json:"type"`
	Payload struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"payload"`
}

// codexOrigin reads a rollout's project and session off its session_meta line.
//
// The first line is the only one read, and it is read under the same bound and
// through the same plain-file open as the transcript itself: it carries the
// session's base instructions and runs to tens of kilobytes, and a FIFO named
// like a rollout must not park the poller here any more than it may in the scan.
//
// A rollout whose first line states no cwd is unattributed rather than credited to
// a directory of the path — every one of those is a date. The session falls back
// to the file's name, which codex builds from the same id, so a rollout with an
// unreadable header is still a session of its own rather than nobody's.
func codexOrigin(_, path string) (project, session string) {
	session = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	f, err := paths.OpenRegular(path)
	if err != nil {
		return "", session
	}
	defer func() { _ = f.Close() }()
	line, over, _ := cappedLine(bufio.NewReader(f))
	var m codexMeta
	if over || json.Unmarshal(line, &m) != nil || m.Type != "session_meta" {
		return "", session
	}
	if m.Payload.ID != "" {
		session = m.Payload.ID
	}
	return m.Payload.Cwd, session
}

// codexTokens is the four counts of codex's token block that the reader uses.
// reasoning_output_tokens and total_tokens are left out on purpose: reasoning is
// already inside output_tokens, and the total is input plus output, so decoding
// either would only invite counting the same tokens twice.
type codexTokens struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
}

// codexRecord is the slice of a token_usage_record line the reader wants. usage
// is the one response's spend; turn_token_usage and thread_token_usage beside it
// are running totals, and summing those would count every earlier response again.
type codexRecord struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		ResponseID string       `json:"response_id"`
		Usage      *codexTokens `json:"usage"`
	} `json:"payload"`
}

// decodeCodex reads one token_usage_record line.
//
// The buckets are grok's. input_tokens is the whole prompt with the cached part
// inside it, so the uncached input is what is left after cached reads and cache
// writes are taken out. output_tokens already contains reasoning_output_tokens —
// total_tokens is input plus output on every line, reasoning or not — so reasoning
// is not a term of its own: adding it would count it twice.
//
// Cost is zero. The rollout states no price and baton keeps no table for codex's
// models; a reading of tokens alone is a true reading, and a guessed dollar figure
// would not be.
func decodeCodex(line []byte) (record, bool) {
	var c codexRecord
	if json.Unmarshal(line, &c) != nil || c.Type != "token_usage_record" || c.Payload.Usage == nil {
		return record{}, false
	}
	ts, err := time.Parse(time.RFC3339, c.Timestamp)
	if err != nil {
		return record{}, false
	}
	u := c.Payload.Usage
	uncached := u.InputTokens - u.CachedInputTokens - u.CacheWriteInputTokens
	if uncached < 0 {
		// As with grok: a vendor's arithmetic disagreeing with itself counts as no
		// uncached input, never as a negative that subtracts from the window.
		uncached = 0
	}
	return record{
		ts:         ts,
		key:        c.Payload.ResponseID, // "" — never a duplicate — when codex wrote none
		input:      uncached,
		output:     u.OutputTokens,
		cacheRead:  u.CachedInputTokens,
		cacheWrite: u.CacheWriteInputTokens,
	}, true
}
