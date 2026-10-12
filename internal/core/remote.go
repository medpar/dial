package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/medpar/dial/internal/claude"
	"github.com/medpar/dial/internal/codex"
	"github.com/medpar/dial/internal/remote"
	"github.com/medpar/dial/internal/state"
	"github.com/medpar/dial/internal/usage"
	"github.com/medpar/dial/internal/vault"
)

// Remote sync keeps another machine (a VPS running Claude Code / Codex for
// remote control) on the same account as this one.
//
// Refresh tokens rotate and are single-use, so two machines must not share
// one login: whichever refreshes second is logged out. Each machine
// therefore holds its own token family per account, matched across
// machines by account key. `dial remote seed` mints that family here (a
// browser login on this machine) and installs it on the remote; after that
// every switch just tells the remote which account to activate.

type RemoteProfile struct {
	Provider vault.Provider `json:"provider"`
	ID       string         `json:"id"`
	Key      string         `json:"key"`
	Label    string         `json:"label"`
	Email    string         `json:"email,omitempty"`
	Plan     string         `json:"plan,omitempty"`
	Lineage  string         `json:"lineage,omitempty"`
	Active   bool           `json:"active"`
	LoggedIn bool           `json:"logged_in"`
}

type RemoteInfo struct {
	Version  string          `json:"version"`
	Host     string          `json:"host"`
	Profiles []RemoteProfile `json:"profiles"`
}

type Push struct {
	Profile vault.Profile `json:"profile"`
	Secret  Secret        `json:"secret"`
}

type ApplyRequest struct {
	Provider vault.Provider `json:"provider"`
	Key      string         `json:"key"`
	Label    string         `json:"label,omitempty"`
	Activate bool           `json:"activate"`
	// Seed installs Push as this host's own login for the account,
	// replacing any older one.
	Seed bool   `json:"seed,omitempty"`
	Push *Push  `json:"push,omitempty"`
	From string `json:"from,omitempty"`
}

type ApplyResponse struct {
	OK        bool   `json:"ok"`
	ProfileID string `json:"profile_id,omitempty"`
	Missing   bool   `json:"missing,omitempty"`
	Message   string `json:"message,omitempty"`
}

// Info describes this host for a remote caller.
func (c *Core) Info(version string) RemoteInfo {
	host, _ := os.Hostname()
	info := RemoteInfo{Version: version, Host: host}
	for _, prov := range vault.Providers {
		live := c.LiveKey(prov)
		list, _ := c.Vault.List(prov)
		for _, p := range list {
			rp := RemoteProfile{Provider: prov, ID: p.ID, Key: p.Key(), Label: p.Display(), Email: p.Email, Plan: p.Plan, Lineage: p.Lineage, Active: p.Key() == live}
			switch prov {
			case vault.Claude:
				if cr, err := c.LoadClaude(p); err == nil {
					rp.LoggedIn = cr.OAuth.HasLogin()
				}
			case vault.Codex:
				if a, err := c.LoadCodex(p); err == nil {
					rp.LoggedIn = a.HasLogin()
				}
			}
			info.Profiles = append(info.Profiles, rp)
		}
	}
	return info
}

// Apply handles a remote caller's request on this host.
func (c *Core) Apply(ctx context.Context, req ApplyRequest) ApplyResponse {
	p, ok := c.Vault.FindByKey(req.Provider, req.Key)
	if req.Push != nil {
		var err error
		p, err = c.acceptPush(ctx, req, p, ok)
		if err != nil {
			return ApplyResponse{Message: err.Error()}
		}
		ok = true
	}
	if !ok {
		return ApplyResponse{Missing: true, Message: fmt.Sprintf("no %s login for %s on this host", req.Provider, req.Label)}
	}
	// Never activate a login this host already knows is dead; report it as
	// missing so the caller offers to seed a fresh one.
	if req.Activate && req.Push == nil {
		if r, cached := usage.LoadCache().Get(string(p.Provider) + ":" + p.ID); cached && len(r.Windows) == 0 && strings.Contains(r.Error, "login expired") {
			return ApplyResponse{Missing: true, ProfileID: p.ID, Message: fmt.Sprintf("%s login for %s has expired on this host", req.Provider, req.Label)}
		}
	}
	if req.Activate {
		if err := c.Use(ctx, p); err != nil {
			return ApplyResponse{ProfileID: p.ID, Message: err.Error()}
		}
	}
	return ApplyResponse{OK: true, ProfileID: p.ID}
}

func (c *Core) acceptPush(ctx context.Context, req ApplyRequest, existing vault.Profile, exists bool) (vault.Profile, error) {
	l, err := c.Lock()
	if err != nil {
		return existing, err
	}
	defer l.Release()
	meta := req.Push.Profile
	origin := "remote"
	if req.From != "" {
		origin = "remote:" + req.From
	}
	switch req.Provider {
	case vault.Claude:
		o, err := claude.NewOAuth(req.Push.Secret.Claude)
		if err != nil || !o.HasLogin() {
			return existing, errors.New("pushed Claude credentials are empty")
		}
		raw := req.Push.Secret.Account
		acct := claude.ParseAccount(raw)
		if acct.Key() != req.Key {
			return existing, errors.New("pushed credentials do not match the requested account")
		}
		lineage := meta.Lineage
		if exists && !req.Seed && existing.Lineage != lineage {
			// This host already has its own login; never overwrite it with a copy.
			return existing, nil
		}
		p, _, err := c.storeClaude(o, raw, acct, req.Label, origin, lineage)
		return p, err
	case vault.Codex:
		a, err := codex.Parse(req.Push.Secret.Codex)
		if err != nil || !a.HasLogin() {
			return existing, errors.New("pushed Codex auth is empty")
		}
		if a.Identity().Key() != req.Key {
			return existing, errors.New("pushed credentials do not match the requested account")
		}
		if exists && !req.Seed && existing.Lineage != meta.Lineage {
			return existing, nil
		}
		p, _, err := c.ImportCodexAuth(a, req.Label, origin, meta.Lineage)
		return p, err
	}
	return existing, errors.New("unknown provider")
}

func (c *Core) Remote(name string) (state.Remote, error) {
	for _, r := range c.Config.Remotes {
		if r.Name == name {
			return r, nil
		}
	}
	return state.Remote{}, fmt.Errorf("no remote named %q (see `dial remote ls`)", name)
}

func remoteFollows(r state.Remote, p vault.Provider) bool {
	if len(r.Providers) == 0 {
		return true
	}
	for _, x := range r.Providers {
		if strings.EqualFold(x, string(p)) {
			return true
		}
	}
	return false
}

func callApply(ctx context.Context, r state.Remote, req ApplyRequest) (ApplyResponse, error) {
	body, _ := json.Marshal(req)
	out, err := remote.Run(ctx, r, []string{"remote-agent", "apply"}, body, 40*time.Second)
	var resp ApplyResponse
	if jerr := json.Unmarshal(out, &resp); jerr == nil && (resp.OK || resp.Message != "") {
		return resp, nil
	}
	if err != nil {
		return resp, err
	}
	return resp, fmt.Errorf("%s: unexpected reply from dial", r.Name)
}

// RemoteInfo fetches a remote host's profiles.
func (c *Core) RemoteInfo(ctx context.Context, r state.Remote) (RemoteInfo, error) {
	out, err := remote.Run(ctx, r, []string{"remote-agent", "info"}, nil, 20*time.Second)
	if err != nil {
		return RemoteInfo{}, err
	}
	var info RemoteInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return info, fmt.Errorf("%s: unexpected reply from dial", r.Name)
	}
	return info, nil
}

// SyncRemotes makes every auto remote follow p. It returns one result per
// remote and records them in state.
func (c *Core) SyncRemotes(ctx context.Context, p vault.Profile) []state.SyncResult {
	var targets []state.Remote
	for _, r := range c.Config.Remotes {
		if r.Auto && remoteFollows(r, p.Provider) {
			targets = append(targets, r)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	host, _ := os.Hostname()
	results := make([]state.SyncResult, len(targets))
	var wg sync.WaitGroup
	for i, r := range targets {
		wg.Add(1)
		go func(i int, r state.Remote) {
			defer wg.Done()
			res := state.SyncResult{Remote: r.Name, At: time.Now().UTC()}
			resp, err := callApply(ctx, r, ApplyRequest{Provider: p.Provider, Key: p.Key(), Label: p.Display(), Activate: true, From: host})
			switch {
			case err != nil:
				res.Message = err.Error()
			case resp.Missing:
				what := "has no login"
				if strings.Contains(resp.Message, "expired") {
					what = "has an expired login"
				}
				res.Message = fmt.Sprintf("%s %s for %s; run `dial remote seed %s %s:%s`", r.Name, what, p.Display(), r.Name, p.Provider, p.ID)
			case !resp.OK:
				res.Message = resp.Message
			default:
				res.OK = true
				res.Message = fmt.Sprintf("%s → %s", p.Provider, resp.ProfileID)
			}
			results[i] = res
		}(i, r)
	}
	wg.Wait()
	_ = state.Update(func(s *state.State) {
		for _, res := range results {
			s.Sync[res.Remote+":"+string(p.Provider)] = res
		}
	})
	return results
}

// Seed mints a fresh login for p here (browser) and installs it on r as
// r's own token family. Nothing changes locally.
func (c *Core) Seed(ctx context.Context, r state.Remote, p vault.Profile, opt LoginOptions, activate bool) (ApplyResponse, error) {
	if opt.Email == "" {
		opt.Email = p.Email
	}
	meta, secret, err := c.Mint(ctx, p.Provider, opt)
	if err != nil {
		return ApplyResponse{}, err
	}
	if meta.Key() != p.Key() {
		return ApplyResponse{}, fmt.Errorf("you signed in as %s, but %s is %s; sign in to the right account in your browser and retry", meta.Email, p.ID, p.Email)
	}
	host, _ := os.Hostname()
	return callApply(ctx, r, ApplyRequest{
		Provider: p.Provider, Key: p.Key(), Label: p.Display(), Activate: activate, Seed: true,
		Push: &Push{Profile: meta, Secret: secret}, From: host,
	})
}

// Share copies this host's login for p to r (same token family). Only safe
// when the account is used on one machine at a time; prefer Seed.
func (c *Core) Share(ctx context.Context, r state.Remote, p vault.Profile, activate bool) (ApplyResponse, error) {
	secret, err := c.ExportSecret(p)
	if err != nil {
		return ApplyResponse{}, err
	}
	host, _ := os.Hostname()
	return callApply(ctx, r, ApplyRequest{
		Provider: p.Provider, Key: p.Key(), Label: p.Display(), Activate: activate, Seed: true,
		Push: &Push{Profile: p, Secret: secret}, From: host,
	})
}
