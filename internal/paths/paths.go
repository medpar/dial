// Package paths resolves every location dial reads or writes, honouring the
// same environment overrides the Claude and Codex CLIs honour.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

func Home() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}

// DialHome is dial's own state directory: $DIAL_HOME or ~/.dial.
func DialHome() string {
	if d := os.Getenv("DIAL_HOME"); d != "" {
		return d
	}
	return filepath.Join(Home(), ".dial")
}

// ClaudeConfigDir is where Claude Code keeps settings and, on Linux, its
// .credentials.json.
func ClaudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(Home(), ".claude")
}

// ClaudeGlobalConfig is the .claude.json that holds oauthAccount.
func ClaudeGlobalConfig() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json")
	}
	return filepath.Join(Home(), ".claude.json")
}

// CodexHome is $CODEX_HOME or ~/.codex.
func CodexHome() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(Home(), ".codex")
}

func IsMac() bool { return runtime.GOOS == "darwin" }

// ClaudeDesktopData is the Claude desktop app's Electron user-data dir.
func ClaudeDesktopData() string {
	if d := os.Getenv("DIAL_CLAUDE_DESKTOP_DATA"); d != "" {
		return d
	}
	if IsMac() {
		return filepath.Join(Home(), "Library", "Application Support", "Claude")
	}
	return filepath.Join(Home(), ".config", "Claude")
}

// CodexDesktopData is the Codex desktop app's Electron user-data dir.
func CodexDesktopData() string {
	if d := os.Getenv("DIAL_CODEX_DESKTOP_DATA"); d != "" {
		return d
	}
	return filepath.Join(Home(), "Library", "Application Support", "Codex")
}
