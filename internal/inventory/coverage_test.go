package inventory

import (
	"path/filepath"
	"testing"
	"time"
)

// The cache says which clouds it has swept, and when, so a cloud it never
// read is noticed rather than shown as empty.
func TestCacheRemembersWhichCloudsItSwept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.json")
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := SaveCacheSwept(path, []Node{{ID: "i-1", Cloud: CloudAWS}}, map[string]time.Time{CloudAWS: at}, nil); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := c.SweptAt(CloudAWS); !ok || !got.Equal(at) {
		t.Errorf("aws swept = %v, %v; want %v", got, ok, at)
	}
	if _, ok := c.SweptAt(CloudOCI); ok {
		t.Error("oci was never swept, yet the cache claims it")
	}
}

// A cache written before clouds were recorded counts a cloud as swept only
// when it holds machines from it: one that holds only Azure says nothing
// about AWS.
func TestCacheWithoutARecordInfersFromItsMachines(t *testing.T) {
	c := &Cache{FetchedAt: time.Now(), Nodes: []Node{{ID: "vm", Cloud: CloudAzure}}}
	if _, ok := c.SweptAt(CloudAzure); !ok {
		t.Error("azure machines are in the cache, so azure was swept")
	}
	if _, ok := c.SweptAt(CloudAWS); ok {
		t.Error("no aws machines, so aws must count as unswept")
	}
}
