package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/medpar/dial/internal/claude"
	"github.com/medpar/dial/internal/core"
	"github.com/medpar/dial/internal/desktop"
	"github.com/medpar/dial/internal/keychain"
	"github.com/medpar/dial/internal/paths"
	"github.com/medpar/dial/internal/procs"
	"github.com/medpar/dial/internal/state"
	"github.com/medpar/dial/internal/ui"
)

func cmdKeepAlive(args []string) {
	o := parse(args)
	c := core.New()
	c.SyncCodexWindows()
	c.FollowExternalChanges(context.Background())
	done := c.KeepAlive(context.Background())
	if o.has("json") {
		printJSON(map[string]any{"refreshed": done})
		return
	}
	if len(done) == 0 {
		fmt.Println(ui.Dim("All stored logins are fresh."))
		return
	}
	ui.OK("Refreshed " + strings.Join(done, ", "))
}

func cmdFollow(args []string) {
	o := parse(args)
	c := core.New()
	res := c.FollowExternalChanges(context.Background())
	if o.has("json") {
		printJSON(map[string]any{"synced": res})
		return
	}
	if len(res) == 0 {
		fmt.Println(ui.Dim("No account changes to follow."))
	}
	for _, r := range res {
		if r.OK {
			ui.OK(r.Remote + ": " + r.Message)
		} else {
			fmt.Println(ui.Yellow("! ") + r.Remote + ": " + r.Message)
		}
	}
}

func cmdDoctor(args []string) {
	c := core.New()
	check := func(ok bool, label, detail string) {
		mark := ui.Green("✓")
		if !ok {
			mark = ui.Yellow("!")
		}
		fmt.Printf("%s %-22s %s\n", mark, label, ui.Dim(detail))
	}
	fmt.Println(ui.Bold("dial " + version + "  ·  " + paths.DialHome()))
	for _, bin := range []string{"claude", "codex"} {
		p, err := core.FindBinary(bin)
		if err != nil {
			check(false, bin+" CLI", "not found")
			continue
		}
		out, _ := exec.Command(p, "--version").Output()
		check(true, bin+" CLI", strings.TrimSpace(string(out))+" — "+p)
	}
	if keychain.Available() {
		_, err := keychain.Get(claude.DefaultTarget().KeychainService(), "")
		check(err == nil || err == keychain.ErrNotFound, "macOS keychain", "Claude Code-credentials readable")
	}
	for _, cli := range procs.ClaudeCLIs(procs.List()) {
		if cli.Version != "" && procs.VersionLess(cli.Version, core.MinLiveSwapVersion) {
			check(false, "running claude", fmt.Sprintf("pid %d is %s; update Claude Code to ≥ %s so open sessions follow switches", cli.PID, cli.Version, core.MinLiveSwapVersion))
			break
		}
	}
	for _, app := range []desktop.App{desktop.Claude, desktop.Codex} {
		if !paths.IsMac() {
			break
		}
		p, err := app.Path()
		if err != nil {
			check(false, app.Name+" desktop", "not installed")
			continue
		}
		check(true, app.Name+" desktop", p)
	}
	all, _ := c.Vault.All()
	check(len(all) > 0, "accounts", strconv.Itoa(len(all))+" stored")
	for _, r := range c.Config.Remotes {
		info, err := c.RemoteInfo(context.Background(), r)
		if err != nil {
			check(false, "remote "+r.Name, err.Error())
			continue
		}
		check(true, "remote "+r.Name, fmt.Sprintf("%s · dial %s · %d accounts", info.Host, info.Version, len(info.Profiles)))
	}
}

func cmdConfig(args []string) {
	o := parse(args)
	cfg := state.LoadConfig()
	if len(o.pos) == 0 {
		printJSON(cfg)
		return
	}
	key := o.pos[0]
	var m map[string]any
	b, _ := json.Marshal(cfg)
	_ = json.Unmarshal(b, &m)
	if len(o.pos) == 1 {
		v, ok := m[key]
		if !ok {
			dief("unknown key %q", key)
		}
		printJSON(v)
		return
	}
	if _, ok := m[key]; !ok {
		dief("unknown key %q (keys: switch_desktop, restart_apps, usage_ttl_seconds, refresh_inactive)", key)
	}
	var val any
	if err := json.Unmarshal([]byte(o.pos[1]), &val); err != nil {
		val = o.pos[1]
	}
	m[key] = val
	b, _ = json.Marshal(m)
	var next state.Config
	die(json.Unmarshal(b, &next))
	die(state.SaveConfig(next))
	ui.OK(fmt.Sprintf("%s = %s", key, o.pos[1]))
}

// cmdDock asks Dial.app to build one launcher app per desktop account in
// ~/Applications/Dial, so each account gets its own stable Dock icon.
func cmdDock(args []string) {
	o := parse(args)
	app := ""
	for _, cand := range []string{"/Applications/Dial.app/Contents/MacOS/Dial", paths.Home() + "/Applications/Dial.app/Contents/MacOS/Dial"} {
		if _, err := os.Stat(cand); err == nil {
			app = cand
			break
		}
	}
	if app == "" {
		dief("Dial.app is not installed; dock launchers need it")
	}
	a := []string{"--make-launchers"}
	if o.has("pin") {
		a = append(a, "--pin")
	}
	if o.has("replace") {
		a = append(a, "--replace")
	}
	cmd := exec.Command(app, a...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	die(cmd.Run())
}
