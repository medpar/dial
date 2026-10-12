package desktop

import (
	"os"
	"path/filepath"

	"github.com/medpar/dial/internal/fsutil"
	"github.com/medpar/dial/internal/paths"
)

// Codex side-by-side instances get their own CODEX_HOME (own auth.json,
// own SQLite state and daemon sockets) that symlinks the user's shared
// content from ~/.codex, so config, skills, memories and session history
// are the same in every window. SQLite files are never shared: concurrent
// app-servers on one WAL database stall with "Reconnecting…".
var codexShared = []string{
	"config.toml", "AGENTS.md", "instructions.md", "prompts", "rules", "skills",
	"memories", "memories_v2", "sessions", "archived_sessions", "history.jsonl",
	"session_index.jsonl", "plugins", "hooks.json", "keybindings.json",
	"vendor_imports", "packages", "generated_images", "artifacts", "attachments",
	"automations", "worktrees", "pets", "visualizations", "computer-use",
}

// Copied once (not linked) so each instance can diverge safely.
var codexCopied = []string{".codex-global-state.json", "internal_storage.json"}

// CodexInstanceRoot holds the instance for one dial profile.
func CodexInstanceRoot(profileID string) string {
	return filepath.Join(paths.DialHome(), "desktop", "codex", profileID)
}

func CodexInstanceHome(profileID string) string {
	return filepath.Join(CodexInstanceRoot(profileID), "home")
}

func CodexInstanceData(profileID string) string {
	return filepath.Join(CodexInstanceRoot(profileID), "data")
}

// PrepareCodexHome creates or repairs the instance CODEX_HOME. It never
// touches auth.json; the caller places credentials.
func PrepareCodexHome(home string) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	src := paths.CodexHome()
	for _, name := range codexShared {
		target := filepath.Join(src, name)
		if _, err := os.Lstat(target); err != nil {
			continue
		}
		link := filepath.Join(home, name)
		if cur, err := os.Readlink(link); err == nil && cur == target {
			continue
		}
		if _, err := os.Lstat(link); err == nil {
			continue // real file created by the instance; leave it
		}
		if err := os.Symlink(target, link); err != nil {
			return err
		}
	}
	for _, name := range codexCopied {
		dst := filepath.Join(home, name)
		if fsutil.Exists(dst) || !fsutil.Exists(filepath.Join(src, name)) {
			continue
		}
		_ = fsutil.CopyFile(filepath.Join(src, name), dst, 0o600)
	}
	return nil
}
