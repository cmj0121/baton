package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// TestMCPSpawnCommand is the conductor's half of #54. `run` is an extra field
// on baton_spawn rather than a sibling tool, for the reason the worktree fields
// already are: a conductor that knows how to spawn should not have to discover a
// second verb to run a build.
//
// The two refusals are the point of the field. `agent` and `run` together is
// refused rather than resolved, because either resolution produces a working panel
// with the wrong standing — a plain binary enrolled in the scheduler, or an agent
// where a command was asked for — and setting one field of an either/or pair is
// exactly the slip a model makes. `worktree` with `run` has no meaning at all:
// a worktree spawn is an agent in the tree it builds.
func TestMCPSpawnCommand(t *testing.T) {
	sock := startServer(t)
	dir := t.TempDir()

	resps := run(t, sock,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"baton_spawn","arguments":{"run":"/bin/sh","args":["-c","sleep 30"],"dir":%q}}}`, dir),
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"baton_spawn","arguments":{"agent":"/bin/sh","run":"/bin/sh"}}}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"baton_spawn","arguments":{"run":"/bin/sh","worktree":true,"branch":"feat/x","dir":%q}}}`, dir),
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"baton_list","arguments":{}}}`,
	)
	if len(resps) != 4 {
		t.Fatalf("want 4 responses, got %d", len(resps))
	}

	if resps[0].Result["isError"] == true {
		t.Fatalf("a command spawn should succeed, got %q", contentText(t, resps[0].Result))
	}
	if txt := contentText(t, resps[0].Result); !strings.HasPrefix(txt, "spawned panel ") {
		t.Fatalf("a command spawn should name the new panel, got %q", txt)
	}

	if resps[1].Result["isError"] != true {
		t.Fatalf("agent with run should be a tool error, got %+v", resps[1].Result)
	}
	if txt := contentText(t, resps[1].Result); !strings.Contains(txt, "set one") {
		t.Fatalf("want the either/or refusal, got %q", txt)
	}

	if resps[2].Result["isError"] != true {
		t.Fatalf("run with worktree should be a tool error, got %+v", resps[2].Result)
	}

	// The listing is what proves the first call produced the right KIND rather
	// than merely a panel: "agent" here would be #54 reaching the conductor.
	if txt := contentText(t, resps[3].Result); !strings.Contains(txt, `"kind": "command"`) {
		t.Fatalf("the fleet should hold one command panel, got %s", txt)
	}
}
