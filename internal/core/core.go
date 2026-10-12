// Package core ties the vault, the live CLI locations and dial's state
// together: import, reconcile, switch, refresh and meter.
package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/medpar/dial/internal/claude"
	"github.com/medpar/dial/internal/codex"
	"github.com/medpar/dial/internal/lock"
	"github.com/medpar/dial/internal/paths"
	"github.com/medpar/dial/internal/procs"
	"github.com/medpar/dial/internal/state"
	"github.com/medpar/dial/internal/vault"
)

// MinLiveSwapVersion is the first Claude Code that re-reads its credential
// store when it changes; older running sessions can write the previous
// login back at their next refresh.
const MinLiveSwapVersion = "2.1.296"

type Core struct {
	Vault  *vault.Vault
	Config state.Config
	// Warnings collects non-fatal notes for the caller to print.
	Warnings []string
}

func New() *Core {
	return &Core{Vault: vault.Open(), Config: state.LoadConfig()}
}

func (c *Core) warn(format string, a ...any) {
	c.Warnings = append(c.Warnings, fmt.Sprintf(format, a...))
}

// Lock serialises every mutation of live credentials or the vault.
func (c *Core) Lock() (*lock.Lock, error) {
	return lock.Acquire(filepath.Join(paths.DialHome(), ".lock"), 30*time.Second)
}

func NewLineage() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ClaudePlan renders subscriptionType + rateLimitTier, e.g. "max 5x".
func ClaudePlan(sub, tier string) string {
	sub = strings.ToLower(sub)
	t := strings.ToLower(tier)
	for _, m := range []string{"20x", "5x"} {
		if strings.Contains(t, "max_"+m) {
			if sub == "" || sub == "max" {
				return "max " + m
			}
			return sub + " · max " + m
		}
	}
	return sub
}

// ---------------------------------------------------------------- Claude

// LiveClaude identifies the login currently in target t.
func (c *Core) LiveClaude(t claude.Target) (*claude.OAuth, claude.Account, error) {
	o, err := t.ReadOAuth()
	if err != nil {
		return nil, claude.Account{}, err
	}
	raw, err := t.ReadAccount()
	if err != nil {
		return o, claude.Account{}, err
	}
	return o, claude.ParseAccount(raw), nil
}

// ImportClaude stores the login found in t, creating or updating the
// matching profile. label is used only when a profile is created.
func (c *Core) ImportClaude(ctx context.Context, t claude.Target, label, origin string) (vault.Profile, bool, error) {
	o, err := t.ReadOAuth()
	if err != nil {
		return vault.Profile{}, false, err
	}
	if !o.HasLogin() {
		return vault.Profile{}, false, errors.New("no Claude login found (run `claude auth login` or `dial add claude`)")
	}
	rawAcct, _ := t.ReadAccount()
	acct := claude.ParseAccount(rawAcct)
	if acct.AccountUUID == "" || acct.OrganizationUUID == "" {
		// Credentials without a matching .claude.json: ask the API who this is.
		if o.AccessExpired() {
			if err := claude.Refresh(ctx, o); err != nil {
				return vault.Profile{}, false, err
			}
			_ = t.WriteOAuth(o)
		}
		prof, err := claude.FetchProfile(ctx, o)
		if err != nil {
			return vault.Profile{}, false, fmt.Errorf("identify Claude login: %w", err)
		}
		rawAcct = claude.AccountFromProfile(prof)
		acct = claude.ParseAccount(rawAcct)
	}
	return c.storeClaude(o, rawAcct, acct, label, origin, "")
}

func (c *Core) storeClaude(o *claude.OAuth, rawAcct []byte, acct claude.Account, label, origin, lineage string) (vault.Profile, bool, error) {
	key := acct.Key()
	if p, ok := c.Vault.FindByKey(vault.Claude, key); ok {
		cur, err := c.LoadClaude(p)
		if err == nil && cur.OAuth.Equal(o) {
			return p, false, nil
		}
		sameFamily := lineage == "" || p.Lineage == lineage
		if err == nil && cur.OAuth.HasLogin() && sameFamily && !o.Newer(cur.OAuth) {
			return p, false, nil
		}
		if err := c.SaveClaude(p, &ClaudeCreds{OAuth: o, Account: rawAcct}); err != nil {
			return p, false, err
		}
		p.Plan, p.Tier = ClaudePlan(o.Subscription(), o.RateLimitTier()), o.RateLimitTier()
		if lineage != "" {
			p.Lineage = lineage
		}
		return p, false, c.Vault.Save(p)
	}
	if label == "" {
		label = acct.EmailAddress
	}
	base := label
	if base == "" {
		base = "claude"
	}
	if lineage == "" {
		lineage = NewLineage()
	}
	p := vault.Profile{
		ID:        c.Vault.UniqueID(vault.Claude, base),
		Provider:  vault.Claude,
		Label:     label,
		Email:     acct.EmailAddress,
		Name:      acct.DisplayName,
		AccountID: acct.AccountUUID,
		OrgID:     acct.OrganizationUUID,
		OrgName:   acct.OrganizationName,
		Plan:      ClaudePlan(o.Subscription(), o.RateLimitTier()),
		Tier:      o.RateLimitTier(),
		Lineage:   lineage,
		Origin:    origin,
	}
	if err := c.Vault.Save(p); err != nil {
		return p, false, err
	}
	return p, true, c.SaveClaude(p, &ClaudeCreds{OAuth: o, Account: rawAcct})
}

// SyncBackClaude copies a newer live login in t back into its profile and
// returns that profile. Unknown live logins are imported so a manual
// `/login` is never lost.
func (c *Core) SyncBackClaude(ctx context.Context, t claude.Target) (vault.Profile, bool) {
	o, acct, err := c.LiveClaude(t)
	if err != nil || !o.HasLogin() || acct.AccountUUID == "" {
		return vault.Profile{}, false
	}
	p, ok := c.Vault.FindByKey(vault.Claude, acct.Key())
	if !ok {
		p, _, err = c.ImportClaude(ctx, t, "", "import")
		return p, err == nil
	}
	cur, err := c.LoadClaude(p)
	if err != nil || cur.OAuth.Equal(o) {
		return p, true
	}
	if o.Newer(cur.OAuth) || !cur.OAuth.HasLogin() {
		raw, _ := t.ReadAccount()
		_ = c.SaveClaude(p, &ClaudeCreds{OAuth: o, Account: raw})
	}
	return p, true
}

// UseClaude makes p the login of target t.
func (c *Core) UseClaude(ctx context.Context, p vault.Profile, t claude.Target) error {
	cr, err := c.LoadClaude(p)
	if err != nil {
		return fmt.Errorf("load %s: %w", p.ID, err)
	}
	if !cr.OAuth.HasLogin() {
		return fmt.Errorf("profile %s has no usable login; run `dial login claude:%s`", p.ID, p.ID)
	}
	c.SyncBackClaude(ctx, t)
	if len(cr.Account) == 0 {
		if prof, err := claude.FetchProfile(ctx, cr.OAuth); err == nil {
			cr.Account = claude.AccountFromProfile(prof)
			_ = c.SaveClaude(p, cr)
		}
	}
	if err := t.WriteOAuth(cr.OAuth); err != nil {
		return err
	}
	if len(cr.Account) > 0 {
		if err := t.WriteAccount(cr.Account); err != nil {
			return err
		}
	}
	for _, cli := range procs.ClaudeCLIs(procs.List()) {
		if cli.Version != "" && procs.VersionLess(cli.Version, MinLiveSwapVersion) {
			c.warn("claude %s (pid %d) is running; versions before %s keep the old login until restarted", cli.Version, cli.PID, MinLiveSwapVersion)
			break
		}
	}
	return nil
}

// ----------------------------------------------------------------- Codex

func codexLabel(id codex.Identity) string {
	if id.Email != "" {
		return id.Email
	}
	return "codex"
}

// ImportCodexAuth stores a Codex auth, creating or updating its profile.
func (c *Core) ImportCodexAuth(a *codex.Auth, label, origin, lineage string) (vault.Profile, bool, error) {
	if !a.HasLogin() {
		if a.APIKey() != "" {
			return vault.Profile{}, false, errors.New("API-key Codex auth is not supported; dial manages ChatGPT logins")
		}
		return vault.Profile{}, false, errors.New("no Codex login found (run `codex login` or `dial add codex`)")
	}
	id := a.Identity()
	if id.UserID == "" || id.AccountID == "" {
		return vault.Profile{}, false, errors.New("cannot identify Codex login (id_token missing claims)")
	}
	if p, ok := c.Vault.FindByKey(vault.Codex, id.Key()); ok {
		cur, err := c.LoadCodex(p)
		if err == nil && cur.Tokens().RefreshToken == a.Tokens().RefreshToken && !a.LastRefresh().After(cur.LastRefresh()) {
			return p, false, nil
		}
		// Same family (or unknown, i.e. a local import): newest copy wins.
		// A different lineage is a fresh login that replaces the old one.
		sameFamily := lineage == "" || p.Lineage == lineage
		if err == nil && cur.HasLogin() && sameFamily && cur.LastRefresh().After(a.LastRefresh()) {
			return p, false, nil
		}
		if err := c.SaveCodex(p, a); err != nil {
			return p, false, err
		}
		p.Plan = id.Plan
		if lineage != "" {
			p.Lineage = lineage
		}
		return p, false, c.Vault.Save(p)
	}
	if label == "" {
		label = codexLabel(id)
	}
	if lineage == "" {
		lineage = NewLineage()
	}
	p := vault.Profile{
		ID:        c.Vault.UniqueID(vault.Codex, label),
		Provider:  vault.Codex,
		Label:     label,
		Email:     id.Email,
		Name:      id.Name,
		AccountID: id.AccountID,
		UserID:    id.UserID,
		OrgName:   id.OrgName,
		Plan:      id.Plan,
		Lineage:   lineage,
		Origin:    origin,
	}
	if err := c.Vault.Save(p); err != nil {
		return p, false, err
	}
	return p, true, c.SaveCodex(p, a)
}

// SyncBackCodex copies a newer live auth.json back into its profile.
func (c *Core) SyncBackCodex(home string) (vault.Profile, bool) {
	live, err := codex.Load(filepath.Join(home, codex.AuthFile))
	if err != nil || !live.HasLogin() {
		return vault.Profile{}, false
	}
	id := live.Identity()
	p, ok := c.Vault.FindByKey(vault.Codex, id.Key())
	if !ok {
		p, _, err = c.ImportCodexAuth(live, "", "import", "")
		return p, err == nil
	}
	cur, err := c.LoadCodex(p)
	if err != nil {
		return p, true
	}
	ct, lt := cur.Tokens(), live.Tokens()
	if ct.RefreshToken == lt.RefreshToken && ct.AccessToken == lt.AccessToken {
		return p, true
	}
	if !live.LastRefresh().Before(cur.LastRefresh()) || !cur.HasLogin() {
		_ = c.SaveCodex(p, live)
	}
	return p, true
}

// UseCodex makes p the login in CODEX_HOME (CLI and desktop app).
func (c *Core) UseCodex(p vault.Profile, home string) error {
	a, err := c.LoadCodex(p)
	if err != nil {
		return fmt.Errorf("load %s: %w", p.ID, err)
	}
	if !a.HasLogin() {
		return fmt.Errorf("profile %s has no usable login; run `dial login codex:%s`", p.ID, p.ID)
	}
	c.SyncBackCodex(home)
	return a.Save(filepath.Join(home, codex.AuthFile))
}

// ------------------------------------------------------------ live state

// LiveKey returns the account key currently live in the default location.
func (c *Core) LiveKey(p vault.Provider) string {
	switch p {
	case vault.Claude:
		o, acct, err := c.LiveClaude(claude.DefaultTarget())
		if err != nil || !o.HasLogin() || acct.AccountUUID == "" {
			return ""
		}
		return acct.Key()
	case vault.Codex:
		a, err := codex.Load(codex.LivePath())
		if err != nil || !a.HasLogin() {
			return ""
		}
		return a.Identity().Key()
	}
	return ""
}

// ActiveID returns the id of the profile live in the default location.
func (c *Core) ActiveID(p vault.Provider) string {
	key := c.LiveKey(p)
	if key == "" {
		return ""
	}
	if prof, ok := c.Vault.FindByKey(p, key); ok {
		return prof.ID
	}
	return ""
}

// Use switches provider p's CLI to profile prof and records it.
func (c *Core) Use(ctx context.Context, prof vault.Profile) error {
	l, err := c.Lock()
	if err != nil {
		return err
	}
	defer l.Release()
	switch prof.Provider {
	case vault.Claude:
		err = c.UseClaude(ctx, prof, claude.DefaultTarget())
	case vault.Codex:
		err = c.UseCodex(prof, paths.CodexHome())
	default:
		err = fmt.Errorf("unknown provider %q", prof.Provider)
	}
	if err != nil {
		return err
	}
	return state.Update(func(s *state.State) {
		s.Active[string(prof.Provider)] = prof.ID
		s.Switched[string(prof.Provider)] = time.Now().UTC()
	})
}
