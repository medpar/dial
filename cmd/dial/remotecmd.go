package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/medpar/dial/internal/core"
	"github.com/medpar/dial/internal/state"
	"github.com/medpar/dial/internal/ui"
	"github.com/medpar/dial/internal/vault"
)

func cmdRemote(args []string) {
	o := parse(args, "providers", "dial")
	c := core.New()
	sub := "ls"
	if len(o.pos) > 0 {
		sub = o.pos[0]
	}
	ctx := context.Background()
	switch sub {
	case "add":
		if len(o.pos) < 3 {
			dief("usage: dial remote add <name> <ssh-target> [--providers claude,codex] [--manual] [--dial path]")
		}
		r := state.Remote{Name: o.pos[1], SSH: o.pos[2], Auto: !o.has("manual"), Dial: o.get("dial")}
		if v := o.get("providers"); v != "" {
			r.Providers = strings.Split(v, ",")
		}
		cfg := state.LoadConfig()
		replaced := false
		for i, x := range cfg.Remotes {
			if x.Name == r.Name {
				cfg.Remotes[i], replaced = r, true
			}
		}
		if !replaced {
			cfg.Remotes = append(cfg.Remotes, r)
		}
		die(state.SaveConfig(cfg))
		c.Config = cfg
		info, err := c.RemoteInfo(ctx, r)
		if err != nil {
			ui.Warn(fmt.Sprintf("saved, but could not reach dial on %s: %v", r.Name, err))
			fmt.Println("Install dial there (see README), then run: dial remote status " + r.Name)
			return
		}
		ui.OK(fmt.Sprintf("Added %s (%s, dial %s, %d accounts)", r.Name, info.Host, info.Version, len(info.Profiles)))
	case "rm", "remove":
		if len(o.pos) < 2 {
			dief("usage: dial remote rm <name>")
		}
		cfg := state.LoadConfig()
		var keep []state.Remote
		for _, x := range cfg.Remotes {
			if x.Name != o.pos[1] {
				keep = append(keep, x)
			}
		}
		cfg.Remotes = keep
		die(state.SaveConfig(cfg))
		ui.OK("Removed remote " + o.pos[1])
	case "ls", "list":
		if o.has("json") {
			printJSON(c.Config.Remotes)
			return
		}
		if len(c.Config.Remotes) == 0 {
			fmt.Println("No remotes. Add one: dial remote add vps user@host")
			return
		}
		for _, r := range c.Config.Remotes {
			mode := "follows switches"
			if !r.Auto {
				mode = "manual"
			}
			provs := "claude,codex"
			if len(r.Providers) > 0 {
				provs = strings.Join(r.Providers, ",")
			}
			fmt.Printf("%-12s %-28s %s %s\n", r.Name, r.SSH, mode, ui.Dim(provs))
		}
	case "status":
		targets := c.Config.Remotes
		if len(o.pos) > 1 {
			r, err := c.Remote(o.pos[1])
			die(err)
			targets = []state.Remote{r}
		}
		local, _ := c.Vault.All()
		for _, r := range targets {
			info, err := c.RemoteInfo(ctx, r)
			if err != nil {
				fmt.Printf("%s %s: %v\n", ui.Red("✗"), r.Name, err)
				continue
			}
			if o.has("json") {
				printJSON(info)
				continue
			}
			fmt.Printf("%s %s %s\n", ui.Bold(r.Name), info.Host, ui.Dim("dial "+info.Version))
			have := map[string]core.RemoteProfile{}
			for _, rp := range info.Profiles {
				have[rp.Key] = rp
			}
			for _, p := range local {
				rp, ok := have[p.Key()]
				switch {
				case !ok:
					fmt.Printf("   %s %s:%-16s %s\n", ui.Yellow("○"), p.Provider, p.ID, ui.Dim("missing — dial remote seed "+r.Name+" "+string(p.Provider)+":"+p.ID))
				case rp.Active:
					fmt.Printf("   %s %s:%-16s %s\n", ui.Green("●"), p.Provider, p.ID, ui.Green("active"))
				default:
					fmt.Printf("   %s %s:%-16s %s\n", ui.Dim("·"), p.Provider, p.ID, ui.Dim("ready"))
				}
				delete(have, p.Key())
			}
			for _, rp := range have {
				fmt.Printf("   %s %s:%-16s %s\n", ui.Dim("·"), rp.Provider, rp.ID, ui.Dim("only on "+r.Name))
			}
		}
	case "seed":
		if len(o.pos) < 2 {
			dief("usage: dial remote seed <name> <account|--all> [--share] [--activate]")
		}
		r, err := c.Remote(o.pos[1])
		die(err)
		var profs []vault.Profile
		if o.has("all") {
			info, err := c.RemoteInfo(ctx, r)
			die(err)
			have := map[string]bool{}
			for _, rp := range info.Profiles {
				if rp.LoggedIn {
					have[rp.Key] = true
				}
			}
			all, _ := c.Vault.All()
			for _, p := range all {
				if remoteFollowsProv(r, p.Provider) && !have[p.Key()] {
					profs = append(profs, p)
				}
			}
			if len(profs) == 0 {
				ui.OK(r.Name + " already has a login for every account")
				return
			}
		} else {
			for _, ref := range o.pos[2:] {
				p, err := c.Vault.Resolve(ref, "")
				die(err)
				profs = append(profs, p)
			}
		}
		for _, p := range profs {
			var resp core.ApplyResponse
			if o.has("share") {
				resp, err = c.Share(ctx, r, p, o.has("activate"))
			} else {
				fmt.Printf("%s Minting a separate %s login for %s on %s — sign in as %s in the browser\n", ui.Cyan("→"), p.Provider, r.Name, p.Display(), ui.Bold(p.Email))
				resp, err = c.Seed(ctx, r, p, loginOpts(o), o.has("activate"))
			}
			if err != nil {
				fmt.Printf("%s %s:%s: %v\n", ui.Red("✗"), p.Provider, p.ID, err)
				continue
			}
			if !resp.OK {
				fmt.Printf("%s %s:%s: %s\n", ui.Red("✗"), p.Provider, p.ID, resp.Message)
				continue
			}
			ui.OK(fmt.Sprintf("%s now has its own login for %s (%s:%s)", r.Name, p.Display(), p.Provider, resp.ProfileID))
		}
	case "sync":
		var targets []state.Remote
		if len(o.pos) > 1 {
			r, err := c.Remote(o.pos[1])
			die(err)
			r.Auto = true
			targets = []state.Remote{r}
		} else {
			targets = c.Config.Remotes
		}
		c.Config.Remotes = targets
		for _, prov := range vault.Providers {
			id := c.ActiveID(prov)
			if id == "" {
				continue
			}
			p, err := c.Vault.Get(prov, id)
			if err != nil {
				continue
			}
			for _, res := range c.SyncRemotes(ctx, p) {
				if res.OK {
					ui.OK(res.Remote + ": " + res.Message)
				} else {
					fmt.Println(ui.Yellow("! ") + res.Remote + ": " + res.Message)
				}
			}
		}
	default:
		dief("unknown remote command %q", sub)
	}
}

func remoteFollowsProv(r state.Remote, p vault.Provider) bool {
	if len(r.Providers) == 0 {
		return true
	}
	for _, x := range r.Providers {
		if strings.EqualFold(x, string(p)) {
			return true
		}
	}
	return false
}

// cmdRemoteAgent is what a remote dial runs over ssh. It reads JSON on
// stdin and writes JSON on stdout; it never prints secrets.
func cmdRemoteAgent(args []string) {
	if len(args) == 0 {
		os.Exit(2)
	}
	c := core.New()
	switch args[0] {
	case "info":
		printJSON(c.Info(version))
	case "apply":
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			printJSON(core.ApplyResponse{Message: err.Error()})
			os.Exit(1)
		}
		var req core.ApplyRequest
		if err := json.Unmarshal(data, &req); err != nil {
			printJSON(core.ApplyResponse{Message: "bad request: " + err.Error()})
			os.Exit(1)
		}
		resp := c.Apply(context.Background(), req)
		if resp.OK && len(c.Warnings) > 0 {
			resp.Message = strings.Join(c.Warnings, "; ")
		}
		printJSON(resp)
		if !resp.OK {
			os.Exit(1)
		}
	default:
		os.Exit(2)
	}
}
