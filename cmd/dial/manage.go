package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/medpar/dial/internal/core"
	"github.com/medpar/dial/internal/desktop"
	"github.com/medpar/dial/internal/ui"
	"github.com/medpar/dial/internal/usage"
	"github.com/medpar/dial/internal/vault"
)

func loginOpts(o opts) core.LoginOptions {
	return core.LoginOptions{Email: o.get("email"), SSO: o.has("sso"), Device: o.has("device", "device-auth", "headless")}
}

func cmdAdd(args []string) {
	o := parse(args, "label", "email")
	if len(o.pos) == 0 {
		dief("usage: dial add claude|codex [--label name] [--email you@x] [--device]")
	}
	prov, ok := vault.ParseProvider(o.pos[0])
	if !ok {
		dief("unknown provider %q", o.pos[0])
	}
	c := core.New()
	label := o.get("label")
	if label == "" && len(o.pos) > 1 {
		label = strings.Join(o.pos[1:], " ")
	}
	fmt.Println(ui.Dim("Your current login is not touched. Sign in with the account to add —"))
	fmt.Println(ui.Dim("the browser uses whatever account it is signed into, so switch it there first."))
	p, created, err := c.Add(context.Background(), prov, label, loginOpts(o))
	die(err)
	verb := "Updated"
	if created {
		verb = "Added"
	}
	ui.OK(fmt.Sprintf("%s %s:%s (%s %s)", verb, prov, p.ID, p.Email, p.Plan))
	fmt.Printf("Switch to it with: dial use %s:%s\n", prov, p.ID)
}

func cmdLogin(args []string) {
	o := parse(args, "email")
	if len(o.pos) == 0 {
		dief("usage: dial login <account>")
	}
	c := core.New()
	p, err := c.Vault.Resolve(o.pos[0], "")
	die(err)
	opt := loginOpts(o)
	if opt.Email == "" {
		opt.Email = p.Email
	}
	np, _, err := c.Add(context.Background(), p.Provider, p.Label, opt)
	die(err)
	if np.ID != p.ID {
		ui.Warn(fmt.Sprintf("signed in as %s, which is a different account (%s:%s)", np.Email, np.Provider, np.ID))
		return
	}
	ui.OK(fmt.Sprintf("Renewed login for %s:%s", p.Provider, p.ID))
	if c.ActiveID(p.Provider) == p.ID {
		die(c.Use(context.Background(), np))
	}
}

func cmdImport(args []string) {
	o := parse(args)
	src := "all"
	if len(o.pos) > 0 {
		src = o.pos[0]
	}
	c := core.New()
	ctx := context.Background()
	var results []core.ImportResult
	// Named sources first so their labels win over email-derived ones.
	if src == "all" || src == "codex-auth" {
		results = append(results, c.ImportCodexAuthTool()...)
	}
	if src == "cps" || src == "codex-profile-switcher" || (src == "all" && o.has("cps")) {
		results = append(results, c.ImportCPS(ctx, o.has("disable-cps"))...)
	}
	if src == "all" || src == "live" || src == "current" {
		results = append(results, c.ImportLive(ctx)...)
	}
	if o.has("json") {
		printJSON(results)
		return
	}
	if len(results) == 0 {
		fmt.Println("Nothing to import. Log in with `dial add claude` / `dial add codex`.")
		return
	}
	for _, r := range results {
		switch {
		case r.Err != nil:
			fmt.Printf("%s %s: %v\n", ui.Yellow("!"), r.Source, r.Err)
		case r.Created:
			ui.OK(fmt.Sprintf("%s → %s:%s (%s %s)", r.Source, r.Profile.Provider, r.Profile.ID, r.Profile.Email, r.Profile.Plan))
		default:
			fmt.Printf("%s %s → %s:%s already known%s\n", ui.Dim("="), r.Source, r.Profile.Provider, r.Profile.ID, ui.Dim(" (kept newest login)"))
		}
	}
}

func cmdList(args []string) {
	o := parse(args)
	c := core.New()
	all, err := c.Vault.All()
	die(err)
	if o.has("json") {
		printJSON(all)
		return
	}
	for _, prov := range vault.Providers {
		active := c.ActiveID(prov)
		n := 0
		for _, p := range all {
			if p.Provider != prov {
				continue
			}
			n++
			mark := "  "
			if p.ID == active {
				mark = ui.Green("● ")
			}
			fmt.Printf("%s%s:%-16s %-34s %s\n", mark, prov, p.ID, p.Email, ui.Dim(p.Plan))
		}
		_ = n
	}
}

func cmdRename(args []string) {
	o := parse(args)
	if len(o.pos) < 2 {
		dief("usage: dial rename <account> <new label> [--id]")
	}
	c := core.New()
	p, err := c.Vault.Resolve(o.pos[0], "")
	die(err)
	label := strings.Join(o.pos[1:], " ")
	p.Label = label
	die(c.Vault.Save(p))
	if o.has("id") {
		die(c.Vault.Rename(p.Provider, p.ID, label))
		usage.LoadCache().Delete(string(p.Provider) + ":" + p.ID)
	}
	ui.OK(fmt.Sprintf("%s:%s is now %q", p.Provider, p.ID, label))
}

func cmdRemove(args []string) {
	o := parse(args)
	if len(o.pos) == 0 {
		dief("usage: dial rm <account> [-y]")
	}
	c := core.New()
	p, err := c.Vault.Resolve(o.pos[0], "")
	die(err)
	if !o.has("y", "yes") {
		fmt.Printf("Remove %s:%s (%s) from dial? The login itself is not revoked. [y/N] ", p.Provider, p.ID, p.Email)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y") {
			fmt.Println("Cancelled.")
			return
		}
	}
	die(c.Vault.Delete(p.Provider, p.ID))
	cache := usage.LoadCache()
	cache.Delete(string(p.Provider) + ":" + p.ID)
	_ = cache.Save()
	ui.OK(fmt.Sprintf("Removed %s:%s", p.Provider, p.ID))
}

func cmdRun(args []string) {
	o := parse(args)
	if len(o.pos) == 0 {
		dief("usage: dial run <account> [-- claude/codex args]")
	}
	c := core.New()
	p, err := c.Vault.Resolve(o.pos[0], "")
	die(err)
	extra := append(o.pos[1:], o.rest...)
	code, err := c.Run(context.Background(), p, extra)
	die(err)
	os.Exit(code)
}

func cmdEnv(args []string) {
	o := parse(args)
	if len(o.pos) == 0 {
		dief("usage: eval \"$(dial env <account>)\"")
	}
	c := core.New()
	p, err := c.Vault.Resolve(o.pos[0], "")
	die(err)
	env, err := c.Env(context.Background(), p)
	die(err)
	for k, v := range env {
		fmt.Printf("export %s=%q\n", k, v)
	}
}

func cmdOpen(args []string) {
	o := parse(args)
	if len(o.pos) == 0 {
		dief("usage: dial open <account>")
	}
	c := core.New()
	p, err := c.Vault.Resolve(o.pos[0], "")
	die(err)
	switch p.Provider {
	case vault.Claude:
		err = c.OpenClaudeWindow(p)
	case vault.Codex:
		err = c.OpenCodexWindow(p)
	}
	if o.has("json") {
		msg := "ok"
		if err != nil {
			msg = err.Error()
		}
		printJSON(map[string]any{"ok": err == nil, "message": msg})
		return
	}
	die(err)
	ui.OK(fmt.Sprintf("Opened a %s window as %s", map[vault.Provider]string{vault.Claude: "Claude", vault.Codex: "Codex"}[p.Provider], ui.Bold(p.Display())))
}

func cmdDesktop(args []string) {
	o := parse(args)
	c := core.New()
	sub := ""
	if len(o.pos) > 0 {
		sub = o.pos[0]
	}
	switch sub {
	case "", "status":
		cs, xs := c.ClaudeDesktopStatus(), c.CodexDesktopStatus()
		if o.has("json") {
			printJSON(DesktopInfo{Claude: cs, Codex: xs})
			return
		}
		state := func(installed, running bool) string {
			if !installed {
				return ui.Dim("not installed")
			}
			if running {
				return ui.Green("running")
			}
			return ui.Dim("closed")
		}
		fmt.Printf("%s %s\n", ui.Bold(providerTitle("claude")+" Desktop"), state(cs.Installed, cs.Running))
		for _, a := range cs.Accounts {
			tag := ui.Dim("saved")
			if a.Live {
				tag = ui.Green("main window")
			} else if a.Window {
				tag = ui.Cyan("own window")
			}
			fmt.Printf("   %-36s %s\n", a.Label, tag)
		}
		fmt.Printf("%s %s %s\n", ui.Bold(providerTitle("codex")+" Desktop"), state(xs.Installed, xs.Running), ui.Dim("(follows the Codex CLI account)"))
		for _, w := range xs.Windows {
			fmt.Printf("   %-36s %s\n", w, ui.Cyan("own window"))
		}
	case "add", "login":
		prov := vault.Claude
		var expect vault.Profile
		if len(o.pos) > 1 {
			if p, ok := vault.ParseProvider(o.pos[1]); ok {
				prov = p
			} else if sub == "login" {
				p, err := c.Vault.Resolve(o.pos[1], vault.Claude)
				die(err)
				expect = p
			}
		}
		if prov != vault.Claude {
			dief("Codex Desktop uses the Codex CLI login: run `dial add codex`")
		}
		opt := core.SigninOptions{
			Expect:   expect.AccountID,
			NoWindow: o.has("no-window"),
			Progress: func(s string) { fmt.Println(ui.Cyan("→ ") + s) },
		}
		acct, err := c.AddClaudeDesktop(opt)
		if o.has("json") {
			msg := "ok"
			if err != nil {
				msg = err.Error()
			}
			printJSON(map[string]any{"ok": err == nil, "message": msg, "account": acct})
			return
		}
		die(err)
		ui.OK(fmt.Sprintf("Saved Claude Desktop login for %s", ui.Bold(acct.Label)))
		if acct.Window {
			ui.OK("It is open in its own window; your other windows are back.")
		}
		if acct.ProfileID == "" {
			fmt.Println("Add its CLI login too (usage, terminal sessions, the VPS): dial add claude")
		}
	case "use", "switch":
		if len(o.pos) < 2 {
			dief("usage: dial desktop use <account>")
		}
		p, err := c.Vault.Resolve(o.pos[1], "")
		die(err)
		res := switchTo(c, p, parse([]string{"--desktop", "--no-remote"}))
		if o.has("json") {
			printJSON(res)
			return
		}
		reportUse(res)
	case "accounts":
		printJSON(desktop.ParkedClaude())
	default:
		dief("unknown desktop command %q", sub)
	}
}
