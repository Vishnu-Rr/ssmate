package resolve

import (
	"strings"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

func suggestFixture() []inventory.Node {
	return []inventory.Node{
		{ID: "i-0aaa", Name: "web-prod-1"},
		{ID: "i-0bbb", Name: "web-prod-2"},
		{ID: "i-0ccc", Name: "db-stage-1"},
		{ID: "i-0ddd", Name: "cache-prod-1"},
	}
}

func TestSuggestFindsNearMisses(t *testing.T) {
	got := Suggest(suggestFixture(), "web-prod", 3)
	if len(got) == 0 {
		t.Fatal("a near miss should produce suggestions")
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "web-prod-1") {
		t.Errorf("suggestions %v should include web-prod-1", got)
	}
}

func TestSuggestRespectsLimit(t *testing.T) {
	if got := Suggest(suggestFixture(), "prod", 2); len(got) > 2 {
		t.Fatalf("got %d suggestions, want at most 2", len(got))
	}
}

func TestSuggestReturnsNothingForTotalNonsense(t *testing.T) {
	if got := Suggest(suggestFixture(), "qqqqqqzzzzz", 3); len(got) != 0 {
		t.Fatalf("got %v, want no suggestions for an unrelated query", got)
	}
}

func TestSuggestHandlesEmptyInputs(t *testing.T) {
	if got := Suggest(nil, "web", 3); len(got) != 0 {
		t.Errorf("nil nodes should produce no suggestions, got %v", got)
	}
	if got := Suggest(suggestFixture(), "", 3); len(got) != 0 {
		t.Errorf("empty query should produce no suggestions, got %v", got)
	}
}
