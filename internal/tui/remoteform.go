package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The connection form (`baton --remote`): a cockpit that shows nothing but two
// fields — the address, then the passkey — and hands them back so the caller can
// dial. It is its own tea.Model rather than a mode of the cockpit because it runs
// BEFORE there is a client to build a cockpit around, and because ssh may need
// the terminal for a passphrase or a host-key question the moment it is done.
//
// A failure comes back INTO the form rather than to a stack trace: the message
// sits under the fields and the address is kept, so a mistyped passkey is one
// keystroke from being retyped.

// RemoteTarget is what the form collected.
type RemoteTarget struct {
	Address string
	Passkey string
}

// remoteFormModel is the two-field form. focus is the field the cursor is in.
type remoteFormModel struct {
	address string
	passkey string
	focus   int // 0 = address, 1 = passkey
	problem string
	width   int
	height  int

	// done is set when the form was submitted; cancelled when esc quit it. A
	// model that is neither is still running.
	done      bool
	cancelled bool
}

// NewRemoteForm builds the connection form, seeded with an address to retry and
// the failure that sent the person back here (both empty on the first run).
func NewRemoteForm(address, problem string) tea.Model {
	m := remoteFormModel{address: address, problem: problem, width: 80, height: 24}
	if address != "" {
		m.focus = 1 // the address already worked as text; the passkey is what is being retyped
	}
	return m
}

// RemoteResult reads the outcome off the form's final model: what was typed, and
// whether it was submitted rather than cancelled.
func RemoteResult(final tea.Model) (RemoteTarget, bool) {
	m, ok := final.(remoteFormModel)
	if !ok || !m.done || m.cancelled {
		return RemoteTarget{}, false
	}
	return RemoteTarget{Address: strings.TrimSpace(m.address), Passkey: strings.TrimSpace(m.passkey)}, true
}

func (m remoteFormModel) Init() tea.Cmd { return nil }

func (m remoteFormModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// key drives the form. Enter advances from the address to the passkey and
// submits from the passkey; tab and the arrows move either way; esc quits.
func (m remoteFormModel) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.cancelled, m.done = true, true
		return m, tea.Quit
	case "tab", "down":
		m.focus, m.problem = 1-m.focus, ""
	case "shift+tab", "up":
		m.focus, m.problem = 1-m.focus, ""
	case "enter":
		if m.focus == 0 && strings.TrimSpace(m.address) != "" {
			m.focus, m.problem = 1, ""
			return m, nil
		}
		return m.submit()
	case "backspace":
		m.problem = ""
		if m.focus == 0 {
			m.address = dropLastRune(m.address)
		} else {
			m.passkey = dropLastRune(m.passkey)
		}
	case "ctrl+u":
		m.problem = ""
		if m.focus == 0 {
			m.address = ""
		} else {
			m.passkey = ""
		}
	default:
		if r := msg.Runes; len(r) > 0 {
			m.problem = ""
			if m.focus == 0 {
				m.address += string(r)
			} else {
				m.passkey += string(r)
			}
		}
	}
	return m, nil
}

// submit accepts the form once both fields hold something, and otherwise says
// which one is still empty rather than dialling a half-filled target.
func (m remoteFormModel) submit() (tea.Model, tea.Cmd) {
	switch {
	case strings.TrimSpace(m.address) == "":
		m.focus, m.problem = 0, "an address is needed — host, user@host, or host:port"
		return m, nil
	case strings.TrimSpace(m.passkey) == "":
		m.focus, m.problem = 1, "the fleet's passkey is needed — read it from its C-t r overlay"
		return m, nil
	}
	m.done = true
	return m, tea.Quit
}

// dropLastRune removes the last rune, so a backspace over a multi-byte character
// deletes the character rather than a fragment of it.
func dropLastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(r[:len(r)-1])
}

func (m remoteFormModel) View() string {
	width := clampInt(m.width-16, 32, 60)

	rows := []string{
		bannerStyle.Render(spaced("BATON REMOTE")),
		"",
		m.field("ADDRESS", m.address, "host · user@host · host:port", 0, width),
		"",
		m.field("PASSKEY", m.passkey, "the 8 characters the fleet's C-t r shows", 1, width),
	}
	if m.problem != "" {
		rows = append(rows, "", lipgloss.NewStyle().Foreground(colFailed).Render(sanitizeText(m.problem)))
	}
	rows = append(rows, "",
		mutedStyle.Render("the port defaults to 22 · ssh carries the connection"),
		"", legend("enter", "attach", "tab", "field", "esc", "quit"))

	body := lipgloss.JoinVertical(lipgloss.Left, rows...)
	box := popupBoxAt(body, width)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// field renders one labelled input: the label, the typed text with a cursor when
// focused, and a hint under it while the field is still empty.
func (m remoteFormModel) field(label, value, hint string, idx, width int) string {
	head := sectionStyle.Render(label)
	text := sanitizeText(value)
	if m.focus == idx {
		head = lipgloss.NewStyle().Bold(true).Foreground(colBrandHi).Render("▸ " + label)
		text += "▌"
	} else {
		head = "  " + head
	}
	line := lipgloss.NewStyle().Foreground(colInk).Render(clipVisible(text, width-4))
	if strings.TrimSpace(value) == "" && m.focus != idx {
		line = mutedStyle.Render(clipVisible(hint, width-4))
	}
	return lipgloss.JoinVertical(lipgloss.Left, head, "  "+line)
}
