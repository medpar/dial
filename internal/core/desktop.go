package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/medpar/dial/internal/codex"
	"github.com/medpar/dial/internal/desktop"
	"github.com/medpar/dial/internal/fsutil"
	"github.com/medpar/dial/internal/paths"
	"github.com/medpar/dial/internal/procs"
	"github.com/medpar/dial/internal/state"
	"github.com/medpar/dial/internal/vault"
)

const quitTimeout = 25 * time.Second

// DesktopAccount is one Claude Desktop login dial knows about.
type DesktopAccount struct {
	AccountUUID string `json:"account_uuid"`
	ProfileID   string `json:"profile_id,omitempty"`
	Label       string `json:"label"`
	Live        bool   `json:"live"`   // signed in to the main app
	Window      bool   `json:"window"` // running in its own window
	Saved       bool   `json:"saved"`  // a parked login exists
}

type DesktopStatus struct {
	Installed bool             `json:"installed"`
	Running   bool             `json:"running"`
	Accounts  []DesktopAccount `json:"accounts,omitempty"`
	// Windows lists Codex profiles running in their own window.
	Windows []string `json:"windows,omitempty"`
}

func (c *Core) claudeProfileForAccount(uuid string) (vault.Profile, bool) {
	list, _ := c.Vault.List(vault.Claude)
	for _, p := range list {
		if p.AccountID == uuid {
			return p, true
		}
	}
	return vault.Profile{}, false
}

// ClaudeDesktopStatus lists the main app login and every parked one.
func (c *Core) ClaudeDesktopStatus() DesktopStatus {
	app := desktop.Claude
	st := DesktopStatus{Installed: app.Installed()}
	if !st.Installed {
		return st
	}
	ps := procs.List()
	data := app.DataDir()
	st.Running = len(app.PIDs(ps, data)) > 0
	seen := map[string]bool{}
	add := func(a desktop.DesktopAccount, live bool) {
		if a.AccountUUID == "" || seen[a.AccountUUID] {
			return
		}
		seen[a.AccountUUID] = true
		da := DesktopAccount{AccountUUID: a.AccountUUID, Live: live, Saved: a.LoggedIn}
		if p, ok := c.claudeProfileForAccount(a.AccountUUID); ok {
			da.ProfileID, da.Label = p.ID, p.Display()
		} else {
			da.Label = "account " + a.AccountUUID[:8]
		}
		if !live {
			da.Window = len(app.PIDs(ps, desktop.ClaudeHome(a.AccountUUID))) > 0
		}
		st.Accounts = append(st.Accounts, da)
	}
	if cur := desktop.ClaudeAccountIn(data); cur.LoggedIn {
		add(cur, true)
	}
	for _, a := range desktop.ParkedClaude() {
		if a.LoggedIn {
			add(a, false)
		}
	}
	return st
}

// SwitchClaudeDesktop signs the main Claude app into p's account by
// swapping parked logins. The app is quit and relaunched if it was open.
func (c *Core) SwitchClaudeDesktop(p vault.Profile) error {
	if p.Provider != vault.Claude {
		return errors.New("not a Claude profile")
	}
	return c.switchClaudeDesktopAccount(p.AccountID, p.Display())
}

func (c *Core) switchClaudeDesktopAccount(target, label string) error {
	app := desktop.Claude
	if !app.Installed() {
		return errors.New("Claude desktop app is not installed")
	}
	data := app.DataDir()
	cur := desktop.ClaudeAccountIn(data)
	if cur.LoggedIn && cur.AccountUUID == target {
		return nil
	}
	home := desktop.ClaudeHome(target)
	if app.Running(home) {
		return fmt.Errorf("%s is open in its own Claude window; close it first", label)
	}
	if !desktop.ClaudeAccountIn(home).LoggedIn {
		return fmt.Errorf("no saved Claude Desktop login for %s yet: run `dial desktop add claude`, sign in as %s, then switch", label, label)
	}
	wasRunning := app.Running(data)
	if wasRunning {
		if err := app.Quit(data, quitTimeout); err != nil {
			return err
		}
	}
	l, err := c.Lock()
	if err != nil {
		return err
	}
	parked, err := desktop.ParkClaude(data)
	if err == nil {
		err = desktop.UnparkClaude(target, data)
		if err != nil && parked.AccountUUID != "" {
			_ = desktop.UnparkClaude(parked.AccountUUID, data) // roll back
		}
	}
	l.Release()
	desktop.CleanTrash()
	if err != nil {
		if wasRunning {
			_ = app.Launch(data, nil)
		}
		return err
	}
	_ = state.Update(func(s *state.State) {
		s.Active["claude-desktop"] = target
		s.Switched["claude-desktop"] = time.Now().UTC()
	})
	if wasRunning || c.Config.RestartApps {
		return app.Launch(data, nil)
	}
	return nil
}

// AddClaudeDesktop parks the current login and opens the app signed out so
// another account can sign in. The previous login stays saved (no logout,
// so its server-side session is not revoked).
func (c *Core) AddClaudeDesktop() error {
	app := desktop.Claude
	if !app.Installed() {
		return errors.New("Claude desktop app is not installed")
	}
	data := app.DataDir()
	if app.Running(data) {
		if err := app.Quit(data, quitTimeout); err != nil {
			return err
		}
	}
	l, err := c.Lock()
	if err != nil {
		return err
	}
	_, err = desktop.ParkClaude(data)
	l.Release()
	if err != nil {
		return err
	}
	return app.Launch(data, nil)
}

// OpenClaudeWindow runs p's Claude Desktop login in its own window.
func (c *Core) OpenClaudeWindow(p vault.Profile) error {
	app := desktop.Claude
	if !app.Installed() {
		return errors.New("Claude desktop app is not installed")
	}
	if cur := desktop.ClaudeAccountIn(app.DataDir()); cur.LoggedIn && cur.AccountUUID == p.AccountID {
		return fmt.Errorf("%s is the main Claude window's account; switch the main window to another account first", p.Display())
	}
	home := desktop.ClaudeHome(p.AccountID)
	if app.Running(home) {
		return app.Focus(home)
	}
	if !desktop.ClaudeAccountIn(home).LoggedIn {
		return fmt.Errorf("no saved Claude Desktop login for %s yet: run `dial desktop add claude` and sign in as %s", p.Display(), p.Display())
	}
	return app.Launch(home, nil)
}

// ------------------------------------------------------------------ Codex

// CodexDesktopStatus reports the main app and per-profile windows.
func (c *Core) CodexDesktopStatus() DesktopStatus {
	app := desktop.Codex
	st := DesktopStatus{Installed: app.Installed()}
	if !st.Installed {
		return st
	}
	ps := procs.List()
	st.Running = len(app.PIDs(ps, app.DataDir())) > 0
	list, _ := c.Vault.List(vault.Codex)
	for _, p := range list {
		if len(app.PIDs(ps, desktop.CodexInstanceData(p.ID))) > 0 {
			st.Windows = append(st.Windows, p.ID)
		}
	}
	return st
}

// RestartCodexApp relaunches the main Codex app so it picks up a new
// auth.json. It does nothing when the app is closed.
func (c *Core) RestartCodexApp() error {
	app := desktop.Codex
	if !app.Installed() {
		return nil
	}
	data := app.DataDir()
	if !app.Running(data) {
		return nil
	}
	if err := app.Quit(data, quitTimeout); err != nil {
		return err
	}
	return app.Launch(data, nil)
}

// SyncCodexWindows folds credentials refreshed inside closed per-profile
// homes back into the vault.
func (c *Core) SyncCodexWindows() {
	list, _ := c.Vault.List(vault.Codex)
	for _, p := range list {
		home := desktop.CodexInstanceHome(p.ID)
		if !fsutil.Exists(filepath.Join(home, codex.AuthFile)) || c.codexHomeBusy(p) {
			continue
		}
		c.SyncBackCodex(home)
	}
}

func (c *Core) codexHomeBusy(p vault.Profile) bool {
	if paths.IsMac() && desktop.Codex.Installed() && desktop.Codex.Running(desktop.CodexInstanceData(p.ID)) {
		return true
	}
	return pidAlive(filepath.Join(desktop.CodexInstanceHome(p.ID), ".dial.pid"))
}

// prepareCodexHome syncs and seeds p's private CODEX_HOME.
func (c *Core) prepareCodexHome(p vault.Profile) (string, error) {
	home := desktop.CodexInstanceHome(p.ID)
	if err := desktop.PrepareCodexHome(home); err != nil {
		return "", err
	}
	l, err := c.Lock()
	if err != nil {
		return "", err
	}
	defer l.Release()
	c.SyncBackCodex(home)
	a, err := c.LoadCodex(p)
	if err != nil {
		return "", err
	}
	if !a.HasLogin() {
		return "", fmt.Errorf("profile %s has no usable login", p.ID)
	}
	return home, a.Save(filepath.Join(home, codex.AuthFile))
}

// OpenCodexWindow runs a separate Codex desktop instance signed in as p.
func (c *Core) OpenCodexWindow(p vault.Profile) error {
	app := desktop.Codex
	if !app.Installed() {
		return errors.New("Codex desktop app is not installed")
	}
	if c.ActiveID(vault.Codex) == p.ID {
		return fmt.Errorf("%s is the main Codex account; switch the main app to another account first", p.Display())
	}
	data := desktop.CodexInstanceData(p.ID)
	if app.Running(data) {
		return app.Focus(data)
	}
	home, err := c.prepareCodexHome(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(data, 0o700); err != nil {
		return err
	}
	return app.Launch(data, map[string]string{
		"CODEX_HOME":                    home,
		"CODEX_ELECTRON_USER_DATA_PATH": data,
	})
}
