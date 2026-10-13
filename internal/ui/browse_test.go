package ui

import (
	"strings"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

// nodes is the shared fixture: three clouds, a stopped machine, an
// unreachable one, and tags worth searching.
func nodes() []inventory.Node {
	return []inventory.Node{
		{
			Cloud: inventory.CloudAWS, ID: "i-0a1", Name: "web-1",
			Region: "eu-west-2", Account: "prod", PrivateIP: "10.0.0.4",
			Reachable: true, Status: inventory.StatusOnline,
			Tags: map[string]string{"Environment": "production", "Team": "platform"},
		},
		{
			Cloud: inventory.CloudAzure, ID: "/subscriptions/x/vm-api-1", Name: "api-1",
			Region: "eastus", Account: "Contoso", PrivateIP: "10.1.2.4",
			Reachable: true, Status: inventory.StatusOnline,
			Tags: map[string]string{"Environment": "staging"},
		},
		{
			Cloud: inventory.CloudOCI, ID: "ocid1.instance.oc1..aaaa", Name: "db-1",
			Region: "us-ashburn-1", Account: "", PrivateIP: "10.2.2.9",
			Status: inventory.StatusNoGateway, Hint: "no bastion in app-vcn",
		},
		{
			Cloud: inventory.CloudAWS, ID: "i-0b2", Name: "batch-9",
			Region: "us-west-2", Account: "prod",
			Status: inventory.StatusStopped,
		},
	}
}

func names(in []Hit) []string { return shown(Nodes(in)) }

// shown lists display names, for assertions against the visible list.
func shown(in []inventory.Node) []string {
	out := make([]string, len(in))
	for i, n := range in {
		out[i] = n.DisplayName()
	}
	return out
}

// An empty query has to keep everything, or the list is a search box rather
// than something you can browse.
func TestApplyWithNoQueryKeepsEverything(t *testing.T) {
	got := Apply(nodes(), Query{}, nil)
	if len(got) != 4 {
		t.Fatalf("got %d, want all four: %v", len(got), names(got))
	}
}

// Cloud is a filter rather than part of the command, because the point of the
// tool is reaching a machine without knowing which cloud it is in.
func TestCloudFilter(t *testing.T) {
	got := Apply(nodes(), Query{Cloud: inventory.CloudAWS}, nil)
	if len(got) != 2 {
		t.Fatalf("got %v, want the two AWS machines", names(got))
	}
	for _, n := range got {
		if n.Node.Cloud != inventory.CloudAWS {
			t.Errorf("%s is %s", n.Node.DisplayName(), n.Node.Cloud)
		}
	}
}

func TestReachableOnlyHidesWhatCannotBeReached(t *testing.T) {
	got := Apply(nodes(), Query{ReachableOnly: true}, nil)
	if len(got) != 2 {
		t.Fatalf("got %v, want only the reachable ones", names(got))
	}
	for _, n := range got {
		if !n.Node.Reachable {
			t.Errorf("%s is not reachable", n.Node.DisplayName())
		}
	}
}

// The three match fields exist because people arrive with different things:
// a name they remember, a fleet they want, or an address from a log.
func TestMatchFields(t *testing.T) {
	all := nodes()

	if got := Apply(all, Query{Text: "web"}, nil); len(got) != 1 || got[0].Node.Name != "web-1" {
		t.Errorf("name match: %v", names(got))
	}
	// A name search must not accidentally match a tag or an address.
	if got := Apply(all, Query{Text: "production"}, nil); len(got) != 0 {
		t.Errorf("name field should not match tags, got %v", names(got))
	}
	if got := Apply(all, Query{Text: "production", Field: FieldTag}, nil); len(got) != 1 {
		t.Errorf("tag match: %v", names(got))
	}
	// An address is the classic "all I have is this line from a log" case.
	if got := Apply(all, Query{Text: "10.1.2.4", Field: FieldAll}, nil); len(got) != 1 || got[0].Node.Name != "api-1" {
		t.Errorf("address match: %v", names(got))
	}
	if got := Apply(all, Query{Text: "us-ashburn", Field: FieldAll}, nil); len(got) != 1 || got[0].Node.Name != "db-1" {
		t.Errorf("region match: %v", names(got))
	}
}

// Map iteration in Go is deliberately random, so tag text has to be ordered
// or the same query would match differently between runs.
func TestTagValuesAreStable(t *testing.T) {
	n := nodes()[0]
	first := strings.Join(tagValues(n), " ")
	for i := 0; i < 50; i++ {
		if got := strings.Join(tagValues(n), " "); got != first {
			t.Fatalf("tag text changed between runs:\n %q\n %q", first, got)
		}
	}
}

// Every sort falls back to name, so the list never reshuffles under the
// cursor between identical runs.
func TestSortModes(t *testing.T) {
	all := nodes()

	byName := Apply(all, Query{Sort: SortName}, nil)
	want := []string{"api-1", "batch-9", "db-1", "web-1"}
	if got := names(byName); !equal(got, want) {
		t.Errorf("by name: %v", got)
	}

	byCloud := Apply(all, Query{Sort: SortCloud}, nil)
	if got := names(byCloud); got[0] != "batch-9" && got[0] != "web-1" {
		t.Errorf("by cloud should start with aws, got %v", got)
	}

	byStatus := Apply(all, Query{Sort: SortStatus}, nil)
	if got := byStatus[0].Node; !got.Reachable {
		t.Errorf("by status should put reachable first, got %s", got.DisplayName())
	}

	recent := func(id string) float64 {
		if id == "i-0b2" {
			return 9
		}
		return 0
	}
	byRecent := Apply(all, Query{Sort: SortRecent}, recent)
	if byRecent[0].Node.ID != "i-0b2" {
		t.Errorf("by recent should put the used one first, got %s", byRecent[0].Node.DisplayName())
	}
}

func TestSortModeCyclesThroughAllFour(t *testing.T) {
	seen := map[SortMode]bool{}
	s := SortRecent
	for i := 0; i < 4; i++ {
		seen[s] = true
		s = s.Next()
	}
	if len(seen) != 4 {
		t.Errorf("cycling visited %d modes, want 4", len(seen))
	}
	if s != SortRecent {
		t.Error("cycling four times should return to the start")
	}
}

// The cloud filter offers what this estate has, and "all" is part of the
// cycle so one key always moves forward.
func TestNextCloudCyclesBackToAll(t *testing.T) {
	clouds := Clouds(nodes())
	if !equal(clouds, []string{"aws", "azure", "oci"}) {
		t.Fatalf("clouds = %v", clouds)
	}
	seq := []string{}
	cur := ""
	for i := 0; i < 4; i++ {
		cur = NextCloud(cur, clouds)
		seq = append(seq, cur)
	}
	if !equal(seq, []string{"aws", "azure", "oci", ""}) {
		t.Errorf("cycle = %v", seq)
	}
	if got := NextCloud("", nil); got != "" {
		t.Errorf("an empty estate has nothing to cycle, got %q", got)
	}
}

func TestSummarise(t *testing.T) {
	c := Summarise(nodes())
	if c.Total != 4 || c.Reachable != 2 {
		t.Errorf("total %d reachable %d", c.Total, c.Reachable)
	}
	if c.ByCloud["aws"] != 2 || c.ByCloud["oci"] != 1 {
		t.Errorf("by cloud = %v", c.ByCloud)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
