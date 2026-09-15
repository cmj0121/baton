package server

import (
	"regexp"
	"strings"
	"testing"

	"github.com/cmj0121/baton/internal/mcp"
)

// conductorprimer_test.go holds the conductor's briefing to the truth, the way
// TestThePrimersRateClaimMatchesTheCap already holds one sentence of it.
//
// That test states the principle exactly: "the briefing is the one text a model
// reads as instruction, so a sentence in it is a claim about the daemon". It
// was applied to the rate cap and to nothing else — and the briefing's most
// load-bearing claim is its TOOL LIST, because that is what the agent uses to
// decide what it is able to do. Five tools were added to the MCP server while
// the hardcoded string sat unchanged, and the result was a conductor that could
// not hand a panel a task because it had never been told it could (#99).

// toolWord matches a tool name as the briefing writes one, so the reverse check
// reads the prose rather than a second list that could go stale the way the
// first one did.
var toolWord = regexp.MustCompile(`\b(?:baton|score)_[a-z_]+`)

// TestThePrimerNamesEveryTool is the drift guard, and it runs in both
// directions on purpose.
//
// A tool the primer omits is a capability the conductor has and does not use. A
// tool the primer advertises that the server does not register is worse: the
// agent calls it, gets an error it cannot act on, and has no way to tell a
// typo from a fence.
func TestThePrimerNamesEveryTool(t *testing.T) {
	primer := string(conductorPrimer("c1"))

	var missing []string
	for _, name := range mcp.ToolNames() {
		if !strings.Contains(primer, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("the conductor has %d tool(s) its briefing never names, so it will not use them:\n    %s",
			len(missing), strings.Join(missing, "\n    "))
	}

	// The other direction: every `baton_…` / `score_…` the primer promises must
	// exist. Scanned out of the prose rather than listed here, so this cannot go
	// stale the way the primer did.
	registered := map[string]bool{}
	for _, name := range mcp.ToolNames() {
		registered[name] = true
	}
	for _, word := range toolWord.FindAllString(primer, -1) {
		if !registered[word] {
			t.Errorf("the briefing promises %q, which no tool registers — the agent will call it and get an error it cannot act on", word)
		}
	}
}

// TestThePrimerTellsSendFromDispatch is the half a name list cannot carry. The
// two verbs are not alternatives: send is keystrokes, dispatch is a task the
// server records, delivers as a unit and watches for completion. Listing both
// and explaining neither is how a reader concludes they are the same thing with
// different spellings — which is what the conductor concluded.
func TestThePrimerTellsSendFromDispatch(t *testing.T) {
	primer := string(conductorPrimer("c1"))
	for _, want := range []string{"baton_send", "baton_dispatch"} {
		if !strings.Contains(primer, want) {
			t.Fatalf("the briefing does not name %s", want)
		}
	}
	if !strings.Contains(primer, "keystroke") {
		t.Error("the briefing never says send is keystrokes, so dispatch reads as a synonym for it")
	}
	if !strings.Contains(primer, "objective") && !strings.Contains(primer, "task the server") {
		t.Error("the briefing never says what dispatch records that send does not")
	}
}

// TestThePrimerKeepsAttentionAboutItself: attention and resolve are the
// conductor asking for a human about ITS OWN panel, and guardConductor
// restricts them to that — a conductor free to raise hands across the fleet can
// freeze the backlog one looping call at a time. A briefing that presented them
// as fleet control would be inviting the refusal.
func TestThePrimerKeepsAttentionAboutItself(t *testing.T) {
	primer := string(conductorPrimer("c1"))
	at := strings.Index(primer, "baton_attention")
	if at < 0 {
		t.Fatal("the briefing does not name baton_attention")
	}
	// Case-folded: the claim is that the briefing SAYS it, not how it shouts it.
	window := strings.ToLower(primer[max(0, at-400):min(len(primer), at+400)])
	if !strings.Contains(window, "your own") && !strings.Contains(window, "yourself") {
		t.Errorf("baton_attention is described without saying it is about the conductor's OWN panel:\n%s", window)
	}
}
