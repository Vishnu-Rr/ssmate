package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/ui"
)

// ids and names are for pipelines: one value per line and nothing else, so
// the output can go straight into xargs.
func TestIdsAndNamesAreOneValuePerLine(t *testing.T) {
	var b bytes.Buffer
	if err := writeMachines(&b, estate(), "ids", ui.NewTheme(false)); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 4 || lines[0] != "i-1" {
		t.Errorf("ids = %q", lines)
	}
	b.Reset()
	_ = writeMachines(&b, estate(), "names", ui.NewTheme(false))
	if !strings.HasPrefix(b.String(), "web-1\n") {
		t.Errorf("names = %q", b.String())
	}
	// No heading and no colour, even with colour on, or the first line of
	// every pipeline would be the word NAME.
	b.Reset()
	_ = writeMachines(&b, estate(), "ids", ui.NewTheme(true))
	if strings.Contains(b.String(), "\x1b") || strings.Contains(b.String(), "NAME") {
		t.Errorf("pipeline output carries decoration: %q", b.String())
	}
}

// wide puts the identifier back, last, where its length does the least harm.
func TestWideAddsAddressAndIdentifier(t *testing.T) {
	var b bytes.Buffer
	_ = writeMachines(&b, estate(), "wide", ui.NewTheme(false))
	head := strings.SplitN(b.String(), "\n", 2)[0]
	if !strings.HasSuffix(strings.TrimSpace(head), "ADDRESS  ID") {
		t.Errorf("wide heading = %q", head)
	}
	if !strings.Contains(b.String(), "ocid1.x") {
		t.Error("wide should include identifiers")
	}
	b.Reset()
	_ = writeMachines(&b, estate(), "table", ui.NewTheme(false))
	if strings.Contains(b.String(), "ocid1.x") {
		t.Error("the default table must leave identifiers out")
	}
}

func TestJSONIsTheNodes(t *testing.T) {
	var b bytes.Buffer
	_ = writeMachines(&b, estate(), "json", ui.NewTheme(false))
	var got []inventory.Node
	if err := json.Unmarshal(b.Bytes(), &got); err != nil || len(got) != 4 {
		t.Errorf("json did not round-trip: %v, %d nodes", err, len(got))
	}
}

// Every order falls back to name, so the same command prints the same order
// every time - a list a script diffs must not reshuffle between runs.
func TestSortOrders(t *testing.T) {
	for _, tc := range []struct {
		by    string
		first string
	}{
		{"name", "api-1"},
		{"cloud", "web-1"},  // aws first, then name
		{"region", "api-1"}, // eastus
		{"status", "db-1"},  // reachable first, then status, then name
	} {
		nodes := estate()
		sortMachines(nodes, tc.by, nil)
		if nodes[0].Name != tc.first {
			t.Errorf("--sort %s put %s first, want %s", tc.by, nodes[0].Name, tc.first)
		}
	}
	nodes := estate()
	recent := func(id string) (float64, time.Time) {
		if id == "ocid1.x" {
			return 5, time.Now()
		}
		return 0, time.Time{}
	}
	sortMachines(nodes, "recent", recent)
	if nodes[0].ID != "ocid1.x" {
		t.Errorf("--sort recent put %s first", nodes[0].ID)
	}
}

// clouds counts what the cache holds per cloud, and shows every cloud this
// build knows, including one with nothing cached.
func TestSummariseClouds(t *testing.T) {
	got := summariseClouds(t.Context(), estate())
	by := map[string]cloudSummary{}
	for _, c := range got {
		by[c.Cloud] = c
	}
	if a := by["aws"]; a.Machines != 2 || a.Reachable != 1 || len(a.Regions) != 2 {
		t.Errorf("aws = %+v", a)
	}
	if a := by["azure"]; a.Machines != 1 || len(a.Accounts) != 1 || a.Accounts[0] != "Contoso" {
		t.Errorf("azure = %+v", a)
	}
	if _, ok := by["oci"]; !ok {
		t.Error("oci should be listed")
	}
}

func TestListSummaryCapsLongLists(t *testing.T) {
	if got := listSummary([]string{"a", "b", "c", "d", "e"}, 3); got != "a, b, c +2" {
		t.Errorf("got %q", got)
	}
	if got := listSummary([]string{"a"}, 3); got != "a" {
		t.Errorf("got %q", got)
	}
}
