package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	vt "github.com/charmbracelet/x/vt"
	"github.com/muesli/termenv"

	"github.com/cmj0121/baton/internal/config"
	"github.com/cmj0121/baton/internal/panel"
	"github.com/cmj0121/baton/internal/proto"
)

// The frames are byte-for-byte snapshots of what the cockpit draws — escapes
// included — for a spread of states wide enough that a layout, a column or a
// badge cannot go missing without one of them changing. Unit tests assert on
// substrings and so cannot see a rendering regression; these can.
//
// Regenerate with BATON_UPDATE_FRAMES=1 go test ./internal/tui -run GoldenFrames
// and read the diff before committing it. A frame that changes without a reason
// you can name in the commit message is a regression, not an update.

// trueColor pins lipgloss's global colour profile for the duration of a frame
// test. Under `go test` stdout is not a terminal, so the default profile is
// Ascii and every colour lipgloss would emit is dropped — which would leave the
// frames blind to exactly the styling regressions they exist to catch.
func trueColor(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	// The palette is package state a cockpit's prefs overwrite, so an earlier
	// case that applied a custom theme would otherwise repaint these frames.
	applyTheme(config.Theme{})
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// frameState is one named cockpit state and the model it renders from.
type frameState struct {
	name  string
	build func(t *testing.T) model
}

// frameModel is baseModel at a fixed size and clock, with the appVersion pinned
// so the header line does not move with the build.
func frameModel() model {
	m := baseModel()
	m.appVersion = "0.0.0-frames"
	m.now = time.Unix(1700000000, 0).UTC()
	return m
}

// filledEmu is an emulator carrying a few lines of believable program output, so
// a zoom frame has something to lose if the drawing changes.
func filledEmu(w, h int) *vt.SafeEmulator {
	emu := vt.NewSafeEmulator(w, h)
	_, _ = emu.Write([]byte("$ make test\r\nok  \tbaton/internal/tui\t0.42s\r\n\x1b[32mPASS\x1b[0m\r\n$ "))
	return emu
}

func frameStates() []frameState {
	return []frameState{
		{"dash-grid-small", func(*testing.T) model {
			m := frameModel()
			m.fleet = sampleFleet()[:3]
			return m
		}},
		{"dash-tree-large", func(*testing.T) model {
			m := frameModel()
			m.fleet = sampleFleet()
			return m
		}},
		{"dash-tree-nested", func(*testing.T) model {
			m := frameModel()
			m.fleet = nestedFleet()
			return m
		}},
		{"dash-grouped", func(*testing.T) model {
			m := frameModel()
			m.fleet = groupedFleet()
			return m
		}},
		{"dash-narrow", func(*testing.T) model {
			m := frameModel()
			m.fleet = sampleFleet()
			m.width = 60
			return m
		}},
		{"dash-prefix-armed", func(*testing.T) model {
			m := frameModel()
			m.fleet = sampleFleet()[:3]
			m.prefix = true
			return m
		}},
		{"dash-error-status", func(*testing.T) model {
			m := frameModel()
			m.fleet = sampleFleet()[:3]
			m.status = "error: boom"
			return m
		}},
		{"too-small", func(*testing.T) model {
			m := frameModel()
			m.width, m.height = 20, 8
			return m
		}},
		{"zoom", func(*testing.T) model {
			m := frameModel()
			m.fleet = sampleFleet()
			m.mode, m.zoomID, m.zoomTitle = modeZoom, "s1", "shell · make build"
			m.emu = filledEmu(m.width, 20)
			return m
		}},
		{"zoom-scrolling", func(*testing.T) model {
			m := frameModel()
			m.fleet = sampleFleet()
			m.mode, m.zoomID, m.zoomTitle = modeZoom, "s1", "shell · make build"
			m.emu = filledEmu(m.width, 20)
			m.scrolling = true
			return m
		}},
		{"group-split", func(t *testing.T) model {
			m := frameModel()
			m.fleet = groupedFleet()
			m = m.zoomGroup(m.dashItems()[0])
			m.groupEmus = map[string]*vt.SafeEmulator{}
			for _, id := range []string{"1", "3", "6"} {
				emu := filledEmu(20, 5)
				m.groupEmus[id] = emu
				t.Cleanup(func() { closeZoom(emu) })
			}
			return m
		}},
		{"group-split-summary", func(*testing.T) model {
			m := frameModel()
			m.fleet = bigGroup("big", maxGroupTiles+4)
			m = m.zoomGroup(m.dashItems()[0])
			m.groupFocus = maxGroupTiles
			return m
		}},
		{"help-dashboard", func(*testing.T) model {
			m := frameModel()
			m.mode, m.helpFrom = modeHelp, modeDashboard
			m.fleet = sampleFleet()[:3]
			return m
		}},
		{"help-zoom", func(*testing.T) model {
			m := frameModel()
			m.mode, m.helpFrom = modeHelp, modeZoom
			return m
		}},
		{"keymap", func(*testing.T) model {
			m := frameModel()
			m.mode = modeKeyMap
			m.fleet = sampleFleet()[:3]
			return m
		}},
		{"panel-config", func(*testing.T) model {
			m := frameModel()
			m.mode, m.shellPath = modePanelConfig, "/bin/zsh"
			return m
		}},
		{"input-new-panel", func(*testing.T) model {
			m := frameModel()
			m.fleet = sampleFleet()[:3]
			m.input, m.inputBuf = inputNewPanelCmd, "/bin/sh"
			return m
		}},
		{"agent-picker", func(*testing.T) model {
			m := frameModel()
			m.mode = modeAgentPick
			m.agentList = []proto.AgentBackend{
				{Name: "claude", Command: "claude"},
				{Name: "aider", Command: "aider"},
			}
			m.agentCursor = 1
			return m
		}},
		{"signal-picker", func(*testing.T) model {
			m := frameModel()
			m.mode = modeSignal
			m.signalTargets = []string{"1"}
			m.signalScope = "api (3 panels)"
			return m
		}},
		{"command-picker", func(*testing.T) model {
			m := frameModel()
			m.mode = modeCommand
			m.fleet = sampleFleet()[:3]
			return m
		}},
		{"usage-overlay", func(*testing.T) model {
			m := frameModel()
			m.mode, m.usageFrom = modeUsage, modeDashboard
			m.usageMode = usageWindow
			m.usageText = "1.2M tok · ≈$12.34 API"
			m.fleet = []panel.Panel{
				{ID: "p1", Kind: panel.Agent, Title: "claude · api"},
				{ID: "p2", Kind: panel.Agent, Title: "claude · web"},
			}
			since := m.now.Add(-time.Hour)
			m.usageInfo = &proto.UsageInfo{
				Tokens: 1_200_000, CostUSD: 12.34, Source: "local", Resets: true,
				Since:   since.Format(time.RFC3339),
				Until:   since.Add(5 * time.Hour).Format(time.RFC3339),
				WarnAt:  0.75,
				AlarmAt: 0.9,
				Panels:  map[string]proto.PanelUsage{"p1": {Tokens: 300_000, CostUSD: 3}},
			}
			return m
		}},
		{"inbox", func(t *testing.T) model {
			failed := wire("4", "exited", time.Minute)
			failed.ExitCode = 3
			m := openedInbox(t,
				wire("1", "attention", 90*time.Second),
				wire("2", "stuck", 3*time.Hour),
				wire("3", "done", 20*time.Second),
				failed,
			)
			m.appVersion = "0.0.0-frames"
			return m
		}},
		{"inbox-composing", func(t *testing.T) model {
			m := openedInbox(t, wire("1", "attention", 90*time.Second))
			m.appVersion = "0.0.0-frames"
			m.inboxComposing, m.inboxReply = true, "yes"
			return m
		}},
		{"dir-picker", func(t *testing.T) model {
			m := frameModel()
			m.mode = modeDirPick
			m.dirPickDir = "/tmp/frames"
			m.dirPickRows = []dirRow{
				{label: "BROWSE", header: true},
				{path: "/tmp/frames", label: "/tmp/frames", caption: true},
				{path: "/tmp", label: "..", up: true},
				{path: "/tmp/frames/src", label: "src"},
			}
			m.dirPickCursor = 3
			return m
		}},
		{"quitting-detach", func(*testing.T) model {
			m := frameModel()
			m.quitting = true
			return m
		}},
	}
}

// TestGoldenFrames renders each state and compares it byte-for-byte with the
// committed frame.
func TestGoldenFrames(t *testing.T) {
	trueColor(t)
	update := os.Getenv("BATON_UPDATE_FRAMES") != ""
	dir := filepath.Join("testdata", "frames")
	if update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, st := range frameStates() {
		t.Run(st.name, func(t *testing.T) {
			got := st.build(t).View().Content
			// .bin, not .txt: a frame's trailing spaces and escapes ARE the
			// content, and the whitespace hooks are excluded from that suffix.
			path := filepath.Join(dir, st.name+".bin")
			if update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing golden frame (regenerate with BATON_UPDATE_FRAMES=1): %v", err)
			}
			if got != string(want) {
				t.Errorf("frame changed; got:\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

// TestGoldenFramesAreDeterministic proves the frames are a usable oracle: the
// same state rendered twice is the same bytes, so a diff after the migration
// means the drawing changed and not that the snapshot was noise.
func TestGoldenFramesAreDeterministic(t *testing.T) {
	trueColor(t)
	for _, st := range frameStates() {
		t.Run(st.name, func(t *testing.T) {
			if a, b := st.build(t).View().Content, st.build(t).View().Content; a != b {
				t.Errorf("frame is not reproducible:\n%s\n--- vs ---\n%s", a, b)
			}
		})
	}
}
