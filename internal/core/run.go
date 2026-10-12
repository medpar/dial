package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/medpar/dial/internal/claude"
	"github.com/medpar/dial/internal/fsutil"
	"github.com/medpar/dial/internal/paths"
	"github.com/medpar/dial/internal/vault"
)

// Side-by-side CLI sessions: `dial run <profile>` starts claude or codex
// with a private config dir for that profile, so several accounts can work
// at once without touching the global login.

// Shared from ~/.claude into every run dir. Credentials, policy caches and
// per-process state stay private.
var claudeShared = []string{
	"settings.json", "CLAUDE.md", "AGENTS.md", "commands", "agents", "skills",
	"plugins", "output-styles", "hooks", "projects", "history.jsonl",
	"keybindings.json", "file-history", "todos", "plans", "statusline.sh",
}

func ClaudeRunDir(id string) string { return filepath.Join(paths.DialHome(), "run", "claude", id) }

func pidAlive(pidFile string) bool {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}
	for _, f := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(f)
		if err == nil && pid > 0 && syscall.Kill(pid, 0) == nil {
			return true
		}
	}
	return false
}

func (c *Core) instanceRunning(p vault.Profile) bool {
	switch p.Provider {
	case vault.Codex:
		return c.codexHomeBusy(p)
	case vault.Claude:
		return pidAlive(filepath.Join(ClaudeRunDir(p.ID), ".dial.pid"))
	}
	return false
}

// prepareClaudeDir builds p's private CLAUDE_CONFIG_DIR and installs its
// credentials there.
func (c *Core) prepareClaudeDir(ctx context.Context, p vault.Profile) (string, error) {
	dir := ClaudeRunDir(p.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	src := paths.ClaudeConfigDir()
	for _, name := range claudeShared {
		target := filepath.Join(src, name)
		if _, err := os.Lstat(target); err != nil {
			continue
		}
		link := filepath.Join(dir, name)
		if _, err := os.Lstat(link); err == nil {
			continue
		}
		if err := os.Symlink(target, link); err != nil {
			return "", err
		}
	}
	t := claude.DirTarget(dir)
	l, err := c.Lock()
	if err != nil {
		return "", err
	}
	defer l.Release()
	// Fold back anything a previous run refreshed, then install the newest.
	c.SyncBackClaude(ctx, t)
	cr, err := c.LoadClaude(p)
	if err != nil {
		return "", err
	}
	if !cr.OAuth.HasLogin() {
		return "", fmt.Errorf("profile %s has no usable login", p.ID)
	}
	if err := t.WriteOAuth(cr.OAuth); err != nil {
		return "", err
	}
	// Seed .claude.json from the main one so onboarding is skipped and
	// per-project trust carries over, with this profile's identity.
	cfg := t.GlobalConfig()
	m, _ := fsutil.ReadJSONMap(cfg)
	if len(m) == 0 {
		m, _ = fsutil.ReadJSONMap(paths.ClaudeGlobalConfig())
		for _, k := range []string{"oauthAccount", "userID", "primaryApiKey", "customApiKeyResponses", "s1mAccessCache", "groveConfigCache", "passesEligibilityCache", "cachedExtraUsageDisabledReason"} {
			delete(m, k)
		}
	}
	m["hasCompletedOnboarding"] = json.RawMessage("true")
	if len(cr.Account) > 0 {
		m["oauthAccount"] = cr.Account
	}
	data, err := fsutil.MarshalIndent(m)
	if err != nil {
		return "", err
	}
	return dir, fsutil.WriteFileAtomic(cfg, data, 0o600)
}

// Run starts the provider CLI for p in its private home and waits.
func (c *Core) Run(ctx context.Context, p vault.Profile, args []string) (int, error) {
	var bin, envKey, home, pidFile string
	var err error
	switch p.Provider {
	case vault.Claude:
		bin = "claude"
		envKey = "CLAUDE_CONFIG_DIR"
		home, err = c.prepareClaudeDir(ctx, p)
		pidFile = filepath.Join(ClaudeRunDir(p.ID), ".dial.pid")
	case vault.Codex:
		bin = "codex"
		envKey = "CODEX_HOME"
		home, err = c.prepareCodexHome(p)
		pidFile = filepath.Join(home, ".dial.pid")
	default:
		return 1, errors.New("unknown provider")
	}
	if err != nil {
		return 1, err
	}
	path, err := FindBinary(bin)
	if err != nil {
		return 1, err
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, envKey+"=") && !strings.HasPrefix(kv, "CLAUDE_CODE_OAUTH_TOKEN=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, envKey+"="+home)
	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT)
	defer signal.Reset(syscall.SIGINT, syscall.SIGQUIT)
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	appendPID(pidFile, cmd.Process.Pid)
	waitErr := cmd.Wait()
	removePID(pidFile, cmd.Process.Pid)
	// Fold refreshed tokens back into the vault.
	if l, err := c.Lock(); err == nil {
		if p.Provider == vault.Claude {
			c.SyncBackClaude(ctx, claude.DirTarget(home))
		} else {
			c.SyncBackCodex(home)
		}
		l.Release()
	}
	if ee, ok := waitErr.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	return 0, waitErr
}

// Env returns the variables that point a shell at p's private home.
func (c *Core) Env(ctx context.Context, p vault.Profile) (map[string]string, error) {
	switch p.Provider {
	case vault.Claude:
		dir, err := c.prepareClaudeDir(ctx, p)
		return map[string]string{"CLAUDE_CONFIG_DIR": dir}, err
	case vault.Codex:
		home, err := c.prepareCodexHome(p)
		return map[string]string{"CODEX_HOME": home}, err
	}
	return nil, errors.New("unknown provider")
}

func appendPID(file string, pid int) {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	fmt.Fprintln(f, pid)
	f.Close()
}

func removePID(file string, pid int) {
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	var keep []string
	for _, f := range strings.Fields(string(data)) {
		if n, err := strconv.Atoi(f); err == nil && n != pid && syscall.Kill(n, 0) == nil {
			keep = append(keep, f)
		}
	}
	if len(keep) == 0 {
		os.Remove(file)
		return
	}
	_ = os.WriteFile(file, []byte(strings.Join(keep, "\n")+"\n"), 0o600)
}

// FindBinary locates a CLI even when PATH is minimal (ssh, launchd).
func FindBinary(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	home := paths.Home()
	for _, dir := range []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".claude", "local"),
		filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(home, ".bun", "bin"),
		"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin",
	} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH", name)
}
