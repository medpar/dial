package core

import (
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

// DIAL_LIVE_SIGNIN=<scratch dir> drives AddClaudeDesktop end to end with
// throwaway Claude instances: a stand-in main window (account A) and an
// extra window (B) are open; a "sign-in" of C happens in the dedicated
// window; afterwards A and B are back and C runs in its own window.
func TestLiveClaudeSignin(t *testing.T) {
	root := os.Getenv("DIAL_LIVE_SIGNIN")
	if root == "" {
		t.Skip("set DIAL_LIVE_SIGNIN")
	}
	os.RemoveAll(root)
	data := filepath.Join(root, "appdata")
	t.Setenv("DIAL_HOME", filepath.Join(root, "dialhome"))
	t.Setenv("DIAL_CLAUDE_DESKTOP_DATA", data)
	realBefore := realMainPIDs()
	var realWindowsBefore []int
	for _, p := range procs.List() {
		if strings.Contains(p.Args, "--user-data-dir=") && !strings.Contains(p.Args, root) && strings.HasPrefix(p.Args, "/Applications/Claude.app/Contents/MacOS/Claude") {
			realWindowsBefore = append(realWindowsBefore, p.PID)
		}
	}

	app := desktop.Claude
	fakeDesktopLogin(t, data, "acct-A", "A")
	fakeDesktopLogin(t, desktop.ClaudeHome("acct-B"), "acct-B", "B")
	for _, d := range []string{data, desktop.ClaudeHome("acct-B")} {
		if err := app.Launch(d, nil); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, d := range []string{data, desktop.ClaudeHome("acct-B"), desktop.ClaudeHome("acct-C")} {
			app.Quit(d, 20*time.Second)
		}
	}()
	waitRunning := func(d string) bool {
		for i := 0; i < 60 && !app.Running(d); i++ {
			time.Sleep(500 * time.Millisecond)
		}
		return app.Running(d)
	}
	if !waitRunning(data) || !waitRunning(desktop.ClaudeHome("acct-B")) {
		t.Fatal("test windows did not start")
	}
	time.Sleep(3 * time.Second)

	// Play the user: once the sign-in window is up, "sign in" as C.
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			matches, _ := filepath.Glob(filepath.Join(desktop.ClaudeHomes(), ".signin-*"))
			for _, m := range matches {
				if app.Running(m) {
					fakeDesktopLogin(t, m, "acct-C", "C")
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
	c := &Core{Vault: vault.Open(), Config: state.DefaultConfig()}
	acct, err := c.AddClaudeDesktop(SigninOptions{Timeout: 2 * time.Minute, Progress: func(s string) { t.Log(s) }})
	close(done)
	if err != nil {
		t.Fatal(err)
	}
	if acct.AccountUUID != "acct-C" || !acct.Window {
		t.Fatalf("result: %+v", acct)
	}
	for _, d := range []string{data, desktop.ClaudeHome("acct-B"), desktop.ClaudeHome("acct-C")} {
		if !waitRunning(d) {
			t.Fatalf("%s not running after sign-in", filepath.Base(d))
		}
	}
	if got := desktop.ClaudeAccountIn(data).AccountUUID; got != "acct-A" {
		t.Fatalf("main window account changed to %q", got)
	}
	st := c.ClaudeDesktopStatus()
	for _, a := range st.Accounts {
		t.Logf("status: %s live=%v window=%v saved=%v", a.AccountUUID, a.Live, a.Window, a.Saved)
	}
	want := map[string][2]bool{"acct-A": {true, false}, "acct-B": {false, true}, "acct-C": {false, true}}
	for _, a := range st.Accounts {
		if w, ok := want[a.AccountUUID]; ok && (a.Live != w[0] || a.Window != w[1]) {
			t.Fatalf("status wrong for %s: %+v", a.AccountUUID, a)
		}
	}
	realAfter := realMainPIDs()
	if len(realBefore) != len(realAfter) || (len(realBefore) > 0 && realBefore[0] != realAfter[0]) {
		t.Fatalf("real Claude window changed: %v → %v", realBefore, realAfter)
	}
	for _, pid := range realWindowsBefore {
		alive := false
		for _, p := range procs.List() {
			if p.PID == pid {
				alive = true
			}
		}
		if !alive {
			t.Fatalf("real extra window %d was closed", pid)
		}
	}
	t.Logf("real windows untouched: main %v, extra %v", realAfter, realWindowsBefore)
}
