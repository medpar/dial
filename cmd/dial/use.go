package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/medpar/dial/internal/core"
	"github.com/medpar/dial/internal/state"
	"github.com/medpar/dial/internal/ui"
	"github.com/medpar/dial/internal/usage"
	"github.com/medpar/dial/internal/vault"
)

// UseResult is the JSON reply of `dial use --json`.
type UseResult struct {
	OK       bool               `json:"ok"`
	Provider string             `json:"provider"`
	Profile  string             `json:"profile"`
	Label    string             `json:"label"`
	CLI      string             `json:"cli"`     // ok | error text
	Desktop  string             `json:"desktop"` // ok | skipped: … | error text
	Remotes  []state.SyncResult `json:"remotes,omitempty"`
	Warnings []string           `json:"warnings,omitempty"`
}

func cmdUse(args []string) {
	o := parse(args)
	if len(o.pos) == 0 {
		dief("usage: dial use <account> [--cli|--desktop] [--no-remote] [--no-restart]")
	}
	c := core.New()
	p, err := c.Vault.Resolve(o.pos[0], "")
	die(err)
	res := switchTo(c, p, o)
	if o.has("json") {
		printJSON(res)
		return
	}
	reportUse(res)
	if !res.OK {
		ui.Fatal(fmt.Errorf("switch incomplete"))
	}
}

func switchTo(c *core.Core, p vault.Profile, o opts) UseResult {
	ctx := context.Background()
	res := UseResult{Provider: string(p.Provider), Profile: p.ID, Label: p.Display(), OK: true}
	// Never put a login that is known to be dead into the live location.
	if r, ok := usage.LoadCache().Get(string(p.Provider) + ":" + p.ID); ok && !o.has("force") &&
		strings.Contains(r.Error, "login expired") && len(r.Windows) == 0 {
		res.OK = false
		res.CLI = fmt.Sprintf("%s's login has expired; renew it with `dial login %s:%s` (or pass --force)", p.Display(), p.Provider, p.ID)
		return res
	}
	onlyCLI, onlyDesktop := o.has("cli"), o.has("desktop", "app")
	doCLI := !onlyDesktop
	doDesktop := !onlyCLI && c.Config.SwitchDesktop
	if onlyDesktop {
		doDesktop = true
	}
	if p.Provider == vault.Codex && c.CodexDesktopStatus().Installed {
		for _, w := range c.CodexDesktopStatus().Windows {
			if w == p.ID {
				res.OK = false
				res.CLI = p.Display() + " is open in its own Codex window; close it first"
				return res
			}
		}
	}
	if doCLI || (p.Provider == vault.Codex && doDesktop) {
		// Codex CLI and desktop share ~/.codex/auth.json: one write switches both.
		if err := c.Use(ctx, p); err != nil {
			res.OK, res.CLI = false, err.Error()
		} else {
			res.CLI = "ok"
		}
	} else {
		res.CLI = "skipped"
	}
	switch {
	case !doDesktop:
		res.Desktop = "skipped"
	case p.Provider == vault.Codex:
		if res.CLI != "ok" {
			res.Desktop = "skipped"
		} else if o.has("no-restart") || !c.Config.RestartApps {
			res.Desktop = "ok (restart the Codex app to apply)"
		} else if err := c.RestartCodexApp(); err != nil {
			res.Desktop = err.Error()
		} else {
			res.Desktop = "ok"
		}
	case p.Provider == vault.Claude:
		res.Desktop = claudeDesktop(c, p, o, onlyDesktop, &res)
	}
	if res.CLI == "ok" && !o.has("no-remote", "local") {
		res.Remotes = c.SyncRemotes(ctx, p)
	}
	// Record what is live now so `dial follow` does not replay this switch.
	c.FollowExternalChanges(ctx)
	res.Warnings = c.Warnings
	return res
}

// claudeDesktop applies a switch to Claude Desktop. Each account keeps its
// own window, so a switch never moves accounts between windows behind the
// user's back:
//   - default: nothing changes on the desktop;
//   - --show: the account's window comes forward (or opens);
//   - --desktop / `dial desktop use`: the main window's account is swapped.
func claudeDesktop(c *core.Core, p vault.Profile, o opts, swap bool, res *UseResult) string {
	if !c.ClaudeDesktopStatus().Installed {
		return "skipped: Claude Desktop not installed"
	}
	w := c.WhereDesktop(p)
	switch {
	case swap:
		switch w.Kind {
		case "none":
			res.OK = false
			return "no Desktop sign-in for this account yet (dial desktop add claude)"
		case "window":
			res.OK = false
			return p.Display() + " is open in its own window; close it before making it the main window's account"
		}
		if err := c.SwitchClaudeDesktop(p); err != nil {
			res.OK = false
			return err.Error()
		}
		return "ok"
	case o.has("show"):
		if _, err := c.ShowDesktop(p); err != nil {
			if w.Kind == "none" {
				return "skipped: no Desktop sign-in for this account yet (Add account → Claude Desktop)"
			}
			return err.Error()
		}
		return "ok"
	}
	return "skipped"
}

func reportUse(res UseResult) {
	if res.CLI == "ok" {
		ui.OK(fmt.Sprintf("%s CLI → %s", res.Provider, ui.Bold(res.Label)))
	} else if res.CLI != "skipped" {
		fmt.Println(ui.Red("✗ ") + "CLI: " + res.CLI)
	}
	switch {
	case res.Desktop == "ok":
		ui.OK(fmt.Sprintf("%s desktop app → %s", res.Provider, ui.Bold(res.Label)))
	case res.Desktop == "skipped" || res.Desktop == "":
	case len(res.Desktop) > 8 && res.Desktop[:8] == "skipped:":
		fmt.Println(ui.Dim("· desktop " + res.Desktop))
	case len(res.Desktop) > 2 && res.Desktop[:2] == "ok":
		ui.OK("desktop " + res.Desktop)
	default:
		fmt.Println(ui.Red("✗ ") + "desktop: " + res.Desktop)
	}
	for _, r := range res.Remotes {
		if r.OK {
			ui.OK(fmt.Sprintf("%s → %s", r.Remote, r.Message))
		} else {
			fmt.Println(ui.Yellow("! ") + r.Remote + ": " + r.Message)
		}
	}
	for _, w := range res.Warnings {
		ui.Warn(w)
	}
}

// cmdCycle switches provider to the next account, or the one with the most
// headroom when best is set.
func cmdCycle(args []string, best bool) {
	o := parse(args)
	if len(o.pos) == 0 {
		dief("usage: dial %s claude|codex", map[bool]string{true: "best", false: "next"}[best])
	}
	prov, ok := vault.ParseProvider(o.pos[0])
	if !ok {
		dief("unknown provider %q", o.pos[0])
	}
	c := core.New()
	list, _ := c.Vault.List(prov)
	var usable []vault.Profile
	for _, p := range list {
		ok := false
		if prov == vault.Claude {
			cr, err := c.LoadClaude(p)
			ok = err == nil && cr.OAuth.HasLogin()
		} else {
			a, err := c.LoadCodex(p)
			ok = err == nil && a.HasLogin()
		}
		if ok {
			usable = append(usable, p)
		}
	}
	if len(usable) == 0 {
		dief("no %s accounts with a login", prov)
	}
	active := c.ActiveID(prov)
	var target vault.Profile
	if best {
		reps := c.Usage(context.Background(), usable, core.UsageOptions{MaxAge: 2 * time.Minute})
		type cand struct {
			p   vault.Profile
			r   usage.Report
			pct float64
		}
		var cands []cand
		for i, p := range usable {
			pct := reps[i].Max()
			if reps[i].Error != "" && len(reps[i].Windows) == 0 {
				pct = 1000
			}
			cands = append(cands, cand{p, reps[i], pct})
		}
		sort.SliceStable(cands, func(i, j int) bool { return cands[i].pct < cands[j].pct })
		target = cands[0].p
		if target.ID == active {
			ui.OK(fmt.Sprintf("%s is already the account with most headroom (%.0f%% used)", target.Display(), cands[0].pct))
			return
		}
	} else {
		idx := 0
		for i, p := range usable {
			if p.ID == active {
				idx = (i + 1) % len(usable)
			}
		}
		target = usable[idx]
	}
	res := switchTo(c, target, o)
	if o.has("json") {
		printJSON(res)
		return
	}
	reportUse(res)
}
