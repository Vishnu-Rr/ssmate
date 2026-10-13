package inventory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadCacheReturnsErrNoCacheWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.json")
	_, err := LoadCache(path)
	if !errors.Is(err, ErrNoCache) {
		t.Fatalf("LoadCache(missing) error = %v, want ErrNoCache", err)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	nodes := []Node{
		{ID: "i-1", Name: "web", Account: "default", Region: "us-west-2", Reachable: true, Status: StatusOnline,
			Agent: Agent{Present: true, Online: true}},
		{ID: "i-2", Account: "default", Region: "us-west-2", State: "stopped"},
	}
	if err := SaveCache(path, nodes); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	got, err := LoadCache(path)
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if len(got.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(got.Nodes))
	}
	if got.Nodes[0].Name != "web" || got.Nodes[1].State != "stopped" {
		t.Fatalf("round trip lost data: %+v", got.Nodes)
	}
	if got.Version != cacheVersion {
		t.Errorf("Version = %d, want %d", got.Version, cacheVersion)
	}
}

func TestSaveCacheCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "cache.json")
	if err := SaveCache(path, nil); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache file not created: %v", err)
	}
}

func TestSaveCacheWritesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := SaveCache(path, nil); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("cache permissions = %o, want 600", perm)
	}
}

func TestLoadCacheRejectsWrongVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(path, []byte(`{"version":999,"nodes":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCache(path)
	if !errors.Is(err, ErrNoCache) {
		t.Fatalf("LoadCache(wrong version) error = %v, want ErrNoCache", err)
	}
}

func TestLoadCacheRejectsCorruptJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCache(path)
	if !errors.Is(err, ErrNoCache) {
		t.Fatalf("LoadCache(corrupt) error = %v, want ErrNoCache", err)
	}
}

func TestStaleComparesAgainstTTL(t *testing.T) {
	c := &Cache{FetchedAt: time.Now().Add(-10 * time.Minute)}
	if c.Stale(30 * time.Minute) {
		t.Error("10 minutes old should not be stale against a 30 minute TTL")
	}
	if !c.Stale(5 * time.Minute) {
		t.Error("10 minutes old should be stale against a 5 minute TTL")
	}
}
