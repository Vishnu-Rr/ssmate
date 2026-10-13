package resolve

import (
	"strings"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/sahilm/fuzzy"
)

// Suggest returns display names close to a query that matched nothing.
//
// This is the "did you mean" affordance. It offers names but never acts on
// them: silently correcting what someone typed means they never learn the
// right form, and in this tool it could also mean connecting to the wrong
// machine.
func Suggest(nodes []inventory.Node, query string, limit int) []string {
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" || len(nodes) == 0 || limit <= 0 {
		return nil
	}
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = n.DisplayName()
	}
	lowered := make([]string, len(names))
	for i, n := range names {
		lowered[i] = strings.ToLower(n)
	}

	var out []string
	for _, hit := range fuzzy.Find(q, lowered) {
		out = append(out, names[hit.Index])
		if len(out) == limit {
			break
		}
	}
	return out
}
