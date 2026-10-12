package desktop

import (
	"os"
	"testing"
	"time"

	"github.com/medpar/dial/internal/procs"
)

// Run with DIAL_LIVE_UD=<user-data-dir of a test instance> to exercise
// detection and quit against a real Claude Desktop instance.
func TestLiveClaudeInstance(t *testing.T) {
	ud := os.Getenv("DIAL_LIVE_UD")
	if ud == "" {
		t.Skip("set DIAL_LIVE_UD")
	}
	ps := procs.List()
	t.Logf("default pids: %v", Claude.PIDs(ps, Claude.DataDir()))
	t.Logf("test pids: %v", Claude.PIDs(ps, ud))
	t.Logf("instances: %v", Claude.Instances(ps))
	if len(Claude.PIDs(ps, ud)) != 1 {
		t.Fatalf("expected one test instance")
	}
	start := time.Now()
	if err := Claude.Quit(ud, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Logf("quit in %s; default still running: %v", time.Since(start), Claude.Running(Claude.DataDir()))
	if !Claude.Running(Claude.DataDir()) {
		t.Fatal("main instance must keep running")
	}
}
