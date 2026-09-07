package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The grok reader. It exists because grok keeps books, which is not what a first
// look at ~/.grok suggests: chat_history.jsonl, events.jsonl, summary.json and
// prompt_context.json carry prompts and transcript and no accounting at all, and
// a survey that reads those concludes the vendor records nothing. The accounting
// is in a fifth file — updates.jsonl — one `turn_completed` line per prompt,
// carrying the turn's tokens and the turn's price.
//
// That is the whole reason this file is small. Everything hard is in local.go's
// engine; grok supplies a root, a filename, a gate and a decoder.

// grokUsageKey is the substring gate. A turn_completed line is the only kind that
// carries a usage block, and it is a small minority of updates.jsonl.
var grokUsageKey = []byte(`"costUsdTicks"`)

// grokSessionsDir locates grok's session root: $GROK_HOME/sessions when set, else
// ~/.grok/sessions.
func grokSessionsDir() string {
	if v := os.Getenv("GROK_HOME"); v != "" {
		return filepath.Join(v, "sessions")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".grok", "sessions")
	}
	return filepath.Join(".grok", "sessions")
}

// grokFormat is the grok session-log reader.
//
// `only` names updates.jsonl and it is load-bearing rather than a speed-up. A
// grok session directory holds several JSONL files describing the same turn, and
// a reader that took every .jsonl in the tree would count one turn's spend once
// per file that mentioned it. updates.jsonl is the accounting record; the others
// are narrative.
func grokFormat() vendorFormat {
	return vendorFormat{
		source: "grok",
		root:   grokSessionsDir(),
		only:   "updates.jsonl",
		gate:   grokUsageKey,
		decode: decodeGrok,
	}
}

// NewGrokProvider builds the grok reader over the user's session logs. window is
// the same window length the Claude reader takes; grok states no window of its
// own, so baton measures it the same way for both rather than inventing a second
// meaning for the same setting.
func NewGrokProvider(window time.Duration) *LocalProvider {
	return newFormatProvider(grokFormat(), window)
}

// grokUpdate is the slice of an updates.jsonl line the reader wants.
//
// The timestamp is a Unix second, not an RFC 3339 string — which is the one place
// grok's format genuinely differs from Claude Code's rather than merely being
// spelled differently, and the reason a decoder is a function and not a struct tag.
type grokUpdate struct {
	Timestamp int64 `json:"timestamp"`
	Params    struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			PromptID      string `json:"prompt_id"`
			Usage         *struct {
				InputTokens         int64 `json:"inputTokens"`
				OutputTokens        int64 `json:"outputTokens"`
				CachedReadTokens    int64 `json:"cachedReadTokens"`
				CacheCreationTokens int64 `json:"cacheCreationTokens"`
				CostUsdTicks        int64 `json:"costUsdTicks"`
			} `json:"usage"`
		} `json:"update"`
	} `json:"params"`
	Meta struct {
		EventID string `json:"eventId"`
	} `json:"_meta"`
}

// grokCostTicksPerUSD converts grok's costUsdTicks to dollars.
//
// The scale is not documented, so it was inferred, and the strength of that
// inference is worth stating precisely because the obvious way to phrase it
// overstates it.
//
// Solving ticks against tokens over this machine's grok-4.6 turns gives 20000
// ticks per uncached input token, 60000 per output token and 5000 per cached
// read. That solve has three unknowns and was fitted on three turns, so it lands
// exactly BY CONSTRUCTION: a zero residual there is arithmetic, not evidence, and
// the other turns in the corpus are other models on other rate cards and do not
// fit these three numbers at all.
//
// What is evidence is that the answer came out ROUND. An arbitrary three-by-three
// solve yields arbitrary numbers; this one yields $2.00, $6.00 and $0.50 per
// million tokens at 1e10 ticks to the dollar, with cached reads a quarter of
// input and output three times it — the ordinary shape of a published rate card.
// A decade either way gives $20/$60 or $0.02/$0.06, and nobody charges either.
//
// The independent check is the whole corpus, which does not depend on the fit at
// all: 136 turns, 349M tokens of which 166M are cached reads, total 325025000400
// ticks. At 1e10 that is $32.50, which is what that traffic costs. At 1e9 it is
// $325 and at 1e11 it is $3.25, and neither is.
//
// The way to falsify this is a turn whose cost grok also reports in dollars, or a
// billing statement. Until then it is an inference with its own arithmetic shown.
//
// baton does not reconstruct this figure, it reads it: grok states the cost of
// every turn, so there is no per-model price table here to go stale when grok
// reprices. The constant is a unit conversion, and the only thing that could
// invalidate it is grok redefining its own unit.
const grokCostTicksPerUSD = 1e10

// decodeGrok reads one updates.jsonl line.
//
// Each turn_completed line is that prompt's own total, not a running one: the
// figures across a session's lines do not increase monotonically, and every line
// carries a distinct prompt_id. So they sum, and summing is what the engine does.
func decodeGrok(line []byte) (record, bool) {
	var u grokUpdate
	if json.Unmarshal(line, &u) != nil {
		return record{}, false
	}
	up := u.Params.Update
	if up.Usage == nil || up.SessionUpdate != "turn_completed" || u.Timestamp <= 0 {
		return record{}, false
	}
	g := up.Usage
	// inputTokens is grok's total prompt size, cached reads included; the engine
	// counts the two apart, as Claude Code's transcript already reports them.
	uncached := g.InputTokens - g.CachedReadTokens - g.CacheCreationTokens
	if uncached < 0 {
		// A vendor's own arithmetic disagreeing with itself is not something to
		// silently carry into a total: count no uncached input rather than a negative
		// one, which would subtract from the window's tokens.
		uncached = 0
	}
	// An empty key means "can never be a duplicate", so it must not be built out of
	// two absent ids — every such line would collapse onto the single key "|" and
	// all but the first would be dropped as a repeat of each other.
	key := ""
	if u.Meta.EventID != "" || up.PromptID != "" {
		key = u.Meta.EventID + "|" + up.PromptID
	}
	return record{
		ts:         time.Unix(u.Timestamp, 0),
		key:        key,
		input:      uncached,
		output:     g.OutputTokens,
		cacheRead:  g.CachedReadTokens,
		cacheWrite: g.CacheCreationTokens,
		cost:       float64(g.CostUsdTicks) / grokCostTicksPerUSD,
	}, true
}
