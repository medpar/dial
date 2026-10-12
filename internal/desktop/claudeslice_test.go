package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDesktop(t *testing.T, dir, account, marker string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "Local Storage", "leveldb"), 0o700)
	os.WriteFile(filepath.Join(dir, "Cookies"), []byte("cookies-"+marker), 0o600)
	os.WriteFile(filepath.Join(dir, "Local Storage", "leveldb", "000001.log"), []byte("ls-"+marker), 0o600)
	cfg := map[string]any{
		"darkMode":             "dark",
		"lastKnownAccountUuid": account,
		"oauth:tokenCacheV2":   "djEw" + strings.Repeat("x", 400) + marker,
	}
	b, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600)
}

func read(t *testing.T, path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParkAndUnpark(t *testing.T) {
	t.Setenv("DIAL_HOME", t.TempDir())
	data := t.TempDir()
	writeDesktop(t, data, "acct-A", "A")

	parked, err := ParkClaude(data)
	if err != nil || parked.AccountUUID != "acct-A" {
		t.Fatalf("park: %v %+v", err, parked)
	}
	if _, err := os.Stat(filepath.Join(data, "Cookies")); !os.IsNotExist(err) {
		t.Fatal("cookies left behind after park")
	}
	if ClaudeAccountIn(data).LoggedIn {
		t.Fatal("data dir still looks logged in")
	}
	// Non-auth preferences stay in the app's config.
	var cfg map[string]any
	json.Unmarshal([]byte(read(t, filepath.Join(data, "config.json"))), &cfg)
	if cfg["darkMode"] != "dark" {
		t.Fatal("preferences lost")
	}
	if !ClaudeAccountIn(ClaudeHome("acct-A")).LoggedIn {
		t.Fatal("parked home not logged in")
	}

	// Sign B in, then switch back to A: B gets parked, A restored.
	writeDesktop(t, data, "acct-B", "B")
	if _, err := ParkClaude(data); err != nil {
		t.Fatal(err)
	}
	if err := UnparkClaude("acct-A", data); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(data, "Cookies")); got != "cookies-A" {
		t.Fatalf("restored cookies = %q", got)
	}
	if got := ClaudeAccountIn(data); got.AccountUUID != "acct-A" || !got.LoggedIn {
		t.Fatalf("restored account = %+v", got)
	}
	if !ClaudeAccountIn(ClaudeHome("acct-B")).LoggedIn {
		t.Fatal("B was not parked")
	}
	if ClaudeAccountIn(ClaudeHome("acct-A")).LoggedIn {
		t.Fatal("A's slice must live in exactly one place")
	}
	if err := UnparkClaude("acct-missing", data); err == nil {
		t.Fatal("unparking an unknown account should fail")
	}
}
