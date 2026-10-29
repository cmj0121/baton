package skills

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/cmj0121/baton/internal/mcp"
)

// toolToken is the shape of every tool name either MCP table registers. It is
// deliberately wider than the tables — any baton_/score_ word at all — so a
// skill that names a tool which does not exist, or exists only in the other
// role's table, is caught rather than read as prose.
var toolToken = regexp.MustCompile(`\b(baton|score)_[a-z_]+\b`)

// registry is the table the agent reading a set's skills is actually handed.
func registry(set Set) []string {
	if set == Conductor {
		return mcp.ToolNames()
	}
	return mcp.WorkerToolNames()
}

func mustRead(t *testing.T, name string) string {
	t.Helper()
	b, err := Read(name)
	if err != nil {
		t.Fatalf("Read(%q): %v", name, err)
	}
	return string(b)
}

// TestSkillsNameOnlyTheirTablesTools is the drift test: a skill is an
// instruction, and an instruction to call a tool the reader was not given is a
// turn spent on a refusal at best. A skill shipped in both sets must hold for
// both tables, which — since no tool is in both — means it names none.
func TestSkillsNameOnlyTheirTablesTools(t *testing.T) {
	for _, set := range []Set{Worker, Conductor} {
		reg := registry(set)
		for _, name := range Names(set) {
			for _, tok := range toolToken.FindAllString(mustRead(t, name), -1) {
				if !slices.Contains(reg, tok) {
					t.Errorf("skill %q (set %d) names %q, which that set's MCP server does not register (%v)", name, set, tok, reg)
				}
			}
		}
	}
}

// TestBatonSkillNamesNoTool: the entry skill ships to both roles, so it points
// at the next skill by NAME and leaves the tools to that skill.
func TestBatonSkillNamesNoTool(t *testing.T) {
	if toks := toolToken.FindAllString(mustRead(t, "baton"), -1); len(toks) > 0 {
		t.Errorf("the baton skill must name no tool, names %v", toks)
	}
}

// TestConductNamesEveryConductorTool: the conductor's skill is checked the way
// its primer is (internal/server/conductorprimer_test.go) — a tool added to the
// table without a word here is a capability the conductor has and is never told
// when to use.
func TestConductNamesEveryConductorTool(t *testing.T) {
	body := mustRead(t, "conduct")
	for _, name := range mcp.ToolNames() {
		if !regexp.MustCompile(`\b` + name + `\b`).MatchString(body) {
			t.Errorf("conduct skill does not name %s", name)
		}
	}
}

// TestSetsAreTheRolesSkills pins the two sets (D2), and that every skill in the
// embedded tree belongs to at least one — an orphan would be text no agent is
// ever handed.
func TestSetsAreTheRolesSkills(t *testing.T) {
	if got, want := Names(Worker), []string{"baton", "peers", "score"}; !slices.Equal(got, want) {
		t.Errorf("Names(Worker) = %v, want %v", got, want)
	}
	if got, want := Names(Conductor), []string{"baton", "conduct"}; !slices.Equal(got, want) {
		t.Errorf("Names(Conductor) = %v, want %v", got, want)
	}
	entries, err := fs.ReadDir(assets, "assets")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !slices.Contains(Names(Worker), e.Name()) && !slices.Contains(Names(Conductor), e.Name()) {
			t.Errorf("assets/%s is in no set", e.Name())
		}
	}
	if _, err := Read("nope"); err == nil {
		t.Error("Read of an unknown skill should fail")
	}
}

// TestFrontmatter holds every skill to the Agent Skills shape: a frontmatter of
// exactly name and description, the name equal to its directory, a description
// short enough to sit in every session's skill index, and a body short enough
// to be read whole when it is loaded.
func TestFrontmatter(t *testing.T) {
	entries, err := fs.ReadDir(assets, "assets")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw := mustRead(t, e.Name())
		rest, ok := strings.CutPrefix(raw, "---\n")
		if !ok {
			t.Errorf("%s: no opening frontmatter fence", e.Name())
			continue
		}
		front, body, ok := strings.Cut(rest, "\n---\n")
		if !ok {
			t.Errorf("%s: no closing frontmatter fence", e.Name())
			continue
		}
		var fm map[string]any
		if err := yaml.Unmarshal([]byte(front), &fm); err != nil {
			t.Errorf("%s: frontmatter is not YAML: %v", e.Name(), err)
			continue
		}
		keys := make([]string, 0, len(fm))
		for k := range fm {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"description", "name"}) {
			t.Errorf("%s: frontmatter keys = %v, want exactly name and description", e.Name(), keys)
		}
		if fm["name"] != e.Name() {
			t.Errorf("%s: name = %v, want the directory name", e.Name(), fm["name"])
		}
		desc, _ := fm["description"].(string)
		if desc == "" || utf8.RuneCountInString(desc) > 160 {
			t.Errorf("%s: description must be 1..160 chars, is %d: %q", e.Name(), utf8.RuneCountInString(desc), desc)
		}
		if n := strings.Count(strings.TrimRight(body, "\n"), "\n") + 1; n > 100 {
			t.Errorf("%s: body is %d lines, budget is 100", e.Name(), n)
		}
	}
}

func readManifest(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return strings.Fields(string(b))
}

// TestWriteSkillsOwnsOnlyWhatItWrote: the conductor's .claude/skills is a
// directory an operator may put their own skills in. WriteSkills replaces and
// retires only the names its own manifest lists, and a manifest entry that is
// not a plain name is never followed out of the directory.
func TestWriteSkillsOwnsOnlyWhatItWrote(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skills")
	mine := filepath.Join(dir, "mine", "SKILL.md")
	outside := filepath.Join(root, "outside", "SKILL.md")
	for _, p := range []string{mine, outside} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("operator's own"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A manifest from an earlier baton that wrote "retired", plus an entry that
	// would escape the directory if it were trusted as a path.
	if err := os.MkdirAll(filepath.Join(dir, "retired"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "retired", "SKILL.md"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte("retired\n../outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteSkills(dir, Conductor); err != nil {
		t.Fatalf("WriteSkills: %v", err)
	}
	for _, name := range Names(Conductor) {
		p := filepath.Join(dir, name, "SKILL.md")
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s not written: %v", name, err)
		}
		if want := mustRead(t, name); string(got) != want {
			t.Errorf("%s: content differs from the embedded skill", name)
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v, want 0600", name, fi.Mode().Perm())
		}
		if fi, _ := os.Stat(filepath.Dir(p)); fi.Mode().Perm() != 0o700 {
			t.Errorf("%s dir: mode %v, want 0700", name, fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "retired")); !os.IsNotExist(err) {
		t.Errorf("a previously-owned name no longer in the set should be removed, stat err = %v", err)
	}
	for _, p := range []string{mine, outside} {
		if b, err := os.ReadFile(p); err != nil || string(b) != "operator's own" {
			t.Errorf("%s was touched: %q, %v", p, b, err)
		}
	}
	if got := readManifest(t, dir); !slices.Equal(got, Names(Conductor)) {
		t.Errorf("manifest = %v, want %v", got, Names(Conductor))
	}

	// Switching sets retires what this call's predecessor wrote and nothing else.
	if err := WriteSkills(dir, Worker); err != nil {
		t.Fatalf("WriteSkills(Worker): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "conduct")); !os.IsNotExist(err) {
		t.Errorf("conduct was baton's and left the set; it should be gone, stat err = %v", err)
	}
	for _, name := range Names(Worker) {
		if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err != nil {
			t.Errorf("%s not written: %v", name, err)
		}
	}
	if b, err := os.ReadFile(mine); err != nil || string(b) != "operator's own" {
		t.Errorf("the operator's skill was touched on the second write: %q, %v", b, err)
	}
	if got := readManifest(t, dir); !slices.Equal(got, Names(Worker)) {
		t.Errorf("manifest = %v, want %v", got, Names(Worker))
	}
}

// assertPlugin checks dir is a complete plugin for set: the manifest Claude
// Code loads a --plugin-dir by, and every skill at the path it looks in.
func assertPlugin(t *testing.T, dir string, set Set) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("plugin.json: %v", err)
	}
	var manifest map[string]string
	if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatalf("plugin.json is not JSON: %v", err)
	}
	if manifest["name"] != "baton-panel" || manifest["description"] == "" {
		t.Errorf("plugin.json = %v, want name baton-panel and a description", manifest)
	}
	for _, name := range Names(set) {
		got, err := os.ReadFile(filepath.Join(dir, "skills", name, "SKILL.md"))
		if err != nil {
			t.Errorf("skill %s missing: %v", name, err)
			continue
		}
		if !bytes.Equal(got, []byte(mustRead(t, name))) {
			t.Errorf("skill %s differs from the embedded one", name)
		}
	}
}

// assertOnlyHashDirs: no build directory is left behind beside the result.
func assertOnlyHashDirs(t *testing.T, base string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, filepath.Join(base, e.Name()))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("base holds %v, want only %v", got, want)
	}
}

// TestPluginIsContentAddressedAndIdempotent: the same set gives the same
// directory every time, a different set a different one, and a second call
// reuses the first's tree instead of building another.
func TestPluginIsContentAddressedAndIdempotent(t *testing.T) {
	base := t.TempDir()
	p1, err := Plugin(base, Worker)
	if err != nil {
		t.Fatalf("Plugin: %v", err)
	}
	if filepath.Dir(p1) != base || len(filepath.Base(p1)) != 12 {
		t.Errorf("Plugin = %s, want base/<12-hex hash>", p1)
	}
	assertPlugin(t, p1, Worker)
	if err := os.WriteFile(filepath.Join(p1, "marker"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p2, err := Plugin(base, Worker)
	if err != nil {
		t.Fatalf("Plugin again: %v", err)
	}
	if p2 != p1 {
		t.Errorf("second call = %s, want %s", p2, p1)
	}
	if _, err := os.Stat(filepath.Join(p1, "marker")); err != nil {
		t.Error("a complete tree should be reused, not rebuilt")
	}
	pc, err := Plugin(base, Conductor)
	if err != nil {
		t.Fatalf("Plugin(Conductor): %v", err)
	}
	if pc == p1 {
		t.Error("two sets hashed to the same directory")
	}
	assertPlugin(t, pc, Conductor)
	assertOnlyHashDirs(t, base, p1, pc)
}

// TestPluginRefusesAnIncompleteTree: a directory under the right hash that
// lacks plugin.json can only be a hand edit, since Plugin installs whole trees
// by rename. Handing it out would launch claude with --plugin-dir pointing at
// nothing, and replacing it could delete a path another call already returned,
// so it is an error — and the tree and base are left exactly as found.
func TestPluginRefusesAnIncompleteTree(t *testing.T) {
	base := t.TempDir()
	p, err := Plugin(base, Worker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(p, ".claude-plugin")); err != nil {
		t.Fatal(err)
	}
	if got, err := Plugin(base, Worker); err == nil {
		t.Fatalf("Plugin over an incomplete tree = %s, want an error", got)
	}
	if complete(p) {
		t.Error("the incomplete tree should not have been repaired")
	}
	if _, err := os.Stat(filepath.Join(p, "skills", "peers", "SKILL.md")); err != nil {
		t.Errorf("the incomplete tree should be left as found: %v", err)
	}
	assertOnlyHashDirs(t, base, p)
}

// TestPluginConcurrent: panels launch concurrently, and every one of them must
// get a directory that is complete when Plugin returns — the loser of the race
// discards its own build rather than the winner's.
func TestPluginConcurrent(t *testing.T) {
	base := t.TempDir()
	const n = 8
	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		dirs  = make([]string, n)
		errs  = make([]error, n)
	)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			dirs[i], errs[i] = Plugin(base, Worker)
		}()
	}
	close(start)
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if dirs[i] != dirs[0] {
			t.Errorf("call %d = %s, want %s", i, dirs[i], dirs[0])
		}
	}
	assertPlugin(t, dirs[0], Worker)
	assertOnlyHashDirs(t, base, dirs[0])
}
