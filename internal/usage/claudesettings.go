package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Reading Claude Code's own settings is what lets baton put itself in front of
// the status line without taking it over.
//
// Baton harvests the account's rate limits by handing Claude Code a status line
// of its own. That is a setting the user may already have spent an afternoon on,
// and a cockpit that silently replaced it would be trading their work for a
// number. So baton resolves what they configured, and runs it — the panel looks
// the same as it did, and the harvesting happens behind it.
//
// The precedence below is Claude Code's own, narrowest first. Only the sources
// baton can see are walked: an enterprise managed policy outranks everything
// including baton's injection, so a fleet under one simply gets no reading, which
// is the correct outcome rather than a broken status line.

// claudeSettings is the slice of a Claude Code settings file baton reads.
// StatusLine is a pointer so "no statusLine key" and "a statusLine key with
// nothing in it" stay distinguishable — they lead to opposite decisions.
type claudeSettings struct {
	StatusLine *struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	} `json:"statusLine"`
}

// StatusLine resolves the status line configured for a panel running in dir: the
// shell command to run, and whether a status line is configured at all.
//
// The two results are separate because there are three outcomes, not two:
//
//   - ("cmd", true)  — a command status line baton can wrap and run.
//   - ("", true)     — a status line baton cannot reproduce (a form it does not
//     know). Injecting would replace something the user set up
//     with something else, so the caller must not inject.
//   - ("", false)    — no status line at all. Injecting adds a row that was not
//     there, which is a change the caller makes deliberately.
func StatusLine(dir string) (command string, configured bool) {
	for _, path := range statusLinePaths(dir) {
		s, ok := readClaudeSettings(path)
		if !ok || s.StatusLine == nil {
			continue
		}
		// Narrowest wins, and the walk runs narrowest first — so the first file that
		// names a status line at all is the one in force, even if it names one baton
		// cannot wrap. Falling through to a broader file would run a status line the
		// user had already overridden.
		if !strings.EqualFold(s.StatusLine.Type, "command") {
			return "", true
		}
		return strings.TrimSpace(s.StatusLine.Command), true
	}
	return "", false
}

// statusLinePaths lists the settings files that can carry a status line, in
// falling precedence: the project's local overrides, then the project's shared
// settings, then the user's own. A panel with no working directory is resolved
// against the user's settings alone.
func statusLinePaths(dir string) []string {
	var paths []string
	if dir = strings.TrimSpace(dir); dir != "" {
		paths = append(paths,
			filepath.Join(dir, ".claude", "settings.local.json"),
			filepath.Join(dir, ".claude", "settings.json"),
		)
	}
	return append(paths, filepath.Join(claudeConfigDir(), "settings.json"))
}

// readClaudeSettings parses one settings file. A missing file is the common case
// and not an error; a malformed one is treated the same way, because a cockpit is
// in no position to adjudicate somebody's JSON and guessing at it is how a status
// line ends up replaced by accident.
func readClaudeSettings(path string) (claudeSettings, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return claudeSettings{}, false
	}
	var s claudeSettings
	if err := json.Unmarshal(b, &s); err != nil {
		return claudeSettings{}, false
	}
	return s, true
}

// claudeConfigDir is Claude Code's own config root: $CLAUDE_CONFIG_DIR when set
// (Claude Code's documented override), else ~/.claude. It is the same resolution
// claudeProjectsDir does one level down, kept separate because a caller after the
// settings file has no business knowing where the transcripts live.
func claudeConfigDir() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".claude")
	}
	return ".claude"
}
