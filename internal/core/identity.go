package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sync"
	"time"

	"github.com/medpar/dial/internal/claude"
	"github.com/medpar/dial/internal/fsutil"
	"github.com/medpar/dial/internal/paths"
	"github.com/medpar/dial/internal/vault"
)

// Which account does a Claude credential belong to?
//
// Claude Code stores the login (keychain or .credentials.json) apart from
// the account it belongs to (oauthAccount in .claude.json), and Claude
// Desktop's built-in Claude Code rewrites .claude.json from every window
// with that window's account. Trusting .claude.json therefore attributed
// logins to the wrong profile. dial now establishes identity from the
// credential itself:
//
//  1. an exact token match with a stored profile (dial wrote it, or synced
//     it back), else
//  2. the API's answer for the access token, cached until the token
//     expires so it is asked at most once per token.
//
// An expired credential that matches nothing stays unidentified; callers
// then leave it alone rather than guess.

type identity struct {
	Key     string // "claude:<account>/<org>", "" when unknown
	Profile vault.Profile
	Found   bool            // a stored profile has this key
	Fetched *claude.Profile // set when the API was asked
	Account []byte          // oauthAccount JSON for the identified account
}

func tokenFP(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:12])
}

type idEntry struct {
	Key     string `json:"key"`
	Account []byte `json:"account,omitempty"`
	Until   int64  `json:"until"` // unix ms, the token's expiry
}

var idCacheMu sync.Mutex

func idCachePath() string { return filepath.Join(paths.DialHome(), "identity.json") }

func loadIDCache() map[string]idEntry {
	m := map[string]idEntry{}
	_ = fsutil.ReadJSON(idCachePath(), &m)
	return m
}

func saveIDCache(m map[string]idEntry) {
	now := time.Now().UnixMilli()
	for k, e := range m {
		if e.Until < now {
			delete(m, k)
		}
	}
	_ = fsutil.WriteJSON(idCachePath(), m, 0o600)
}

// identifyClaude resolves the account behind o without trusting any file
// that other programs rewrite.
func (c *Core) identifyClaude(ctx context.Context, o *claude.OAuth) identity {
	if o == nil || !o.HasLogin() {
		return identity{}
	}
	// 1. Exact token match with a stored profile.
	list, _ := c.Vault.List(vault.Claude)
	for _, p := range list {
		cr, err := c.LoadClaude(p)
		if err != nil || !cr.OAuth.HasLogin() {
			continue
		}
		if cr.OAuth.RefreshToken() == o.RefreshToken() || (o.AccessToken() != "" && cr.OAuth.AccessToken() == o.AccessToken()) {
			return identity{Key: p.Key(), Profile: p, Found: true, Account: cr.Account}
		}
	}
	// 2. Cached API answer for this access token.
	fp := tokenFP(o.AccessToken())
	idCacheMu.Lock()
	cache := loadIDCache()
	e, hit := cache[fp]
	idCacheMu.Unlock()
	if hit && e.Key != "" && e.Until > time.Now().UnixMilli() {
		id := identity{Key: e.Key, Account: e.Account}
		id.Profile, id.Found = c.Vault.FindByKey(vault.Claude, e.Key)
		return id
	}
	if o.AccessExpired() {
		return identity{}
	}
	// 3. Ask the API.
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	prof, err := claude.FetchProfile(cctx, o)
	if err != nil || prof.Account.UUID == "" || prof.Organization.UUID == "" {
		return identity{}
	}
	acct := claude.AccountFromProfile(prof)
	key := "claude:" + prof.Account.UUID + "/" + prof.Organization.UUID
	idCacheMu.Lock()
	cache = loadIDCache()
	cache[fp] = idEntry{Key: key, Account: acct, Until: o.ExpiresAt()}
	saveIDCache(cache)
	idCacheMu.Unlock()
	id := identity{Key: key, Fetched: &prof, Account: acct}
	id.Profile, id.Found = c.Vault.FindByKey(vault.Claude, key)
	return id
}

// liveClaude reads target t's login and identifies it.
func (c *Core) liveClaude(ctx context.Context, t claude.Target) (*claude.OAuth, identity, error) {
	o, err := t.ReadOAuth()
	if err != nil {
		return nil, identity{}, err
	}
	return o, c.identifyClaude(ctx, o), nil
}
