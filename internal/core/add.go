package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/medpar/dial/internal/claude"
	"github.com/medpar/dial/internal/codex"
	"github.com/medpar/dial/internal/keychain"
	"github.com/medpar/dial/internal/paths"
	"github.com/medpar/dial/internal/vault"
)

// LoginOptions tune an isolated browser login.
type LoginOptions struct {
	Email  string // Claude: prefill the login page
	SSO    bool   // Claude: force SSO
	Device bool   // Codex: device-code flow (headless machines)
}

// isolatedLogin runs the provider's own login command in a throwaway home,
// so the current login is untouched, and returns that home.
func isolatedLogin(p vault.Provider, opt LoginOptions) (dir string, cleanup func(), err error) {
	root := filepath.Join(paths.DialHome(), "tmp")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", nil, err
	}
	cleanStaleLogins(root)
	dir, err = os.MkdirTemp(root, "login-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() {
		if p == vault.Claude && keychain.Available() {
			_ = keychain.Delete(claude.DirTarget(dir).KeychainService(), "")
		}
		_ = os.RemoveAll(dir)
	}
	var bin string
	var args []string
	env := os.Environ()
	switch p {
	case vault.Claude:
		bin = "claude"
		args = []string{"auth", "login", "--claudeai"}
		if opt.Email != "" {
			args = append(args, "--email", opt.Email)
		}
		if opt.SSO {
			args = append(args, "--sso")
		}
		env = append(env, "CLAUDE_CONFIG_DIR="+dir)
	case vault.Codex:
		bin = "codex"
		args = []string{"login", "-c", `cli_auth_credentials_store="file"`}
		if opt.Device {
			args = append(args, "--device-auth")
		}
		env = append(env, "CODEX_HOME="+dir)
	}
	path, err := FindBinary(bin)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("%s login failed: %w", bin, err)
	}
	return dir, cleanup, nil
}

// cleanStaleLogins removes throwaway homes (and their keychain items) left
// behind by logins that were interrupted more than an hour ago.
func cleanStaleLogins(root string) {
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "login-") {
			continue
		}
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < time.Hour {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if keychain.Available() {
			_ = keychain.Delete(claude.DirTarget(dir).KeychainService(), "")
		}
		_ = os.RemoveAll(dir)
	}
}

// Add logs in a new account without disturbing the current one and stores
// it. If the account already exists its login is replaced by the new one.
func (c *Core) Add(ctx context.Context, p vault.Provider, label string, opt LoginOptions) (vault.Profile, bool, error) {
	dir, cleanup, err := isolatedLogin(p, opt)
	if err != nil {
		return vault.Profile{}, false, err
	}
	defer cleanup()
	l, err := c.Lock()
	if err != nil {
		return vault.Profile{}, false, err
	}
	defer l.Release()
	switch p {
	case vault.Claude:
		return c.importFreshClaude(ctx, claude.DirTarget(dir), label)
	default:
		a, err := codex.Load(filepath.Join(dir, codex.AuthFile))
		if err != nil {
			return vault.Profile{}, false, fmt.Errorf("codex login wrote no auth.json: %w", err)
		}
		return c.ImportCodexAuth(a, label, "login", NewLineage())
	}
}

// importFreshClaude stores a brand-new login, replacing any older family.
func (c *Core) importFreshClaude(ctx context.Context, t claude.Target, label string) (vault.Profile, bool, error) {
	o, err := t.ReadOAuth()
	if err != nil {
		return vault.Profile{}, false, err
	}
	if !o.HasLogin() {
		return vault.Profile{}, false, fmt.Errorf("claude login produced no credentials")
	}
	raw, _ := t.ReadAccount()
	acct := claude.ParseAccount(raw)
	if acct.AccountUUID == "" || acct.OrganizationUUID == "" {
		prof, err := claude.FetchProfile(ctx, o)
		if err != nil {
			return vault.Profile{}, false, fmt.Errorf("identify Claude login: %w", err)
		}
		raw = claude.AccountFromProfile(prof)
		acct = claude.ParseAccount(raw)
	}
	p, created, err := c.storeClaude(o, raw, acct, label, "login", NewLineage())
	if err == nil {
		p = c.enrichClaude(ctx, p, o)
	}
	return p, created, err
}

// Mint performs an isolated login and returns the credentials without
// storing them, for seeding another machine with its own token family.
func (c *Core) Mint(ctx context.Context, p vault.Provider, opt LoginOptions) (vault.Profile, Secret, error) {
	dir, cleanup, err := isolatedLogin(p, opt)
	if err != nil {
		return vault.Profile{}, Secret{}, err
	}
	defer cleanup()
	switch p {
	case vault.Claude:
		t := claude.DirTarget(dir)
		o, err := t.ReadOAuth()
		if err != nil || !o.HasLogin() {
			return vault.Profile{}, Secret{}, fmt.Errorf("claude login produced no credentials")
		}
		raw, _ := t.ReadAccount()
		acct := claude.ParseAccount(raw)
		if acct.AccountUUID == "" {
			prof, err := claude.FetchProfile(ctx, o)
			if err != nil {
				return vault.Profile{}, Secret{}, err
			}
			raw = claude.AccountFromProfile(prof)
			acct = claude.ParseAccount(raw)
		}
		meta := vault.Profile{Provider: vault.Claude, Email: acct.EmailAddress, AccountID: acct.AccountUUID, OrgID: acct.OrganizationUUID, OrgName: acct.OrganizationName, Plan: ClaudePlan(o.Subscription(), o.RateLimitTier()), Tier: o.RateLimitTier(), Lineage: NewLineage()}
		return meta, Secret{Claude: o.JSON(), Account: raw}, nil
	default:
		a, err := codex.Load(filepath.Join(dir, codex.AuthFile))
		if err != nil {
			return vault.Profile{}, Secret{}, err
		}
		id := a.Identity()
		b, _ := a.Bytes()
		meta := vault.Profile{Provider: vault.Codex, Email: id.Email, AccountID: id.AccountID, UserID: id.UserID, OrgName: id.OrgName, Plan: id.Plan, Lineage: NewLineage()}
		return meta, Secret{Codex: b}, nil
	}
}
