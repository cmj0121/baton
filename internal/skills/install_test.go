package skills

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// snapshot is every file under dir as relative path → content, so "nothing was
// written" is checked against the whole tree rather than the names a test
// happens to think of.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		b, err := os.ReadFile(p)
		out[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func writeFile(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestInstallWritesTheSetAndItsManifest: into an empty dir, every name in the
// set lands with the embedded content, the manifest records them, and the
// returned paths are exactly what is on disk.
func TestInstallWritesTheSetAndItsManifest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	paths, err := Install(dir, Outside, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range Names(Outside) {
		want, _ := Read(name)
		p := filepath.Join(dir, name, "SKILL.md")
		got, err := os.ReadFile(p)
		if err != nil || string(got) != string(want) {
			t.Errorf("%s: err %v, content matches %v", name, err, string(got) == string(want))
		}
		if !slices.Contains(paths, p) {
			t.Errorf("returned paths miss %s: %v", p, paths)
		}
	}
	owned, err := manifest(dir)
	if err != nil || !slices.Equal(owned, Names(Outside)) {
		t.Errorf("manifest = %v, %v; want %v", owned, err, Names(Outside))
	}
	if !slices.Contains(paths, filepath.Join(dir, manifestName)) {
		t.Errorf("returned paths miss the manifest: %v", paths)
	}
}

// TestInstallRefusesForeignNamesAndWritesNothing is the all-or-nothing rule.
// conduct is the operator's own and drive sorts after it, so an install that
// checks one name at a time and writes as it goes would have written baton/ by
// the time it met conduct/. Every foreign name is listed, and the tree is
// byte-for-byte what it was.
func TestInstallRefusesForeignNamesAndWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	writeFile(t, filepath.Join(dir, "conduct", "SKILL.md"), "mine")
	writeFile(t, filepath.Join(dir, "drive"), "a plain file is foreign too")
	before := snapshot(t, dir)

	_, err := Install(dir, Outside, false)
	if err == nil {
		t.Fatal("install over the operator's own conduct/ succeeded")
	}
	for _, name := range []string{"conduct", "drive"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
	if strings.Contains(err.Error(), "baton,") {
		t.Errorf("error names baton, which is not foreign: %v", err)
	}
	if after := snapshot(t, dir); !maps.Equal(before, after) {
		t.Errorf("a refused install changed the tree:\nbefore %v\nafter  %v", before, after)
	}
}

// TestInstallForceReplacesAForeignName: --force takes the name over whole — no
// file of the operator's left inside it — and records it, so uninstall removes
// it too.
func TestInstallForceReplacesAForeignName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	writeFile(t, filepath.Join(dir, "conduct", "SKILL.md"), "mine")
	writeFile(t, filepath.Join(dir, "conduct", "extra.sh"), "mine too")

	if _, err := Install(dir, Outside, true); err != nil {
		t.Fatal(err)
	}
	want, _ := Read("conduct")
	if got, _ := os.ReadFile(filepath.Join(dir, "conduct", "SKILL.md")); string(got) != string(want) {
		t.Errorf("conduct/SKILL.md not replaced: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "conduct", "extra.sh")); err == nil {
		t.Error("a forced install left the operator's file inside conduct/")
	}
	if owned, _ := manifest(dir); !slices.Contains(owned, "conduct") {
		t.Errorf("manifest does not own conduct after --force: %v", owned)
	}
}

// TestInstallOverItsOwnInstallIsNotARefusal: a name the manifest lists is
// baton's, so re-running install (after an upgrade, say) needs no --force.
func TestInstallOverItsOwnInstallIsNotARefusal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	if _, err := Install(dir, Outside, false); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "conduct", "SKILL.md"), "stale")
	if _, err := Install(dir, Outside, false); err != nil {
		t.Fatalf("re-install refused its own names: %v", err)
	}
	want, _ := Read("conduct")
	if got, _ := os.ReadFile(filepath.Join(dir, "conduct", "SKILL.md")); string(got) != string(want) {
		t.Errorf("re-install left the stale copy: %q", got)
	}
}

// TestUninstallRemovesOnlyOwnedNames: the manifest's names and the manifest go,
// a neighbour baton did not write stays, and the returned paths are the ones
// removed.
func TestUninstallRemovesOnlyOwnedNames(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	if _, err := Install(dir, Outside, false); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "mine", "SKILL.md"), "operator's")
	// An entry that is not a plain name must not steer RemoveAll out of dir.
	writeFile(t, filepath.Join(filepath.Dir(dir), "outside", "keep"), "x")
	f, err := os.OpenFile(filepath.Join(dir, manifestName), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("../outside\n")
	_ = f.Close()

	removed, err := Uninstall(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range Names(Outside) {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived uninstall", name)
		}
		if !slices.Contains(removed, p) {
			t.Errorf("returned paths miss %s: %v", p, removed)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, manifestName)); err == nil {
		t.Error("the manifest survived uninstall")
	}
	if _, err := os.Stat(filepath.Join(dir, "mine", "SKILL.md")); err != nil {
		t.Errorf("uninstall removed a skill it did not own: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "outside", "keep")); err != nil {
		t.Errorf("a ../ manifest entry steered a removal out of dir: %v", err)
	}
}

// TestUninstallWithNoManifestIsANoOp: nothing of baton's is there, so nothing
// is removed and it is no error — including a same-name dir baton never wrote.
func TestUninstallWithNoManifestIsANoOp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	writeFile(t, filepath.Join(dir, "conduct", "SKILL.md"), "mine")
	removed, err := Uninstall(dir)
	if err != nil || len(removed) != 0 {
		t.Fatalf("Uninstall = %v, %v; want nothing", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "conduct", "SKILL.md")); err != nil {
		t.Errorf("a same-name dir with no manifest was removed: %v", err)
	}
}

// swapForLink installs, then replaces the owned conduct/ with a symlink to a
// directory outside dir holding a file of the operator's, and returns that
// directory. The manifest still lists conduct.
func swapForLink(t *testing.T, dir string) string {
	t.Helper()
	if _, err := Install(dir, Outside, false); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	writeFile(t, filepath.Join(target, "SKILL.md"), "outside")
	if err := os.RemoveAll(filepath.Join(dir, "conduct")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "conduct")); err != nil {
		t.Fatal(err)
	}
	return target
}

// TestInstallRefusesAnOwnedNameSwappedForASymlink: the manifest is not proof of
// what is on disk. Following the link would write SKILL.md wherever it points.
func TestInstallRefusesAnOwnedNameSwappedForASymlink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	target := swapForLink(t, dir)
	before := snapshot(t, target)

	_, err := Install(dir, Outside, false)
	if err == nil || !strings.Contains(err.Error(), "conduct") {
		t.Fatalf("install through a symlinked conduct/: err %v; want a refusal naming conduct", err)
	}
	if after := snapshot(t, target); !maps.Equal(before, after) {
		t.Errorf("the link's target changed:\nbefore %v\nafter  %v", before, after)
	}
}

// TestInstallForceRemovesOnlyTheLink: --force takes the link away, not what it
// pointed at, and writes a real conduct/ in its place.
func TestInstallForceRemovesOnlyTheLink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	target := swapForLink(t, dir)
	before := snapshot(t, target)

	if _, err := Install(dir, Outside, true); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, target); !maps.Equal(before, after) {
		t.Errorf("--force touched the link's target:\nbefore %v\nafter  %v", before, after)
	}
	fi, err := os.Lstat(filepath.Join(dir, "conduct"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("conduct/ after --force: %v, %v; want a real directory", fi, err)
	}
}

// TestInstallDoesNotFollowAPlantedTempLink: WriteFileAtomic opens SKILL.md.tmp
// without O_EXCL, so a link under that name inside an owned skill must be
// cleared before the write rather than written through.
func TestInstallDoesNotFollowAPlantedTempLink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	if _, err := Install(dir, Outside, false); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	writeFile(t, victim, "untouched")
	if err := os.Symlink(victim, filepath.Join(dir, "conduct", "SKILL.md.tmp")); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(dir, Outside, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "untouched" {
		t.Errorf("install wrote through a planted .tmp link: %q", b)
	}
}
