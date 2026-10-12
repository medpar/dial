package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/medpar/dial/internal/claude"
	"github.com/medpar/dial/internal/codex"
	"github.com/medpar/dial/internal/state"
	"github.com/medpar/dial/internal/usage"
	"github.com/medpar/dial/internal/vault"
)

func newTestCore(t *testing.T) *Core {
	t.Helper()
	t.Setenv("DIAL_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	return &Core{Vault: vault.Open(), Config: state.DefaultConfig()}
}

func fakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func codexAuth(user, account, email, refresh string, lastRefresh time.Time) *codex.Auth {
	idt := fakeJWT(map[string]any{
		"email": email,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_user_id": user, "chatgpt_account_id": account, "chatgpt_plan_type": "plus",
		},
	})
	at := fakeJWT(map[string]any{"exp": time.Now().Add(240 * time.Hour).Unix()})
	raw := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens":         map[string]any{"id_token": idt, "access_token": at, "refresh_token": refresh, "account_id": account},
		"last_refresh":   lastRefresh.UTC().Format(time.RFC3339Nano),
	}
	b, _ := json.Marshal(raw)
	a, _ := codex.Parse(b)
	return a
}

func TestCodexImportNewestWins(t *testing.T) {
	c := newTestCore(t)
	now := time.Now()
	p, created, err := c.ImportCodexAuth(codexAuth("u1", "a1", "x@y.z", "r-new", now), "work", "test", "")
	if err != nil || !created || p.ID != "work" {
		t.Fatalf("first import: %v %v %+v", err, created, p)
	}
	// An older copy of the same family must not overwrite the newer one.
	if _, _, err := c.ImportCodexAuth(codexAuth("u1", "a1", "x@y.z", "r-old", now.Add(-time.Hour)), "", "test", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := c.LoadCodex(p)
	if got.Tokens().RefreshToken != "r-new" {
		t.Fatalf("older copy overwrote newer: %s", got.Tokens().RefreshToken)
	}
	// A fresh login (new lineage) replaces it even if its timestamp is older.
	if _, _, err := c.ImportCodexAuth(codexAuth("u1", "a1", "x@y.z", "r-fresh", now.Add(-2*time.Hour)), "", "login", NewLineage()); err != nil {
		t.Fatal(err)
	}
	got, _ = c.LoadCodex(p)
	if got.Tokens().RefreshToken != "r-fresh" {
		t.Fatalf("fresh login did not replace old family: %s", got.Tokens().RefreshToken)
	}
	// Same user, different workspace is a different profile.
	p2, created, _ := c.ImportCodexAuth(codexAuth("u1", "a2", "x@y.z", "r2", now), "", "test", "")
	if !created || p2.ID == p.ID {
		t.Fatalf("workspace should be its own profile: %+v", p2)
	}
}

func TestSyncBackCodex(t *testing.T) {
	c := newTestCore(t)
	home := t.TempDir()
	now := time.Now()
	p, _, _ := c.ImportCodexAuth(codexAuth("u1", "a1", "x@y.z", "r1", now.Add(-time.Hour)), "work", "test", "")
	// The CLI refreshed in place: live is newer than the vault.
	if err := codexAuth("u1", "a1", "x@y.z", "r2", now).Save(home + "/auth.json"); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.SyncBackCodex(home); !ok || got.ID != p.ID {
		t.Fatalf("sync back matched %v %+v", ok, got)
	}
	a, _ := c.LoadCodex(p)
	if a.Tokens().RefreshToken != "r2" {
		t.Fatalf("live refresh not folded back: %s", a.Tokens().RefreshToken)
	}
	// An unknown live login is imported rather than lost.
	codexAuth("u9", "a9", "new@y.z", "r9", now).Save(home + "/auth.json")
	if got, ok := c.SyncBackCodex(home); !ok || got.Email != "new@y.z" {
		t.Fatalf("unknown login not imported: %+v", got)
	}
}

func claudeOAuth(access, refresh string, exp time.Time) *claude.OAuth {
	b, _ := json.Marshal(map[string]any{
		"accessToken": access, "refreshToken": refresh, "expiresAt": exp.UnixMilli(),
		"scopes": []string{"user:inference"}, "subscriptionType": "max", "rateLimitTier": "default_claude_max_20x",
	})
	o, _ := claude.NewOAuth(b)
	return o
}

func claudeAccount(uuid, org, email string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"accountUuid": uuid, "organizationUuid": org, "emailAddress": email})
	return b
}

func TestClaudeStoreNewestWins(t *testing.T) {
	c := newTestCore(t)
	now := time.Now()
	acct := claudeAccount("acc", "org", "me@x.y")
	p, created, err := c.storeClaude(claudeOAuth("a1", "r1", now.Add(8*time.Hour)), acct, claude.ParseAccount(acct), "", "test", "")
	if err != nil || !created {
		t.Fatal(err)
	}
	if p.Plan != "max 20x" {
		t.Fatalf("plan = %q", p.Plan)
	}
	c.storeClaude(claudeOAuth("a0", "r0", now.Add(time.Hour)), acct, claude.ParseAccount(acct), "", "test", "")
	cr, _ := c.LoadClaude(p)
	if cr.OAuth.RefreshToken() != "r1" {
		t.Fatalf("older Claude copy overwrote newer")
	}
	c.storeClaude(claudeOAuth("a2", "r2", now.Add(9*time.Hour)), acct, claude.ParseAccount(acct), "", "test", "")
	cr, _ = c.LoadClaude(p)
	if cr.OAuth.RefreshToken() != "r2" {
		t.Fatalf("newer Claude copy not stored")
	}
}

func TestApplyRules(t *testing.T) {
	c := newTestCore(t)
	ctx := context.Background()
	now := time.Now()
	acct := claudeAccount("acc", "org", "me@x.y")
	key := claude.ParseAccount(acct).Key()

	resp := c.Apply(ctx, ApplyRequest{Provider: vault.Claude, Key: key, Label: "me"})
	if resp.OK || !resp.Missing {
		t.Fatalf("expected missing: %+v", resp)
	}
	// Seeding installs this host's own login.
	push := &Push{Profile: vault.Profile{Lineage: "L1"}, Secret: Secret{Claude: claudeOAuth("a", "r-seed", now.Add(time.Hour)).JSON(), Account: acct}}
	resp = c.Apply(ctx, ApplyRequest{Provider: vault.Claude, Key: key, Label: "me", Seed: true, Push: push})
	if !resp.OK {
		t.Fatalf("seed failed: %+v", resp)
	}
	p, _ := c.Vault.FindByKey(vault.Claude, key)
	if p.Lineage != "L1" {
		t.Fatalf("lineage = %q", p.Lineage)
	}
	// A plain push from another family must never overwrite the host's login.
	other := &Push{Profile: vault.Profile{Lineage: "L2"}, Secret: Secret{Claude: claudeOAuth("b", "r-other", now.Add(5*time.Hour)).JSON(), Account: acct}}
	c.Apply(ctx, ApplyRequest{Provider: vault.Claude, Key: key, Push: other})
	cr, _ := c.LoadClaude(p)
	if cr.OAuth.RefreshToken() != "r-seed" {
		t.Fatalf("foreign family overwrote host login")
	}
	// Credentials for a different account are rejected.
	bad := &Push{Secret: Secret{Claude: claudeOAuth("c", "r", now).JSON(), Account: claudeAccount("zzz", "org", "z@x.y")}}
	if r := c.Apply(ctx, ApplyRequest{Provider: vault.Claude, Key: key, Seed: true, Push: bad}); r.OK {
		t.Fatalf("mismatched account accepted")
	}
}

func TestCodexUsageRefreshOn401(t *testing.T) {
	c := newTestCore(t)
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		switch r.URL.Path {
		case "/token":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["grant_type"] != "refresh_token" || body["refresh_token"] != "r1" || body["client_id"] != codex.ClientID {
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "v": 2}), "refresh_token": "r2"})
		case "/usage":
			if calls["/token"] == 0 {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":42,"limit_window_seconds":18000,"reset_at":1900000000},"secondary_window":{"used_percent":7,"limit_window_seconds":604800,"reset_after_seconds":3600}},"rate_limit_reset_credits":{"available_count":2}}`))
		}
	}))
	defer srv.Close()
	codex.TokenURL, codex.UsageURL = srv.URL+"/token", srv.URL+"/usage"
	p, _, _ := c.ImportCodexAuth(codexAuth("u1", "a1", "x@y.z", "r1", time.Now()), "w", "test", "")
	reps := c.Usage(context.Background(), []vault.Profile{p}, UsageOptions{Force: true})
	r := reps[0]
	if r.Error != "" || len(r.Windows) != 2 {
		t.Fatalf("usage: %+v", r)
	}
	if r.Windows[0].Label != "5h session" || r.Windows[0].UsedPercent != 42 || r.Windows[1].Label != "Weekly" {
		t.Fatalf("windows: %+v", r.Windows)
	}
	a, _ := c.LoadCodex(p)
	if a.Tokens().RefreshToken != "r2" {
		t.Fatalf("refreshed token not persisted")
	}
}

func TestApplyRefusesExpiredLogin(t *testing.T) {
	c := newTestCore(t)
	p, _, _ := c.ImportCodexAuth(codexAuth("u1", "a1", "x@y.z", "r1", time.Now()), "w", "test", "")
	cache := usage.LoadCache()
	cache.Put("codex:"+p.ID, usage.Report{FetchedAt: time.Now(), Error: "login expired; run `dial login codex:w`"})
	cache.Save()
	resp := c.Apply(context.Background(), ApplyRequest{Provider: vault.Codex, Key: p.Key(), Label: "w", Activate: true})
	if resp.OK || !resp.Missing || !strings.Contains(resp.Message, "expired") {
		t.Fatalf("expired login was activated: %+v", resp)
	}
}
