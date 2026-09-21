package server

import (
	"encoding/json"

	"github.com/cmj0121/baton/internal/issues"
	"github.com/cmj0121/baton/internal/proto"
)

// issues.board / issues.block run git+gh on the daemon host, as the operator's
// uid, against the panel's cwd. The cockpit only draws; a remote attach still
// sees the repo the fleet is sitting in.

func (s *Server) issuesBoard(cc *clientConn, dir string) {
	// Fetch off the command goroutine: LoadBoard talks to GitHub and used to
	// stall the connection for the whole wait. The overlay sat on "fetching"
	// with an empty board until the reply — and `gh pr view` with no PR ate
	// the 15s tool timeout, which is what fediqo's shell panel looked like.
	go s.replyIssuesBoard(cc, dir)
}

func (s *Server) replyIssuesBoard(cc *clientConn, dir string) {
	if dir == "" {
		failIssues(cc, "issues.board needs a working directory")
		return
	}
	b, err := issues.LoadBoard(issues.ExecRunner{}, dir)
	if err != nil {
		failIssues(cc, err.Error())
		return
	}
	raw, err := json.Marshal(b)
	if err != nil {
		failIssues(cc, err.Error())
		return
	}
	send(cc, proto.ServerMsg{Type: "issues", Issues: raw})
}

func (s *Server) issuesBlock(cc *clientConn, dir string, issue, blocker int) {
	go s.blockIssue(cc, dir, issue, blocker)
}

// blockIssue records the blocker, then replies with the refreshed board. It
// needs only the repo for the write, which git alone can tell it.
func (s *Server) blockIssue(cc *clientConn, dir string, issue, blocker int) {
	if dir == "" || issue <= 0 || blocker <= 0 {
		failIssues(cc, "issues.block needs dir, issue, and blocker")
		return
	}
	_, repo, err := issues.ResolveRepo(issues.ExecRunner{}, dir)
	if err != nil {
		failIssues(cc, err.Error())
		return
	}
	if err := issues.BlockedBy(issues.ExecRunner{}, dir, repo, issue, blocker); err != nil {
		failIssues(cc, err.Error())
		return
	}
	s.replyIssuesBoard(cc, dir)
}

func failIssues(cc *clientConn, msg string) {
	send(cc, proto.ServerMsg{Type: "issues", Failed: true, Error: msg})
}
