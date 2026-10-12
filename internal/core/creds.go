package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/medpar/dial/internal/claude"
	"github.com/medpar/dial/internal/codex"
	"github.com/medpar/dial/internal/fsutil"
	"github.com/medpar/dial/internal/vault"
)

// ClaudeCreds is what dial stores for one Claude profile.
type ClaudeCreds struct {
	OAuth   *claude.OAuth
	Account json.RawMessage
}

func (c *Core) LoadClaude(p vault.Profile) (*ClaudeCreds, error) {
	dir := c.Vault.Dir(vault.Claude, p.ID)
	store, err := fsutil.ReadJSONMap(filepath.Join(dir, claude.CredsFile))
	if err != nil {
		return nil, err
	}
	o, err := claude.NewOAuth(store["claudeAiOauth"])
	if err != nil {
		return nil, err
	}
	acct, _ := os.ReadFile(filepath.Join(dir, claude.AccountFile))
	return &ClaudeCreds{OAuth: o, Account: acct}, nil
}

func (c *Core) SaveClaude(p vault.Profile, cr *ClaudeCreds) error {
	dir := c.Vault.Dir(vault.Claude, p.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := fsutil.WriteJSON(filepath.Join(dir, claude.CredsFile), map[string]json.RawMessage{"claudeAiOauth": cr.OAuth.JSON()}, 0o600); err != nil {
		return err
	}
	if len(cr.Account) > 0 {
		return fsutil.WriteFileAtomic(filepath.Join(dir, claude.AccountFile), cr.Account, 0o600)
	}
	return nil
}

func (c *Core) LoadCodex(p vault.Profile) (*codex.Auth, error) {
	return codex.Load(filepath.Join(c.Vault.Dir(vault.Codex, p.ID), codex.AuthFile))
}

func (c *Core) SaveCodex(p vault.Profile, a *codex.Auth) error {
	return a.Save(filepath.Join(c.Vault.Dir(vault.Codex, p.ID), codex.AuthFile))
}

// Secret is a provider-neutral credential bundle used for remote transfer.
type Secret struct {
	Claude  json.RawMessage `json:"claude_oauth,omitempty"`
	Account json.RawMessage `json:"claude_account,omitempty"`
	Codex   json.RawMessage `json:"codex_auth,omitempty"`
}

func (c *Core) ExportSecret(p vault.Profile) (Secret, error) {
	switch p.Provider {
	case vault.Claude:
		cr, err := c.LoadClaude(p)
		if err != nil {
			return Secret{}, err
		}
		return Secret{Claude: cr.OAuth.JSON(), Account: cr.Account}, nil
	case vault.Codex:
		a, err := c.LoadCodex(p)
		if err != nil {
			return Secret{}, err
		}
		b, err := a.Bytes()
		return Secret{Codex: b}, err
	}
	return Secret{}, fmt.Errorf("unknown provider %q", p.Provider)
}

func (c *Core) ImportSecret(p vault.Profile, s Secret) error {
	switch p.Provider {
	case vault.Claude:
		if len(s.Claude) == 0 {
			return errors.New("secret has no Claude credentials")
		}
		o, err := claude.NewOAuth(s.Claude)
		if err != nil {
			return err
		}
		return c.SaveClaude(p, &ClaudeCreds{OAuth: o, Account: s.Account})
	case vault.Codex:
		if len(s.Codex) == 0 {
			return errors.New("secret has no Codex auth")
		}
		a, err := codex.Parse(s.Codex)
		if err != nil {
			return err
		}
		return c.SaveCodex(p, a)
	}
	return fmt.Errorf("unknown provider %q", p.Provider)
}
