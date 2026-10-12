package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/medpar/dial/internal/claude"
	"github.com/medpar/dial/internal/codex"
	"github.com/medpar/dial/internal/fsutil"
	"github.com/medpar/dial/internal/paths"
	"github.com/medpar/dial/internal/vault"
)

// ImportResult describes one imported or updated profile.
type ImportResult struct {
	Profile vault.Profile
	Created bool
	Source  string
	Err     error
}

// ImportLive imports whatever the CLIs are logged into right now.
func (c *Core) ImportLive(ctx context.Context) []ImportResult {
	l, err := c.Lock()
	if err != nil {
		return []ImportResult{{Err: err}}
	}
	defer l.Release()
	var out []ImportResult
	if o, err := claude.DefaultTarget().ReadOAuth(); err == nil && o.HasLogin() {
		p, created, err := c.ImportClaude(ctx, claude.DefaultTarget(), "", "import")
		out = append(out, ImportResult{p, created, "claude (current login)", err})
	}
	if a, err := codex.Load(codex.LivePath()); err == nil && a.HasLogin() {
		p, created, err := c.ImportCodexAuth(a, "", "import", "")
		out = append(out, ImportResult{p, created, "codex (current login)", err})
	}
	return out
}

// ImportCodexAuthTool imports accounts saved by loongphy/codex-auth from
// ~/.codex/accounts (both the old <name>.json and the newer registry).
func (c *Core) ImportCodexAuthTool() []ImportResult {
	dir := filepath.Join(paths.CodexHome(), "accounts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	aliases := map[string]string{}
	var reg struct {
		Accounts []struct {
			Key   string `json:"record_key"`
			Alias string `json:"alias"`
			Email string `json:"email"`
		} `json:"accounts"`
	}
	if fsutil.ReadJSON(filepath.Join(dir, "registry.json"), &reg) == nil {
		for _, a := range reg.Accounts {
			if a.Alias != "" {
				aliases[a.Key] = a.Alias
			}
		}
	}
	l, err := c.Lock()
	if err != nil {
		return []ImportResult{{Err: err}}
	}
	defer l.Release()
	var out []ImportResult
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || name == "registry.json" {
			continue
		}
		a, err := codex.Load(filepath.Join(dir, name))
		if err != nil || !a.HasLogin() {
			continue
		}
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".json"), ".auth")
		label := base
		if al, ok := aliases[base]; ok {
			label = al
		} else if len(base) > 40 { // base64url record key, no alias
			label = ""
		}
		p, created, err := c.ImportCodexAuth(a, label, "codex-auth", "")
		out = append(out, ImportResult{p, created, "codex-auth " + base, err})
	}
	return out
}

const cpsHelper = "/Applications/CodexProfileSwitcher.app/Contents/Helpers/codex-profile"

// ImportCPS imports profiles from 4LAU/codex-profile-switcher through its
// own CLI (`lease begin` materialises one profile's auth into a private
// CODEX_HOME). disable stops its daily token renewal afterwards, which
// would otherwise rotate the refresh tokens dial now holds.
func (c *Core) ImportCPS(ctx context.Context, disable bool) []ImportResult {
	if !fsutil.Exists(cpsHelper) {
		return nil
	}
	out, err := exec.Command(cpsHelper, "list", "--json").Output()
	if err != nil {
		return []ImportResult{{Source: "codex-profile-switcher", Err: fmt.Errorf("codex-profile list: %v", err)}}
	}
	var list []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return []ImportResult{{Source: "codex-profile-switcher", Err: err}}
	}
	labels := map[string]string{}
	var cfg struct {
		Profiles []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"profiles"`
	}
	if fsutil.ReadJSON(filepath.Join(paths.Home(), ".codex-switcher", "config.json"), &cfg) == nil {
		for _, p := range cfg.Profiles {
			labels[p.ID] = p.Label
		}
	}
	var results []ImportResult
	for _, item := range list {
		var others []string
		for _, o := range list {
			if o.ID != item.ID {
				others = append(others, o.ID)
			}
		}
		args := []string{"lease", "begin", "--json", "--non-interactive", "--ttl", "120"}
		if len(others) > 0 {
			args = append(args, "--exclude", strings.Join(others, ","))
		}
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		raw, err := exec.CommandContext(cctx, cpsHelper, args...).Output()
		cancel()
		src := "codex-profile-switcher " + item.ID
		if l := labels[item.ID]; l != "" {
			src += " (" + l + ")"
		}
		if err != nil {
			results = append(results, ImportResult{Source: src, Err: fmt.Errorf("could not export (it may be at its usage limit); log in with `dial add codex` instead")})
			continue
		}
		var lease struct {
			Profile string `json:"profile"`
			Home    string `json:"home"`
			Token   string `json:"token"`
		}
		if err := json.Unmarshal(raw, &lease); err != nil || lease.Home == "" {
			results = append(results, ImportResult{Source: src, Err: fmt.Errorf("unexpected lease output")})
			continue
		}
		a, err := codex.Load(filepath.Join(lease.Home, codex.AuthFile))
		if err == nil {
			l, lerr := c.Lock()
			if lerr == nil {
				p, created, ierr := c.ImportCodexAuth(a, cleanCPSLabel(labels[item.ID]), "codex-profile-switcher", "")
				l.Release()
				results = append(results, ImportResult{p, created, src, ierr})
			}
		} else {
			results = append(results, ImportResult{Source: src, Err: err})
		}
		_ = exec.Command(cpsHelper, "lease", "end", lease.Token, "--profile", lease.Profile, "--non-interactive").Run()
	}
	if disable {
		uid := fmt.Sprint(os.Getuid())
		_ = exec.Command("launchctl", "disable", "gui/"+uid+"/com.4lau.codex-profile-switcher.renew").Run()
		_ = exec.Command("launchctl", "bootout", "gui/"+uid+"/com.4lau.codex-profile-switcher.renew").Run()
		_ = exec.Command("osascript", "-e", `tell application id "com.4lau.codex-profile-switcher" to quit`).Run()
	}
	return results
}

// "Profile 4 - paki1" → "paki1"
func cleanCPSLabel(s string) string {
	if i := strings.Index(s, " - "); i >= 0 && strings.HasPrefix(s, "Profile ") {
		return strings.TrimSpace(s[i+3:])
	}
	return s
}
