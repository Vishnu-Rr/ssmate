package cli

import (
	"strings"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

func completionNodes() []inventory.Node {
	return []inventory.Node{
		{Cloud: "aws", ID: "i-0a1", Name: "web-1", Region: "eu-west-2", Status: "online"},
		{Cloud: "aws", ID: "i-0b2", Name: "worker", Region: "eu-west-2", Status: "online"},
		{Cloud: "aws", ID: "i-0c3", Name: "worker", Region: "us-west-2", Status: "online"},
		{Cloud: "oci", ID: "ocid1.instance.oc1..zzz", Name: "db-1", Region: "us-ashburn-1", Status: "no gateway"},
	}
}

func values(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i], _, _ = strings.Cut(s, "\t")
	}
	return out
}

// A unique name completes to itself, because that is what the user will type
// and what connect will accept.
func TestCompletionOffersUniqueNames(t *testing.T) {
	got := values(suggestTargets(completionNodes(), nil, "web"))
	if len(got) != 1 || got[0] != "web-1" {
		t.Errorf("got %v", got)
	}
}

// A name shared by several machines is ambiguous, so completion offers the
// identifiers instead - but it still has to match on the name, because the
// name is what the user knows. Matching identifiers against typed text was
// this function's first bug: typing "worker" completed to nothing at all.
func TestCompletionResolvesAmbiguousNamesToIdentifiers(t *testing.T) {
	got := values(suggestTargets(completionNodes(), nil, "worker"))
	if len(got) != 2 {
		t.Fatalf("got %v, want both identifiers", got)
	}
	for _, g := range got {
		if !strings.HasPrefix(g, "i-0") {
			t.Errorf("%q is not an identifier", g)
		}
	}
	// The description has to carry what tells them apart.
	for _, line := range suggestTargets(completionNodes(), nil, "worker") {
		if !strings.Contains(line, "worker") || !strings.Contains(line, "west") {
			t.Errorf("description should name the machine and region: %q", line)
		}
	}
}

// Bookmarks exist precisely so they are what gets typed, so they come first.
func TestCompletionOffersBookmarksFirst(t *testing.T) {
	got := suggestTargets(completionNodes(), []string{"prod"}, "")
	if len(got) == 0 || !strings.HasPrefix(got[0], "prod\t") {
		t.Fatalf("first completion = %q", got[0])
	}
	if !strings.Contains(got[0], "bookmark") {
		t.Errorf("a bookmark should say so: %q", got[0])
	}
}

func TestCompletionMatchesIdentifierPrefixToo(t *testing.T) {
	got := values(suggestTargets(completionNodes(), nil, "ocid1"))
	if len(got) != 1 || got[0] != "db-1" {
		t.Errorf("got %v, want the machine whose identifier starts with ocid1", got)
	}
}
