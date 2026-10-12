package vault

import (
	"strings"
	"testing"
	"time"
)

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"jane@example.com":      "jane",
		"Profile 4 - paki1":     "profile-4-paki1",
		"  Work Account!! ":     "work-account",
		"":                      "account",
		strings.Repeat("a", 50): strings.Repeat("a", 32),
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolve(t *testing.T) {
	t.Setenv("DIAL_HOME", t.TempDir())
	v := Open()
	base := time.Now()
	for i, p := range []Profile{
		{ID: "work", Provider: Claude, Label: "Work", Email: "me@work.com"},
		{ID: "personal", Provider: Claude, Email: "me@home.com"},
		{ID: "work", Provider: Codex, Email: "me@work.com"},
	} {
		p.CreatedAt = base.Add(time.Duration(i) * time.Second)
		if err := v.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	check := func(ref string, hint Provider, wantProv Provider, wantID string) {
		t.Helper()
		p, err := v.Resolve(ref, hint)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", ref, err)
		}
		if p.Provider != wantProv || p.ID != wantID {
			t.Fatalf("Resolve(%q) = %s:%s", ref, p.Provider, p.ID)
		}
	}
	check("claude:work", "", Claude, "work")
	check("codex/1", "", Codex, "work")
	check("claude:2", "", Claude, "personal")
	check("me@home.com", "", Claude, "personal")
	check("pers", "", Claude, "personal")
	check("work", Codex, Codex, "work")
	if _, err := v.Resolve("work", ""); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity, got %v", err)
	}
	if _, err := v.Resolve("nobody", ""); err == nil {
		t.Fatal("expected not found")
	}
	if id := v.UniqueID(Claude, "work"); id != "work-2" {
		t.Fatalf("UniqueID = %q", id)
	}
}
