package tui

import (
	"fmt"
	"sort"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmj0121/baton/internal/agents"
	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/proto"
)

// The agent-backend picker: the agent CLIs the machine the FLEET runs on actually
// has, listed when there is a choice to make. It is reached two ways and gets no
// key of its own — spawning an agent (the new-agent action) opens it on the way
// to the workdir prompt, and the panel-config page opens it to set the fleet's
// default. A fleet with one backend never sees it at all, so the common case
// keeps costing exactly the keystrokes it did before.
//
// Only backends that were detected are listed. An entry you cannot spawn is not
// information, it is a trap, and greying one out just moves the trap.

// agentPurpose is what the picker is being opened for: the two callers commit
// the choice differently, and the picker itself is the same list either way.
type agentPurpose int

const (
	agentForSpawn   agentPurpose = iota // the new-agent action: choose, then ask for the workdir
	agentForDefault                     // the panel-config page: choose the fleet-wide default
)

// availableAgents is the list the picker shows: what the daemon detected on the
// machine the panels are spawned from.
//
// The fallback matters more than it looks. The daemon outlives the binary that
// started it — a fleet keeps running across an upgrade — so a new cockpit can
// attach to a daemon that has never heard of detection and will send nothing.
// Rather than claim the machine has no agents, the cockpit then offers what the
// config names, unverified: it cannot say what exists without a scan, and the
// user's own profiles are a better answer than an empty list.
func (m model) availableAgents() []proto.AgentBackend {
	if len(m.backends) > 0 {
		return m.backends
	}
	names := make([]string, 0, len(m.agents)+1)
	for name := range m.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]proto.AgentBackend, 0, len(names)+1)
	for _, name := range names {
		out = append(out, proto.AgentBackend{Name: name, Command: m.agents[name].Command, Args: m.agents[name].Args})
	}
	if _, ok := m.agents[agents.Default]; !ok {
		out = append(out, proto.AgentBackend{Name: agents.Default, Command: agents.Default})
	}
	return out
}

// openAgentPicker opens the picker, remembering from so esc returns there and
// starting the cursor on the backend that would be used anyway — so enter alone
// is always the "yes, the usual one" answer.
func (m model) openAgentPicker(from mode, purpose agentPurpose) model {
	list := m.availableAgents()
	if len(list) == 0 {
		m.status = "no agent backend found on the fleet's machine · install one, then " + keyLabel(m.effPrefix()) + " R"
		return m
	}
	m.agentList = list
	m.agentFrom = from
	m.agentPurpose = purpose
	m.agentCursor = 0
	for i, b := range list {
		if b.Name == m.effDefaultAgent() {
			m.agentCursor = i
			break
		}
	}
	m.mode = modeAgentPick
	if purpose == agentForDefault {
		m.status = "default agent · enter sets it · esc cancels"
	} else {
		m.status = "pick an agent · enter chooses it · esc cancels"
	}
	return m
}

// handleAgentKey drives the picker: ↑↓ (or j/k) move, enter commits, esc backs
// out. Any other key is ignored, so a stray press never spawns anything.
func (m model) handleAgentKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = m.agentFrom
		m.status = "cancelled"
		return m, nil
	case "up", "k":
		m.agentCursor = wrapIndex(m.agentCursor, -1, len(m.agentList))
		return m, nil
	case "down", "j":
		m.agentCursor = wrapIndex(m.agentCursor, 1, len(m.agentList))
		return m, nil
	case "enter":
		if m.agentCursor < 0 || m.agentCursor >= len(m.agentList) {
			return m, nil
		}
		return m.chooseAgent(m.agentList[m.agentCursor].Name)
	}
	return m, nil
}

// chooseAgent commits the highlighted backend: the panel-config page writes it to
// the config as the fleet default, and the new-agent action carries it into the
// workdir prompt as this one spawn's backend.
func (m model) chooseAgent(name string) (tea.Model, tea.Cmd) {
	m.mode = m.agentFrom
	switch m.agentPurpose {
	case agentForDefault:
		m.defaultAgent = name
		if err := m.saveConfig(); err != nil {
			m.status = "save failed: " + err.Error()
			return m, nil
		}
		m.status = "default agent · " + name
		return m, nil
	default:
		m.pendingAgent = name
		m.input = inputAgentDir
		m.inputBuf = m.defaultWorkdir()
		m.status = "new " + name + " agent · type the workdir, enter to spawn"
		return m, nil
	}
}

// agentPickerView renders the picker as a centred popup: one row per backend with
// the command behind it, the fleet default marked so the list says what enter
// would have done.
func (m model) agentPickerView() string {
	nameStyle := lipgloss.NewStyle().Foreground(colCyan).Bold(true).Width(16)
	caret := func(on bool) string {
		if on {
			return lipgloss.NewStyle().Foreground(colBrand).Bold(true).Render("▸ ")
		}
		return "  "
	}

	title := "PICK AGENT"
	if m.agentPurpose == agentForDefault {
		title = "DEFAULT AGENT"
	}
	rows := []string{sectionStyle.Render(spaced(title)), ""}
	for i, b := range m.agentList {
		tail := b.Command
		if b.Name == m.effDefaultAgent() {
			tail += "  · default"
		}
		rows = append(rows, caret(m.agentCursor == i)+nameStyle.Render(b.Name)+mutedStyle.Render(tail))
	}

	rows = append(rows, "", mutedStyle.Render("found on the machine the fleet runs on · "+keyLabel(m.effPrefix())+" R re-detects"), "",
		legend("↑↓", "move", "enter", "choose", "esc", "cancel"))
	return m.popupBox(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

// effDefaultAgent is the backend a new agent panel uses when nothing is picked:
// the configured default, or the built-in when the config names none.
func (m model) effDefaultAgent() string {
	if m.defaultAgent != "" {
		return m.defaultAgent
	}
	return agents.Default
}

// resolveAgent picks the profile a new agent panel runs: the one just chosen in
// the picker if there is one, else the fleet default.
func (m model) resolveAgent() (config.AgentProfile, string, bool) {
	if m.pendingAgent != "" {
		return m.resolveAgentNamed(m.pendingAgent)
	}
	return m.resolveAgentNamed(m.effDefaultAgent())
}

// resolveAgentNamed resolves one backend name to the profile that spawns it, in
// the layering the daemon detects with: the user's own profile wins, then what
// was detected on the fleet's machine, and finally — only when nothing was
// detected at all — the built-in catalogue, so a cockpit attached to a daemon
// that does not detect still spawns the way it always did.
//
// ok is false when the name resolves to nothing that can be spawned; the caller
// says why with agentUnavailable.
func (m model) resolveAgentNamed(name string) (config.AgentProfile, string, bool) {
	if prof, ok := m.agents[name]; ok {
		return prof, name, true
	}
	for _, b := range m.backends {
		if b.Name == name {
			return config.AgentProfile{Command: b.Command, Args: b.Args}, name, true
		}
	}
	if len(m.backends) == 0 {
		if b, ok := agents.Find(agents.Presets(), name); ok {
			return config.AgentProfile{Command: b.Command, Args: b.Args}, name, true
		}
	}
	return config.AgentProfile{}, name, false
}

// agentUnavailable is what to say when a backend cannot be spawned. The two
// reasons read nothing alike and used to share one message: a name baton has
// never heard of is a config error, while a name it knows and did not find is a
// machine that is missing the binary — which a re-detect can fix, and which the
// old wording ("no agent profile configured") blamed the config for.
func (m model) agentUnavailable(name string) string {
	if _, known := agents.Find(agents.Presets(), name); known || len(m.backends) > 0 {
		return name + " was not found on the fleet's machine · install it, then " + keyLabel(m.effPrefix()) + " R re-detects"
	}
	return fmt.Sprintf("no agent profile %q configured", name)
}

// defaultAgentLabel is the panel-config row's value: the fleet default, and
// whether it is actually there. A default naming a backend the machine does not
// have is the failure this whole page exists to make visible — it used to surface
// only as a spawn that did nothing.
func (m model) defaultAgentLabel() string {
	name := m.effDefaultAgent()
	if _, _, ok := m.resolveAgentNamed(name); !ok {
		return name + "  (not found)"
	}
	return name
}
