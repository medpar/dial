package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/medpar/dial/internal/claude"
	"github.com/medpar/dial/internal/codex"
	"github.com/medpar/dial/internal/httpx"
	"github.com/medpar/dial/internal/paths"
	"github.com/medpar/dial/internal/procs"
	"github.com/medpar/dial/internal/usage"
	"github.com/medpar/dial/internal/vault"
)

// minRefreshGap stops dial hammering the token endpoint, which refuses
// bursts from one IP.
const minRefreshGap = 11 * time.Minute

type refreshError struct{ err error }

func (e *refreshError) Error() string { return e.err.Error() }
func (e *refreshError) Unwrap() error { return e.err }

func cacheKey(p vault.Profile) string { return string(p.Provider) + ":" + p.ID }

// UsageOptions controls one usage pass.
type UsageOptions struct {
	MaxAge time.Duration // reuse cached reports younger than this
	Force  bool          // ignore cache (still honours 429 backoff)
}

type liveInfo struct {
	claudeKey     string
	codexKey      string
	claudeCLIBusy bool
	codexCLIBusy  bool
	// claudeUnknown: the CLI holds a Claude login dial can't identify yet.
	// It could be any profile's account, so no Claude profile is refreshed
	// until it is identified (a refresh with a superseded token can revoke
	// the live one).
	claudeUnknown bool
}

func (c *Core) live() liveInfo {
	ps := procs.List()
	li := liveInfo{
		codexKey:      c.LiveKey(vault.Codex),
		claudeCLIBusy: len(procs.ClaudeCLIs(ps)) > 0,
		codexCLIBusy:  len(procs.CodexCLIs(ps)) > 0,
	}
	if o, id, err := c.liveClaude(context.Background(), claude.DefaultTarget()); err == nil && o.HasLogin() {
		li.claudeKey = id.Key
		li.claudeUnknown = id.Key == ""
	}
	return li
}

// Usage returns reports for profiles, fetching stale ones concurrently.
func (c *Core) Usage(ctx context.Context, profiles []vault.Profile, opt UsageOptions) []usage.Report {
	cache := usage.LoadCache()
	c.healLiveClaude(ctx)
	li := c.live()
	out := make([]usage.Report, len(profiles))
	var wg sync.WaitGroup
	for i, p := range profiles {
		cached, ok := cache.Get(cacheKey(p))
		if ok && cached.RetryAfter != nil && time.Now().Before(*cached.RetryAfter) {
			cached.Stale = true
			out[i] = cached
			continue
		}
		if ok && !opt.Force && time.Since(cached.FetchedAt) < opt.MaxAge && cached.Error == "" {
			out[i] = cached
			continue
		}
		wg.Add(1)
		go func(i int, p vault.Profile, cached usage.Report, had bool) {
			defer wg.Done()
			// Stagger requests so a burst never trips the usage endpoints.
			time.Sleep(time.Duration(i%6) * 150 * time.Millisecond)
			rep, err := c.fetchOne(ctx, p, li, cached)
			if err != nil {
				if had && cached.Error == "" {
					cached.Stale = true
					cached.Error = err.Error()
					rep = cached
				} else {
					rep = usage.Report{FetchedAt: time.Now().UTC(), Error: err.Error()}
				}
				var re *refreshError
				if errors.As(err, &re) {
					now := time.Now().UTC()
					rep.RefreshTriedAt = &now
				} else if cached.RefreshTriedAt != nil {
					rep.RefreshTriedAt = cached.RefreshTriedAt
				}
				var se *httpx.StatusError
				if errors.As(err, &se) && se.Status == 429 {
					ra := time.Now().Add(5 * time.Minute)
					rep.RetryAfter = &ra
				}
			}
			rep.Provider, rep.ProfileID = string(p.Provider), p.ID
			if rep.Email == "" {
				rep.Email = p.Email
			}
			out[i] = rep
			cache.Put(cacheKey(p), rep)
		}(i, p, cached, ok)
	}
	wg.Wait()
	_ = cache.Save()
	return out
}

func (c *Core) fetchOne(ctx context.Context, p vault.Profile, li liveInfo, prev usage.Report) (usage.Report, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	switch p.Provider {
	case vault.Claude:
		return c.claudeUsage(ctx, p, li, prev)
	case vault.Codex:
		return c.codexUsage(ctx, p, li, prev)
	}
	return usage.Report{}, fmt.Errorf("unknown provider %q", p.Provider)
}

func (c *Core) claudeUsage(ctx context.Context, p vault.Profile, li liveInfo, prev usage.Report) (usage.Report, error) {
	isLive := li.claudeKey != "" && li.claudeKey == p.Key()
	var o *claude.OAuth
	if isLive {
		// The CLI may have refreshed since dial last looked: trust live first.
		l, err := c.Lock()
		if err != nil {
			return usage.Report{}, err
		}
		c.SyncBackClaude(ctx, claude.DefaultTarget())
		l.Release()
	}
	cr, err := c.LoadClaude(p)
	if err != nil {
		return usage.Report{}, err
	}
	o = cr.OAuth
	if !o.HasLogin() {
		return usage.Report{}, errors.New("logged out")
	}
	if o.AccessExpired() {
		if isLive && li.claudeCLIBusy {
			return usage.Report{}, errors.New("token expired; the running claude CLI will refresh it")
		}
		if !isLive && li.claudeUnknown {
			return usage.Report{}, errors.New("token expired; waiting until the CLI's own login is identified before refreshing")
		}
		if err := c.refreshClaude(ctx, p, isLive, prev, false); err != nil {
			return usage.Report{}, expired(p, err)
		}
		cr, err = c.LoadClaude(p)
		if err != nil {
			return usage.Report{}, err
		}
		o = cr.OAuth
	}
	rep, err := claude.FetchUsage(ctx, o)
	if isUnauthorized(err) && !(isLive && li.claudeCLIBusy) {
		// Revoked before expiry (rotated elsewhere): one refresh, one retry.
		if rerr := c.refreshClaude(ctx, p, isLive, prev, true); rerr != nil {
			return rep, fmt.Errorf("login expired; run `dial login claude:%s` (%v)", p.ID, rerr)
		}
		if cr, err = c.LoadClaude(p); err != nil {
			return rep, err
		}
		o = cr.OAuth
		rep, err = claude.FetchUsage(ctx, o)
	}
	if err != nil {
		return rep, err
	}
	rep.Plan = ClaudePlan(o.Subscription(), o.RateLimitTier())
	return rep, nil
}

// refreshClaude refreshes p under the lock with compare-and-swap, and also
// updates the live store when p is live (no CLI is running then).
func (c *Core) refreshClaude(ctx context.Context, p vault.Profile, isLive bool, prev usage.Report, force bool) error {
	if prev.RefreshTriedAt != nil && time.Since(*prev.RefreshTriedAt) < minRefreshGap && prev.Error != "" {
		// Keep the last verdict (e.g. "login expired") while backing off.
		return &refreshError{errors.New(prev.Error)}
	}
	l, err := c.Lock()
	if err != nil {
		return err
	}
	defer l.Release()
	cr, err := c.LoadClaude(p)
	if err != nil {
		return err
	}
	if !force && !cr.OAuth.AccessExpired() {
		return nil // someone else refreshed while we waited
	}
	before := string(cr.OAuth.JSON())
	if err := claude.Refresh(ctx, cr.OAuth); err != nil {
		return &refreshError{err}
	}
	again, err := c.LoadClaude(p)
	if err == nil && string(again.OAuth.JSON()) != before {
		return errors.New("credentials changed during refresh; discarded")
	}
	if err := c.SaveClaude(p, cr); err != nil {
		return err
	}
	if isLive {
		t := claude.DefaultTarget()
		if lo, id, err := c.liveClaude(ctx, t); err == nil && id.Key == p.Key() && lo.RefreshToken() != cr.OAuth.RefreshToken() {
			_ = t.WriteOAuth(cr.OAuth)
		}
	}
	return nil
}

func (c *Core) codexUsage(ctx context.Context, p vault.Profile, li liveInfo, prev usage.Report) (usage.Report, error) {
	isLive := li.codexKey != "" && li.codexKey == p.Key()
	if isLive {
		l, err := c.Lock()
		if err != nil {
			return usage.Report{}, err
		}
		c.SyncBackCodex(paths.CodexHome())
		l.Release()
	}
	a, err := c.LoadCodex(p)
	if err != nil {
		return usage.Report{}, err
	}
	if !a.HasLogin() {
		return usage.Report{}, errors.New("logged out")
	}
	if codex.AccessExpired(a) {
		if isLive && li.codexCLIBusy {
			return usage.Report{}, errors.New("token expired; the running codex will refresh it")
		}
		if err := c.refreshCodex(ctx, p, isLive, prev); err != nil {
			return usage.Report{}, expired(p, err)
		}
		if a, err = c.LoadCodex(p); err != nil {
			return usage.Report{}, err
		}
	}
	rep, err := codex.FetchUsage(ctx, a)
	if isUnauthorized(err) && !(isLive && li.codexCLIBusy) {
		if rerr := c.refreshCodex(ctx, p, isLive, prev); rerr != nil {
			return rep, fmt.Errorf("login expired; run `dial login codex:%s` (%v)", p.ID, rerr)
		}
		if a, err = c.LoadCodex(p); err != nil {
			return rep, err
		}
		rep, err = codex.FetchUsage(ctx, a)
	}
	return rep, err
}

// expired labels a refresh the provider refused (400/401) as a dead login,
// which `dial use` and remotes refuse to activate. Network errors pass
// through unchanged.
func expired(p vault.Profile, err error) error {
	var se *httpx.StatusError
	if errors.As(err, &se) && (se.Status == 400 || se.Status == 401) {
		return &refreshError{fmt.Errorf("login expired; run `dial login %s:%s` (%v)", p.Provider, p.ID, err)}
	}
	return err
}

func isUnauthorized(err error) bool {
	var se *httpx.StatusError
	return errors.As(err, &se) && se.Status == 401
}

func (c *Core) refreshCodex(ctx context.Context, p vault.Profile, isLive bool, prev usage.Report) error {
	if prev.RefreshTriedAt != nil && time.Since(*prev.RefreshTriedAt) < minRefreshGap && prev.Error != "" {
		// Keep the last verdict (e.g. "login expired") while backing off.
		return &refreshError{errors.New(prev.Error)}
	}
	l, err := c.Lock()
	if err != nil {
		return err
	}
	defer l.Release()
	a, err := c.LoadCodex(p)
	if err != nil {
		return err
	}
	before := a.Tokens().RefreshToken
	if err := codex.Refresh(ctx, a); err != nil {
		return &refreshError{err}
	}
	if again, err := c.LoadCodex(p); err == nil && again.Tokens().RefreshToken != before {
		return errors.New("credentials changed during refresh; discarded")
	}
	if err := c.SaveCodex(p, a); err != nil {
		return err
	}
	if isLive {
		if live, err := codex.Load(codex.LivePath()); err == nil && live.Identity().Key() == p.Key() && live.Tokens().RefreshToken == before {
			_ = a.Save(codex.LivePath())
		}
	}
	return nil
}

// KeepAlive refreshes stored logins that are not live anywhere and are
// close to going stale, so switching never needs a new browser login.
// Codex: last refresh older than 5 days. Claude: refresh token expiring
// within 3 days. Returns the ids it refreshed.
func (c *Core) KeepAlive(ctx context.Context) []string {
	c.healLiveClaude(ctx)
	li := c.live()
	var done []string
	all, _ := c.Vault.All()
	for _, p := range all {
		switch p.Provider {
		case vault.Codex:
			if p.Key() == li.codexKey || c.instanceRunning(p) {
				continue
			}
			a, err := c.LoadCodex(p)
			if err != nil || !a.HasLogin() {
				continue
			}
			if lr := a.LastRefresh(); !lr.IsZero() && time.Since(lr) < 5*24*time.Hour {
				continue
			}
			if c.refreshCodex(ctx, p, false, usage.Report{}) == nil {
				done = append(done, "codex:"+p.ID)
			}
		case vault.Claude:
			if p.Key() == li.claudeKey || li.claudeUnknown || c.instanceRunning(p) {
				continue
			}
			cr, err := c.LoadClaude(p)
			if err != nil || !cr.OAuth.HasLogin() {
				continue
			}
			rexp := cr.OAuth.RefreshExpiresAt()
			if rexp == 0 || time.Until(time.UnixMilli(rexp)) > 3*24*time.Hour {
				continue
			}
			if !cr.OAuth.AccessExpired() {
				continue
			}
			if c.refreshClaude(ctx, p, false, usage.Report{}, false) == nil {
				done = append(done, "claude:"+p.ID)
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return done
}
