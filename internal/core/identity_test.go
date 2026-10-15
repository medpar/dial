package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/medpar/dial/internal/claude"
	"github.com/medpar/dial/internal/procs"
	"github.com/medpar/dial/internal/vault"
)

// Claude Desktop's built-in Claude Code rewrites ~/.claude.json from every
// window, so .claude.json may name a different account than the login in
// the credential store. Sync-back must follow the credential, never the file.
func TestSyncBackIgnoresClaudeJSON(t *testing.T) {
	c := newTestCore(t)
	t.Setenv("DIAL_CLAUDE_STORE", "file")
	t.Setenv("HOME", t.TempDir())
	cfg := t.TempDir()
	target := claude.DirTarget(cfg)

	// The API says which account an access token belongs to.
	owners := map[string]string{} // access token -> "acct/org"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("Authorization")[len("Bearer "):]
		who, ok := owners[tok]
		if !ok {
			w.WriteHeader(401)
			return
		}
		var acct, org string
		for i := range who {
			if who[i] == '/' {
				acct, org = who[:i], who[i+1:]
			}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"account":      map[string]any{"uuid": acct, "email": acct + "@x.y"},
			"organization": map[string]any{"uuid": org, "organization_type": "claude_max"},
		})
	}))
	defer srv.Close()
	claude.ProfileURL = srv.URL

	now := time.Now()
	acctA, acctB := claudeAccount("A", "orgA", "a@x.y"), claudeAccount("B", "orgB", "b@x.y")
	pA, _, _ := c.storeClaude(claudeOAuth("a1", "rA1", now.Add(2*time.Hour)), acctA, claude.ParseAccount(acctA), "", "test", "")
	pB, _, _ := c.storeClaude(claudeOAuth("b1", "rB1", now.Add(2*time.Hour)), acctB, claude.ParseAccount(acctB), "", "test", "")

	// 1. Live holds A's exact tokens while .claude.json names B.
	if err := target.WriteOAuth(claudeOAuth("a1", "rA1", now.Add(2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := target.WriteAccount(acctB); err != nil {
		t.Fatal(err)
	}
	if p, ok := c.SyncBackClaude(context.Background(), target); !ok || p.ID != pA.ID {
		t.Fatalf("live login attributed to %q, want %q", p.ID, pA.ID)
	}

	// 2. The CLI rotated A's tokens (unknown to dial); .claude.json still says B.
	owners["a2"] = "A/orgA"
	target.WriteOAuth(claudeOAuth("a2", "rA2", now.Add(8*time.Hour)))
	c.SyncBackClaude(context.Background(), target)
	gotA, _ := c.LoadClaude(pA)
	gotB, _ := c.LoadClaude(pB)
	if gotA.OAuth.RefreshToken() != "rA2" {
		t.Fatalf("A not updated with its rotated login: %s", gotA.OAuth.RefreshToken())
	}
	if gotB.OAuth.RefreshToken() != "rB1" {
		t.Fatalf("B was overwritten with another account's login: %s", gotB.OAuth.RefreshToken())
	}
	if c.LiveKeyFor(target) != pA.Key() {
		t.Fatalf("live key = %q, want A", c.LiveKeyFor(target))
	}

	// 3. An expired login nobody can identify is left alone.
	target.WriteOAuth(claudeOAuth("zz", "rZZ", now.Add(-time.Hour)))
	if _, ok := c.SyncBackClaude(context.Background(), target); ok {
		t.Fatal("unidentifiable login was attributed")
	}
	gotB, _ = c.LoadClaude(pB)
	if gotB.OAuth.RefreshToken() != "rB1" {
		t.Fatal("B changed")
	}

	// 4. API answers are cached per access token.
	delete(owners, "a2")
	target.WriteOAuth(claudeOAuth("a2", "rA2-other-family", now.Add(8*time.Hour)))
	if c.LiveKeyFor(target) != pA.Key() {
		t.Fatal("cached identity not used")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("DIAL_HOME"), "identity.json")); err != nil {
		t.Fatal("identity cache not written")
	}
}

// The CLI renewed its login since dial last saw it, then went idle: the live
// login is expired and unknown, the stored copy is superseded. dial must
// renew the live one (as Claude Code would) and never send the superseded
// refresh token, which could revoke the live login.
func TestHealExpiredUnknownLiveLogin(t *testing.T) {
	if len(procs.ClaudeCLIs(procs.List())) > 0 {
		t.Skip("a claude CLI is running on this machine; healing is (correctly) skipped")
	}
	c := newTestCore(t)
	t.Setenv("DIAL_CLAUDE_STORE", "file")
	t.Setenv("HOME", t.TempDir())
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			sent = append(sent, body["refresh_token"])
			if body["refresh_token"] != "rA2" {
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "a3", "refresh_token": "rA3", "expires_in": 28800})
		case "/profile":
			if r.Header.Get("Authorization") != "Bearer a3" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"account": map[string]any{"uuid": "A"}, "organization": map[string]any{"uuid": "orgA"}})
		case "/usage":
			w.Write([]byte(`{"limits":[{"kind":"session","percent":5,"is_active":true,"resets_at":"2030-01-01T00:00:00Z"}]}`))
		}
	}))
	defer srv.Close()
	claude.TokenURL, claude.ProfileURL, claude.UsageURL = srv.URL+"/token", srv.URL+"/profile", srv.URL+"/usage"

	now := time.Now()
	acctA := claudeAccount("A", "orgA", "a@x.y")
	pA, _, _ := c.storeClaude(claudeOAuth("a1", "rA1", now.Add(-50*time.Hour)), acctA, claude.ParseAccount(acctA), "", "test", "")
	live := claude.DefaultTarget()
	if err := live.WriteOAuth(claudeOAuth("a2", "rA2", now.Add(-2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	reps := c.Usage(context.Background(), []vault.Profile{pA}, UsageOptions{Force: true})
	for _, rt := range sent {
		if rt == "rA1" {
			t.Fatal("superseded refresh token was sent to the token endpoint")
		}
	}
	got, _ := c.LoadClaude(pA)
	lo, _ := live.ReadOAuth()
	if got.OAuth.RefreshToken() != "rA3" || lo.RefreshToken() != "rA3" {
		t.Fatalf("vault %q live %q, want both rA3", got.OAuth.RefreshToken(), lo.RefreshToken())
	}
	if reps[0].Error != "" {
		t.Fatalf("usage after heal: %s", reps[0].Error)
	}
}
