package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/medpar/dial/internal/core"
	"github.com/medpar/dial/internal/desktop"
	"github.com/medpar/dial/internal/state"
	"github.com/medpar/dial/internal/ui"
	"github.com/medpar/dial/internal/usage"
	"github.com/medpar/dial/internal/vault"
)

// Status is the JSON model the menu bar app renders.
type Status struct {
	Version     string           `json:"version"`
	Host        string           `json:"host"`
	GeneratedAt time.Time        `json:"generated_at"`
	Providers   []ProviderStatus `json:"providers"`
	Desktop     DesktopInfo      `json:"desktop"`
	Remotes     []RemoteStatus   `json:"remotes,omitempty"`
	Warnings    []string         `json:"warnings,omitempty"`
}

type ProviderStatus struct {
	Provider string          `json:"provider"`
	Active   string          `json:"active,omitempty"`
	Profiles []ProfileStatus `json:"profiles"`
}

type ProfileStatus struct {
	ID       string        `json:"id"`
	Label    string        `json:"label"`
	Email    string        `json:"email,omitempty"`
	Org      string        `json:"org,omitempty"`
	Plan     string        `json:"plan,omitempty"`
	Active   bool          `json:"active"`
	LoggedIn bool          `json:"logged_in"`
	Desktop  string        `json:"desktop,omitempty"` // live | saved | window
	Window   bool          `json:"window,omitempty"`  // own desktop window running
	Usage    *usage.Report `json:"usage,omitempty"`
}

type DesktopInfo struct {
	Claude core.DesktopStatus `json:"claude"`
	Codex  core.DesktopStatus `json:"codex"`
}

type RemoteStatus struct {
	Name string                      `json:"name"`
	SSH  string                      `json:"ssh"`
	Auto bool                        `json:"auto"`
	Sync map[string]state.SyncResult `json:"sync,omitempty"`
}

func buildStatus(c *core.Core, refresh bool, force bool, only vault.Provider) Status {
	host, _ := os.Hostname()
	st := Status{Version: version, Host: host, GeneratedAt: time.Now().UTC()}
	c.SyncCodexWindows()
	ctx := context.Background()
	if refresh || force {
		c.FollowExternalChanges(ctx)
	}
	st.Desktop.Claude = c.ClaudeDesktopStatus()
	st.Desktop.Codex = c.CodexDesktopStatus()
	desk := map[string]core.DesktopAccount{}
	for _, a := range st.Desktop.Claude.Accounts {
		desk[a.AccountUUID] = a
	}
	codexWindows := map[string]bool{}
	for _, id := range st.Desktop.Codex.Windows {
		codexWindows[id] = true
	}
	var all []vault.Profile
	for _, prov := range vault.Providers {
		if only != "" && prov != only {
			continue
		}
		list, _ := c.Vault.List(prov)
		all = append(all, list...)
	}
	var reports []usage.Report
	if refresh || force {
		reports = c.Usage(ctx, all, core.UsageOptions{MaxAge: time.Duration(c.Config.UsageTTL) * time.Second, Force: force})
	} else {
		cache := usage.LoadCache()
		for _, p := range all {
			r, _ := cache.Get(string(p.Provider) + ":" + p.ID)
			reports = append(reports, r)
		}
	}
	idx := 0
	for _, prov := range vault.Providers {
		if only != "" && prov != only {
			continue
		}
		ps := ProviderStatus{Provider: string(prov), Active: c.ActiveID(prov)}
		list, _ := c.Vault.List(prov)
		for _, p := range list {
			pst := ProfileStatus{ID: p.ID, Label: p.Display(), Email: p.Email, Org: p.OrgName, Plan: p.Plan, Active: p.ID == ps.Active}
			switch prov {
			case vault.Claude:
				if cr, err := c.LoadClaude(p); err == nil {
					pst.LoggedIn = cr.OAuth.HasLogin()
				}
				if d, ok := desk[p.AccountID]; ok {
					switch {
					case d.Live:
						pst.Desktop = "live"
					case d.Window:
						pst.Desktop, pst.Window = "window", true
					case d.Saved:
						pst.Desktop = "saved"
					}
				}
			case vault.Codex:
				if a, err := c.LoadCodex(p); err == nil {
					pst.LoggedIn = a.HasLogin()
				}
				if codexWindows[p.ID] {
					pst.Desktop, pst.Window = "window", true
				} else if pst.Active && st.Desktop.Codex.Installed {
					pst.Desktop = "live"
				}
			}
			if idx < len(reports) && !reports[idx].FetchedAt.IsZero() {
				r := reports[idx]
				pst.Usage = &r
				if pst.Plan == "" {
					pst.Plan = r.Plan
				}
			}
			idx++
			ps.Profiles = append(ps.Profiles, pst)
		}
		st.Providers = append(st.Providers, ps)
	}
	s := state.Load()
	for _, r := range c.Config.Remotes {
		rs := RemoteStatus{Name: r.Name, SSH: r.SSH, Auto: r.Auto, Sync: map[string]state.SyncResult{}}
		for _, prov := range vault.Providers {
			if res, ok := s.Sync[r.Name+":"+string(prov)]; ok {
				rs.Sync[string(prov)] = res
			}
		}
		st.Remotes = append(st.Remotes, rs)
	}
	st.Warnings = c.Warnings
	return st
}

func cmdStatus(args []string) {
	o := parse(args, "provider")
	c := core.New()
	prov, _ := vault.ParseProvider(o.get("provider"))
	st := buildStatus(c, o.has("refresh", "r"), o.has("force", "f"), prov)
	if o.has("json") {
		printJSON(st)
		return
	}
	renderStatus(st)
}

func providerTitle(p string) string {
	if p == "claude" {
		return ui.Orange("◖ CLAUDE")
	}
	return ui.Cyan("◗ CODEX")
}

func renderStatus(st Status) {
	empty := true
	for _, ps := range st.Providers {
		if len(ps.Profiles) == 0 {
			continue
		}
		empty = false
		fmt.Println(ui.Bold(providerTitle(ps.Provider)))
		labelW := 10
		for _, p := range ps.Profiles {
			if l := len([]rune(p.Label)); l > labelW {
				labelW = l
			}
		}
		if labelW > 34 {
			labelW = 34
		}
		for i, p := range ps.Profiles {
			mark := "  "
			if p.Active {
				mark = ui.Green("● ")
			}
			num := ui.Dim(fmt.Sprintf("%d ", i+1))
			label := ui.Pad(p.Label, labelW)
			if p.Active {
				label = ui.Bold(label)
			}
			plan := p.Plan
			if p.Org != "" && !strings.Contains(strings.ToLower(p.Org), strings.ToLower(strings.Split(p.Email, "@")[0])) {
				plan = p.Org + " · " + plan
			}
			tags := ""
			switch p.Desktop {
			case "live":
				tags += ui.Dim(" ⌂app")
			case "window":
				tags += ui.Cyan(" ⧉window")
			case "saved":
				tags += ui.Dim(" ⌂saved")
			}
			if !p.LoggedIn {
				tags += ui.Red(" logged out")
			}
			fmt.Printf(" %s%s%s  %s%s\n", mark, num, label, ui.Dim(plan), tags)
			renderUsage(p.Usage, labelW)
		}
		fmt.Println()
	}
	if empty {
		fmt.Println("No accounts yet. Start with:")
		fmt.Println("  dial import          # the accounts you are logged into now")
		fmt.Println("  dial add claude      # log in another Claude account")
		fmt.Println("  dial add codex       # log in another Codex account")
		return
	}
	for _, r := range st.Remotes {
		var parts []string
		for _, prov := range []string{"claude", "codex"} {
			if res, ok := r.Sync[prov]; ok {
				if res.OK {
					parts = append(parts, ui.Green("✓ ")+res.Message+ui.Dim(" "+ui.Ago(res.At)))
				} else {
					parts = append(parts, ui.Red("✗ ")+prov+": "+res.Message)
				}
			}
		}
		auto := ""
		if !r.Auto {
			auto = ui.Dim(" (manual)")
		}
		line := strings.Join(parts, "  ")
		if line == "" {
			line = ui.Dim("not synced yet")
		}
		fmt.Printf("%s %s%s  %s\n", ui.Dim("⇄"), ui.Bold(r.Name), auto, line)
	}
	for _, w := range st.Warnings {
		ui.Warn(w)
	}
}

func renderUsage(r *usage.Report, labelW int) {
	indent := strings.Repeat(" ", 5)
	if r == nil {
		fmt.Println(indent + ui.Dim("usage not fetched yet — dial status --refresh"))
		return
	}
	if len(r.Windows) == 0 && r.Error != "" {
		fmt.Println(indent + ui.Red("usage: ") + ui.Dim(r.Error))
		return
	}
	for _, w := range r.Windows {
		reset := ""
		if w.ResetsAt != nil {
			reset = ui.Dim(fmt.Sprintf("resets in %s (%s)", ui.Until(*w.ResetsAt), ui.When(*w.ResetsAt)))
		}
		pct := ui.Level(w.UsedPercent, fmt.Sprintf("%3.0f%%", w.UsedPercent))
		fmt.Printf("%s%s %s %s  %s\n", indent, ui.Pad(w.Label, 16), ui.Bar(w.UsedPercent, 20), pct, reset)
	}
	var extras []string
	for _, e := range r.Extra {
		extras = append(extras, e.Label+": "+e.Value)
	}
	if len(extras) > 0 {
		fmt.Println(indent + ui.Dim(strings.Join(extras, " · ")))
	}
	if r.Stale {
		msg := "stale (" + ui.Ago(r.FetchedAt) + ")"
		if r.Error != "" {
			msg += ": " + r.Error
		}
		fmt.Println(indent + ui.Yellow(msg))
	}
}

func cmdUsage(args []string) {
	o := parse(args)
	c := core.New()
	var profs []vault.Profile
	if len(o.pos) == 0 {
		profs, _ = c.Vault.All()
	}
	for _, ref := range o.pos {
		p, err := c.Vault.Resolve(ref, "")
		die(err)
		profs = append(profs, p)
	}
	reps := c.Usage(context.Background(), profs, core.UsageOptions{MaxAge: time.Duration(c.Config.UsageTTL) * time.Second, Force: o.has("refresh", "r", "force")})
	if o.has("json") {
		printJSON(reps)
		return
	}
	for i, p := range profs {
		fmt.Printf("%s %s %s\n", providerTitle(string(p.Provider)), ui.Bold(p.Display()), ui.Dim(reps[i].Plan))
		r := reps[i]
		renderUsage(&r, 0)
	}
}

func cmdWatch(args []string) {
	o := parse(args, "interval")
	interval := 60 * time.Second
	if v := o.get("interval"); v != "" {
		if d, err := time.ParseDuration(v + "s"); err == nil && d >= 15*time.Second {
			interval = d
		}
	}
	for {
		c := core.New()
		st := buildStatus(c, true, false, "")
		fmt.Print("\x1b[H\x1b[2J")
		fmt.Println(ui.Dim(fmt.Sprintf("dial · %s · every %s · ctrl-c to quit", time.Now().Format("15:04:05"), interval)))
		fmt.Println()
		renderStatus(st)
		time.Sleep(interval)
	}
}

// cmdWaybar prints a waybar/polybar custom-module JSON line.
func cmdWaybar(args []string) {
	c := core.New()
	st := buildStatus(c, true, false, "")
	var text, tip []string
	maxPct := 0.0
	for _, ps := range st.Providers {
		for _, p := range ps.Profiles {
			if p.Usage == nil {
				continue
			}
			line := fmt.Sprintf("%s %s:", ps.Provider, p.Label)
			for _, w := range p.Usage.Windows {
				line += fmt.Sprintf(" %s %.0f%%", w.Label, w.UsedPercent)
				if w.ResetsAt != nil {
					line += " (" + ui.Until(*w.ResetsAt) + ")"
				}
			}
			tip = append(tip, line)
			if p.Active {
				m := p.Usage.Max()
				if m > maxPct {
					maxPct = m
				}
				text = append(text, fmt.Sprintf("%s %.0f%%", map[string]string{"claude": "", "codex": ""}[ps.Provider]+strings.ToUpper(ps.Provider[:2]), m))
			}
		}
	}
	class := "low"
	switch {
	case maxPct >= 90:
		class = "critical"
	case maxPct >= 75:
		class = "high"
	case maxPct >= 50:
		class = "mid"
	}
	printJSON(map[string]any{"text": strings.Join(text, " · "), "tooltip": strings.Join(tip, "\n"), "class": class, "percentage": int(maxPct)})
}

var _ = desktop.Claude
