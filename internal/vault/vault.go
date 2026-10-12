// Package vault stores account profiles on disk under ~/.dial/profiles.
//
// Layout (directories 0700, files 0600):
//
//	profiles/<provider>/<id>/profile.json   metadata, no secrets
//	profiles/<provider>/<id>/<secret files>  provider-owned credentials
//
// Each profile directory is the single source of truth for that account on
// this machine; the live CLI location only ever holds a copy of the active
// profile, which is reconciled back before every switch.
package vault

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/medpar/dial/internal/fsutil"
	"github.com/medpar/dial/internal/paths"
)

type Provider string

const (
	Claude Provider = "claude"
	Codex  Provider = "codex"
)

var Providers = []Provider{Claude, Codex}

func ParseProvider(s string) (Provider, bool) {
	switch strings.ToLower(s) {
	case "claude", "cc", "anthropic", "c":
		return Claude, true
	case "codex", "cx", "openai", "chatgpt", "x":
		return Codex, true
	}
	return "", false
}

// Profile is the non-secret metadata of one stored account.
type Profile struct {
	ID       string   `json:"id"`
	Provider Provider `json:"provider"`
	Label    string   `json:"label"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	// AccountID is Claude's accountUuid or Codex's chatgpt_account_id
	// (the workspace the login belongs to).
	AccountID string `json:"account_id,omitempty"`
	// UserID is Codex's chatgpt_user_id; empty for Claude.
	UserID  string `json:"user_id,omitempty"`
	OrgID   string `json:"org_id,omitempty"`
	OrgName string `json:"org_name,omitempty"`
	Plan    string `json:"plan,omitempty"`
	Tier    string `json:"tier,omitempty"`
	// Lineage identifies a token family. Copies that share a lineage hold
	// the same rotating refresh token and must be merged newest-wins.
	Lineage   string    `json:"lineage,omitempty"`
	Origin    string    `json:"origin,omitempty"`
	Order     int       `json:"order,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Key identifies the account across machines, independent of local ids.
func (p Profile) Key() string {
	switch p.Provider {
	case Codex:
		return "codex:" + p.UserID + "/" + p.AccountID
	default:
		return "claude:" + p.AccountID + "/" + p.OrgID
	}
}

// Display is the short human label for lists and the menu bar.
func (p Profile) Display() string {
	if p.Label != "" {
		return p.Label
	}
	if p.Email != "" {
		return p.Email
	}
	return p.ID
}

type Vault struct{ Root string }

func Open() *Vault { return &Vault{Root: filepath.Join(paths.DialHome(), "profiles")} }

func (v *Vault) Dir(p Provider, id string) string { return filepath.Join(v.Root, string(p), id) }

func (v *Vault) List(p Provider) ([]Profile, error) {
	entries, err := os.ReadDir(filepath.Join(v.Root, string(p)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Profile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		prof, err := v.Get(p, e.Name())
		if err != nil {
			continue
		}
		out = append(out, prof)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (v *Vault) All() ([]Profile, error) {
	var all []Profile
	for _, p := range Providers {
		l, err := v.List(p)
		if err != nil {
			return nil, err
		}
		all = append(all, l...)
	}
	return all, nil
}

func (v *Vault) Get(p Provider, id string) (Profile, error) {
	var prof Profile
	if err := fsutil.ReadJSON(filepath.Join(v.Dir(p, id), "profile.json"), &prof); err != nil {
		return Profile{}, err
	}
	prof.ID, prof.Provider = id, p
	return prof, nil
}

func (v *Vault) Save(prof Profile) error {
	if prof.ID == "" || prof.Provider == "" {
		return errors.New("profile needs id and provider")
	}
	now := time.Now().UTC()
	if prof.CreatedAt.IsZero() {
		prof.CreatedAt = now
	}
	prof.UpdatedAt = now
	dir := v.Dir(prof.Provider, prof.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return fsutil.WriteJSON(filepath.Join(dir, "profile.json"), prof, 0o600)
}

func (v *Vault) Delete(p Provider, id string) error {
	dir := v.Dir(p, id)
	if !fsutil.Exists(dir) {
		return fmt.Errorf("no %s profile %q", p, id)
	}
	return os.RemoveAll(dir)
}

func (v *Vault) Rename(p Provider, oldID, newID string) error {
	newID = Slug(newID)
	if fsutil.Exists(v.Dir(p, newID)) {
		return fmt.Errorf("%s profile %q already exists", p, newID)
	}
	if err := os.Rename(v.Dir(p, oldID), v.Dir(p, newID)); err != nil {
		return err
	}
	prof, err := v.Get(p, newID)
	if err != nil {
		return err
	}
	return v.Save(prof)
}

// FindByKey returns the profile with the given cross-machine key.
func (v *Vault) FindByKey(p Provider, key string) (Profile, bool) {
	list, _ := v.List(p)
	for _, prof := range list {
		if prof.Key() == key {
			return prof, true
		}
	}
	return Profile{}, false
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns a label or email into a short, filesystem-safe id.
func Slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexByte(s, '@'); i > 0 {
		s = s[:i]
	}
	s = strings.Trim(slugRe.ReplaceAllString(s, "-"), "-")
	if len(s) > 32 {
		s = strings.Trim(s[:32], "-")
	}
	if s == "" {
		s = "account"
	}
	return s
}

// UniqueID returns base, or base-2, base-3… if taken.
func (v *Vault) UniqueID(p Provider, base string) string {
	base = Slug(base)
	id := base
	for i := 2; fsutil.Exists(v.Dir(p, id)); i++ {
		id = base + "-" + strconv.Itoa(i)
	}
	return id
}

// Resolve finds a profile from a user reference: "work", "claude:work",
// "codex/2" (1-based index), an email, or a unique id prefix.
func (v *Vault) Resolve(ref string, hint Provider) (Profile, error) {
	ref = strings.TrimSpace(ref)
	provs := Providers
	if hint != "" {
		provs = []Provider{hint}
	}
	if i := strings.IndexAny(ref, ":/"); i > 0 {
		if p, ok := ParseProvider(ref[:i]); ok {
			provs, ref = []Provider{p}, ref[i+1:]
		}
	}
	var matches []Profile
	for _, p := range provs {
		list, err := v.List(p)
		if err != nil {
			return Profile{}, err
		}
		if n, err := strconv.Atoi(ref); err == nil && len(provs) == 1 {
			if n >= 1 && n <= len(list) {
				return list[n-1], nil
			}
		}
		for _, prof := range list {
			if prof.ID == ref || strings.EqualFold(prof.Email, ref) || strings.EqualFold(prof.Label, ref) {
				matches = append(matches, prof)
			}
		}
	}
	if len(matches) == 0 {
		for _, p := range provs {
			list, _ := v.List(p)
			for _, prof := range list {
				if strings.HasPrefix(prof.ID, strings.ToLower(ref)) {
					matches = append(matches, prof)
				}
			}
		}
	}
	switch len(matches) {
	case 0:
		return Profile{}, fmt.Errorf("no profile matches %q", ref)
	case 1:
		return matches[0], nil
	}
	var names []string
	for _, m := range matches {
		names = append(names, string(m.Provider)+":"+m.ID)
	}
	return Profile{}, fmt.Errorf("%q is ambiguous: %s", ref, strings.Join(names, ", "))
}
