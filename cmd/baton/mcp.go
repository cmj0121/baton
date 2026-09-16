package main

import (
	"fmt"
	"os"

	"github.com/cmj0121/baton/internal/mcp"
)

// mcpMain runs `baton mcp`: a Model Context Protocol server on stdin/stdout that
// exposes the fleet-control verbs as MCP tools. It is what a conductor agent's
// .mcp.json launches, so the agent drives baton through native tool calls. It
// returns a process exit code.
//
// scoreOnly narrows the table to the fleet memory's write tool, which is what an
// ordinary agent panel loads — see mcp.NewScore and the config baton writes for
// worker panels (Server.agentMCPConfig).
func mcpMain(scoreOnly bool) int {
	srv := mcp.New(version)
	if scoreOnly {
		srv = mcp.NewScore(version)
	}
	if err := srv.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "baton mcp:", err)
		return 1
	}
	return 0
}
