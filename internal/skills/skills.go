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
// is actually handed. They come from two trees: the public ones (baton,
// conduct, drive) from agentplugin.FS, the same files the marketplace serves to
// agents outside baton, and the worker-only ones (peers, score) from assets
// here, which nothing outside baton has a use for. A name lives in exactly one
// of the two, so Read never has to choose.
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
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/cmj0121/baton/agentplugin"
	"github.com/cmj0121/baton/internal/paths"
)

//go:embed assets
var assets embed.FS

// Set is which role's skills: the one an ordinary agent panel is handed, the
// conductor's, or an outside agent's.
type Set int

// The three roles. Worker is every ordinary agent panel; Conductor is the one
// panel baton marks as driving the fleet; Outside is an agent baton did not
// launch, driving it on the operator's behalf through `baton ctl` or `baton
// mcp` — the full, unfenced conductor table.
const (
	Worker Set = iota
	Conductor
	Outside
)

// sets is each role's skills. The entry skill, baton, is in every set and names
// no tool because no tool is in both the worker's table and the full one the
// conductor and an outside agent are served; each role's own skill names that
// role's tools. A worker gets no conduct (it is refused the fleet verbs) and the
// conductor gets no peers or score (it is refused the peer verbs, and its own
// memory duties are a section of conduct). An outside agent gets the
// conductor's skills plus drive, which is how it reaches baton at all.
var sets = map[Set][]string{
	Worker:    {"baton", "peers", "score"},
	Conductor: {"baton", "conduct"},
	Outside:   {"baton", "conduct", "drive"},
}

// manifestName is the file WriteSkills records its own names in. It is how a
// later write tells a skill baton put there from one the operator did.
const manifestName = ".baton-skills"

// manifestCap is the most bytes manifest reads. A real one is a few skill
// names; anything past this is not one baton wrote.
const manifestCap = 4 << 10

// pluginName is the plugin's name in its manifest. It is not "baton", so the
// directory baton hands a panel never collides with a baton plugin the operator
// installed themselves.
const pluginName = "baton-panel"

const pluginDescription = "Guidance for an agent running inside baton, the terminal multiplexer for AI coding agents: what baton is and when to use its tools."

// Names is the skills in set, in a stable order.
func Names(set Set) []string {
	return slices.Clone(sets[set])
}

// Read is one skill's SKILL.md, as embedded: the public tree first, then the
// worker-only one. A miss in the second is the error returned, so an unknown
// name still fails as fs.ErrNotExist.
func Read(name string) ([]byte, error) {
	if data, err := agentplugin.FS.ReadFile("skills/" + name + "/SKILL.md"); err == nil {
		return data, nil
	}
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
	prev, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var retire []string
	for _, old := range strings.Fields(string(prev)) {
		if old == filepath.Base(old) && old != "." && old != ".." {
			retire = append(retire, old)
		}
	}
	return writeSet(dir, set, retire)
}

// writeSet writes set into dir, then removes every name in prev that set does
// not hold, then records set as dir's manifest. Each caller decides which
// listed names it trusts enough to pass as prev.
func writeSet(dir string, set Set, prev []string) error {
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
	for _, old := range prev {
		if slices.Contains(names, old) {
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

// Install writes set into dir the way WriteSkills does, but refuses to clobber
// what it does not own, and returns every path it wrote.
//
// It is a separate function rather than an option on WriteSkills because the
// two dirs differ in who else is there. The conductor's workspace is baton's,
// and a same-name skill in it is one baton wrote or a stale copy it should
// replace, so WriteSkills keeps overwriting and the conductor path is untouched.
// `baton skill install` writes into the operator's repository or their home,
// where a skill called conduct may be the operator's own, and overwriting it
// without a word destroys work. So every target name is checked BEFORE anything
// is written: a name that exists and is not in dir's manifest is foreign, and
// any foreign name fails the whole install with all of them listed, leaving dir
// exactly as it was. Half an install would be the worst of both — some of the
// operator's skills replaced and baton's set incomplete.
//
// Foreign also covers a name the manifest lists that is no longer a real
// directory: a symlink there would carry WriteSkills' MkdirAll and write out of
// dir to wherever it points, and the manifest is a file on disk, not proof of
// what sits beside it. Every name is Lstat'ed, owned or not.
//
// dir itself, and the path above it, may be symlinks: Install writes wherever
// dir resolves. Whether a symlinked dir is acceptable depends on who chose it,
// so that is the caller's check — `baton skill` confines a --project dir to the
// current directory and lets a --user one follow the operator's dotfiles. The
// guard here is on the levels baton creates and owns — dir/<name> and the files
// in it.
//
// Ownership comes from manifest, which vouches only for the names in set: a
// .baton-skills listing anything else — committed to a cloned repository, say
// — neither claims that name nor gets it retired. That includes baton's own
// worker-only skills, which an install of set never writes.
//
// force replaces foreign names outright: removed (a symlink as the link, never
// its target), then written fresh, so no file of the operator's lingers inside
// a directory baton now claims, and the manifest takes ownership so uninstall
// removes it too.
func Install(dir string, set Set, force bool) ([]string, error) {
	owned, _, err := manifest(dir, set)
	if err != nil {
		return nil, err
	}
	var foreign []string
	for _, name := range Names(set) {
		// Accepted residual risk: a process running as the same user can swap
		// dir/<name> for a link between this Lstat and the write. Closing that
		// race needs openat-style writes in the shared paths.WriteFileAtomic, and
		// such a process can already write anywhere this user can.
		fi, err := os.Lstat(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() || !slices.Contains(owned, name) {
			foreign = append(foreign, name)
		}
	}
	if len(foreign) > 0 && !force {
		return nil, fmt.Errorf("%s already holds skills baton did not install, or that are not plain directories: %s (re-run with --force to replace them)", dir, strings.Join(foreign, ", "))
	}
	for _, name := range foreign {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return nil, err
		}
	}
	// WriteFileAtomic opens the target's .tmp sibling without O_EXCL, so a link
	// planted under that name would be followed. Remove is Lstat-based and
	// takes the link, never its target.
	for _, tmp := range append(tmpNames(dir, set), filepath.Join(dir, manifestName+".tmp")) {
		if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	if err := writeSet(dir, set, owned); err != nil {
		return nil, err
	}
	var written []string
	for _, name := range Names(set) {
		written = append(written, filepath.Join(dir, name, "SKILL.md"))
	}
	return append(written, filepath.Join(dir, manifestName)), nil
}

// tmpNames is the temp path WriteFileAtomic uses for each SKILL.md in set.
func tmpNames(dir string, set Set) []string {
	var out []string
	for _, name := range Names(set) {
		out = append(out, filepath.Join(dir, name, "SKILL.md.tmp"))
	}
	return out
}

// Uninstall removes from dir every skill of set its manifest lists, then the
// manifest, and returns every path it removed. A directory the manifest does
// not name is left alone whatever it is called, and so is one it names that is
// not in set. A dir with no manifest — or with one that is not a regular file,
// which baton never writes — has nothing of baton's in it, so that is no error.
func Uninstall(dir string, set Set) ([]string, error) {
	m := filepath.Join(dir, manifestName)
	owned, present, err := manifest(dir, set)
	if err != nil || !present {
		return nil, err
	}
	var removed []string
	for _, name := range owned {
		p := filepath.Join(dir, name)
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			return removed, err
		}
		removed = append(removed, p)
	}
	if err := os.Remove(m); err != nil {
		return removed, err
	}
	return append(removed, m), nil
}

// manifest is the names of set that dir's .baton-skills records, and whether
// a manifest is there at all. Any other entry is dropped: the file may come
// from a cloned repository, and a name it lists is not proof baton wrote that
// directory. A missing manifest is an empty one, and so is one that is not a
// regular file — a symlink could point at any file, a FIFO would block the
// read. The file is opened once, without following a link and without
// blocking, and its type is checked on the open descriptor, so nothing can
// swap it between the check and the read. One over manifestCap is an error
// rather than a truncated read, which could cut a name in half.
func manifest(dir string, set Set) ([]string, bool, error) {
	p := filepath.Join(dir, manifestName)
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ELOOP) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, false, nil
	}
	b, err := io.ReadAll(io.LimitReader(f, manifestCap+1))
	if err != nil {
		return nil, false, err
	}
	if len(b) > manifestCap {
		return nil, false, fmt.Errorf("%s is over %d bytes; baton did not write it", p, manifestCap)
	}
	var names []string
	for _, n := range strings.Fields(string(b)) {
		if slices.Contains(sets[set], n) {
			names = append(names, n)
		}
	}
	return names, true, nil
}
