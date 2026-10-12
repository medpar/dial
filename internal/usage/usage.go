// Package usage defines the provider-neutral usage report and its on-disk
// cache, so the CLI and the menu bar never hit the APIs more than needed.
package usage

import (
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/medpar/dial/internal/fsutil"
	"github.com/medpar/dial/internal/paths"
)

// Window is one rate-limit window, e.g. the 5-hour session or the week.
type Window struct {
	Key           string     `json:"key"`
	Label         string     `json:"label"`
	UsedPercent   float64    `json:"used_percent"`
	ResetsAt      *time.Time `json:"resets_at,omitempty"`
	WindowSeconds int64      `json:"window_seconds,omitempty"`
}

type Report struct {
	Provider  string   `json:"provider"`
	ProfileID string   `json:"profile_id"`
	Email     string   `json:"email,omitempty"`
	Plan      string   `json:"plan,omitempty"`
	Windows   []Window `json:"windows"`
	// Extra holds provider-specific lines such as credits or overage.
	Extra        []Extra   `json:"extra,omitempty"`
	LimitReached bool      `json:"limit_reached,omitempty"`
	FetchedAt    time.Time `json:"fetched_at"`
	Error        string    `json:"error,omitempty"`
	// Stale marks a cached report returned because a refresh failed.
	Stale bool `json:"stale,omitempty"`
	// RetryAfter is set after a 429 so nothing polls before it.
	RetryAfter *time.Time `json:"retry_after,omitempty"`
	// RefreshTriedAt records a failed token refresh to rate-limit retries.
	RefreshTriedAt *time.Time `json:"refresh_tried_at,omitempty"`
}

type Extra struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Max returns the highest used percentage across windows.
func (r Report) Max() float64 {
	m := 0.0
	for _, w := range r.Windows {
		if w.UsedPercent > m {
			m = w.UsedPercent
		}
	}
	return m
}

// WindowLabel names a window from its length.
func WindowLabel(seconds int64) (key, label string) {
	switch {
	case seconds == 0:
		return "window", "Window"
	case seconds <= 6*3600:
		h := seconds / 3600
		return fmt.Sprintf("%dh", h), fmt.Sprintf("%dh session", h)
	case seconds >= 6*86400 && seconds <= 8*86400:
		return "7d", "Weekly"
	case seconds >= 28*86400 && seconds <= 31*86400:
		return "30d", "Monthly"
	case seconds%86400 == 0:
		return fmt.Sprintf("%dd", seconds/86400), fmt.Sprintf("%d days", seconds/86400)
	default:
		return fmt.Sprintf("%dh", seconds/3600), fmt.Sprintf("%dh window", seconds/3600)
	}
}

// Cache is a JSON map keyed by "provider:id".
type Cache struct {
	mu      sync.Mutex
	path    string
	Reports map[string]Report `json:"reports"`
}

func LoadCache() *Cache {
	c := &Cache{path: filepath.Join(paths.DialHome(), "usage.json"), Reports: map[string]Report{}}
	_ = fsutil.ReadJSON(c.path, c)
	if c.Reports == nil {
		c.Reports = map[string]Report{}
	}
	return c
}

func (c *Cache) Get(key string) (Report, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.Reports[key]
	return r, ok
}

func (c *Cache) Put(key string, r Report) {
	c.mu.Lock()
	c.Reports[key] = r
	c.mu.Unlock()
}

func (c *Cache) Delete(key string) {
	c.mu.Lock()
	delete(c.Reports, key)
	c.mu.Unlock()
}

func (c *Cache) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fsutil.WriteJSON(c.path, c, 0o600)
}
