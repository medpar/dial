// Package claude reads, writes, refreshes and meters Claude Code logins.
//
// Live locations, matching Claude Code 2.1.x:
//   - credentials: macOS keychain generic password "Claude Code-credentials"
//     (plus "-<sha256(configDir)[:8]>" when CLAUDE_CONFIG_DIR is set),
//     account $USER; Linux <configDir>/.credentials.json. The JSON holds
//     claudeAiOauth and other keys (mcpOAuth) that must be preserved.
//   - identity: oauthAccount in ~/.claude.json (or <configDir>/.claude.json).
package claude

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/medpar/dial/internal/fsutil"
	"github.com/medpar/dial/internal/httpx"
	"github.com/medpar/dial/internal/keychain"
	"github.com/medpar/dial/internal/paths"
	"github.com/medpar/dial/internal/usage"
)

// Endpoints are variables so tests can point them at a local server.
var (
	TokenURL   = "https://platform.claude.com/v1/oauth/token"
	UsageURL   = "https://api.anthropic.com/api/oauth/usage"
	ProfileURL = "https://api.anthropic.com/api/oauth/profile"
)

const (
	ClientID   = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	BetaHeader = "oauth-2025-04-20"
	userAgent  = "claude-cli/2.1.274 (external, cli)"

	CredsFile   = "credentials.json" // in a dial profile dir
	AccountFile = "account.json"     // oauthAccount snapshot
)

var DefaultScopes = []string{"user:file_upload", "user:inference", "user:mcp_servers", "user:plugins", "user:profile", "user:sessions:claude_code"}

// OAuth is the claudeAiOauth object. Raw preserves fields dial does not
// model so a round-trip never drops data newer CLIs add.
type OAuth struct {
	Raw map[string]json.RawMessage
}

func NewOAuth(raw json.RawMessage) (*OAuth, error) {
	o := &OAuth{Raw: map[string]json.RawMessage{}}
	if len(raw) == 0 || string(raw) == "null" {
		return o, nil
	}
	if err := json.Unmarshal(raw, &o.Raw); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *OAuth) str(k string) string {
	var s string
	_ = json.Unmarshal(o.Raw[k], &s)
	return s
}

func (o *OAuth) num(k string) int64 {
	var f float64
	_ = json.Unmarshal(o.Raw[k], &f)
	return int64(f)
}

func (o *OAuth) set(k string, v any) {
	b, _ := json.Marshal(v)
	o.Raw[k] = b
}

func (o *OAuth) AccessToken() string     { return o.str("accessToken") }
func (o *OAuth) RefreshToken() string    { return o.str("refreshToken") }
func (o *OAuth) ExpiresAt() int64        { return o.num("expiresAt") }
func (o *OAuth) Subscription() string    { return o.str("subscriptionType") }
func (o *OAuth) RefreshExpiresAt() int64 { return o.num("refreshTokenExpiresAt") }
func (o *OAuth) RateLimitTier() string {
	return o.str("rateLimitTier")
}

func (o *OAuth) Scopes() []string {
	var s []string
	_ = json.Unmarshal(o.Raw["scopes"], &s)
	return s
}

// HasLogin reports whether these credentials can authenticate.
func (o *OAuth) HasLogin() bool { return o != nil && o.RefreshToken() != "" }

// AccessExpired reports whether the access token is expired or about to be.
func (o *OAuth) AccessExpired() bool {
	exp := o.ExpiresAt()
	return exp == 0 || time.Now().Add(2*time.Minute).UnixMilli() >= exp
}

// Newer reports whether o is a later state of the same token family than p.
func (o *OAuth) Newer(p *OAuth) bool {
	if p == nil || !p.HasLogin() {
		return o.HasLogin()
	}
	return o.HasLogin() && o.ExpiresAt() > p.ExpiresAt()
}

func (o *OAuth) Equal(p *OAuth) bool {
	return p != nil && o.AccessToken() == p.AccessToken() && o.RefreshToken() == p.RefreshToken()
}

func (o *OAuth) JSON() json.RawMessage {
	b, _ := json.Marshal(o.Raw)
	return b
}

// Target is one Claude Code installation: the default one, or an isolated
// CLAUDE_CONFIG_DIR used to run a profile side by side.
type Target struct {
	ConfigDir string
	Custom    bool // CLAUDE_CONFIG_DIR is set for this target
}

// DefaultTarget is what a bare `claude` in this environment would use.
func DefaultTarget() Target {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return Target{ConfigDir: d, Custom: true}
	}
	return Target{ConfigDir: filepath.Join(paths.Home(), ".claude")}
}

// DirTarget is a profile-isolated installation rooted at dir.
func DirTarget(dir string) Target { return Target{ConfigDir: dir, Custom: true} }

// GlobalConfig is the .claude.json path for t.
func (t Target) GlobalConfig() string {
	if t.Custom {
		return filepath.Join(t.ConfigDir, ".claude.json")
	}
	return filepath.Join(paths.Home(), ".claude.json")
}

// KeychainService mirrors Claude Code's service-name derivation.
func (t Target) KeychainService() string {
	if !t.Custom {
		return "Claude Code-credentials"
	}
	// Claude Code NFC-normalises the path first; dial paths are ASCII.
	sum := sha256.Sum256([]byte(t.ConfigDir))
	return "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
}

func (t Target) credentialsFile() string { return filepath.Join(t.ConfigDir, ".credentials.json") }

var userRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func keychainAccount() string {
	name := os.Getenv("USER")
	if name == "" {
		if u, err := user.Current(); err == nil {
			name = u.Username
		}
	}
	if !userRe.MatchString(name) {
		return "claude-code-user"
	}
	return name
}

// readStore returns the whole credential store JSON for t.
func (t Target) readStore() (map[string]json.RawMessage, error) {
	store := map[string]json.RawMessage{}
	if keychain.Available() {
		data, err := keychain.Get(t.KeychainService(), keychainAccount())
		if errors.Is(err, keychain.ErrNotFound) {
			return store, nil
		}
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return store, nil
		}
		if err := json.Unmarshal(data, &store); err != nil {
			return nil, fmt.Errorf("parse keychain credentials: %w", err)
		}
		return store, nil
	}
	err := fsutil.ReadJSON(t.credentialsFile(), &store)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	return store, err
}

func (t Target) writeStore(store map[string]json.RawMessage) error {
	data, err := json.Marshal(store)
	if err != nil {
		return err
	}
	if keychain.Available() {
		return keychain.Set(t.KeychainService(), keychainAccount(), data)
	}
	return fsutil.WriteFileAtomic(t.credentialsFile(), data, 0o600)
}

// ReadOAuth returns the live claudeAiOauth for t (empty if logged out).
func (t Target) ReadOAuth() (*OAuth, error) {
	store, err := t.readStore()
	if err != nil {
		return nil, err
	}
	return NewOAuth(store["claudeAiOauth"])
}

// WriteOAuth replaces claudeAiOauth and keeps every other key (mcpOAuth…).
func (t Target) WriteOAuth(o *OAuth) error {
	store, err := t.readStore()
	if err != nil {
		return err
	}
	store["claudeAiOauth"] = o.JSON()
	return t.writeStore(store)
}

// ReadAccount returns the raw oauthAccount object from .claude.json.
func (t Target) ReadAccount() (json.RawMessage, error) {
	m, err := fsutil.ReadJSONMap(t.GlobalConfig())
	if err != nil {
		return nil, err
	}
	return m["oauthAccount"], nil
}

// WriteAccount swaps oauthAccount in .claude.json, preserving all else.
func (t Target) WriteAccount(acct json.RawMessage) error {
	path := t.GlobalConfig()
	m, err := fsutil.ReadJSONMap(path)
	if err != nil {
		return err
	}
	if len(acct) == 0 {
		delete(m, "oauthAccount")
	} else {
		m["oauthAccount"] = acct
	}
	perm := fs.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		perm = st.Mode().Perm()
	}
	data, err := fsutil.MarshalIndent(m)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data, perm)
}

// Account is the subset of oauthAccount dial displays and matches on.
type Account struct {
	AccountUUID      string `json:"accountUuid"`
	EmailAddress     string `json:"emailAddress"`
	OrganizationUUID string `json:"organizationUuid"`
	OrganizationName string `json:"organizationName"`
	OrganizationType string `json:"organizationType"`
	DisplayName      string `json:"displayName"`
	BillingType      string `json:"billingType"`
	SeatTier         string `json:"seatTier"`
	OrgRateLimitTier string `json:"organizationRateLimitTier"`
}

func ParseAccount(raw json.RawMessage) Account {
	var a Account
	_ = json.Unmarshal(raw, &a)
	return a
}

func (a Account) Key() string { return "claude:" + a.AccountUUID + "/" + a.OrganizationUUID }

// Refresh exchanges the refresh token. Anthropic rotates refresh tokens, so
// the result must be persisted before the old copy is used anywhere.
func Refresh(ctx context.Context, o *OAuth) error {
	if !o.HasLogin() {
		return errors.New("no refresh token")
	}
	scopes := o.Scopes()
	if len(scopes) == 0 {
		scopes = DefaultScopes
	}
	var resp struct {
		AccessToken           string `json:"access_token"`
		RefreshToken          string `json:"refresh_token"`
		ExpiresIn             int64  `json:"expires_in"`
		RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
		Scope                 string `json:"scope"`
	}
	err := httpx.Do(ctx, "POST", TokenURL, map[string]string{"User-Agent": userAgent}, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": o.RefreshToken(),
		"client_id":     ClientID,
		"scope":         strings.Join(scopes, " "),
	}, &resp)
	if err != nil {
		return fmt.Errorf("claude token refresh: %w", err)
	}
	if resp.AccessToken == "" {
		return errors.New("claude token refresh: empty access token")
	}
	now := time.Now()
	o.set("accessToken", resp.AccessToken)
	if resp.RefreshToken != "" {
		o.set("refreshToken", resp.RefreshToken)
	}
	o.set("expiresAt", now.Add(time.Duration(resp.ExpiresIn)*time.Second).UnixMilli())
	if resp.RefreshTokenExpiresIn > 0 {
		o.set("refreshTokenExpiresAt", now.Add(time.Duration(resp.RefreshTokenExpiresIn)*time.Second).UnixMilli())
	}
	if resp.Scope != "" {
		o.set("scopes", strings.Fields(resp.Scope))
	}
	return nil
}

func authHeaders(o *OAuth) map[string]string {
	return map[string]string{
		"Authorization":  "Bearer " + o.AccessToken(),
		"anthropic-beta": BetaHeader,
		"User-Agent":     userAgent,
	}
}

// Profile is the /api/oauth/profile response subset dial uses.
type Profile struct {
	Account struct {
		UUID        string `json:"uuid"`
		Email       string `json:"email"`
		FullName    string `json:"full_name"`
		DisplayName string `json:"display_name"`
		HasMax      bool   `json:"has_claude_max"`
		HasPro      bool   `json:"has_claude_pro"`
		CreatedAt   string `json:"created_at"`
	} `json:"account"`
	Organization struct {
		UUID              string `json:"uuid"`
		Name              string `json:"name"`
		OrganizationType  string `json:"organization_type"`
		BillingType       string `json:"billing_type"`
		RateLimitTier     string `json:"rate_limit_tier"`
		HasExtraUsage     bool   `json:"has_extra_usage_enabled"`
		SubscriptionStart string `json:"subscription_created_at"`
	} `json:"organization"`
}

func FetchProfile(ctx context.Context, o *OAuth) (Profile, error) {
	var p Profile
	err := httpx.Do(ctx, "GET", ProfileURL, authHeaders(o), nil, &p)
	return p, err
}

// AccountFromProfile builds an oauthAccount object the CLI accepts when
// dial imported credentials without a matching .claude.json.
func AccountFromProfile(p Profile) json.RawMessage {
	m := map[string]any{
		"accountUuid":      p.Account.UUID,
		"emailAddress":     p.Account.Email,
		"organizationUuid": p.Organization.UUID,
		"organizationName": p.Organization.Name,
		"displayName":      p.Account.DisplayName,
		"fullName":         p.Account.FullName,
	}
	if p.Organization.BillingType != "" {
		m["billingType"] = p.Organization.BillingType
	}
	if p.Organization.OrganizationType != "" {
		m["organizationType"] = p.Organization.OrganizationType
	}
	if p.Organization.RateLimitTier != "" {
		m["organizationRateLimitTier"] = p.Organization.RateLimitTier
	}
	m["hasExtraUsageEnabled"] = p.Organization.HasExtraUsage
	b, _ := json.Marshal(m)
	return b
}

// FetchUsage reads subscription utilisation for the account behind o.
// The modern response lists every limit in limits[]; older responses only
// had the five_hour / seven_day* objects, which remain the fallback.
func FetchUsage(ctx context.Context, o *OAuth) (usage.Report, error) {
	var raw map[string]json.RawMessage
	if err := httpx.Do(ctx, "GET", UsageURL, authHeaders(o), nil, &raw); err != nil {
		return usage.Report{}, err
	}
	rep := usage.Report{Provider: "claude", Plan: o.Subscription(), FetchedAt: time.Now().UTC()}
	rep.Windows = parseLimits(raw["limits"])
	if len(rep.Windows) == 0 {
		rep.Windows = parseLegacyWindows(raw)
	}
	for _, w := range rep.Windows {
		if w.UsedPercent >= 100 {
			rep.LimitReached = true
		}
	}
	rep.Extra = parseSpend(raw)
	return rep, nil
}

func parseTime(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	ts, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		return nil
	}
	ts = ts.UTC()
	return &ts
}

func parseLimits(data json.RawMessage) []usage.Window {
	var limits []struct {
		Kind     string   `json:"kind"`
		Group    string   `json:"group"`
		Percent  *float64 `json:"percent"`
		ResetsAt *string  `json:"resets_at"`
		IsActive *bool    `json:"is_active"`
		Scope    *struct {
			Model *struct {
				ID          *string `json:"id"`
				DisplayName *string `json:"display_name"`
			} `json:"model"`
			Surface *string `json:"surface"`
		} `json:"scope"`
	}
	if json.Unmarshal(data, &limits) != nil {
		return nil
	}
	var out []usage.Window
	for _, l := range limits {
		if l.Percent == nil {
			continue
		}
		w := usage.Window{UsedPercent: *l.Percent}
		switch l.Kind {
		case "session":
			w.Key, w.Label, w.WindowSeconds = "5h", "5h session", 5*3600
		case "weekly_all":
			w.Key, w.Label, w.WindowSeconds = "7d", "Weekly", 7*86400
		case "weekly_scoped":
			name := "scoped"
			if l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.DisplayName != nil {
				name = *l.Scope.Model.DisplayName
			} else if l.Scope != nil && l.Scope.Surface != nil {
				name = *l.Scope.Surface
			}
			w.Key, w.Label, w.WindowSeconds = "7d-"+strings.ToLower(name), "Weekly · "+name, 7*86400
		default:
			w.Key, w.Label = l.Kind, strings.ReplaceAll(l.Kind, "_", " ")
		}
		// An inactive window has not started; its reset time is meaningless.
		if l.IsActive == nil || *l.IsActive {
			w.ResetsAt = parseTime(l.ResetsAt)
		}
		out = append(out, w)
	}
	return out
}

var windowNames = map[string][2]string{
	"five_hour":            {"5h", "5h session"},
	"seven_day":            {"7d", "Weekly"},
	"seven_day_opus":       {"7d-opus", "Weekly · Opus"},
	"seven_day_sonnet":     {"7d-sonnet", "Weekly · Sonnet"},
	"seven_day_oauth_apps": {"7d-apps", "Weekly · apps"},
	"seven_day_cowork":     {"7d-cowork", "Weekly · Cowork"},
}

var windowOrder = []string{"five_hour", "seven_day", "seven_day_sonnet", "seven_day_opus", "seven_day_oauth_apps", "seven_day_cowork"}

func parseLegacyWindows(raw map[string]json.RawMessage) []usage.Window {
	var out []usage.Window
	for _, k := range windowOrder {
		var w struct {
			Utilization *float64 `json:"utilization"`
			ResetsAt    *string  `json:"resets_at"`
		}
		if json.Unmarshal(raw[k], &w) != nil || w.Utilization == nil {
			continue
		}
		name := windowNames[k]
		uw := usage.Window{Key: name[0], Label: name[1], UsedPercent: *w.Utilization, ResetsAt: parseTime(w.ResetsAt)}
		if k == "five_hour" {
			uw.WindowSeconds = 5 * 3600
		} else {
			uw.WindowSeconds = 7 * 86400
		}
		out = append(out, uw)
	}
	return out
}

func parseSpend(raw map[string]json.RawMessage) []usage.Extra {
	var spend struct {
		Enabled bool `json:"enabled"`
		Used    *struct {
			AmountMinor float64 `json:"amount_minor"`
			Currency    string  `json:"currency"`
			Exponent    int     `json:"exponent"`
		} `json:"used"`
		Limit *struct {
			AmountMinor float64 `json:"amount_minor"`
		} `json:"limit"`
	}
	if json.Unmarshal(raw["spend"], &spend) == nil && spend.Enabled && spend.Used != nil {
		div := 1.0
		for i := 0; i < spend.Used.Exponent; i++ {
			div *= 10
		}
		v := fmt.Sprintf("%.2f %s", spend.Used.AmountMinor/div, spend.Used.Currency)
		if spend.Limit != nil {
			v = fmt.Sprintf("%.2f / %.2f %s", spend.Used.AmountMinor/div, spend.Limit.AmountMinor/div, spend.Used.Currency)
		}
		return []usage.Extra{{Label: "Usage credits", Value: v}}
	}
	var extra struct {
		IsEnabled    bool     `json:"is_enabled"`
		MonthlyLimit *float64 `json:"monthly_limit"`
		UsedCredits  *float64 `json:"used_credits"`
		Currency     *string  `json:"currency"`
	}
	if json.Unmarshal(raw["extra_usage"], &extra) == nil && extra.IsEnabled && extra.UsedCredits != nil && extra.MonthlyLimit != nil {
		cur := "USD"
		if extra.Currency != nil {
			cur = *extra.Currency
		}
		return []usage.Extra{{Label: "Extra usage", Value: fmt.Sprintf("%.2f / %.2f %s", *extra.UsedCredits/100, *extra.MonthlyLimit/100, cur)}}
	}
	return nil
}
