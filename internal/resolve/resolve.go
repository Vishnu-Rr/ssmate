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
	// Fuzzy marks a match only on letters in order, with gaps. It is the
	// last resort and the least sure: a caller must not connect to one
	// without asking.
	Fuzzy bool
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

	// Nothing exact, so names that contain what was typed: every word of it,
	// in any order. Those that start with it come first.
	if contains := containing(nodes, lower); len(contains) > 0 {
		return contains
	}

	// Only then letters in order with gaps, which is how "ssh" came to match
	// every name with an s, another s and an h somewhere in it.
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = strings.ToLower(n.DisplayName())
	}
	var out []Match
	for _, hit := range fuzzy.Find(lower, names) {
		out = append(out, Match{Node: nodes[hit.Index], Reason: "fuzzy", Fuzzy: true})
	}
	return out
}

// containing returns the nodes whose name or hostname contains every word of
// q, or whose identifier starts with it; those whose name starts with q
// first, then by name.
func containing(nodes []inventory.Node, q string) []Match {
	words := strings.Fields(q)
	if len(words) == 0 {
		return nil
	}
	type hit struct {
		m      Match
		starts bool
		name   string
	}
	var hits []hit
	for _, n := range nodes {
		name := strings.ToLower(n.DisplayName())
		text := name + " " + strings.ToLower(n.Hostname)
		id := strings.ToLower(n.ID)
		all := true
		for _, w := range words {
			// Inside a name or hostname, or at the start of the identifier.
			// Not inside it: an Azure resource ID spells out
			// "virtualMachines", and every Azure machine matched "ch".
			if !strings.Contains(text, w) && !strings.HasPrefix(id, w) {
				all = false
				break
			}
		}
		if all {
			hits = append(hits, hit{Match{Node: n, Reason: "name contains"}, strings.HasPrefix(name, q), name})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.starts != b.starts {
			return a.starts
		}
		if a.name != b.name {
			return a.name < b.name
		}
		if a.m.Node.Region != b.m.Node.Region {
			return a.m.Node.Region < b.m.Node.Region
		}
		return a.m.Node.ID < b.m.Node.ID
	})
	out := make([]Match, len(hits))
	for i, h := range hits {
		out[i] = h.m
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
