package issues

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/cmj0121/baton/internal/paths"
)

// bindPath is the persist file; tests replace it.
var bindPath = paths.IssueBindFile

// UseBindFile points persist at path for the rest of the test. Restore with the
// returned function.
func UseBindFile(path string) func() {
	old := bindPath
	bindPath = func() string { return path }
	return func() { bindPath = old }
}

var bindMu sync.Mutex

// BindKey identifies a card binding by repo and working tree, not panel id
// (panel ids are recycled).
func BindKey(repo Repo, cwd string) string {
	if repo.Owner == "" || cwd == "" {
		return ""
	}
	return repo.String() + "\t" + cwd
}

// LoadBinds reads persisted issue binds. A missing or unreadable file is empty.
func LoadBinds() map[string]int {
	path := bindPath()
	if path == "" {
		return map[string]int{}
	}
	bindMu.Lock()
	defer bindMu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]int{}
	}
	var m map[string]int
	if json.Unmarshal(data, &m) != nil || m == nil {
		return map[string]int{}
	}
	return m
}

// SaveBind records that this repo+cwd is bound to issue n.
func SaveBind(repo Repo, cwd string, n int) {
	key := BindKey(repo, cwd)
	if key == "" || n <= 0 {
		return
	}
	path := bindPath()
	if path == "" {
		return
	}
	bindMu.Lock()
	defer bindMu.Unlock()
	m := map[string]int{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &m)
		if m == nil {
			m = map[string]int{}
		}
	}
	m[key] = n
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	if paths.EnsureDir(path) != nil {
		return
	}
	_ = paths.WriteFileAtomic(path, raw, 0o600)
}

// LookupBind returns the persisted issue for repo+cwd, or zero.
func LookupBind(repo Repo, cwd string, binds map[string]int) int {
	if binds == nil {
		return 0
	}
	return binds[BindKey(repo, cwd)]
}
