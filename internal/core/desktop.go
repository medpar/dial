package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	Live        bool   `json:"live"`   // signed in to the main window
	Window      bool   `json:"window"` // running in its own window
	Saved       bool   `json:"saved"`  // a saved login exists (not running)
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

// claudeView is what is running right now, identified by content: the
// account inside each instance's data dir, never the dir's name.
type claudeView struct {
	instances []desktop.Instance
	accounts  map[string]string // dir → account uuid signed in there
	busy      map[string]bool   // dirs used by a running instance
}

func (c *Core) viewClaude() claudeView {
	app := desktop.Claude
	v := claudeView{accounts: map[string]string{}, busy: map[string]bool{}}
	for _, in := range app.RunningInstances(procs.List()) {
		// With a relocated data dir (tests, demos) only dial's own dirs
		// count, so real windows are never quit or relaunched.
		if app.Overridden() && !under(in.Dir, app.DataDir()) && !under(in.Dir, desktop.ClaudeHomes()) {
			continue
		}
		v.instances = append(v.instances, in)
	}
	for _, in := range v.instances {
		v.busy[filepath.Clean(in.Dir)] = true
		if a := desktop.ClaudeAccountIn(in.Dir); a.LoggedIn {
			v.accounts[filepath.Clean(in.Dir)] = a.AccountUUID
		}
	}
	return v
}

func under(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && !strings.HasPrefix(rel, "..")
}

func (v claudeView) isBusy(dir string) bool { return v.busy[filepath.Clean(dir)] }

// runningWith returns the instance currently signed in as account.
func (v claudeView) runningWith(account string) (desktop.Instance, bool) {
	for _, in := range v.instances {
		if v.accounts[filepath.Clean(in.Dir)] == account {
			return in, true
		}
	}
	return desktop.Instance{}, false
}

func (c *Core) reconcileClaude() claudeView {
	v := c.viewClaude()
	desktop.ReconcileClaudeHomes(v.isBusy)
	desktop.StaleSignins(v.isBusy)
	return v
}

func (c *Core) desktopLabel(uuid string) (string, string) {
	if p, ok := c.claudeProfileForAccount(uuid); ok {
		return p.ID, p.Display()
	}
	if len(uuid) > 8 {
		return "", "account " + uuid[:8]
	}
	return "", "account " + uuid
}

// ClaudeDesktopStatus lists the main window's login, every extra window and
// every saved login, identified by what each data dir actually holds.
func (c *Core) ClaudeDesktopStatus() DesktopStatus {
	app := desktop.Claude
	st := DesktopStatus{Installed: app.Installed()}
	if !st.Installed {
		return st
	}
	v := c.reconcileClaude()
	seen := map[string]int{}
	add := func(uuid string, fn func(*DesktopAccount)) {
		if uuid == "" {
			return
		}
		i, ok := seen[uuid]
		if !ok {
			pid, label := c.desktopLabel(uuid)
			st.Accounts = append(st.Accounts, DesktopAccount{AccountUUID: uuid, ProfileID: pid, Label: label})
			i = len(st.Accounts) - 1
			seen[uuid] = i
		}
		fn(&st.Accounts[i])
	}
	for _, in := range v.instances {
		uuid := v.accounts[filepath.Clean(in.Dir)]
		if in.Default {
			st.Running = true
			add(uuid, func(a *DesktopAccount) { a.Live = true })
		} else {
			add(uuid, func(a *DesktopAccount) { a.Window = true })
		}
	}
	if !st.Running {
		if cur := desktop.ClaudeAccountIn(app.DataDir()); cur.LoggedIn {
			add(cur.AccountUUID, func(a *DesktopAccount) { a.Live = true })
		}
	}
	for _, a := range desktop.ParkedClaude() {
		if a.LoggedIn && !v.isBusy(a.Dir) {
			add(a.AccountUUID, func(d *DesktopAccount) { d.Saved = true })
		}
	}
	return st
}

// SwitchClaudeDesktop signs the main Claude window into p's account by
// swapping saved logins. The app is quit and relaunched if it was open;
// extra windows keep running.
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
	v := c.reconcileClaude()
	if in, ok := v.runningWith(target); ok && !in.Default {
		return fmt.Errorf("%s is open in its own Claude window; close that window first", label)
	}
	home := desktop.ClaudeHome(target)
	if !desktop.ClaudeAccountIn(home).LoggedIn {
		return fmt.Errorf("no saved Claude Desktop login for %s yet: run `dial desktop add claude` and sign in as %s", label, label)
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

// SigninOptions tune AddClaudeDesktop.
type SigninOptions struct {
	Expect   string        // account uuid the user should sign in as (renewals)
	NoWindow bool          // don't open the new account in its own window
	Timeout  time.Duration // how long to wait for the sign-in
	Progress func(string)  // status lines for the caller
}

// AddClaudeDesktop signs a new account into Claude Desktop.
//
// Claude Desktop finishes a sign-in through a claude:// link carrying a
// token only the instance that started it accepts, and macOS hands that link
// to just one running instance. So every Claude window is closed first and
// the sign-in happens in a single, fresh window. When it completes, that
// login is saved as the account's home, the windows that were open come
// back, and the new account opens in its own window. Nothing is logged out.
func (c *Core) AddClaudeDesktop(opt SigninOptions) (DesktopAccount, error) {
	app := desktop.Claude
	say := func(s string) {
		if opt.Progress != nil {
			opt.Progress(s)
		}
	}
	if !app.Installed() {
		return DesktopAccount{}, errors.New("Claude desktop app is not installed")
	}
	if opt.Timeout == 0 {
		opt.Timeout = 15 * time.Minute
	}
	v := c.reconcileClaude()
	before := v.instances
	if len(before) > 0 {
		say(fmt.Sprintf("Closing %d Claude window(s) so the sign-in reaches the right one…", len(before)))
	}
	for _, in := range before {
		if err := app.Quit(in.Dir, quitTimeout); err != nil {
			return DesktopAccount{}, err
		}
	}
	restore := func(extra ...string) {
		dirs := make([]string, 0, len(before)+len(extra))
		for _, in := range before {
			dirs = append(dirs, in.Dir)
		}
		launchInOrder(app, append(dirs, extra...))
	}
	dir, err := desktop.NewSigninDir()
	if err != nil {
		restore()
		return DesktopAccount{}, err
	}
	desktop.SeedSettings(app.DataDir(), dir)
	if err := app.Launch(dir, nil); err != nil {
		_ = os.RemoveAll(dir)
		restore()
		return DesktopAccount{}, err
	}
	say("Sign in with the account to add in the Claude window that just opened.")
	acct, err := waitForSignin(app, dir, opt.Timeout)
	if err == nil {
		say("Signed in. Saving the login…")
		// Give the app a moment to persist cookies and tokens, then quit it.
		time.Sleep(3 * time.Second)
	}
	if qerr := app.Quit(dir, quitTimeout); qerr != nil && err == nil {
		err = qerr
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		restore()
		return DesktopAccount{}, err
	}
	if opt.Expect != "" && acct.AccountUUID != opt.Expect {
		_ = os.RemoveAll(dir)
		restore()
		_, want := c.desktopLabel(opt.Expect)
		return DesktopAccount{}, fmt.Errorf("you signed in as a different account than %s; nothing was changed", want)
	}
	// Each login lives in one place: if the main window already holds this
	// account, the new sign-in is redundant.
	if main := desktop.ClaudeAccountIn(app.DataDir()); main.LoggedIn && main.AccountUUID == acct.AccountUUID {
		_ = os.RemoveAll(dir)
		restore()
		pid, label := c.desktopLabel(acct.AccountUUID)
		return DesktopAccount{AccountUUID: acct.AccountUUID, ProfileID: pid, Label: label, Live: true},
			fmt.Errorf("%s is already the main window's account; sign in with a different account", label)
	}
	l, err := c.Lock()
	if err != nil {
		restore()
		return DesktopAccount{}, err
	}
	adopted, err := desktop.AdoptSignin(dir)
	l.Release()
	if err != nil {
		restore()
		return DesktopAccount{}, err
	}
	pid, label := c.desktopLabel(adopted.AccountUUID)
	res := DesktopAccount{AccountUUID: adopted.AccountUUID, ProfileID: pid, Label: label, Saved: true}
	wasOpen := false
	for _, in := range before {
		if filepath.Clean(in.Dir) == filepath.Clean(adopted.Dir) {
			wasOpen = true
		}
	}
	if opt.NoWindow || wasOpen {
		restore()
	} else {
		restore(adopted.Dir)
		res.Window, res.Saved = true, false
	}
	return res, nil
}

// waitForSignin polls dir until a login is present and stable, the window
// is closed, or timeout passes.
func waitForSignin(app desktop.App, dir string, timeout time.Duration) (desktop.DesktopAccount, error) {
	deadline := time.Now().Add(timeout)
	started := false
	var last string
	stable := 0
	for time.Now().Before(deadline) {
		running := app.Running(dir)
		if running {
			started = true
		} else if started {
			return desktop.DesktopAccount{}, errors.New("the sign-in window was closed before signing in")
		}
		a := desktop.ClaudeAccountIn(dir)
		if a.LoggedIn && a.AccountUUID == last {
			stable++
			if stable >= 2 {
				return a, nil
			}
		} else {
			stable = 0
		}
		if a.LoggedIn {
			last = a.AccountUUID
		}
		time.Sleep(time.Second)
	}
	return desktop.DesktopAccount{}, errors.New("timed out waiting for the sign-in")
}

// launchInOrder starts the main window (default data dir) first and waits
// for it, then every other dir. The Dock binds its pinned Claude icon to the
// first instance it sees, so this keeps that icon on the main window.
func launchInOrder(app desktop.App, dirs []string) {
	var rest []string
	for _, d := range dirs {
		if filepath.Clean(d) == filepath.Clean(app.DataDir()) && !app.Overridden() {
			if app.Launch(d, nil) == nil {
				app.WaitRunning(d, 20*time.Second)
			}
			continue
		}
		rest = append(rest, d)
	}
	for _, d := range rest {
		_ = app.Launch(d, nil)
	}
}

// ensureMainFirst starts the signed-in main window before another window
// opens, when it isn't running yet.
func (c *Core) ensureMainFirst(app desktop.App) {
	if !c.Config.DesktopMainFirst || app.Overridden() {
		return
	}
	main := app.DataDir()
	if app.Running(main) || !desktop.ClaudeAccountIn(main).LoggedIn {
		return
	}
	if app.Launch(main, nil) == nil {
		app.WaitRunning(main, 20*time.Second)
	}
}

// OpenClaudeWindow runs p's Claude Desktop login in its own window.
func (c *Core) OpenClaudeWindow(p vault.Profile) error {
	app := desktop.Claude
	if !app.Installed() {
		return errors.New("Claude desktop app is not installed")
	}
	v := c.reconcileClaude()
	if in, ok := v.runningWith(p.AccountID); ok {
		if in.Default {
			return fmt.Errorf("%s is the main Claude window's account; switch the main window to another account first", p.Display())
		}
		return desktop.Activate(in.PID)
	}
	if cur := desktop.ClaudeAccountIn(app.DataDir()); cur.LoggedIn && cur.AccountUUID == p.AccountID {
		return fmt.Errorf("%s is the main Claude window's account; switch the main window to another account first", p.Display())
	}
	home := desktop.ClaudeHome(p.AccountID)
	if !desktop.ClaudeAccountIn(home).LoggedIn {
		return fmt.Errorf("no saved Claude Desktop login for %s yet: run `dial desktop add claude` and sign in as %s", p.Display(), p.Display())
	}
	c.ensureMainFirst(app)
	return app.Launch(home, nil)
}

// Where reports where an account's desktop app is: its main window, its own
// window, saved but closed, or nowhere.
type Where struct {
	Provider string `json:"provider"`
	Profile  string `json:"profile"`
	Kind     string `json:"kind"` // main | window | saved | none
	Running  bool   `json:"running"`
	PID      int    `json:"pid,omitempty"`
	Dir      string `json:"dir,omitempty"`
}

// WhereDesktop locates p's desktop app instance.
func (c *Core) WhereDesktop(p vault.Profile) Where {
	w := Where{Provider: string(p.Provider), Profile: p.ID, Kind: "none"}
	switch p.Provider {
	case vault.Claude:
		app := desktop.Claude
		if !app.Installed() {
			return w
		}
		v := c.reconcileClaude()
		if in, ok := v.runningWith(p.AccountID); ok {
			w.Running, w.PID, w.Dir = true, in.PID, in.Dir
			w.Kind = "window"
			if in.Default {
				w.Kind = "main"
			}
			return w
		}
		if cur := desktop.ClaudeAccountIn(app.DataDir()); cur.LoggedIn && cur.AccountUUID == p.AccountID {
			w.Kind, w.Dir = "main", app.DataDir()
			return w
		}
		if desktop.ClaudeAccountIn(desktop.ClaudeHome(p.AccountID)).LoggedIn {
			w.Kind, w.Dir = "saved", desktop.ClaudeHome(p.AccountID)
		}
	case vault.Codex:
		app := desktop.Codex
		if !app.Installed() {
			return w
		}
		if c.ActiveID(vault.Codex) == p.ID {
			w.Kind, w.Dir = "main", app.DataDir()
			if pid := app.PID(app.DataDir()); pid > 0 {
				w.Running, w.PID = true, pid
			}
			return w
		}
		data := desktop.CodexInstanceData(p.ID)
		if pid := app.PID(data); pid > 0 {
			w.Kind, w.Running, w.PID, w.Dir = "window", true, pid, data
			return w
		}
		if a, err := c.LoadCodex(p); err == nil && a.HasLogin() {
			w.Kind, w.Dir = "saved", data
		}
	}
	return w
}

// ShowDesktop brings p's desktop app forward without changing which account
// any window holds: its running window comes to the front, a closed one
// opens (in its own window for Claude). The main Claude window only
// changes account through SwitchClaudeDesktop (`dial desktop use`).
func (c *Core) ShowDesktop(p vault.Profile) (Where, error) {
	w := c.WhereDesktop(p)
	switch {
	case w.Running:
		return w, desktop.Activate(w.PID)
	case w.Kind == "main" && p.Provider == vault.Claude:
		err := desktop.Claude.Launch(desktop.Claude.DataDir(), nil)
		return w, err
	case w.Kind == "main" && p.Provider == vault.Codex:
		err := desktop.Codex.Launch(desktop.Codex.DataDir(), nil)
		return w, err
	case w.Kind == "saved" && p.Provider == vault.Claude:
		return w, c.OpenClaudeWindow(p)
	case w.Kind == "saved" && p.Provider == vault.Codex:
		return w, c.OpenCodexWindow(p)
	}
	return w, fmt.Errorf("no desktop sign-in for %s yet", p.Display())
}

// ------------------------------------------------------------------ Codex

// CodexDesktopStatus reports the main app and per-profile windows. A
// window is attributed to the account in its CODEX_HOME's auth.json, so a
// window someone signed into another account inside is reported correctly.
func (c *Core) CodexDesktopStatus() DesktopStatus {
	app := desktop.Codex
	st := DesktopStatus{Installed: app.Installed()}
	if !st.Installed {
		return st
	}
	ps := procs.List()
	st.Running = len(app.PIDs(ps, app.DataDir())) > 0
	list, _ := c.Vault.List(vault.Codex)
	seen := map[string]bool{}
	for _, p := range list {
		if len(app.PIDs(ps, desktop.CodexInstanceData(p.ID))) == 0 {
			continue
		}
		id := p.ID
		if a, err := codex.Load(filepath.Join(desktop.CodexInstanceHome(p.ID), codex.AuthFile)); err == nil && a.HasLogin() {
			if q, ok := c.Vault.FindByKey(vault.Codex, a.Identity().Key()); ok {
				id = q.ID
			}
		}
		if !seen[id] {
			seen[id] = true
			st.Windows = append(st.Windows, id)
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
	if err := desktop.PrepareCodexHome(home, c.codexHomeBusy(p)); err != nil {
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
