// Package state keeps dial's settings (config.json) and runtime state
// (state.json) under ~/.dial.
package state

import (
	"errors"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/medpar/dial/internal/fsutil"
	"github.com/medpar/dial/internal/paths"
)

type Remote struct {
	Name string `json:"name"`
	// SSH is the ssh destination, e.g. "user@host" or a ~/.ssh/config alias.
	SSH string `json:"ssh"`
	// Dial is the dial binary on the remote host.
	Dial string `json:"dial,omitempty"`
	// Providers limits which providers follow local switches.
	Providers []string `json:"providers,omitempty"`
	// Auto makes every local CLI switch propagate to this host.
	Auto bool `json:"auto"`
}

type Config struct {
	Remotes []Remote `json:"remotes,omitempty"`
	// SwitchDesktop makes `dial use` also switch the desktop app (macOS).
	SwitchDesktop bool `json:"switch_desktop"`
	// RestartApps relaunches a running desktop app after a switch.
	RestartApps bool `json:"restart_apps"`
	// UsageTTL is how long fetched usage stays fresh, in seconds.
	UsageTTL int `json:"usage_ttl_seconds"`
	// RefreshInactive keeps stored logins alive by refreshing tokens of
	// profiles that are not live anywhere before they expire.
	RefreshInactive bool `json:"refresh_inactive"`
	// DesktopMainFirst starts the main Claude window before an account's own
	// window, so the Dock's pinned Claude icon stays bound to the main one.
	DesktopMainFirst bool `json:"desktop_main_first"`
}

func DefaultConfig() Config {
	return Config{SwitchDesktop: true, RestartApps: true, UsageTTL: 120, RefreshInactive: true, DesktopMainFirst: true}
}

func configPath() string { return filepath.Join(paths.DialHome(), "config.json") }
func statePath() string  { return filepath.Join(paths.DialHome(), "state.json") }

func LoadConfig() Config {
	c := DefaultConfig()
	if err := fsutil.ReadJSON(configPath(), &c); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return DefaultConfig()
	}
	if c.UsageTTL <= 0 {
		c.UsageTTL = 120
	}
	return c
}

func SaveConfig(c Config) error { return fsutil.WriteJSON(configPath(), c, 0o600) }

type SyncResult struct {
	Remote  string    `json:"remote"`
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Message string    `json:"message,omitempty"`
}

type State struct {
	// Active maps a target ("claude", "codex", "claude-desktop") to the
	// profile id dial last applied there.
	Active   map[string]string     `json:"active"`
	Switched map[string]time.Time  `json:"switched,omitempty"`
	Sync     map[string]SyncResult `json:"sync,omitempty"`
}

func Load() State {
	s := State{}
	_ = fsutil.ReadJSON(statePath(), &s)
	if s.Active == nil {
		s.Active = map[string]string{}
	}
	if s.Switched == nil {
		s.Switched = map[string]time.Time{}
	}
	if s.Sync == nil {
		s.Sync = map[string]SyncResult{}
	}
	return s
}

func Save(s State) error { return fsutil.WriteJSON(statePath(), s, 0o600) }

// Update loads, mutates and saves state in one step.
func Update(fn func(*State)) error {
	s := Load()
	fn(&s)
	return Save(s)
}
