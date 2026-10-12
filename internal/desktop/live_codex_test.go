package desktop

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/medpar/dial/internal/procs"
)

// DIAL_LIVE_CODEX=<scratch dir> launches a throwaway Codex instance with its
// own CODEX_HOME and user-data dir, checks detection, then quits it.
func TestLiveCodexInstance(t *testing.T) {
	root := os.Getenv("DIAL_LIVE_CODEX")
	if root == "" {
		t.Skip("set DIAL_LIVE_CODEX")
	}
	home, data := filepath.Join(root, "home"), filepath.Join(root, "data")
	if err := PrepareCodexHome(home); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(data, 0o700)
	if err := Codex.Launch(data, map[string]string{"CODEX_HOME": home, "CODEX_ELECTRON_USER_DATA_PATH": data}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && !Codex.Running(data) {
		time.Sleep(500 * time.Millisecond)
	}
	ps := procs.List()
	t.Logf("instance pids: %v; default running: %v", Codex.PIDs(ps, data), Codex.Running(Codex.DataDir()))
	if !Codex.Running(data) {
		t.Fatal("instance did not start")
	}
	time.Sleep(4 * time.Second)
	entries, _ := os.ReadDir(home)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	t.Logf("CODEX_HOME entries: %v", names)
	if err := Codex.Quit(data, 20*time.Second); err != nil {
		t.Fatal(err)
	}
}
