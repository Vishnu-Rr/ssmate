package cli

import (
	"reflect"
	"testing"
	"time"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

// A cloud signed in here but missing from the cache, or swept too long ago,
// is read before the list is shown; one that just failed is not retried on
// every command.
func TestCloudsToSweep(t *testing.T) {
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	c := &inventory.Cache{
		FetchedAt: now.Add(-time.Hour),
		Nodes:     []inventory.Node{{ID: "vm", Cloud: inventory.CloudAzure}},
		Swept: map[string]time.Time{
			inventory.CloudAzure: now.Add(-time.Hour),
			"gcp":                now.Add(-48 * time.Hour),
		},
		Failed: map[string]time.Time{"failing": now.Add(-10 * time.Minute)},
	}
	got := cloudsToSweep(c, []string{inventory.CloudAWS, inventory.CloudAzure, "gcp", "failing"}, now)
	want := []string{inventory.CloudAWS, "gcp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cloudsToSweep = %v, want %v", got, want)
	}
}

// One cloud's read failing must not empty that cloud from the list: its
// machines were not re-read, so the cached ones stay, and the failure is
// noted. The cloud that was read replaces its machines and is recorded.
func TestSettleKeepsAFailedCloudsMachines(t *testing.T) {
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	cached := &inventory.Cache{FetchedAt: now.Add(-time.Hour), Nodes: []inventory.Node{
		{Cloud: "aws", ID: "i-old"},
		{Cloud: "azure", ID: "vm-1"},
	}}
	w := sweep{clouds: map[string]bool{"aws": true, "azure": true}}
	fresh := []inventory.Node{{Cloud: "aws", ID: "i-new"}}

	merged, swept, fails := settle(w, cached, fresh, map[string]bool{"aws": true}, map[string]bool{"azure": true}, now)

	got := map[string]bool{}
	for _, n := range merged {
		got[n.ID] = true
	}
	if !got["vm-1"] {
		t.Error("azure's read failed, yet its cached machine was dropped")
	}
	if !got["i-new"] || got["i-old"] {
		t.Errorf("aws was read, so its machines should be the fresh ones: %v", got)
	}
	if !swept["aws"].Equal(now) {
		t.Errorf("aws swept = %v, want %v", swept["aws"], now)
	}
	if !fails["azure"].Equal(now) {
		t.Errorf("azure's failure was not recorded: %v", fails)
	}
	if !w.clouds["azure"] {
		t.Error("settle must not change the caller's sweep")
	}
}

// A read narrowed to one region is not a read of the cloud.
func TestSettleRecordsOnlyWholeCloudReads(t *testing.T) {
	now := time.Now().UTC()
	w := sweep{clouds: map[string]bool{"aws": true}, region: "eu-west-2"}
	_, swept, _ := settle(w, nil, nil, map[string]bool{"aws": true}, nil, now)
	if _, ok := swept["aws"]; ok {
		t.Error("a region-scoped read was recorded as the whole cloud")
	}
}
