// Package agents is baton's catalogue of agent backends: the CLI names baton
// knows how to spawn, and which of them the machine the FLEET runs on actually
// has.
//
// It is a neutral package rather than part of the config file format, for the
// same reason internal/attn and internal/limits are: the YAML layer reads the
// user's profiles into it and the daemon reads a detected set out, and neither
// should have to depend on the other to speak about the same names.
//
// The catalogue is deliberately thin — a name and the binary behind it, nothing
// else. No default arguments, no per-backend attention thresholds, no
// "recommended flags": those are behavioural assumptions about someone else's
// CLI, and they rot the release after they are written. docs/ISOLATION.md makes
// the same argument for shipping no container image. baton names the thing; the
// user configures it.
package agents

import (
	"os/exec"
	"sort"
)

// Default is the backend assumed when nothing is configured — the one baton has
// always spawned, kept as the default so an existing fleet behaves the way it
// did before there was anything to choose.
const Default = "claude"

// Backend is one way to launch an agent: the profile name a panel is spawned
// under and the command behind it.
type Backend struct {
	Name     string   // the profile name — what the user selects and what the card carries
	Command  string   // the binary to run
	Args     []string // arguments passed on every spawn; empty for every preset
	Isolated bool     // the command runs inside a container image, so the host's PATH cannot answer for it
	Homepage string   // where to get it, for a preset the machine does not have; empty on a user profile
}

// Scanned is one catalogue entry with this machine's verdict on it: the backend,
// and whether the command behind it is absent.
//
// It is a separate type rather than a field on Backend because the two are
// different kinds of fact. A Backend is the catalogue — what baton knows how to
// spawn, true on every machine. Missing is what one machine happens to have, true
// only until someone runs an install. Folding the second into the first would
// mean every list of backends carried a staleness date it has no way to state.
type Scanned struct {
	Backend
	Missing bool // the command was not found on the fleet's machine
}

// presets is the built-in catalogue, in the order a picker shows it. Every name
// here is a promise that selecting it spawns something, so the list grows by
// evidence that a CLI is worth the promise rather than by enthusiasm.
//
// A name maps to the command it is conventionally installed as, and to nothing
// else. Where a backend wants arguments, a working directory, longer patience
// before it reads as stuck, or a container to run in, the user writes a
// panel.agents profile — which overrides the preset of the same name entirely.
// Homepage is the one exception to "nothing else". A name on its own tells
// someone who already knows what opencode is that they have not got it, and tells
// everyone else nothing at all — so the catalogue carries where to get each entry,
// and nothing about how. Install instructions would be an assumption about
// someone else's release process, which is the kind of claim this package refuses
// everywhere else.
//
// Each command below is the bin entry the published package actually installs,
// not the name its documentation uses in prose. The two differ: xAI's announcement
// reads as though the command is "grok-build", which is the product and the repo;
// @xai-official/grok declares bin {"grok": "bin/grok"}, and PATH is the only thing
// Detect asks about. Check the registry, not the README, before editing this list.
//
// The URLs point at a repo or product root rather than a docs page, because that
// is the layer that moves slowest — and it does move: sst/opencode is now
// anomalyco/opencode. Expect to revisit this table.
var presets = []Backend{
	{Name: "claude", Command: "claude", Homepage: "https://github.com/anthropics/claude-code"},
	{Name: "codex", Command: "codex", Homepage: "https://github.com/openai/codex"},
	{Name: "gemini", Command: "gemini", Homepage: "https://github.com/google-gemini/gemini-cli"},
	{Name: "aider", Command: "aider", Homepage: "https://github.com/Aider-AI/aider"},
	{Name: "opencode", Command: "opencode", Homepage: "https://opencode.ai"},
	{Name: "grok", Command: "grok", Homepage: "https://github.com/xai-org/grok-build"},
}

// lookPath decides whether a command exists on this machine. It is a variable so
// a test can answer for a machine it does not have.
var lookPath = exec.LookPath

// Presets returns the built-in catalogue. The copy is defensive: the caller
// merges the user's profiles into what it gets back, and a shared slice would
// let one fleet's config edit the next one's defaults.
func Presets() []Backend {
	out := make([]Backend, len(presets))
	copy(out, presets)
	return out
}

// Merge layers the user's own profiles over the presets: a profile whose name
// matches a preset replaces it in place — command, arguments and all — and one
// with a new name is appended. The presets keep their order because it is the
// order a picker reads in; the added names are sorted so the tail of the list
// does not reshuffle itself between reloads on map iteration order.
func Merge(user []Backend) []Backend {
	out := Presets()
	at := make(map[string]int, len(out))
	for i, b := range out {
		at[b.Name] = i
	}
	extra := make([]Backend, 0, len(user))
	for _, b := range user {
		if b.Name == "" || b.Command == "" {
			continue // a profile with nothing to run is not a backend, it is a typo
		}
		if i, ok := at[b.Name]; ok {
			out[i] = b
			continue
		}
		extra = append(extra, b)
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].Name < extra[j].Name })
	return append(out, extra...)
}

// Detect returns the backends this machine can actually run, in the order given.
//
// Presence on PATH is the whole test. There is no --version probe: it is a fork
// per backend, not every agent CLI has the flag, and one that hangs would hang
// the thing that decides whether you can spawn at all. What a detected backend
// promises is that the binary is there — not that it is authenticated, current,
// or willing.
//
// An isolated profile is kept without asking. Its command runs inside a
// container image, so the host's PATH has no opinion worth having: absent there
// means nothing about the image, and dropping the profile would hide a backend
// that works.
func Detect(cands []Backend) []Backend {
	out := make([]Backend, 0, len(cands))
	for _, s := range Scan(cands) {
		if !s.Missing {
			out = append(out, s.Backend)
		}
	}
	return out
}

// Scan is Detect without the discarding: every candidate comes back, in the order
// given, carrying whether its command is there.
//
// Detect drops the misses because its callers are about to spawn one. Scan keeps
// them because a name baton knows and did not find is the one thing the old
// filtering could not say — and silence there reads as "this machine has one agent
// backend" when the truth is "this machine has one of the six baton knows".
func Scan(cands []Backend) []Scanned {
	out := make([]Scanned, 0, len(cands))
	for _, b := range cands {
		miss := false
		if !b.Isolated {
			_, err := lookPath(b.Command)
			miss = err != nil
		}
		out = append(out, Scanned{Backend: b, Missing: miss})
	}
	return out
}

// Find returns the named backend from a list. It exists so the callers that ask
// "is the default still there" all ask it the same way.
func Find(list []Backend, name string) (Backend, bool) {
	for _, b := range list {
		if b.Name == name {
			return b, true
		}
	}
	return Backend{}, false
}
