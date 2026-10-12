package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/medpar/dial/internal/fsutil"
	"github.com/medpar/dial/internal/paths"
)

// A Claude Desktop login is a "slice" of its user-data dir: the cookie and
// web-storage databases plus three keys in config.json (the encrypted
// OAuth token caches and lastKnownAccountUuid). Everything else — settings,
// MCP config, sessions keyed by account — stays put.
//
// Each account's slice lives in exactly one place, so no two copies of its
// rotating tokens ever diverge: either in the app's user-data dir (live) or
// in ~/.dial/desktop/claude/<accountUuid>/ (parked). A parked dir is laid
// out like a user-data dir, so it doubles as the --user-data-dir of a
// side-by-side instance for that account.

var claudeSliceItems = []string{
	"Cookies", "Cookies-journal", "DIPS", "DIPS-wal", "fcache",
	"Local Storage", "Session Storage", "IndexedDB", "Network Persistent State",
}

var claudeAuthKeys = []string{"oauth:tokenCacheV2", "oauth:tokenCache", "lastKnownAccountUuid"}

// Settings copied into a fresh instance dir so it looks like the main app.
var claudeSharedFiles = []string{"claude_desktop_config.json", "ssh_configs.json", "extensions-installations.json"}

// ClaudeHomes is the root of parked Claude Desktop accounts.
func ClaudeHomes() string { return filepath.Join(paths.DialHome(), "desktop", "claude") }

// ClaudeHome is the parked dir (and instance user-data dir) of account.
func ClaudeHome(accountUUID string) string { return filepath.Join(ClaudeHomes(), accountUUID) }

// DesktopAccount describes a login found in a user-data dir.
type DesktopAccount struct {
	AccountUUID string `json:"account_uuid"`
	LoggedIn    bool   `json:"logged_in"`
	Dir         string `json:"dir"`
}

func readConfig(dir string) (map[string]json.RawMessage, error) {
	return fsutil.ReadJSONMap(filepath.Join(dir, "config.json"))
}

func writeConfig(dir string, m map[string]json.RawMessage) error {
	data, err := fsutil.MarshalIndent(m)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, "config.json"), data, 0o600)
}

// An encrypted "{}" token cache is ~30 base64 chars; a real one is >400.
func hasTokens(m map[string]json.RawMessage) bool {
	for _, k := range []string{"oauth:tokenCacheV2", "oauth:tokenCache"} {
		var s string
		if json.Unmarshal(m[k], &s) == nil && len(s) > 120 {
			return true
		}
	}
	return false
}

// ClaudeAccountIn reports the Claude Desktop login stored in dir.
func ClaudeAccountIn(dir string) DesktopAccount {
	m, err := readConfig(dir)
	if err != nil {
		return DesktopAccount{Dir: dir}
	}
	var uuid string
	_ = json.Unmarshal(m["lastKnownAccountUuid"], &uuid)
	return DesktopAccount{AccountUUID: uuid, LoggedIn: uuid != "" && hasTokens(m), Dir: dir}
}

// ParkedClaude lists accounts parked under ~/.dial/desktop/claude.
func ParkedClaude() []DesktopAccount {
	entries, _ := os.ReadDir(ClaudeHomes())
	var out []DesktopAccount
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		a := ClaudeAccountIn(filepath.Join(ClaudeHomes(), e.Name()))
		if a.AccountUUID == "" {
			a.AccountUUID = e.Name()
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountUUID < out[j].AccountUUID })
	return out
}

func move(src, dst string) error {
	if err := os.Rename(src, dst); err != nil {
		var le *os.LinkError
		if errors.As(err, &le) && errors.Is(le.Err, syscall.EXDEV) {
			if err := copyTree(src, dst); err != nil {
				return err
			}
			return os.RemoveAll(src)
		}
		return err
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		return fsutil.CopyFile(p, target, 0o600)
	})
}

// moveSlice moves slice items and auth keys from src to dst. Items already
// in dst are moved into trash first so nothing is overwritten blindly.
func moveSlice(src, dst, trash string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, it := range claudeSliceItems {
		from := filepath.Join(src, it)
		if _, err := os.Lstat(from); err != nil {
			continue
		}
		to := filepath.Join(dst, it)
		if _, err := os.Lstat(to); err == nil {
			if err := os.MkdirAll(trash, 0o700); err != nil {
				return err
			}
			if err := move(to, filepath.Join(trash, it)); err != nil {
				return err
			}
		}
		if err := move(from, to); err != nil {
			return fmt.Errorf("move %s: %w", it, err)
		}
	}
	sm, err := readConfig(src)
	if err != nil {
		return err
	}
	dm, err := readConfig(dst)
	if err != nil {
		return err
	}
	for _, k := range claudeAuthKeys {
		if v, ok := sm[k]; ok {
			dm[k] = v
		} else {
			delete(dm, k)
		}
		delete(sm, k)
	}
	if err := writeConfig(dst, dm); err != nil {
		return err
	}
	return writeConfig(src, sm)
}

func newTrash() string {
	return filepath.Join(ClaudeHomes(), ".trash", time.Now().UTC().Format("20060102-150405.000"))
}

// ParkClaude moves the login in dataDir to its parked home and returns it.
// The app using dataDir must not be running.
func ParkClaude(dataDir string) (DesktopAccount, error) {
	cur := ClaudeAccountIn(dataDir)
	if cur.AccountUUID == "" {
		// No identifiable login: clear stray slice items into trash.
		trash := newTrash()
		for _, it := range claudeSliceItems {
			p := filepath.Join(dataDir, it)
			if _, err := os.Lstat(p); err == nil {
				_ = os.MkdirAll(trash, 0o700)
				_ = move(p, filepath.Join(trash, it))
			}
		}
		return cur, nil
	}
	home := ClaudeHome(cur.AccountUUID)
	if err := moveSlice(dataDir, home, newTrash()); err != nil {
		return cur, err
	}
	seedShared(dataDir, home)
	cur.Dir = home
	return cur, nil
}

// UnparkClaude moves account's parked login into dataDir. The app using
// dataDir must not be running, and dataDir should have been parked first.
func UnparkClaude(accountUUID, dataDir string) error {
	home := ClaudeHome(accountUUID)
	if !ClaudeAccountIn(home).LoggedIn {
		return fmt.Errorf("no saved Claude Desktop login for account %s", accountUUID)
	}
	return moveSlice(home, dataDir, newTrash())
}

// seedShared copies app-wide settings into an instance dir once.
func seedShared(from, to string) {
	for _, f := range claudeSharedFiles {
		dst := filepath.Join(to, f)
		if fsutil.Exists(dst) {
			continue
		}
		if fsutil.Exists(filepath.Join(from, f)) {
			_ = fsutil.CopyFile(filepath.Join(from, f), dst, 0o600)
		}
	}
	// Carry over non-auth preferences (theme, locale…) on first use.
	fm, err1 := readConfig(from)
	tm, err2 := readConfig(to)
	if err1 != nil || err2 != nil {
		return
	}
	changed := false
	for k, v := range fm {
		if _, ok := tm[k]; ok || isAuthKey(k) || strings.HasPrefix(k, "updater") {
			continue
		}
		tm[k] = v
		changed = true
	}
	if changed {
		_ = writeConfig(to, tm)
	}
}

func isAuthKey(k string) bool {
	for _, a := range claudeAuthKeys {
		if a == k {
			return true
		}
	}
	return false
}

// CleanTrash removes trash older than a week.
func CleanTrash() {
	root := filepath.Join(ClaudeHomes(), ".trash")
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 7*24*time.Hour {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// NewSigninDir creates an empty user-data dir for signing in a new account.
func NewSigninDir() (string, error) {
	if err := os.MkdirAll(ClaudeHomes(), 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(ClaudeHomes(), ".signin-")
}

// SeedSettings copies app-wide settings (MCP config, preferences) from the
// main data dir into dir.
func SeedSettings(from, dir string) { seedShared(from, dir) }

// AdoptSignin turns a finished sign-in dir into that account's home. A home
// that already exists gets the fresh login; the old one goes to trash.
func AdoptSignin(dir string) (DesktopAccount, error) {
	acct := ClaudeAccountIn(dir)
	if !acct.LoggedIn {
		return acct, fmt.Errorf("no sign-in found")
	}
	home := ClaudeHome(acct.AccountUUID)
	if _, err := os.Stat(home); os.IsNotExist(err) {
		if err := os.Rename(dir, home); err != nil {
			return acct, err
		}
		acct.Dir = home
		return acct, nil
	}
	trash := newTrash()
	if err := moveSlice(dir, home, trash); err != nil {
		return acct, err
	}
	_ = os.MkdirAll(trash, 0o700)
	_ = move(dir, filepath.Join(trash, "signin"))
	acct.Dir = home
	return acct, nil
}

// ReconcileClaudeHomes makes every idle home hold the account it is named
// after. Someone may have signed a different account into a window; its
// login is moved under the right name so lookups by account stay correct.
// busy holds dirs of running instances, which are never touched.
func ReconcileClaudeHomes(busy func(dir string) bool) {
	entries, _ := os.ReadDir(ClaudeHomes())
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(ClaudeHomes(), e.Name())
		acct := ClaudeAccountIn(dir)
		if !acct.LoggedIn || acct.AccountUUID == e.Name() || busy(dir) {
			continue
		}
		target := ClaudeHome(acct.AccountUUID)
		if busy(target) || ClaudeAccountIn(target).LoggedIn {
			continue // the right home already holds a login; leave both alone
		}
		_ = moveSlice(dir, target, newTrash())
		seedShared(dir, target)
	}
}

// StaleSignins removes sign-in dirs abandoned more than a day ago.
func StaleSignins(busy func(dir string) bool) {
	entries, _ := os.ReadDir(ClaudeHomes())
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ".signin-") {
			continue
		}
		dir := filepath.Join(ClaudeHomes(), e.Name())
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour && !busy(dir) {
			_ = os.RemoveAll(dir)
		}
	}
}
