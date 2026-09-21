package issues

import (
	"path/filepath"
	"testing"
)

func TestBindPersistsByRepoAndCwd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issue-bind.json")
	old := bindPath
	bindPath = func() string { return path }
	t.Cleanup(func() { bindPath = old })

	repo := Repo{Owner: "cmj0121", Name: "baton"}
	SaveBind(repo, "/tmp/wt", 128)
	got := LoadBinds()
	if LookupBind(repo, "/tmp/wt", got) != 128 {
		t.Fatalf("got %v", got)
	}
	if LookupBind(repo, "/other", got) != 0 {
		t.Fatal("cwd must be part of the key")
	}
}
