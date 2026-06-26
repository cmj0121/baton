// Package config is baton's persistent client configuration: a small YAML file
// at $HOME/.baton/config. Today it stores the user's key-binding overrides; it
// is the place future per-user settings land.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/cmj0121/baton/internal/paths"
)

// Config is the on-disk client configuration.
type Config struct {
	// Prefix is the leader key pressed before a binding, e.g. "ctrl+t".
	Prefix string `yaml:"prefix,omitempty"`

	// Keys maps a binding's stable name to the key pressed after the prefix,
	// e.g. {"new-panel": "p", "close": "w"}.
	Keys map[string]string `yaml:"keys,omitempty"`

	// Settings holds the cockpit toggles.
	Settings Settings `yaml:"settings,omitempty"`

	// Panel holds the default behaviour for new panels.
	Panel PanelDefaults `yaml:"panel,omitempty"`

	// Queue holds the task-backlog caps.
	Queue QueueDefaults `yaml:"queue,omitempty"`

	// TUI holds the cockpit appearance — the colour theme and the group-split
	// layouts. Its canonical source is a separate file ($HOME/.baton/TUI.yaml,
	// see LoadTUI); it lives here so it rides the same config broadcast to every
	// frontend.
	TUI TUIConfig `yaml:"tui,omitempty"`
}

// TUIConfig is the cockpit appearance: the colour theme and the named group-split
// layouts. It is read from $HOME/.baton/TUI.yaml (see LoadTUI) and carried on
// Config.TUI so frontends receive it over the existing config broadcast.
type TUIConfig struct {
	// Theme overrides the cockpit palette; an empty field keeps the built-in.
	Theme Theme `yaml:"theme,omitempty"`

	// Layouts are the named group-split arrangements offered in addition to the
	// built-in presets (tiled, main-vertical, main-horizontal, stack). A custom
	// entry with the same name as a preset overrides it.
	Layouts []Layout `yaml:"layouts,omitempty"`

	// DefaultLayout names the layout a group opens with; empty uses "tiled".
	DefaultLayout string `yaml:"default-layout,omitempty"`
}

// Theme is the cockpit colour palette. Each field is a colour string (a hex
// "#rrggbb" or an ANSI index); an empty field falls back to the built-in default,
// so a partial theme only overrides what it names.
type Theme struct {
	Brand   string `yaml:"brand,omitempty"`    // primary accent (banner, active borders, selection)
	BrandHi string `yaml:"brand-hi,omitempty"` // brighter accent (titles, pins, summary, hits)

	// The five lifecycle-state LEDs, by state name.
	Spawning  string `yaml:"spawning,omitempty"`
	Running   string `yaml:"running,omitempty"`
	Idle      string `yaml:"idle,omitempty"`
	Attention string `yaml:"attention,omitempty"`
	Exited    string `yaml:"exited,omitempty"`
}

// Layout is one named group-split arrangement. With no Areas it names a built-in
// preset (tiled, main-vertical, main-horizontal, stack). A custom layout gives an
// Areas grid: Areas[r] is one row of region names, each cell naming the region
// that owns it, so a region spanning several cells repeats its name across them.
// Members fill the regions in row-major order of first appearance; members past
// the region count fold into the summary tile.
type Layout struct {
	Name  string     `yaml:"name"`
	Rows  int        `yaml:"rows,omitempty"`
	Cols  int        `yaml:"cols,omitempty"`
	Areas [][]string `yaml:"areas,omitempty"`
}

// QueueDefaults caps the task backlog: Max is the most queued (unassigned) tasks
// the backlog holds before an enqueue is refused (0 = unlimited; unset uses the
// built-in default), and Concurrency is the most tasks one work item runs at once
// (0 = unlimited).
type QueueDefaults struct {
	Max         int `yaml:"max,omitempty"`
	Concurrency int `yaml:"concurrency,omitempty"`
}

// Settings are the persisted cockpit toggles. Pointers distinguish "unset"
// (use the default) from an explicit false.
type Settings struct {
	ConfirmClose *bool `yaml:"confirm-close,omitempty"` // ask y/n before closing a panel

	// AllowNameConflict lets two work items share a name. Unset or false keeps
	// the default policy: panel titles and group names must be unique.
	AllowNameConflict *bool `yaml:"allow-name-conflict,omitempty"`

	// Bell rings the terminal when a panel enters the attention state. Unset
	// defaults to on; set false to silence the audible nudge.
	Bell *bool `yaml:"bell,omitempty"`

	// Mouse enables mouse reporting in the cockpit — the wheel scrolls the
	// scrollback and moves the dashboard selection. Unset defaults to off, so
	// the terminal's own selection and copy stay available until you opt in.
	Mouse *bool `yaml:"mouse,omitempty"`
}

// PanelDefaults configure how new panels are spawned.
type PanelDefaults struct {
	Shell string `yaml:"shell,omitempty"` // default shell binary path; empty = system shell

	// Workdir is the directory new panels run in when none is given. Empty falls
	// back to the user's home — never the directory the daemon was launched from.
	Workdir string `yaml:"workdir,omitempty"`

	// ReplayKB is the per-panel replay buffer in kibibytes — how much recent
	// output the server keeps and replays when a frontend attaches, seeding the
	// scrollback you can page through. Unset or zero uses the built-in default.
	ReplayKB int `yaml:"replay-kb,omitempty"`

	// DefaultAgent is the agent profile spawned by the new-agent action; empty
	// means the built-in "claude" profile.
	DefaultAgent string `yaml:"default-agent,omitempty"`

	// DiffCommand is the diff command run by the agent diff pop-up; empty falls
	// back to the repo's git diff.tool, then a built-in untracked-inclusive diff.
	DiffCommand string `yaml:"diff-command,omitempty"`

	// Editor is the commit editor the git menu's commit op opens (injected as
	// GIT_EDITOR); empty lets git use its own GIT_EDITOR / core.editor / EDITOR / vi
	// chain.
	Editor string `yaml:"editor,omitempty"`

	// WorktreeDir is the base directory new git-menu worktrees are created under;
	// empty defaults to a sibling "<repo>-worktrees/<branch>" of the agent's repo.
	WorktreeDir string `yaml:"worktree-dir,omitempty"`

	// Agents are the named agent profiles, e.g. {"claude": {command: "claude"}}.
	// A built-in "claude" profile is always available unless overridden here.
	Agents map[string]AgentProfile `yaml:"agents,omitempty"`
}

// AgentProfile is one named way to launch an agent: the CLI binary and its
// arguments. The panel runs it directly as the panel's process.
type AgentProfile struct {
	Command string   `yaml:"command"`        // the agent CLI binary, e.g. "claude"
	Args    []string `yaml:"args,omitempty"` // arguments passed on every spawn
}

// Load reads the config file. A missing file yields an empty Config and no
// error, so a first run just uses the defaults.
func Load() (Config, error) {
	var c Config
	data, err := os.ReadFile(paths.ConfigFile())
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("parse config %s: %w", paths.ConfigFile(), err)
	}
	c.normalize()
	return c, nil
}

// LoadTUI reads the cockpit appearance file ($HOME/.baton/TUI.yaml). A missing
// file yields a zero TUIConfig and no error, so the built-in theme and the preset
// layouts apply. The caller attaches the result onto Config.TUI before the config
// is broadcast to frontends.
func LoadTUI() (TUIConfig, error) {
	var t TUIConfig
	data, err := os.ReadFile(paths.TUIConfigFile())
	if err != nil {
		if os.IsNotExist(err) {
			return t, nil
		}
		return t, fmt.Errorf("read TUI config: %w", err)
	}
	if err := yaml.Unmarshal(data, &t); err != nil {
		return t, fmt.Errorf("parse TUI config %s: %w", paths.TUIConfigFile(), err)
	}
	return t, nil
}

// normalize coerces a parsed config back into sane bounds so a hand-edited file
// cannot smuggle a nonsensical value past Load. A negative replay buffer is
// meaningless — clamp it to zero, which every consumer already reads as "use the
// built-in default".
func (c *Config) normalize() {
	if c.Panel.ReplayKB < 0 {
		c.Panel.ReplayKB = 0
	}
}

// Save writes the config file as YAML, creating $HOME/.baton as needed. The write
// is atomic: it marshals to a sibling temp file, fsyncs it, and renames it into
// place, so a crash or full disk mid-write can never leave a truncated config.
func (c Config) Save() error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	path := paths.ConfigFile()
	if err := paths.EnsureDir(path); err != nil {
		return fmt.Errorf("prepare config dir: %w", err)
	}
	if err := paths.WriteFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}
