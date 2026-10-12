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

func TestAdoptAndReconcile(t *testing.T) {
	t.Setenv("DIAL_HOME", t.TempDir())
	// A fresh sign-in becomes the account's home.
	dir, err := NewSigninDir()
	if err != nil {
		t.Fatal(err)
	}
	writeDesktop(t, dir, "acct-N", "N")
	a, err := AdoptSignin(dir)
	if err != nil || a.AccountUUID != "acct-N" || a.Dir != ClaudeHome("acct-N") {
		t.Fatalf("adopt: %v %+v", err, a)
	}
	// Re-signing replaces the login inside an existing home.
	dir2, _ := NewSigninDir()
	writeDesktop(t, dir2, "acct-N", "N2")
	if _, err := AdoptSignin(dir2); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(ClaudeHome("acct-N"), "Cookies")); got != "cookies-N2" {
		t.Fatalf("re-sign-in not adopted: %q", got)
	}
	// A window signed into a different account than its name gets fixed.
	writeDesktop(t, ClaudeHome("acct-X"), "acct-Y", "Y")
	ReconcileClaudeHomes(func(string) bool { return false })
	if got := ClaudeAccountIn(ClaudeHome("acct-Y")); !got.LoggedIn || got.AccountUUID != "acct-Y" {
		t.Fatalf("reconcile did not move Y home: %+v", got)
	}
	if ClaudeAccountIn(ClaudeHome("acct-X")).LoggedIn {
		t.Fatal("misnamed home still holds a login")
	}
	// Busy dirs are never touched.
	writeDesktop(t, ClaudeHome("acct-P"), "acct-Q", "Q")
	ReconcileClaudeHomes(func(d string) bool { return filepath.Base(d) == "acct-P" })
	if ClaudeAccountIn(ClaudeHome("acct-Q")).LoggedIn {
		t.Fatal("reconcile touched a running window")
	}
}

func TestCodexHomeSanitizesState(t *testing.T) {
	t.Setenv("DIAL_HOME", t.TempDir())
	main := t.TempDir()
	t.Setenv("CODEX_HOME", main)
	os.MkdirAll(filepath.Join(main, "sessions"), 0o700)
	os.MkdirAll(filepath.Join(main, "skills"), 0o700)
	os.WriteFile(filepath.Join(main, "config.toml"), []byte("model = \"x\"\n"), 0o600)
	state := map[string]any{
		"electron-saved-workspace-roots":               []string{"/w"},
		"electron-local-remote-control-environment-id": "env_main",
		"electron-mac-push-deregistration-token":       "secret",
		"thread-project-assignments":                   map[string]any{"t1": "p"},
	}
	b, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(main, ".codex-global-state.json"), b, 0o600)

	home := CodexInstanceHome("work")
	if err := PrepareCodexHome(home, false); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal([]byte(read(t, filepath.Join(home, ".codex-global-state.json"))), &got)
	if _, ok := got["electron-saved-workspace-roots"]; !ok {
		t.Fatal("preferences not inherited")
	}
	for _, k := range []string{"electron-local-remote-control-environment-id", "electron-mac-push-deregistration-token", "thread-project-assignments"} {
		if _, ok := got[k]; ok {
			t.Fatalf("%s leaked into the instance", k)
		}
	}
	if _, err := os.Lstat(filepath.Join(home, "sessions")); !os.IsNotExist(err) {
		t.Fatal("sessions must not be shared")
	}
	if l, err := os.Readlink(filepath.Join(home, "config.toml")); err != nil || l != filepath.Join(main, "config.toml") {
		t.Fatalf("config not shared: %v %q", err, l)
	}

	// An old (v1) home: shared sessions link and a full copy of main's state.
	old := CodexInstanceHome("legacy")
	os.MkdirAll(old, 0o700)
	os.Symlink(filepath.Join(main, "sessions"), filepath.Join(old, "sessions"))
	os.WriteFile(filepath.Join(old, ".codex-global-state.json"), b, 0o600)
	// While busy nothing is migrated.
	PrepareCodexHome(old, true)
	if _, err := os.Lstat(filepath.Join(old, "sessions")); err != nil {
		t.Fatal("busy home was modified")
	}
	PrepareCodexHome(old, false)
	if _, err := os.Lstat(filepath.Join(old, "sessions")); !os.IsNotExist(err) {
		t.Fatal("legacy sessions link not removed")
	}
	got = nil
	json.Unmarshal([]byte(read(t, filepath.Join(old, ".codex-global-state.json"))), &got)
	if _, ok := got["electron-local-remote-control-environment-id"]; ok {
		t.Fatal("legacy state not sanitized")
	}
}
