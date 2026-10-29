// Package skills is the guidance baton hands an agent about baton itself, as
// Agent Skills: a SKILL.md per topic that the agent's runtime lists by its one-
// line description and loads only when the agent decides it needs it.
//
// It exists because a tool description says what a tool does and nothing about
// when to reach for it, what the etiquette is, or what a refusal means — and the
// conductor's primer, the one other carrier, is read by the conductor alone. A
// worker given baton_tell with no word about when to type into a group-mate's
// terminal either never uses it or uses it badly. A skill is that word, costing
// a session one line of its context until it is wanted.
//
// The skills are embedded rather than read from disk so the text an agent reads
// is always the text that matches the tools this binary serves; the test beside
// this file holds every tool name a skill mentions to the MCP table its reader
// is actually handed.
//
// Two ways out, one per role. A conductor runs in a workspace baton owns, so its
// skills are written into that workspace's .claude/skills (WriteSkills). A
// worker runs in the operator's repository, where a dotfile baton wrote would
// show in git status, so its skills are built into a plugin directory under
// baton's own home and handed to the CLI by flag (Plugin).
package skills

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/cmj0121/baton/internal/paths"
)

//go:embed assets
var assets embed.FS

// Set is which role's skills: the one an ordinary agent panel is handed, or the
// conductor's.
type Set int

// The two roles. Worker is every ordinary agent panel; Conductor is the one
// panel baton marks as driving the fleet.
const (
	Worker Set = iota
	Conductor
)

// sets is each role's skills. The entry skill, baton, is in both and names no
// tool because no tool is in both tables; each role's own skill names that
// role's tools. A worker gets no conduct (it is refused the fleet verbs) and the
// conductor gets no peers or score (it is refused the peer verbs, and its own
// memory duties are a section of conduct).
var sets = map[Set][]string{
	Worker:    {"baton", "peers", "score"},
	Conductor: {"baton", "conduct"},
}

// manifestName is the file WriteSkills records its own names in. It is how a
// later write tells a skill baton put there from one the operator did.
const manifestName = ".baton-skills"

// pluginName is the plugin's name in its manifest. It is not "baton", so the
// directory baton hands a panel never collides with a baton plugin the operator
// installed themselves.
const pluginName = "baton-panel"

const pluginDescription = "Guidance for an agent running inside baton, the terminal multiplexer for AI coding agents: what baton is and when to use its tools."

// Names is the skills in set, in a stable order.
func Names(set Set) []string {
	return slices.Clone(sets[set])
}

// Read is one skill's SKILL.md, as embedded.
func Read(name string) ([]byte, error) {
	return assets.ReadFile("assets/" + name + "/SKILL.md")
}

// WriteSkills writes dir/<name>/SKILL.md for every skill in set, each file
// replaced atomically so an agent reading mid-write sees the old skill or the
// new one, never half of either.
//
// dir is shared with whoever else keeps skills there — the conductor's
// .claude/skills is somewhere an operator may well drop their own. So the names
// written are recorded in dir/.baton-skills, and on the next write only a name
// listed there and no longer in the set is retired. A directory baton did not
// write is never touched, whatever it is called. A manifest entry that is not a
// plain name is skipped rather than followed: the file is on disk, and the one
// thing worse than a stale skill is a RemoveAll steered out of dir.
func WriteSkills(dir string, set Set) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	names := Names(set)
	for _, name := range names {
		data, err := Read(name)
		if err != nil {
			return err
		}
		sub := filepath.Join(dir, name)
		if err := os.MkdirAll(sub, 0o700); err != nil {
			return err
		}
		if err := paths.WriteFileAtomic(filepath.Join(sub, "SKILL.md"), data, 0o600); err != nil {
			return err
		}
	}

	// Retire after writing, and record the new manifest last: a failure anywhere
	// above leaves the old manifest naming everything baton may own, so the next
	// write still knows to clean it.
	prev, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, old := range strings.Fields(string(prev)) {
		if slices.Contains(names, old) || old != filepath.Base(old) || old == "." || old == ".." {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, old)); err != nil {
			return err
		}
	}
	return paths.WriteFileAtomic(filepath.Join(dir, manifestName), []byte(strings.Join(names, "\n")+"\n"), 0o600)
}

// Plugin builds set's skills as a Claude Code plugin under base and returns its
// directory: base/<hash>/.claude-plugin/plugin.json beside
// base/<hash>/skills/<name>/SKILL.md.
//
// The directory is named for a hash of everything in it, so it is never
// rewritten in place. A panel launched with --plugin-dir pointing at it keeps
// reading the tree it was launched against while a newer baton builds the next
// one beside it, and every call for the same content lands on the same path.
// That makes reuse a single check: a tree under the right hash holding its
// plugin.json is complete, because plugin.json is in it only once it was
// renamed in whole. One without it can only be a hand edit, and is an error
// rather than something to repair — see install.
//
// The build happens in a temporary sibling that is renamed into place, which is
// atomic for a directory too, so concurrent launches race only on the rename:
// one wins and every loser finds a complete tree there and discards its own.
// Old hashes are left where they are; a running panel may still be reading one.
func Plugin(base string, set Set) (string, error) {
	files, err := pluginFiles(set)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, hashFiles(files))
	if complete(dir) {
		return dir, nil
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(base, ".build-")
	if err != nil {
		return "", err
	}
	if err := writeTree(tmp, files); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	if err := install(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		return "", err
	}
	return dir, nil
}

// pluginFiles is the plugin's tree as relative path → content.
func pluginFiles(set Set) (map[string][]byte, error) {
	manifest, err := json.MarshalIndent(map[string]string{"name": pluginName, "description": pluginDescription}, "", "  ")
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{filepath.Join(".claude-plugin", "plugin.json"): append(manifest, '\n')}
	for _, name := range Names(set) {
		data, err := Read(name)
		if err != nil {
			return nil, err
		}
		files[filepath.Join("skills", name, "SKILL.md")] = data
	}
	return files, nil
}

// hashFiles is the first 12 hex digits of a sha256 over every path and its
// content in path order, each NUL-terminated so no two trees can concatenate to
// the same stream.
func hashFiles(files map[string][]byte) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write(files[k])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func writeTree(root string, files map[string][]byte) error {
	for rel, data := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// complete reports whether dir holds a plugin manifest — see Plugin for why
// that one file stands for the whole tree.
func complete(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	return err == nil && fi.Mode().IsRegular()
}

// install renames the built tree tmp to dir. A rename that fails because dir
// is already there and complete means another call won the race, so tmp is
// discarded and the winner's tree is the answer.
//
// A dir that is there and NOT complete is refused rather than repaired. Plugin
// only ever puts a tree at the hash path by renaming a whole one in, so a
// partial one there was made by hand, and replacing it would mean deleting a
// path another call may already have returned to a launching panel. The caller
// launches without skills and says so; deleting the directory fixes it.
func install(tmp, dir string) error {
	err := os.Rename(tmp, dir)
	if err == nil {
		return nil
	}
	if complete(dir) {
		return os.RemoveAll(tmp)
	}
	return fmt.Errorf("install plugin %s: %w (an incomplete tree is there; remove it)", dir, err)
}
