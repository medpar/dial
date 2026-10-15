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

// DIAL_LIVE_DUP=<scratch dir>: launching an instance that already runs must
// focus it, never start a second process on the same data dir.
func TestLiveNoDuplicateLaunch(t *testing.T) {
	dir := os.Getenv("DIAL_LIVE_DUP")
	if dir == "" {
		t.Skip("set DIAL_LIVE_DUP")
	}
	os.MkdirAll(dir, 0o700)
	if err := Claude.Launch(dir, nil); err != nil {
		t.Fatal(err)
	}
	defer Claude.Quit(dir, 20*time.Second)
	if !Claude.WaitRunning(dir, 30*time.Second) {
		t.Fatal("did not start")
	}
	time.Sleep(2 * time.Second)
	_ = Claude.Launch(dir, nil) // second request: must not spawn another
	time.Sleep(3 * time.Second)
	if n := len(Claude.PIDs(procs.List(), dir)); n != 1 {
		t.Fatalf("%d processes on one data dir", n)
	}
}
