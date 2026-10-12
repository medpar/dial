// Package codex reads, writes, refreshes and meters OpenAI Codex logins
// (the auth.json used by both the Codex CLI and the Codex desktop app).
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/medpar/dial/internal/fsutil"
	"github.com/medpar/dial/internal/httpx"
	"github.com/medpar/dial/internal/jwt"
	"github.com/medpar/dial/internal/paths"
	"github.com/medpar/dial/internal/usage"
)

// Endpoints are variables so tests can point them at a local server.
var (
	TokenURL = "https://auth.openai.com/oauth/token"
	UsageURL = "https://chatgpt.com/backend-api/wham/usage"
)

const (
	ClientID   = "app_EMoamEEZ73f0CkXaXp7hrann"
	AuthFile   = "auth.json"
	authClaim  = "https://api.openai.com/auth"
	userAgent  = "codex_cli_rs/0.160.0 (dial)"
	refreshAge = 8 * 24 * time.Hour
)

// Auth is auth.json. Raw keeps unknown top-level keys intact on rewrite.
type Auth struct {
	Raw map[string]json.RawMessage
}

type Tokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id,omitempty"`
}

func Parse(data []byte) (*Auth, error) {
	a := &Auth{Raw: map[string]json.RawMessage{}}
	if err := json.Unmarshal(data, &a.Raw); err != nil {
		return nil, err
	}
	return a, nil
}

func Load(path string) (*Auth, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

func (a *Auth) Bytes() ([]byte, error) { return fsutil.MarshalIndent(a.Raw) }

func (a *Auth) Save(path string) error {
	data, err := a.Bytes()
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data, 0o600)
}

func (a *Auth) Tokens() Tokens {
	var t Tokens
	_ = json.Unmarshal(a.Raw["tokens"], &t)
	return t
}

func (a *Auth) SetTokens(t Tokens) {
	b, _ := json.Marshal(t)
	a.Raw["tokens"] = b
}

func (a *Auth) APIKey() string {
	var s string
	_ = json.Unmarshal(a.Raw["OPENAI_API_KEY"], &s)
	return s
}

// LastRefresh is the CLI's own refresh timestamp, used to pick the newest
// copy of a token family.
func (a *Auth) LastRefresh() time.Time {
	var s string
	_ = json.Unmarshal(a.Raw["last_refresh"], &s)
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (a *Auth) setLastRefresh(t time.Time) {
	b, _ := json.Marshal(t.UTC().Format(time.RFC3339Nano))
	a.Raw["last_refresh"] = b
}

// HasLogin reports whether this auth carries a ChatGPT login.
func (a *Auth) HasLogin() bool { return a.Tokens().RefreshToken != "" }

// Identity is what the id_token says about the login.
type Identity struct {
	Email     string
	Name      string
	UserID    string
	AccountID string
	Plan      string
	OrgName   string
	ActiveTil string
}

func (a *Auth) Identity() Identity {
	t := a.Tokens()
	id := Identity{AccountID: t.AccountID}
	c, err := jwt.Claims(t.IDToken)
	if err != nil {
		return id
	}
	id.Email, _ = c["email"].(string)
	id.Name, _ = c["name"].(string)
	if au, ok := c[authClaim].(map[string]any); ok {
		if s, _ := au["chatgpt_user_id"].(string); s != "" {
			id.UserID = s
		} else if s, _ := au["user_id"].(string); s != "" {
			id.UserID = s
		}
		if s, _ := au["chatgpt_account_id"].(string); s != "" {
			id.AccountID = s
		}
		id.Plan, _ = au["chatgpt_plan_type"].(string)
		id.ActiveTil, _ = au["chatgpt_subscription_active_until"].(string)
	}
	if id.UserID == "" {
		id.UserID, _ = c["sub"].(string)
	}
	return id
}

// Key matches vault.Profile.Key for codex profiles.
func (id Identity) Key() string { return "codex:" + id.UserID + "/" + id.AccountID }

// LivePath is the auth.json the CLI and desktop app read.
func LivePath() string { return filepath.Join(paths.CodexHome(), AuthFile) }

// Refresh exchanges the refresh token for new tokens. OpenAI rotates refresh
// tokens, so the caller must persist the result before anything else uses
// the old copy.
func Refresh(ctx context.Context, a *Auth) error {
	t := a.Tokens()
	if t.RefreshToken == "" {
		return errors.New("no refresh token")
	}
	var resp struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	err := httpx.Do(ctx, "POST", TokenURL, map[string]string{"User-Agent": userAgent}, map[string]string{
		"client_id":     ClientID,
		"grant_type":    "refresh_token",
		"refresh_token": t.RefreshToken,
		"scope":         "openid profile email",
	}, &resp)
	if err != nil {
		return fmt.Errorf("codex token refresh: %w", err)
	}
	if resp.IDToken != "" {
		t.IDToken = resp.IDToken
	}
	if resp.AccessToken != "" {
		t.AccessToken = resp.AccessToken
	}
	if resp.RefreshToken != "" {
		t.RefreshToken = resp.RefreshToken
	}
	a.SetTokens(t)
	a.setLastRefresh(time.Now())
	return nil
}

// NeedsRefresh mirrors the CLI: refresh when the access token is about to
// expire or the last refresh is older than eight days.
func NeedsRefresh(a *Auth) bool {
	t := a.Tokens()
	if exp := jwt.Exp(t.AccessToken); exp > 0 && time.Until(time.Unix(exp, 0)) < 10*time.Minute {
		return true
	}
	lr := a.LastRefresh()
	return !lr.IsZero() && time.Since(lr) > refreshAge
}

// AccessExpired reports whether the access token can no longer be used.
func AccessExpired(a *Auth) bool {
	exp := jwt.Exp(a.Tokens().AccessToken)
	return exp > 0 && time.Now().After(time.Unix(exp, 0).Add(-time.Minute))
}

type window struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type usageResp struct {
	Email     string `json:"email"`
	PlanType  string `json:"plan_type"`
	RateLimit *struct {
		Allowed         bool    `json:"allowed"`
		LimitReached    bool    `json:"limit_reached"`
		PrimaryWindow   *window `json:"primary_window"`
		SecondaryWindow *window `json:"secondary_window"`
	} `json:"rate_limit"`
	CodeReview *struct {
		PrimaryWindow *window `json:"primary_window"`
	} `json:"code_review_rate_limit"`
	Credits *struct {
		HasCredits bool   `json:"has_credits"`
		Unlimited  bool   `json:"unlimited"`
		Balance    string `json:"balance"`
	} `json:"credits"`
	ResetCredits *struct {
		AvailableCount int `json:"available_count"`
	} `json:"rate_limit_reset_credits"`
}

// FetchUsage calls the same endpoint the Codex CLI uses for /status.
func FetchUsage(ctx context.Context, a *Auth) (usage.Report, error) {
	t := a.Tokens()
	h := map[string]string{
		"Authorization": "Bearer " + t.AccessToken,
		"User-Agent":    userAgent,
	}
	if id := a.Identity(); id.AccountID != "" {
		h["ChatGPT-Account-Id"] = id.AccountID
	}
	var r usageResp
	if err := httpx.Do(ctx, "GET", UsageURL, h, nil, &r); err != nil {
		return usage.Report{}, err
	}
	rep := usage.Report{Provider: "codex", Email: r.Email, Plan: r.PlanType, FetchedAt: time.Now().UTC()}
	add := func(w *window, prefix string) {
		if w == nil {
			return
		}
		key, label := usage.WindowLabel(w.LimitWindowSeconds)
		win := usage.Window{Key: prefix + key, Label: label, UsedPercent: w.UsedPercent, WindowSeconds: w.LimitWindowSeconds}
		if w.ResetAt > 0 {
			ts := time.Unix(w.ResetAt, 0).UTC()
			win.ResetsAt = &ts
		} else if w.ResetAfterSeconds > 0 {
			ts := time.Now().Add(time.Duration(w.ResetAfterSeconds) * time.Second).UTC()
			win.ResetsAt = &ts
		}
		if prefix != "" {
			win.Label = "Code review · " + strings.ToLower(win.Label)
		}
		rep.Windows = append(rep.Windows, win)
	}
	if r.RateLimit != nil {
		add(r.RateLimit.PrimaryWindow, "")
		add(r.RateLimit.SecondaryWindow, "")
		rep.LimitReached = r.RateLimit.LimitReached
	}
	if r.CodeReview != nil {
		add(r.CodeReview.PrimaryWindow, "review-")
	}
	if r.Credits != nil && (r.Credits.HasCredits || r.Credits.Unlimited) {
		v := r.Credits.Balance
		if r.Credits.Unlimited {
			v = "unlimited"
		}
		rep.Extra = append(rep.Extra, usage.Extra{Label: "Credits", Value: v})
	}
	if r.ResetCredits != nil && r.ResetCredits.AvailableCount > 0 {
		rep.Extra = append(rep.Extra, usage.Extra{Label: "Limit resets available", Value: fmt.Sprint(r.ResetCredits.AvailableCount)})
	}
	return rep, nil
}
