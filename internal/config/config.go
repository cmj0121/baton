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
}

// Settings are the persisted cockpit toggles. Pointers distinguish "unset"
// (use the default) from an explicit false.
type Settings struct {
	ConfirmClose *bool `yaml:"confirm-close,omitempty"` // ask y/n before closing a panel

	// AllowNameConflict lets two work items share a name. Unset or false keeps
	// the default policy: panel titles and group names must be unique.
	AllowNameConflict *bool `yaml:"allow-name-conflict,omitempty"`
}

// PanelDefaults configure how new panels are spawned.
type PanelDefaults struct {
	Shell string `yaml:"shell,omitempty"` // default shell binary path; empty = system shell
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
	return c, nil
}

// Save writes the config file as YAML, creating $HOME/.baton as needed.
func (c Config) Save() error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	path := paths.ConfigFile()
	if err := paths.EnsureDir(path); err != nil {
		return fmt.Errorf("prepare config dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}
