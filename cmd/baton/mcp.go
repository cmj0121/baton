package main

import (
	"fmt"
	"os"

	"github.com/cmj0121/baton/internal/mcp"
)

// mcpMain runs `baton mcp`: a Model Context Protocol server on stdin/stdout that
// exposes baton's verbs as MCP tools, so an agent drives baton through native
// tool calls. It returns a process exit code.
//
// worker selects the table an ordinary agent panel loads — the fleet memory's
// write tool and the peer tools (mcp.NewWorker) — instead of the conductor's
// fleet-control table. The flag that sets it is still spelled --score-only: that
// is what the config baton writes for worker panels passes
// (writeAgentMCPConfig), and panels already running hold a copy of it.
func mcpMain(worker bool) int {
	srv := mcp.New(version)
	if worker {
		srv = mcp.NewWorker(version)
	}
	if err := srv.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "baton mcp:", err)
		return 1
	}
	return 0
}
