package tui

import (
	"sort"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/proto"
)

// The panel-config page's score-feedback rows: one per configured agent profile,
// saying whether that profile's briefs carry the line telling the agent it may
// record what it learned.
//
// They are on THIS page and not on a key of their own, because the switch is per
// PROFILE and a key pressed while a panel is selected would lie about that: it
// would read as "stop telling this agent" and would in fact move every panel of
// that profile, including ones not yet spawned. A page that lists profiles by
// name makes the scope the thing you are looking at.
//
// They are also the reason this page's length is no longer a constant. Every
// other row is a fixed field of the config; these are one per name in the user's
// file, so the count comes from feedbackProfiles and the rows sit after
// numPanelConfigRows rather than inside the enum.

// feedbackProfiles is the configured agent profiles in display order — sorted by
// name, so the rows do not reshuffle between frames.
//
// Map iteration order is the reason this exists rather than a range in the view.
// The cursor indexes rows, so an order that changed between the frame that drew
// the page and the keystroke that edited it would toggle a profile the user was
// not looking at.
func (m model) feedbackProfiles() []string {
	if len(m.agents) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.agents))
	for name := range m.agents {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// feedbackSection renders the rows, using the page's own row helper so the caret,
// the padding and the scroll anchor stay in one place. It returns the section
// header and the lines the helper did not already append.
//
// A fleet with no configured profiles gets a line saying so rather than an empty
// section: this page is where someone comes to find out what the fleet could do,
// and a heading with nothing under it reads as a feature that is broken rather
// than as a file with no profiles in it.
func (m model) feedbackSection(row func(idx int, label, value string)) []string {
	names := m.feedbackProfiles()
	// No heading of its own: these rows are a TAB of the panel-config page now, and
	// the tab bar above them already says what they are.
	//
	// The FLEET's own switch leads, and it is the reason this tab is never empty.
	// It used to list overrides and nothing else, so a fleet with no configured
	// profiles — which is every fresh install — opened the tab on a sentence
	// explaining that there was nothing here, with the one switch that does apply
	// to it nowhere on the page at all. The house rule is a setting like any
	// other; the profiles below it are the exceptions to it.
	row(panelRowFleetFeedback, m.tr("panel.cfg.fleet-feedback", "fleet default"), m.feedbackOnOff(m.scoreFeedback))
	for i, name := range names {
		row(panelRowFleetFeedback+1+i, name, m.feedbackLabel(m.agents[name].ScoreFeedback, m.scoreFeedback))
	}
	if len(names) == 0 {
		return []string{"", mutedStyle.Render("  " + m.tr("panel.cfg.no-profiles",
			"no agent profiles configured · panel.agents in your config names them"))}
	}
	return nil
}

// toggleFleetFeedback flips score.feedback — the answer every profile that has
// not overridden it inherits — and persists it.
func (m model) toggleFleetFeedback() model {
	m.scoreFeedback = !m.scoreFeedback
	m.feedbackChosen = true // now it is a choice, and worth persisting
	if err := m.saveConfig(); err != nil {
		m.status = m.tr("status.save-failed", "save failed: ") + err.Error()
		return m
	}
	m.sendf(proto.Command{Action: "server.reload"})
	m.status = m.tr("feedback.score", "score feedback") + " · " +
		m.tr("panel.cfg.fleet-feedback", "fleet default") + " · " + m.feedbackOnOff(m.scoreFeedback)
	return m
}

// feedbackLabel is how one profile's answer reads. Three states, not two: a
// profile that has not answered is INHERITING, and flattening that into the value
// it currently resolves to would make the fleet-wide key look like it had been
// copied onto every profile — after which switching the fleet over would appear
// to do nothing.
func (m model) feedbackLabel(own *bool, fleet bool) string {
	if own == nil {
		return m.tr("feedback.inherit", "inherit") + " · " + m.feedbackOnOff(fleet)
	}
	return m.feedbackOnOff(*own)
}

// feedbackOnOff is lower case and unpadded, which is why it is not the onOff
// beside it: that one renders a fixed-width "ON "/"OFF" for a column in another
// view, and its trailing space would show up mid-sentence here ("inherit · ON ").
// Every other value on this page is lower case prose.
func (m model) feedbackOnOff(b bool) string {
	if b {
		return m.tr("value.on", "on")
	}
	return m.tr("value.off", "off")
}

// cycleFeedback advances one profile's switch: inherit → on → off → inherit, and
// persists it.
//
// A cycle rather than a toggle, because the value has three states and the third
// is not reachable any other way from here. A two-way toggle would let a profile
// out of "inherit" and never back into it, so the first press on any profile
// would silently pin it to whatever the fleet said at that moment — the exact
// staleness the tri-state exists to avoid.
//
// The save is followed by a reload, which is what makes this a switch rather than
// a note to self. Every other row on this page edits a value the daemon reads at
// spawn, so the file is enough; this one is resolved per delivery from policy the
// daemon is holding, and an edit it was never told about would leave the page
// showing one answer and the fleet using another.
func (m model) cycleFeedback(i int) model {
	names := m.feedbackProfiles()
	if i < 0 || i >= len(names) {
		return m
	}
	name := names[i]
	prof := m.agents[name]
	prof.ScoreFeedback = nextFeedback(prof.ScoreFeedback)

	// Copy before writing. m.agents is shared with the prefs the cockpit loaded
	// and with any model value copied from this one, and a map is a reference:
	// mutating it in place would change a profile on models that never ran this.
	next := make(map[string]config.AgentProfile, len(m.agents))
	for k, v := range m.agents {
		next[k] = v
	}
	next[name] = prof
	m.agents = next

	if err := m.saveConfig(); err != nil {
		m.status = m.tr("status.save-failed", "save failed: ") + err.Error()
		return m
	}
	m.sendf(proto.Command{Action: "server.reload"})
	m.status = m.tr("feedback.score", "score feedback") + " · " + name + " · " + m.feedbackLabel(prof.ScoreFeedback, m.scoreFeedback)
	return m
}

// nextFeedback is the cycle itself: unset → true → false → unset.
func nextFeedback(cur *bool) *bool {
	switch {
	case cur == nil:
		on := true
		return &on
	case *cur:
		off := false
		return &off
	default:
		return nil
	}
}

// feedbackHintLine is the page footer's one line about these rows. It says what
// the switch does NOT do, because that is the half a reader will otherwise assume
// from a row that says "off": submission stays open to every panel, and what this
// takes away is the telling.
func (m model) feedbackHintLine() string {
	return mutedStyle.Render(m.tr("panel.cfg.hint.feedback", "score feedback · off stops the telling, not the submitting"))
}

// toggleAgentMCP flips panel.agent-mcp — whether an agent panel is launched
// pointing at baton's own MCP config, so the agent can see the memory's
// score_submit tool without waiting to be dispatched to — and persists it.
//
// The daemon reads it at SPAWN, so this changes what the next agent panel starts
// with and leaves every running one alone: an agent's tool list is fixed when its
// process starts, and there is nothing to migrate into one already up. The status
// says so, because a switch that appears to do nothing is worse than one that
// says when it will.
func (m model) toggleAgentMCP() model {
	m.agentMCP = !m.agentMCP
	m.agentMCPSet = true // now it is a choice, and worth persisting
	if err := m.saveConfig(); err != nil {
		m.status = m.tr("status.save-failed", "save failed: ") + err.Error()
		return m
	}
	m.sendf(proto.Command{Action: "server.reload"})
	m.status = m.tr("panel.cfg.agent-mcp", "agent mcp") + " · " + m.feedbackOnOff(m.agentMCP) +
		" · " + m.tr("panel.cfg.agent-mcp.when", "applies to the next agent panel")
	return m
}
