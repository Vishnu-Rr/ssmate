package cli

import (
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

func estate() []inventory.Node {
	return []inventory.Node{
		{Cloud: "aws", ID: "i-1", Name: "web-1", Region: "eu-west-2", Account: "",
			Status: "online", Reachable: true, Tags: map[string]string{"Environment": "production"}},
		{Cloud: "aws", ID: "i-2", Name: "web-2", Region: "us-west-2", Account: "",
			Status: "stopped", Tags: map[string]string{"environment": "staging"}},
		{Cloud: "azure", ID: "/vm/api-1", Name: "api-1", Region: "eastus", Account: "Contoso",
			Status: "no tunnel"},
		{Cloud: "oci", ID: "ocid1.x", Name: "db-1", Region: "us-ashburn-1", Account: "DEFAULT",
			Status: "online", Reachable: true, Tags: map[string]string{"Team": "data"}},
	}
}

func ids(nodes []inventory.Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}

func mustScope(t *testing.T, o options) scope {
	t.Helper()
	s, err := scopeFrom(&o)
	if err != nil {
		t.Fatalf("scopeFrom: %v", err)
	}
	return s
}

// The regression this whole file exists for: --region used to scope a refresh
// and be silently ignored on the cached path, so ls --region eu-west-2 listed
// every region there was.
func TestRegionFiltersCachedResults(t *testing.T) {
	got := ids(mustScope(t, options{region: "eu-west-2"}).apply(estate()))
	if len(got) != 1 || got[0] != "i-1" {
		t.Errorf("--region eu-west-2 kept %v", got)
	}
}

// --profile had the same silent-ignore bug.
func TestAccountFiltersCachedResults(t *testing.T) {
	got := ids(mustScope(t, options{profile: "contoso"}).apply(estate()))
	if len(got) != 1 || got[0] != "/vm/api-1" {
		t.Errorf("--profile contoso kept %v", got)
	}
	// "default" means each cloud's default account. AWS's has no name, and
	// the list shows it as "default"; OCI's is a profile literally named
	// DEFAULT. Both are what someone typing --profile default is asking for.
	got = ids(mustScope(t, options{profile: "default"}).apply(estate()))
	if len(got) != 3 {
		t.Errorf("--profile default kept %v, want the two unnamed AWS machines and OCI's DEFAULT", got)
	}
}

func TestListsAreCommaSeparated(t *testing.T) {
	got := mustScope(t, options{cloud: "aws, oci"}).apply(estate())
	if len(got) != 3 {
		t.Errorf("--cloud aws,oci kept %v", ids(got))
	}
	got = mustScope(t, options{region: "eu-west-2,eastus"}).apply(estate())
	if len(got) != 2 {
		t.Errorf("two regions kept %v", ids(got))
	}
}

func TestStatusAndReachable(t *testing.T) {
	if got := mustScope(t, options{status: "stopped,no tunnel"}).apply(estate()); len(got) != 2 {
		t.Errorf("--status kept %v", ids(got))
	}
	if got := mustScope(t, options{reachable: true}).apply(estate()); len(got) != 2 {
		t.Errorf("--reachable kept %v", ids(got))
	}
}

// Tag conventions are inconsistent on real estates - Environment and
// environment both occur - so matching ignores case.
func TestTagsMatchWithoutRegardToCase(t *testing.T) {
	if got := mustScope(t, options{tags: []string{"environment"}}).apply(estate()); len(got) != 2 {
		t.Errorf("--tag environment kept %v, want both spellings", ids(got))
	}
	if got := mustScope(t, options{tags: []string{"Environment=PRODUCTION"}}).apply(estate()); len(got) != 1 {
		t.Errorf("--tag with a value kept %v", ids(got))
	}
	// Several tags must all hold.
	got := mustScope(t, options{tags: []string{"environment", "team"}}).apply(estate())
	if len(got) != 0 {
		t.Errorf("two tags no machine has both of kept %v", ids(got))
	}
}

// A mistyped cloud must say so. An empty list reads as "you have no
// machines", which is the wrong conclusion to hand someone.
func TestUnknownCloudIsAnError(t *testing.T) {
	if _, err := scopeFrom(&options{cloud: "gcp"}); err == nil {
		t.Error("an unknown cloud should be refused")
	}
	if _, err := scopeFrom(&options{tags: []string{"=value"}}); err == nil {
		t.Error("a tag with no key should be refused")
	}
}

// The second regression: a refresh only merged into the cache when --cloud
// was set, so ls --refresh --region eu-west-2 saved one region's machines as
// the whole estate and every other machine vanished.
func TestRegionScopedRefreshKeepsOtherRegions(t *testing.T) {
	w := sweep{clouds: map[string]bool{"aws": true, "azure": true, "oci": true}, region: "eu-west-2"}
	fresh := []inventory.Node{{Cloud: "aws", ID: "i-1", Name: "web-1-renamed", Region: "eu-west-2"}}
	merged := w.merge(estate(), fresh)

	byID := map[string]inventory.Node{}
	for _, n := range merged {
		byID[n.ID] = n
	}
	if len(merged) != 4 {
		t.Fatalf("merged %d machines, want all four kept: %v", len(merged), ids(merged))
	}
	if byID["i-1"].Name != "web-1-renamed" {
		t.Error("the machine the sweep re-read should carry the fresh data")
	}
	for _, id := range []string{"i-2", "/vm/api-1", "ocid1.x"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("%s, outside the swept region, was dropped from the cache", id)
		}
	}
}

// A machine the sweep covered but did not find has gone, and must leave the
// cache rather than linger forever.
func TestCoveredMachineThatVanishedIsDropped(t *testing.T) {
	w := sweep{clouds: map[string]bool{"aws": true}, region: "us-west-2"}
	merged := w.merge(estate(), nil)
	for _, n := range merged {
		if n.ID == "i-2" {
			t.Error("i-2 was in the swept region and was not found, so it should be gone")
		}
	}
	if len(merged) != 3 {
		t.Errorf("kept %d, want the three outside the sweep", len(merged))
	}
}

func TestAccountScopedRefreshKeepsOtherAccounts(t *testing.T) {
	w := sweep{clouds: map[string]bool{"azure": true, "oci": true}, account: "contoso"}
	merged := w.merge(estate(), nil)
	for _, n := range merged {
		if n.ID == "/vm/api-1" {
			t.Error("the swept account's machine should have been replaced")
		}
	}
	found := false
	for _, n := range merged {
		if n.ID == "ocid1.x" {
			found = true
		}
	}
	if !found {
		t.Error("the OCI machine in another account was dropped")
	}
}

// A sweep can only ask a cloud for one region at a time, so naming several
// sweeps them all and lets the result filter narrow - slower, never wrong.
func TestSeveralRegionsSweepEverything(t *testing.T) {
	sc := mustScope(t, options{region: "eu-west-2,us-west-2"})
	if w := sc.sweep(nil, false); w.region != "" {
		t.Errorf("sweep region = %q, want every region", w.region)
	}
	if w := mustScope(t, options{region: "eu-west-2"}).sweep(nil, false); w.region != "eu-west-2" {
		t.Errorf("a single region should be swept alone, got %q", w.region)
	}
	if w := mustScope(t, options{profile: "a,b"}).sweep(nil, false); !w.allAccounts {
		t.Error("several accounts should sweep all of them")
	}
}

// A provider may return more than it was asked for: Azure lists a whole
// subscription whatever region is named. The merge must not take those, and
// must never let one machine appear twice - an earlier version doubled every
// Azure machine on a refresh of one AWS region.
func TestMergeIgnoresWhatTheSweepDidNotCover(t *testing.T) {
	w := sweep{clouds: map[string]bool{"aws": true, "azure": true}, region: "eu-west-2"}
	cached := estate()
	// Azure hands back its machine even though it is in eastus.
	fresh := []inventory.Node{
		{Cloud: "aws", ID: "i-1", Name: "web-1-new", Region: "eu-west-2"},
		{Cloud: "azure", ID: "/vm/api-1", Name: "api-1", Region: "eastus"},
	}
	merged := w.merge(cached, fresh)

	seen := map[string]int{}
	for _, n := range merged {
		seen[n.Cloud+"/"+n.ID]++
	}
	for k, c := range seen {
		if c > 1 {
			t.Errorf("%s appears %d times", k, c)
		}
	}
	if len(merged) != len(cached) {
		t.Errorf("merged %d, want %d: one machine refreshed, none added", len(merged), len(cached))
	}
}

// Whatever overlaps, the fresher copy is the one kept.
func TestMergePrefersFreshData(t *testing.T) {
	w := sweep{clouds: map[string]bool{"aws": true}}
	merged := w.merge(estate(), []inventory.Node{{Cloud: "aws", ID: "i-1", Name: "renamed", Region: "eu-west-2"}})
	for _, n := range merged {
		if n.ID == "i-1" && n.Name != "renamed" {
			t.Errorf("kept the stale copy of i-1: %q", n.Name)
		}
	}
}
