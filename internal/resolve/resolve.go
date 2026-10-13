// Package resolve turns what a user typed into a set of candidate nodes.
package resolve

import (
	"sort"
	"strings"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/sahilm/fuzzy"
)

// Match is one candidate for a query, with the reason it matched so the UI
// can show why a host is on the list.
type Match struct {
	Node   inventory.Node
	Reason string
	Exact  bool
}

// Resolve returns every node a query could mean, best first.
//
// It deliberately does not choose. When a query matches two hosts, both come
// back and the caller prompts. Picking one automatically is how tools connect
// people to the wrong machine, and it is the thing this package exists to
// avoid.
//
// Exact matches suppress fuzzy ones, so typing a complete hostname never
// buries it among near misses.
func Resolve(nodes []inventory.Node, query string) []Match {
	q := strings.TrimSpace(query)
	if q == "" {
		out := make([]Match, 0, len(nodes))
		for _, n := range nodes {
			out = append(out, Match{Node: n, Reason: "all"})
		}
		return out
	}

	lower := strings.ToLower(q)
	var exact []Match

	for _, n := range nodes {
		switch {
		case strings.EqualFold(n.ID, q):
			exact = append(exact, Match{Node: n, Reason: "instance id", Exact: true})
		case n.PrivateIP != "" && n.PrivateIP == q:
			exact = append(exact, Match{Node: n, Reason: "private ip", Exact: true})
		case n.PublicIP != "" && n.PublicIP == q:
			exact = append(exact, Match{Node: n, Reason: "public ip", Exact: true})
		case n.Name != "" && strings.EqualFold(n.Name, q):
			exact = append(exact, Match{Node: n, Reason: "name", Exact: true})
		case hasAlias(n, q):
			exact = append(exact, Match{Node: n, Reason: "alias", Exact: true})
		case n.Hostname != "" && strings.EqualFold(n.Hostname, q):
			exact = append(exact, Match{Node: n, Reason: "hostname", Exact: true})
		default:
			for key, val := range n.Tags {
				if key != "Name" && strings.EqualFold(val, q) {
					exact = append(exact, Match{Node: n, Reason: "tag " + key, Exact: true})
					break
				}
			}
		}
	}
	if len(exact) > 0 {
		sortMatches(exact)
		return exact
	}

	// Nothing exact, so fall back to fuzzy matching over display names.
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = strings.ToLower(n.DisplayName())
	}
	var out []Match
	for _, hit := range fuzzy.Find(lower, names) {
		out = append(out, Match{Node: nodes[hit.Index], Reason: "fuzzy"})
	}
	return out
}

// sortMatches orders by region then instance ID, so a list of duplicates is
// presented in a stable, readable order.
func sortMatches(ms []Match) {
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].Node.Region != ms[j].Node.Region {
			return ms[i].Node.Region < ms[j].Node.Region
		}
		return ms[i].Node.ID < ms[j].Node.ID
	})
}

// hasAlias reports whether q is another identifier the machine is known by,
// such as the session service's ID for a machine whose records were linked.
func hasAlias(n inventory.Node, q string) bool {
	for _, a := range n.Aliases {
		if strings.EqualFold(a, q) {
			return true
		}
	}
	return false
}
