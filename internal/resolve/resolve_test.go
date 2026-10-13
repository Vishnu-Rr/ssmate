package resolve

import (
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

func fixture() []inventory.Node {
	return []inventory.Node{
		{ID: "i-0aaa", Name: "web-prod-1", PrivateIP: "10.0.0.1", Region: "us-west-2",
			Tags: map[string]string{"Name": "web-prod-1", "Env": "prod"}},
		{ID: "i-0bbb", Name: "web-prod-2", PrivateIP: "10.0.0.2", Region: "us-west-2",
			Tags: map[string]string{"Name": "web-prod-2", "Env": "prod"}},
		{ID: "i-0ccc", Name: "db-stage", PrivateIP: "10.1.0.1", PublicIP: "54.1.2.3",
			Region: "eu-west-1", Tags: map[string]string{"Name": "db-stage", "Env": "stage"}},
		{ID: "i-0ddd", Name: "web-prod-1", PrivateIP: "10.2.0.1", Region: "eu-west-1",
			Tags: map[string]string{"Name": "web-prod-1"}},
	}
}

func ids(ms []Match) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Node.ID)
	}
	return out
}

func TestResolveByInstanceIDReturnsExactlyOne(t *testing.T) {
	got := Resolve(fixture(), "i-0ccc")
	if len(got) != 1 || got[0].Node.ID != "i-0ccc" {
		t.Fatalf("got %v, want exactly [i-0ccc]", ids(got))
	}
	if !got[0].Exact {
		t.Error("instance ID match should be exact")
	}
}

func TestResolveByPrivateIP(t *testing.T) {
	got := Resolve(fixture(), "10.1.0.1")
	if len(got) != 1 || got[0].Node.ID != "i-0ccc" {
		t.Fatalf("got %v, want [i-0ccc]", ids(got))
	}
}

func TestResolveByPublicIP(t *testing.T) {
	got := Resolve(fixture(), "54.1.2.3")
	if len(got) != 1 || got[0].Node.ID != "i-0ccc" {
		t.Fatalf("got %v, want [i-0ccc]", ids(got))
	}
}

func TestResolveReturnsAllDuplicateNamesNeverPicksOne(t *testing.T) {
	// Two different instances in two regions share a Name tag. Silently
	// choosing either one is the failure mode this whole function exists
	// to prevent.
	got := Resolve(fixture(), "web-prod-1")
	if len(got) != 2 {
		t.Fatalf("got %v, want both i-0aaa and i-0ddd", ids(got))
	}
	for _, m := range got {
		if !m.Exact {
			t.Errorf("%s should be an exact name match", m.Node.ID)
		}
	}
}

func TestResolveExactNameSuppressesFuzzyMatches(t *testing.T) {
	// "web-prod-1" is also a fuzzy match for "web-prod-2"; the exact hits
	// must not be diluted by it.
	got := Resolve(fixture(), "web-prod-1")
	for _, m := range got {
		if m.Node.ID == "i-0bbb" {
			t.Fatal("fuzzy match leaked in alongside exact matches")
		}
	}
}

func TestResolveFuzzyMatchesWhenNothingIsExact(t *testing.T) {
	got := Resolve(fixture(), "webprod")
	if len(got) < 2 {
		t.Fatalf("got %v, want several fuzzy matches", ids(got))
	}
	for _, m := range got {
		if m.Exact {
			t.Errorf("%s marked exact but query was fuzzy", m.Node.ID)
		}
	}
}

func TestResolveByTagValue(t *testing.T) {
	got := Resolve(fixture(), "stage")
	found := false
	for _, m := range got {
		if m.Node.ID == "i-0ccc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("got %v, want i-0ccc via its Env tag", ids(got))
	}
}

func TestResolveEmptyQueryReturnsEverything(t *testing.T) {
	got := Resolve(fixture(), "")
	if len(got) != 4 {
		t.Fatalf("got %d matches, want all 4", len(got))
	}
}

func TestResolveUnknownQueryReturnsNothing(t *testing.T) {
	if got := Resolve(fixture(), "zzzzzz-no-such-host"); len(got) != 0 {
		t.Fatalf("got %v, want none", ids(got))
	}
}

func TestResolveIsCaseInsensitiveOnNames(t *testing.T) {
	if got := Resolve(fixture(), "WEB-PROD-2"); len(got) != 1 {
		t.Fatalf("got %v, want [i-0bbb]", ids(got))
	}
}

// A machine whose registration was folded into it still answers to the
// registration's ID, and to its own hostname.
func TestResolveByAliasAndHostname(t *testing.T) {
	nodes := []inventory.Node{
		{ID: "ocid1.instance.a", Name: "build-02", Hostname: "ci-runner-2", Aliases: []string{"mi-0abc"}},
		{ID: "i-1", Name: "web-1"},
	}
	for _, q := range []string{"mi-0abc", "MI-0ABC", "ci-runner-2"} {
		if ms := Resolve(nodes, q); len(ms) != 1 || ms[0].Node.ID != "ocid1.instance.a" || !ms[0].Exact {
			t.Errorf("%q resolved to %+v", q, ms)
		}
	}
}
