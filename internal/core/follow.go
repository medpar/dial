package core

import (
	"context"
	"time"

	"github.com/medpar/dial/internal/desktop"
	"github.com/medpar/dial/internal/paths"
	"github.com/medpar/dial/internal/state"
	"github.com/medpar/dial/internal/vault"
)

// FollowExternalChanges notices account changes made outside dial — a
// `/login` in the CLI, `codex login`, or signing in to another account
// inside Claude Desktop — records them and makes auto remotes follow.
// It is cheap when nothing changed (a few small file reads).
func (c *Core) FollowExternalChanges(ctx context.Context) []state.SyncResult {
	s := state.Load()
	var changed []vault.Profile
	for _, prov := range vault.Providers {
		id := c.ActiveID(prov)
		if id == "" || s.Active[string(prov)] == id {
			continue
		}
		s.Active[string(prov)] = id
		s.Switched[string(prov)] = time.Now().UTC()
		if p, err := c.Vault.Get(prov, id); err == nil {
			changed = append(changed, p)
		}
	}
	if paths.IsMac() && desktop.Claude.Installed() {
		cur := desktop.ClaudeAccountIn(desktop.Claude.DataDir())
		if cur.LoggedIn && s.Active["claude-desktop"] != cur.AccountUUID {
			first := s.Active["claude-desktop"] == ""
			s.Active["claude-desktop"] = cur.AccountUUID
			s.Switched["claude-desktop"] = time.Now().UTC()
			// The desktop account is what the user is looking at, so it
			// wins over a CLI change seen in the same pass.
			if p, ok := c.claudeProfileForAccount(cur.AccountUUID); ok && !first {
				for i := range changed {
					if changed[i].Provider == vault.Claude {
						changed = append(changed[:i], changed[i+1:]...)
						break
					}
				}
				changed = append(changed, p)
			}
		}
	}
	if len(changed) == 0 {
		return nil
	}
	_ = state.Save(s)
	var results []state.SyncResult
	for _, p := range changed {
		results = append(results, c.SyncRemotes(ctx, p)...)
	}
	return results
}
