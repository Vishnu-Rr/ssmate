package inventory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/adrg/xdg"
)

// cacheVersion is bumped whenever the Node shape changes incompatibly. A
// cache written by a different version is discarded rather than migrated;
// it costs one fan-out to rebuild and migration code is not worth its bugs.
const cacheVersion = 5

// ErrNoCache means there is no usable cache: absent, unreadable, corrupt, or
// written by an incompatible version. Callers treat all of those the same
// way, by fetching fresh data.
var ErrNoCache = errors.New("no usable cache")

// Cache is the on-disk inventory snapshot.
type Cache struct {
	Version   int       `json:"version"`
	FetchedAt time.Time `json:"fetched_at"`
	Nodes     []Node    `json:"nodes"`
	// Swept is when each cloud was last read in full. Without it a cloud the
	// cache never read looks the same as one with no machines, and stays
	// missing until someone thinks to pass --refresh.
	Swept map[string]time.Time `json:"swept,omitempty"`
	// Failed is when an automatic read of a cloud last failed, so a cloud
	// whose sign-in is broken is not retried, slowly, on every command.
	Failed map[string]time.Time `json:"failed,omitempty"`
}

// SweptClouds returns when each cloud was last read in full, as a map the
// caller may change; for an older cache, inferred from its machines.
func (c *Cache) SweptClouds() map[string]time.Time {
	out := map[string]time.Time{}
	if c.Swept != nil {
		for k, v := range c.Swept {
			out[k] = v
		}
		return out
	}
	for _, n := range c.Nodes {
		out[n.Cloud] = c.FetchedAt
	}
	return out
}

// SweptAt reports when cloud was last read in full. A cache written before
// clouds were recorded counts a cloud as read only when it holds machines
// from it.
func (c *Cache) SweptAt(cloud string) (time.Time, bool) {
	if c.Swept != nil {
		t, ok := c.Swept[cloud]
		return t, ok
	}
	for _, n := range c.Nodes {
		if n.Cloud == cloud {
			return c.FetchedAt, true
		}
	}
	return time.Time{}, false
}

// Age is how long ago the cache was written.
func (c *Cache) Age() time.Duration { return time.Since(c.FetchedAt) }

// Stale reports whether the cache is older than ttl. A stale cache is still
// usable; callers show it with its age rather than discarding it.
func (c *Cache) Stale(ttl time.Duration) bool { return c.Age() > ttl }

// CachePath is where the inventory cache lives, following the platform's
// conventions for cache data.
func CachePath() (string, error) {
	p, err := xdg.CacheFile(filepath.Join("ssmate", "inventory.json"))
	if err != nil {
		return "", fmt.Errorf("resolve cache path: %w", err)
	}
	return p, nil
}

// LoadCache reads the cache. Every failure mode returns ErrNoCache, because
// the caller's response to all of them is identical.
func LoadCache(path string) (*Cache, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoCache, err)
	}
	var c Cache
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoCache, err)
	}
	if c.Version != cacheVersion {
		return nil, fmt.Errorf("%w: version %d, want %d", ErrNoCache, c.Version, cacheVersion)
	}
	return &c, nil
}

// SaveCache writes the cache atomically, so an interrupted write cannot leave
// a half-written file that the next run would reject.
//
// The file is mode 0600. It holds an inventory of a user's infrastructure:
// no credentials, but not something to leave world-readable either.
func SaveCache(path string, nodes []Node) error { return SaveCacheSwept(path, nodes, nil, nil) }

// SaveCacheSwept is SaveCache with the record of which clouds were read in
// full, and which automatic reads failed, and when.
func SaveCacheSwept(path string, nodes []Node, swept, failed map[string]time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	c := Cache{Version: cacheVersion, FetchedAt: time.Now().UTC(), Nodes: nodes, Swept: swept, Failed: failed}
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode cache: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".inventory-*.json")
	if err != nil {
		return fmt.Errorf("create temp cache file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("set cache permissions: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close cache: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install cache: %w", err)
	}
	return nil
}
