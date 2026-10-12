// Package desktop controls the Claude and Codex desktop apps on macOS:
// finding, quitting and relaunching them, and running extra instances
// with their own user-data directory (and, for Codex, CODEX_HOME).
package desktop

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/medpar/dial/internal/paths"
	"github.com/medpar/dial/internal/procs"
)

// App describes an Electron desktop app.
type App struct {
	Name     string
	BundleID string
	Bundles  []string // candidate bundle names in /Applications
	Exec     string   // main executable inside Contents/MacOS
	// DataDir returns the default user-data directory.
	DataDir func() string
}

var Claude = App{
	Name:     "Claude",
	BundleID: "com.anthropic.claudefordesktop",
	Bundles:  []string{"Claude.app"},
	Exec:     "Claude",
	DataDir:  paths.ClaudeDesktopData,
}

var Codex = App{
	Name:     "Codex",
	BundleID: "com.openai.codex",
	Bundles:  []string{"ChatGPT.app", "Codex.app"},
	DataDir:  paths.CodexDesktopData,
}

// Path finds the installed bundle.
func (a App) Path() (string, error) {
	for _, root := range []string{"/Applications", filepath.Join(paths.Home(), "Applications")} {
		for _, b := range a.Bundles {
			p := filepath.Join(root, b)
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				return p, nil
			}
		}
	}
	out, err := exec.Command("mdfind", "kMDItemCFBundleIdentifier == '"+a.BundleID+"'").Output()
	if err == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.HasSuffix(l, ".app") {
				return l, nil
			}
		}
	}
	return "", fmt.Errorf("%s desktop app not found", a.Name)
}

// Installed reports whether the app exists on this machine.
func (a App) Installed() bool {
	if !paths.IsMac() {
		return false
	}
	_, err := a.Path()
	return err == nil
}

func (a App) execPath() string {
	p, err := a.Path()
	if err != nil {
		return ""
	}
	name := a.Exec
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(p), ".app")
	}
	return filepath.Join(p, "Contents", "MacOS", name)
}

func userDataArg(args string) string {
	const flag = "--user-data-dir="
	i := strings.Index(args, flag)
	if i < 0 {
		return ""
	}
	rest := args[i+len(flag):]
	// Paths may contain spaces; the flag is the last argument dial passes,
	// and Chromium switches that follow always start with " --".
	if j := strings.Index(rest, " --"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

func samePath(a, b string) bool {
	ca, err1 := filepath.EvalSymlinks(a)
	cb, err2 := filepath.EvalSymlinks(b)
	if err1 == nil && err2 == nil {
		return ca == cb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// PIDs returns main-process pids of the instance using dataDir. The default
// instance is the one launched without --user-data-dir.
func (a App) PIDs(ps []procs.Proc, dataDir string) []int {
	exe := a.execPath()
	if exe == "" {
		return nil
	}
	isDefault := samePath(dataDir, a.DataDir())
	var out []int
	for _, p := range ps {
		if !strings.HasPrefix(p.Args, exe) {
			continue
		}
		// Helpers live under Frameworks/, so a prefix match on the main
		// executable followed by end-of-string or a space is the app itself.
		rest := p.Args[len(exe):]
		if rest != "" && rest[0] != ' ' {
			continue
		}
		ud := userDataArg(rest)
		switch {
		case ud == "" && isDefault:
			out = append(out, p.PID)
		case ud != "" && samePath(ud, dataDir):
			out = append(out, p.PID)
		}
	}
	return out
}

func (a App) Running(dataDir string) bool { return len(a.PIDs(procs.List(), dataDir)) > 0 }

// Instances returns user-data dirs of every running non-default instance.
func (a App) Instances(ps []procs.Proc) []string {
	exe := a.execPath()
	var out []string
	for _, p := range ps {
		if exe == "" || !strings.HasPrefix(p.Args, exe) {
			continue
		}
		rest := p.Args[len(exe):]
		if rest != "" && rest[0] != ' ' {
			continue
		}
		if ud := userDataArg(rest); ud != "" && !samePath(ud, a.DataDir()) {
			out = append(out, ud)
		}
	}
	return out
}

// childPIDs lists direct children of pid (Codex keeps an app-server child).
func childPIDs(pid int) []int {
	out, err := exec.Command("pgrep", "-P", fmt.Sprint(pid)).Output()
	if err != nil {
		return nil
	}
	var kids []int
	for _, f := range strings.Fields(string(out)) {
		var k int
		if _, err := fmt.Sscan(f, &k); err == nil {
			kids = append(kids, k)
		}
	}
	return kids
}

// Quit closes the instance using dataDir and waits for it to exit.
func (a App) Quit(dataDir string, timeout time.Duration) error {
	ps := procs.List()
	pids := a.PIDs(ps, dataDir)
	if len(pids) == 0 {
		return nil
	}
	var kids []int
	for _, pid := range pids {
		kids = append(kids, childPIDs(pid)...)
	}
	onlyDefault := samePath(dataDir, a.DataDir()) && len(a.Instances(ps)) == 0
	if onlyDefault {
		// AppleScript quit runs the app's own quit path (flushes cookies,
		// LevelDB). It is ambiguous once extra instances run, so then we
		// fall back to SIGTERM, which Chromium also handles gracefully.
		_ = exec.Command("osascript", "-e", fmt.Sprintf(`tell application id "%s" to quit`, a.BundleID)).Run()
	} else {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(a.PIDs(procs.List(), dataDir)) == 0 {
			for _, k := range kids {
				_ = syscall.Kill(k, syscall.SIGTERM)
			}
			time.Sleep(300 * time.Millisecond)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("%s did not quit within %s; close it and retry", a.Name, timeout)
}

// Launch starts the instance using dataDir. env is passed to the app
// process via `open --env`.
func (a App) Launch(dataDir string, env map[string]string) error {
	path, err := a.Path()
	if err != nil {
		return err
	}
	args := []string{}
	isDefault := samePath(dataDir, a.DataDir())
	if !isDefault {
		args = append(args, "-n")
	}
	for k, v := range env {
		args = append(args, "--env", k+"="+v)
	}
	args = append(args, "-a", path)
	if !isDefault {
		args = append(args, "--args", "--user-data-dir="+dataDir)
	}
	cmd := exec.Command("/usr/bin/open", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("open %s: %v %s", a.Name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Focus brings the instance using dataDir to the front.
func (a App) Focus(dataDir string) error {
	pids := a.PIDs(procs.List(), dataDir)
	if len(pids) == 0 {
		return errors.New("not running")
	}
	script := fmt.Sprintf(`tell application "System Events" to set frontmost of (first process whose unix id is %d) to true`, pids[0])
	return exec.Command("osascript", "-e", script).Run()
}
