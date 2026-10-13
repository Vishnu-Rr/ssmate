package inventory

import (
	"sort"
	"strings"
)

// Route is one way into a machine.
//
// Where a machine lives and how you get into it are separate questions. A
// server in one cloud can be reachable through that cloud's bastion and also
// through an agent registered with another cloud's session service, and the
// best way in is not always the one its own cloud provides. So a node carries
// every way in that was found, and the choice between them is made in one
// place, by one rule, rather than inside each cloud.
type Route struct {
	// Kind names the handler that opens sessions along this route.
	Kind string `json:"kind"`

	// Target, Account and Region are what that handler connects to. For a
	// route that belongs to the machine's own cloud they repeat the node's
	// own identity. For one that does not they name something else: the
	// session service's identifier for the machine, in that service's own
	// account and region.
	Target  string `json:"target"`
	Account string `json:"account,omitempty"`
	Region  string `json:"region,omitempty"`

	// Ready is the route's own verdict: a session along it can plausibly be
	// opened right now. Status and Hint say why not when it cannot.
	Ready  bool   `json:"ready"`
	Status string `json:"status"`
	Hint   string `json:"hint,omitempty"`

	// Link says how a route from another inventory was tied to this machine:
	// "source" when the registering service recorded it, "tag" when the
	// ssmate:source tag named it, "address" when the address and hostname
	// matched. Empty for a route from the machine's own cloud. An address
	// match is a judgement rather than a fact, so it is shown.
	Link string `json:"link,omitempty"`
}

// How a route from another inventory was tied to a machine.
const (
	LinkSource  = "source"
	LinkTag     = "tag"
	LinkAddress = "address"
)

// Route kinds. Each has exactly one handler.
const (
	// RouteSSM is AWS Session Manager. The machine's agent connects out, so
	// nothing on the machine listens and no login is needed on it.
	RouteSSM = "ssm"
	// RouteArc is SSH through Azure Arc, for machines outside Azure. The agent
	// connects out; the machine still wants a login.
	RouteArc = "arc"
	// RouteAzureBastion is Azure Bastion's native client tunnel.
	RouteAzureBastion = "azure-bastion"
	// RouteOCIForward is an OCI bastion port-forwarding session, with your own
	// key on the machine.
	RouteOCIForward = "oci-forward"
	// RouteOCIManaged is an OCI bastion managed SSH session, where OCI places
	// a one-time key on the machine, so no key of yours is needed.
	RouteOCIManaged = "oci-managed"
)

// DefaultRouteOrder ranks the ways in, best first.
//
// The rank asks two questions. Does anything have to accept a connection from
// outside? An agent that connects out exposes nothing; a bastion is an
// endpoint on the internet. And does the machine want a credential of its
// own? A route that needs none is one less secret to manage. Session Manager
// answers no to both, Arc no to the first, and the bastions yes to the first.
//
// OCI's managed SSH needs no key of yours, which would put it ahead of port
// forwarding, but it is ranked last on measurement: it took three to five
// minutes to open against port forwarding's under one, and the state OCI
// reports for the plugin it depends on did not predict whether it would
// work. So it is the way in for someone with no key on the machine, asked
// for with --via bastion, rather than the one a session takes by default.
var DefaultRouteOrder = []string{RouteSSM, RouteArc, RouteAzureBastion, RouteOCIForward, RouteOCIManaged}

// RouteKinds is every kind this build knows, in default order.
func RouteKinds() []string { return append([]string(nil), DefaultRouteOrder...) }

// labels are the short names shown in a list and accepted by --via. Two
// bastion kinds share "bastion" because the cloud column already says which
// cloud's bastion it is; port forwarding keeps a name of its own because it
// asks for something the others do not: your own key on the machine.
var labels = map[string]string{
	RouteSSM:          "ssm",
	RouteArc:          "arc",
	RouteOCIManaged:   "bastion",
	RouteAzureBastion: "bastion",
	RouteOCIForward:   "bastion-fwd",
}

// RouteCloud names the cloud whose service carries a route kind, and so whose
// credentials and tools a session along it depends on.
func RouteCloud(kind string) string {
	switch kind {
	case RouteSSM:
		return CloudAWS
	case RouteArc, RouteAzureBastion:
		return CloudAzure
	case RouteOCIManaged, RouteOCIForward:
		return CloudOCI
	}
	return ""
}

// RouteLabel is the short name for a route kind.
func RouteLabel(kind string) string {
	if l, ok := labels[kind]; ok {
		return l
	}
	return kind
}

// RouteMatches reports whether a route kind answers to a name the user typed:
// the kind itself or its short label, in any case.
func RouteMatches(kind, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name != "" && (name == kind || name == RouteLabel(kind))
}

// KnownRoute reports whether a name the user typed means any route at all.
func KnownRoute(name string) bool {
	for _, k := range DefaultRouteOrder {
		if RouteMatches(k, name) {
			return true
		}
	}
	return false
}

// ParseRouteOrder reads a preferred order, such as the SSMATE_ROUTES
// environment variable, as a comma-separated list of kinds or labels.
//
// Kinds it names come first, in the order given. Kinds it leaves out keep
// their default order after them, so naming one route you prefer never makes
// the others unusable. Names it does not recognise are returned separately,
// so the caller can say so instead of ignoring a typo.
func ParseRouteOrder(spec string) (order []string, unknown []string) {
	seen := map[string]bool{}
	for _, part := range strings.Split(spec, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		matched := false
		for _, k := range DefaultRouteOrder {
			if !RouteMatches(k, name) {
				continue
			}
			matched = true
			if !seen[k] {
				order = append(order, k)
				seen[k] = true
			}
		}
		if !matched {
			unknown = append(unknown, name)
		}
	}
	for _, k := range DefaultRouteOrder {
		if !seen[k] {
			order = append(order, k)
		}
	}
	return order, unknown
}

// AddRoute records a way in, replacing an earlier one of the same kind to the
// same target, so a node rebuilt from two sources never lists a route twice.
func (n *Node) AddRoute(r Route) {
	for i, have := range n.Routes {
		if have.Kind == r.Kind && have.Target == r.Target {
			n.Routes[i] = r
			return
		}
	}
	n.Routes = append(n.Routes, r)
}

// OrderedRoutes returns the node's routes, best first by order. A kind the
// order does not name sorts last, and ties keep the order they were found in.
func (n Node) OrderedRoutes(order []string) []Route {
	rank := map[string]int{}
	for i, k := range order {
		rank[k] = i
	}
	pos := func(kind string) int {
		if i, ok := rank[kind]; ok {
			return i
		}
		return len(order)
	}
	out := append([]Route(nil), n.Routes...)
	sort.SliceStable(out, func(i, j int) bool { return pos(out[i].Kind) < pos(out[j].Kind) })
	return out
}

// BestRoute is the route a session would use: the first ready one in order,
// or failing that the first in order, which is the one worth fixing. False
// means the node has no route at all.
func (n Node) BestRoute(order []string) (Route, bool) {
	ordered := n.OrderedRoutes(order)
	for _, r := range ordered {
		if r.Ready {
			return r, true
		}
	}
	if len(ordered) > 0 {
		return ordered[0], true
	}
	return Route{}, false
}

// Summarize decides the node's own verdict from its routes.
//
// A machine that is not running is described by its own state, whatever its
// routes say: an agent that stopped reporting because the machine is off is
// not the problem to fix. Otherwise the node is as reachable as its best
// route, and when no route is ready it shows the blocker on the route it
// would most like to use, since that is the one worth fixing.
//
// This is the only place a node's Reachable, Status, Hint and Via are derived,
// so the list, the listing and the diagnosis can never disagree about them.
func Summarize(n *Node, order []string) {
	if n.Down {
		n.Reachable, n.Via = false, ""
		return
	}
	best, ok := n.BestRoute(order)
	if !ok {
		n.Reachable, n.Status, n.Hint, n.Via = false, StatusNoRoute, "", ""
		return
	}
	n.Reachable, n.Status, n.Hint, n.Via = best.Ready, best.Status, best.Hint, RouteLabel(best.Kind)
}

// SummarizeAll applies Summarize to every node.
func SummarizeAll(nodes []Node, order []string) {
	for i := range nodes {
		Summarize(&nodes[i], order)
	}
}

// HasRoute reports whether a node has a route answering to a name the user
// typed, ready or not.
func (n Node) HasRoute(name string) bool {
	for _, r := range n.Routes {
		if RouteMatches(r.Kind, name) {
			return true
		}
	}
	return false
}
