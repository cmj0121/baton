package proctree

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/proto"
)

// fleet + OS table for the canonical hybrid case: two panels in a group (one with
// an OS child), one ungrouped panel (with an OS child), under the daemon.
func sampleTree() *Node {
	panels := []proto.Panel{
		{ID: "1", Title: "hale", State: "running", Group: "feature-x", Pid: 41180},
		{ID: "2", Title: "ellis", State: "idle", Group: "feature-x", Pid: 41205},
		{ID: "3", Title: "shell", State: "running", Pid: 41240},
	}
	children := map[int][]int{
		41022: {41180, 41205, 41240},
		41180: {41199},
		41240: {41250},
	}
	comm := map[int]string{
		41022: "baton", 41180: "claude", 41199: "node",
		41205: "bash", 41240: "zsh", 41250: "vim",
	}
	return Build(41022, panels, children, comm)
}

func TestRenderGolden(t *testing.T) {
	want := strings.Join([]string{
		"baton (daemon) pid=41022  baton",
		"├─ [group: feature-x]",
		"│  ├─ [hale/running] pid=41180  claude",
		"│  │  └─ pid=41199  node",
		"│  └─ [ellis/idle] pid=41205  bash",
		"└─ [ungrouped]",
		"   └─ [shell/running] pid=41240  zsh",
		"      └─ pid=41250  vim",
		"",
	}, "\n")

	if got := Render(sampleTree()); got != want {
		t.Fatalf("rendered tree mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// A panel that has exited (pid 0) still appears, with no pid and no descendants —
// nothing in the fleet silently vanishes from the tree.
func TestExitedPanelHasNoDescendants(t *testing.T) {
	panels := []proto.Panel{{ID: "1", Title: "gone", State: "exited", Pid: 0}}
	root := Build(41022, panels, map[int][]int{41022: {999}}, map[int]string{999: "leftover"})

	got := Render(root)

	if !strings.Contains(got, "[gone/exited]") {
		t.Fatalf("exited panel missing from tree:\n%s", got)
	}
	if strings.Contains(got, "leftover") {
		t.Fatalf("an exited panel must not adopt OS processes:\n%s", got)
	}
}

// Nested slash-delimited groups render as nested scaffold nodes, deepest last.
func TestNestedGroupsScaffold(t *testing.T) {
	panels := []proto.Panel{
		{ID: "1", Title: "api", State: "running", Group: "backend/api", Pid: 100},
		{ID: "2", Title: "db", State: "idle", Group: "backend", Pid: 200},
	}
	root := Build(1, panels, map[int][]int{}, map[int]string{})

	got := Render(root)

	for _, seg := range []string{"[group: backend]", "[group: api]", "[api/running] pid=100", "[db/idle] pid=200"} {
		if !strings.Contains(got, seg) {
			t.Fatalf("missing %q in:\n%s", seg, got)
		}
	}
	if idxAPI, idxPanel := strings.Index(got, "[group: api]"), strings.Index(got, "[api/running]"); idxAPI > idxPanel {
		t.Fatalf("nested group must precede its panel:\n%s", got)
	}
}

func TestJSONShape(t *testing.T) {
	out, err := json.Marshal(sampleTree())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var root Node
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if root.Kind != "daemon" || root.Pid != 41022 {
		t.Fatalf("root not the daemon: %+v", root)
	}
	grp := root.Children[0]
	if grp.Kind != "group" {
		t.Fatalf("first child is not a group: %+v", grp)
	}
	hale := grp.Children[0]
	if hale.Kind != "panel" || hale.Panel == nil || hale.Panel.Name != "hale" || hale.Panel.Group != "feature-x" {
		t.Fatalf("panel identity not carried in JSON: %+v", hale)
	}
	if len(hale.Children) != 1 || hale.Children[0].Pid != 41199 {
		t.Fatalf("panel OS descendant missing in JSON: %+v", hale.Children)
	}
}
