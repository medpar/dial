package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestKeychainService(t *testing.T) {
	if s := (Target{ConfigDir: "/x/.claude"}).KeychainService(); s != "Claude Code-credentials" {
		t.Fatalf("default service = %q", s)
	}
	dir := "/Users/me/.dial/run/claude/work"
	sum := sha256.Sum256([]byte(dir))
	want := "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
	if s := DirTarget(dir).KeychainService(); s != want {
		t.Fatalf("custom service = %q, want %q", s, want)
	}
}

func TestParseLimits(t *testing.T) {
	data := json.RawMessage(`[
	 {"kind":"session","group":"session","percent":57,"resets_at":"2026-10-12T14:39:59.801709+00:00","scope":null,"is_active":true},
	 {"kind":"weekly_all","percent":12,"resets_at":"2026-10-15T09:00:00+00:00","is_active":true},
	 {"kind":"weekly_scoped","group":"weekly","percent":0,"resets_at":"2026-10-12T13:00:00+00:00","scope":{"model":{"id":null,"display_name":"Fable"},"surface":null},"is_active":false}]`)
	w := parseLimits(data)
	if len(w) != 3 {
		t.Fatalf("got %d windows", len(w))
	}
	if w[0].Label != "5h session" || w[0].UsedPercent != 57 || w[0].ResetsAt == nil {
		t.Fatalf("session: %+v", w[0])
	}
	if w[1].Label != "Weekly" {
		t.Fatalf("weekly: %+v", w[1])
	}
	if w[2].Label != "Weekly · Fable" || w[2].ResetsAt != nil {
		t.Fatalf("inactive scoped window should have no reset: %+v", w[2])
	}
}

func TestParseLegacy(t *testing.T) {
	raw := map[string]json.RawMessage{
		"five_hour":      json.RawMessage(`{"utilization":20.5,"resets_at":"2026-10-12T14:00:00Z"}`),
		"seven_day":      json.RawMessage(`{"utilization":3,"resets_at":null}`),
		"seven_day_opus": json.RawMessage(`null`),
	}
	w := parseLegacyWindows(raw)
	if len(w) != 2 || w[0].Key != "5h" || w[1].Key != "7d" || w[1].ResetsAt != nil {
		t.Fatalf("legacy: %+v", w)
	}
}

func TestOAuthRoundTripKeepsUnknownFields(t *testing.T) {
	o, err := NewOAuth(json.RawMessage(`{"accessToken":"a","refreshToken":"r","expiresAt":1,"futureField":{"x":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	json.Unmarshal(o.JSON(), &back)
	if _, ok := back["futureField"]; !ok {
		t.Fatal("unknown field dropped")
	}
}
