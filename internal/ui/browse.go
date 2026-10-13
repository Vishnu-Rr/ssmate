package ui

import (
	"sort"
	"strings"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/sahilm/fuzzy"
)

// This file is the browser's whole mind, deliberately separated from anything
// that draws. Filtering, sorting and selection are ordinary functions over
// ordinary values, so the behaviour that actually matters can be tested
// without a terminal, and the Bubble Tea model below it stays thin enough to
// read in one sitting.

// SortMode orders the list.
type SortMode int

const (
	// SortRecent puts machines you actually use at the top. It is the default
	// because on an estate of hundreds the one you want is nearly always one
	// you reached recently.
	SortRecent SortMode = iota
	SortName
	SortCloud
	SortStatus
)

func (s SortMode) String() string {
	switch s {
	case SortName:
		return "name"
	case SortCloud:
		return "cloud"
	case SortStatus:
		return "status"
	default:
		return "recent"
	}
}

// Next cycles to the following sort, so one key can walk them all.
func (s SortMode) Next() SortMode { return (s + 1) % 4 }

// FilterField is what the typed query is matched against.
type FilterField int

const (
	// FieldName matches the machine's name, which is what people type.
	FieldName FilterField = iota
	// FieldTag matches tag values, for finding a fleet rather than a machine.
	FieldTag
	// FieldAll also matches identifiers, addresses, regions and accounts, for
	// when you have an IP from a log and nothing else.
	FieldAll
)

func (f FilterField) String() string {
	switch f {
	case FieldTag:
		return "tag"
	case FieldAll:
		return "any"
	default:
		return "name"
	}
}

func (f FilterField) Next() FilterField { return (f + 1) % 3 }

// Query is everything the user has narrowed the list by.
type Query struct {
	// Text is the typed search.
	Text string
	// Field is what Text is matched against.
	Field FilterField
	// Cloud restricts to one cloud. Empty means every cloud.
	//
	// This is a filter rather than part of the command because the point of
	// the tool is to reach a machine without knowing which cloud it is in.
	// Naming the cloud is for narrowing a long list or separating two
	// machines that share a name, not a thing you should have to know first.
	Cloud string
	// ReachableOnly hides what cannot be connected to right now.
	ReachableOnly bool
	// Sort orders whatever survives.
	Sort SortMode
}

// Hit is one machine that survived the query, with the positions the search
// matched on so the list can show why.
//
// Matched holds rune offsets into the machine's display name, and only when
// the search was matching names. Matching tags or every field searches text
// the row does not display, so there would be nothing honest to underline.
type Hit struct {
	Node    inventory.Node
	Matched []int
}

// Nodes drops the match positions, for callers that only want the machines.
func Nodes(hits []Hit) []inventory.Node {
	out := make([]inventory.Node, len(hits))
	for i, h := range hits {
		out[i] = h.Node
	}
	return out
}

// Apply narrows and orders nodes.
//
// Order of operations matters: the cheap exact filters run before the fuzzy
// match, so typing stays responsive on an estate of several hundred machines,
// and the sort runs last so it applies to what is actually on screen.
func Apply(nodes []inventory.Node, q Query, recent func(string) float64) []Hit {
	kept := make([]inventory.Node, 0, len(nodes))
	for _, n := range nodes {
		if q.Cloud != "" && n.Cloud != q.Cloud {
			continue
		}
		if q.ReachableOnly && !n.Reachable {
			continue
		}
		kept = append(kept, n)
	}
	out := match(kept, q)
	sort.SliceStable(out, func(i, j int) bool {
		return less(out[i].Node, out[j].Node, q.Sort, recent)
	})
	return out
}

// match applies the typed query.
//
// An empty query keeps everything, which is what makes the list usable as a
// browser rather than only as a search box.
func match(nodes []inventory.Node, q Query) []Hit {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		out := make([]Hit, len(nodes))
		for i, n := range nodes {
			out[i] = Hit{Node: n}
		}
		return out
	}
	hay := make([]string, len(nodes))
	for i, n := range nodes {
		hay[i] = haystack(n, q.Field)
	}
	found := fuzzy.Find(text, hay)
	out := make([]Hit, 0, len(found))
	for _, f := range found {
		h := Hit{Node: nodes[f.Index]}
		// Only a name search matched text the row actually shows, so only
		// that one can be underlined truthfully.
		if q.Field == FieldName {
			h.Matched = f.MatchedIndexes
		}
		out = append(out, h)
	}
	return out
}

// haystack is the text one node is matched against, for a given field.
func haystack(n inventory.Node, field FilterField) string {
	switch field {
	case FieldTag:
		return strings.Join(tagValues(n), " ")
	case FieldAll:
		parts := []string{n.DisplayName(), n.ID, n.Cloud, n.Region, n.Account,
			n.PrivateIP, n.PublicIP, n.Status}
		parts = append(parts, tagValues(n)...)
		return strings.Join(parts, " ")
	default:
		return n.DisplayName()
	}
}

// tagValues flattens a node's tags into stable, searchable text.
//
// Both halves are included: people search for "production" as readily as for
// "Environment=production", and map order in Go is deliberately random, so
// this has to be sorted or the same query would match differently each run.
func tagValues(n inventory.Node) []string {
	if len(n.Tags) == 0 {
		return nil
	}
	keys := make([]string, 0, len(n.Tags))
	for k := range n.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		out = append(out, k, n.Tags[k])
	}
	return out
}

// sortNodes orders in place.
//
// Every mode falls back to name, so the list never reorders itself between
// identical runs - a list that shuffles under the cursor is worse than one
// ordered badly.
func less(a, b inventory.Node, mode SortMode, recent func(string) float64) bool {
	switch mode {
	case SortName:
		// fall through to the shared tiebreak
	case SortCloud:
		if a.Cloud != b.Cloud {
			return a.Cloud < b.Cloud
		}
	case SortStatus:
		if a.Reachable != b.Reachable {
			return a.Reachable
		}
		if a.Status != b.Status {
			return a.Status < b.Status
		}
	default: // SortRecent
		if recent != nil {
			ra, rb := recent(a.ID), recent(b.ID)
			if ra != rb {
				// A larger rank means used more recently. Machines never
				// used share rank zero and fall through to name.
				return ra > rb
			}
		}
	}
	if an, bn := a.DisplayName(), b.DisplayName(); an != bn {
		return an < bn
	}
	return a.ID < b.ID
}

// Clouds lists the clouds present, in a stable order, so the cloud filter can
// offer exactly what this estate actually has rather than every cloud the
// build knows about.
func Clouds(nodes []inventory.Node) []string {
	seen := map[string]bool{}
	for _, n := range nodes {
		if n.Cloud != "" {
			seen[n.Cloud] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// NextCloud cycles the cloud filter through the clouds present and back to
// all of them. Empty is "all", and is part of the cycle rather than a
// separate key, so one press always moves forward.
func NextCloud(current string, clouds []string) string {
	if len(clouds) == 0 {
		return ""
	}
	if current == "" {
		return clouds[0]
	}
	for i, c := range clouds {
		if c == current {
			if i+1 == len(clouds) {
				return ""
			}
			return clouds[i+1]
		}
	}
	return ""
}

// Counts summarises a list for the header, so the shape of the estate is
// visible without scrolling it.
type Counts struct {
	Total     int
	Reachable int
	ByCloud   map[string]int
}

func Summarise(nodes []inventory.Node) Counts {
	c := Counts{ByCloud: map[string]int{}}
	for _, n := range nodes {
		c.Total++
		if n.Reachable {
			c.Reachable++
		}
		c.ByCloud[n.Cloud]++
	}
	return c
}
