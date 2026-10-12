package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/medpar/dial/internal/fsutil"
	"github.com/medpar/dial/internal/paths"
)

// Codex side-by-side instances get their own CODEX_HOME (own auth.json,
// SQLite state, daemon sockets and thread history) that symlinks the user's
// shared setup from ~/.codex, so config, skills, rules, memories and plugins
// are the same in every window.
//
// Not shared, on purpose:
//   - SQLite files: concurrent app-servers on one WAL database stall.
//   - sessions/: each instance keeps its own thread database, so shared
//     rollout files would show threads the instance knows nothing about.
var codexShared = []string{
	"config.toml", "AGENTS.md", "instructions.md", "prompts", "rules", "skills",
	"memories", "memories_v2", "history.jsonl", "plugins", "hooks.json",
	"keybindings.json", "vendor_imports", "packages", "generated_images",
	"artifacts", "attachments", "automations", "pets", "visualizations",
	"computer-use",
}

// Links that earlier versions created and that must go.
var codexNoLongerShared = []string{"sessions", "archived_sessions", "session_index.jsonl", "worktrees"}

// Global-state keys a new window may inherit: plain preferences and remote
// host definitions. Everything else in .codex-global-state.json is tied to
// an account (remote-control enrollments and environment ids, push tokens,
// cached account ids) or to the main window's projects and threads.
var codexStateKeep = map[string]bool{
	"electron-saved-workspace-roots":                      true,
	"electron-workspace-root-labels":                      true,
	"codex-managed-remote-connections":                    true,
	"remote-connection-auto-connect-by-host-id":           true,
	"remote-projects":                                     true,
	"selected-remote-host-id":                             true,
	"use-copilot-auth-if-available":                       true,
	"electron-prefer-mxc-enabled":                         true,
	"computer-use-bundled-plugin-auto-install-disabled":   true,
	"global-dictation-keep-visible":                       true,
	"electron-initial-follow-up-queue-mode":               true,
	"electron-openai-mcp-form-elicitations-enabled":       true,
	"electron-remote-hosted-pip-content-max-display-size": true,
	"electron-internal-update-cdn-enabled":                true,
}

const codexStateFile = ".codex-global-state.json"

// homeVersion marks homes laid out by this version of dial.
const homeVersion = ".dial-home-v2"

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

// sanitizedState returns the main window's global state reduced to keys a
// window for another account may safely inherit.
func sanitizedState(src string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := fsutil.ReadJSON(src, &m); err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for k, v := range m {
		if codexStateKeep[k] {
			out[k] = v
		}
	}
	return fsutil.MarshalIndent(out)
}

// PrepareCodexHome creates or repairs the instance CODEX_HOME. It never
// touches auth.json; the caller places credentials. busy means an app or
// CLI is using the home right now, so its state files are left alone.
func PrepareCodexHome(home string, busy bool) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	src := paths.CodexHome()
	fresh := !fsutil.Exists(filepath.Join(home, codexStateFile))
	if !busy && !fsutil.Exists(filepath.Join(home, homeVersion)) {
		// Homes from dial ≤ 0.1.5 linked thread history and copied the main
		// window's whole global state; undo both once.
		for _, name := range codexNoLongerShared {
			link := filepath.Join(home, name)
			if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(link)
			}
		}
		if !fresh {
			if data, err := sanitizedState(filepath.Join(home, codexStateFile)); err == nil {
				_ = fsutil.WriteFileAtomic(filepath.Join(home, codexStateFile), data, 0o644)
				_ = os.Remove(filepath.Join(home, codexStateFile+".bak"))
			}
		}
	}
	for _, name := range codexShared {
		target := filepath.Join(src, name)
		if _, err := os.Lstat(target); err != nil {
			continue
		}
		link := filepath.Join(home, name)
		if _, err := os.Lstat(link); err == nil {
			continue // already linked, or a real file the instance created
		}
		if err := os.Symlink(target, link); err != nil {
			return err
		}
	}
	if fresh {
		if data, err := sanitizedState(filepath.Join(src, codexStateFile)); err == nil {
			_ = fsutil.WriteFileAtomic(filepath.Join(home, codexStateFile), data, 0o644)
		}
	}
	if !busy {
		_ = os.WriteFile(filepath.Join(home, homeVersion), []byte("2\n"), 0o600)
	}
	return nil
}
