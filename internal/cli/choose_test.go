package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

// A query that only matches loosely, letters in order with gaps, is a guess.
// Connecting to the one machine it happens to hit is how a typo lands on the
// wrong machine, so it is offered, never taken.
func TestChooseNodeNeverConnectsToALooseMatch(t *testing.T) {
	nodes := []inventory.Node{{ID: "i-1", Name: "appsmith", Region: "r"}}

	_, err := chooseNode(nodes, "apsmth", &options{noInput: true})
	var e *exitcode.Error
	if !errors.As(err, &e) || e.Code != exitcode.NotFound {
		t.Fatalf("a loose match with nobody to ask = %v, want NotFound", err)
	}
	if !strings.Contains(e.Msg+e.Hint, "appsmith") {
		t.Errorf("the refusal should name what was nearly matched: %s / %s", e.Msg, e.Hint)
	}
}

// A name that contains the query is as good as typing it.
func TestChooseNodeTakesAContainedName(t *testing.T) {
	nodes := []inventory.Node{
		{ID: "i-1", Name: "appsmith", Region: "r"},
		{ID: "i-2", Name: "billing", Region: "r"},
	}
	n, err := chooseNode(nodes, "smith", &options{noInput: true})
	if err != nil || n.ID != "i-1" {
		t.Fatalf("chooseNode(smith) = %v, %v", n.ID, err)
	}
}
