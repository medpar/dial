package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/medpar/dial/internal/desktop"
	"github.com/medpar/dial/internal/procs"
	"github.com/medpar/dial/internal/state"
	"github.com/medpar/dial/internal/vault"
)

func fakeDesktopLogin(t *testing.T, dir, account, marker string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "IndexedDB"), 0o700)
	os.WriteFile(filepath.Join(dir, "IndexedDB", "dial-test-marker"), []byte(marker), 0o600)
	b, _ := json.Marshal(map[string]any{"lastKnownAccountUuid": account, "oauth:tokenCacheV2": strings.Repeat("x", 400) + marker})
	os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600)
}

func realMainPIDs() []int {
	var out []int
	for _, p := range procs.List() {
		if strings.HasPrefix(p.Args, "/Applications/Claude.app/Contents/MacOS/Claude") && !strings.Contains(p.Args, "--user-data-dir") {
			out = append(out, p.PID)
		}
	}
	return out
}

// DIAL_LIVE_SWITCH=<scratch dir> drives SwitchClaudeDesktop end to end
// against a throwaway Claude Desktop instance standing in for the main app.
func TestLiveClaudeDesktopSwitch(t *testing.T) {
	root := os.Getenv("DIAL_LIVE_SWITCH")
	if root == "" {
		t.Skip("set DIAL_LIVE_SWITCH")
	}
	os.RemoveAll(root)
	data := filepath.Join(root, "appdata")
	t.Setenv("DIAL_HOME", filepath.Join(root, "dialhome"))
	t.Setenv("DIAL_CLAUDE_DESKTOP_DATA", data)
	mainBefore := realMainPIDs()

	fakeDesktopLogin(t, data, "acct-A", "A")
	fakeDesktopLogin(t, desktop.ClaudeHome("acct-B"), "acct-B", "B")
	app := desktop.Claude
	if err := app.Launch(data, nil); err != nil {
		t.Fatal(err)
	}
	defer app.Quit(data, 20*time.Second)
	for i := 0; i < 60 && !app.Running(data); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if !app.Running(data) {
		t.Fatal("test instance did not start")
	}
	time.Sleep(3 * time.Second)

	c := &Core{Vault: vault.Open(), Config: state.DefaultConfig()}
	start := time.Now()
	if err := c.switchClaudeDesktopAccount("acct-B", "B"); err != nil {
		t.Fatal(err)
	}
	t.Logf("switch took %s", time.Since(start))
	marker, _ := os.ReadFile(filepath.Join(data, "IndexedDB", "dial-test-marker"))
	if string(marker) != "B" {
		t.Fatalf("app data holds %q, want B", marker)
	}
	if got := desktop.ClaudeAccountIn(data).AccountUUID; got != "acct-B" {
		t.Fatalf("lastKnownAccountUuid = %q", got)
	}
	parkedA, _ := os.ReadFile(filepath.Join(desktop.ClaudeHome("acct-A"), "IndexedDB", "dial-test-marker"))
	if string(parkedA) != "A" {
		t.Fatalf("A was not parked: %q", parkedA)
	}
	for i := 0; i < 60 && !app.Running(data); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if !app.Running(data) {
		t.Fatal("instance was not relaunched")
	}
	mainAfter := realMainPIDs()
	if len(mainBefore) != len(mainAfter) || (len(mainBefore) > 0 && mainBefore[0] != mainAfter[0]) {
		t.Fatalf("real Claude window changed: %v → %v", mainBefore, mainAfter)
	}
	t.Logf("relaunched; real main window untouched %v", mainAfter)
}
