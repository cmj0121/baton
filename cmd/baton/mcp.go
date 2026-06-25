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
func mcpMain() int {
	if err := mcp.New(version).Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "baton mcp:", err)
		return 1
	}
	return 0
}
