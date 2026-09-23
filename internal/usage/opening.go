package usage

import (
	"bufio"
	"encoding/json"
	"io"
	"path/filepath"
	"unicode/utf8"

	"github.com/cmj0121/baton/internal/paths"
)

// A session's opening cost is what an agent pays before the conversation has said
// anything: the system prompt, the tool definitions, and everything Claude Code
// loads into the first turn on the agent's behalf — CLAUDE.md, the auto-memory
// index, the skill and agent listings, hook context. It is the part of every turn
// the user can shrink by editing files, and the one part no per-turn figure
// separates out.
//
// It is read, not estimated. The first assistant turn's input — uncached, cache
// written and cache read together — is exactly what the API was sent, and at that
// point the only thing in it besides the opening cost is what the user typed.
// Claude Code records the loaded context as `attachment` lines of their own and
// the typed prompt as a `user` line, so the one estimate here is the typed text,
// and its error is bounded by the size of that text.
//
// The figure is fixed for the session's life: the system prompt is assembled at
// session start, so a memory file edited mid-session changes the NEXT session's
// opening cost and not this one's. A caller reads it once per session.

// maxOpeningRead bounds how far into a transcript the reader looks for the first
// turn. The loaded context runs to tens of kilobytes; a transcript that reaches
// this far with no assistant turn in it is not a session opening any more.
const maxOpeningRead = 64 << 20

// openingEntry is the slice of a transcript line the opening reader needs.
type openingEntry struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Message     struct {
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int64 `json:"input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// Opening reads the opening cost of the Claude Code session with the given id,
// looking for its transcript under root (a Claude Code projects directory). ok is
// false while the session has no assistant turn yet, and when no transcript for it
// exists — the caller asks again later rather than caching a zero.
func Opening(root, session string) (tokens int64, ok bool) {
	if !sessionIDShape(session) {
		return 0, false
	}
	// The project directory is found rather than computed: Claude Code shortens a
	// long cwd's directory name with a hash no cwd encodes to, while a session id is
	// a UUID no two directories share.
	matches, err := filepath.Glob(filepath.Join(root, "*", session+".jsonl"))
	if err != nil || len(matches) == 0 {
		return 0, false
	}
	f, err := paths.OpenRegular(matches[0])
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	return openingOf(io.LimitReader(f, maxOpeningRead))
}

// sessionIDShape reports whether s can be a session id: letters, digits and
// dashes only. The id is spliced into a glob, so anything else — a separator, a
// "*" — would reach some other session's transcript, or several.
func sessionIDShape(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// ClaudeProjectsDir is the transcript root Opening is normally pointed at: the
// same one the local usage reader walks.
func ClaudeProjectsDir() string { return claudeProjectsDir() }

// openingOf scans a transcript for its first main-chain assistant turn, and
// returns that turn's input less the typed prompt that preceded it.
func openingOf(r io.Reader) (int64, bool) {
	br := bufio.NewReader(r)
	var typed int64
	for {
		line, over, err := cappedLine(br)
		if !over && len(line) > 0 {
			var e openingEntry
			if json.Unmarshal(line, &e) == nil && !e.IsSidechain {
				switch e.Type {
				case "user":
					if !e.IsMeta {
						typed += promptTokens(e.Message.Content)
					}
				case "assistant":
					if u := e.Message.Usage; u != nil {
						// A turn the API never answered — an error Claude Code writes as a
						// synthetic assistant line — states no input, and is not the opening.
						if in := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens; in > 0 {
							return max(in-typed, 0), true
						}
					}
				}
			}
		}
		if err != nil {
			return 0, false
		}
	}
}

// promptTokens estimates the tokens in a user line's text: a string, or the text
// blocks of a content list. Anything else a user line can carry before the first
// turn (an image) is left in the opening figure rather than guessed at.
func promptTokens(raw json.RawMessage) int64 {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return estimateTokens(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return 0
	}
	var n int64
	for _, b := range blocks {
		if b.Type == "text" {
			n += estimateTokens(b.Text)
		}
	}
	return n
}

// estimateTokens approximates a text's token count without the tokenizer, which
// is not public: about four characters a token for ASCII text, and about one a
// token for everything else — a CJK character is rarely merged with its neighbour.
func estimateTokens(s string) int64 {
	var ascii, other int64
	for _, r := range s {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+3)/4 + other
}
