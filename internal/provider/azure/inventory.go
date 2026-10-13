package azure

import (
	"regexp"
	"sort"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

// StatusNoTunnel is Azure's own kind of unreachable, and it has no equivalent
// in the other clouds. The bastion exists, it is running, it is in the right
// network, and it still cannot be used from a terminal: the native client
// needs a Standard or Premium SKU with tunnelling enabled. A Basic bastion
// works perfectly well through the browser, so nothing about it looks broken.
const StatusNoTunnel = "no tunnel"

// vnetOf extracts the virtual network from any resource ID beneath one.
//
// Azure resource IDs are a path, and a subnet's path contains its network. So
// the join from a machine to the bastion that can reach it needs no extra
// call: the subnet ID a NIC already carries names the network.
var vnetPattern = regexp.MustCompile(`(?i)/virtualNetworks/([^/]+)`)

// vnetKey identifies a virtual network within a subscription.
//
// Network names repeat across resource groups on any estate of size - every
// team's default network is called something like vnet-default - so the name
// alone would join a machine to a bastion in an unrelated network. Any
// resource ID beneath a network carries both halves.
var vnetKeyPattern = regexp.MustCompile(`(?i)/resourceGroups/([^/]+)/providers/Microsoft\.Network/virtualNetworks/([^/]+)`)

func vnetKey(resourceID string) string {
	m := vnetKeyPattern.FindStringSubmatch(resourceID)
	if len(m) != 3 {
		return ""
	}
	return strings.ToLower(m[1] + "/" + m[2])
}

func vnetOf(resourceID string) string {
	m := vnetPattern.FindStringSubmatch(resourceID)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

// gateway is one bastion, reduced to what reachability turns on.
type gateway struct {
	name    string
	group   string
	vnet    string // the network the bastion itself sits in, by name
	sku     string
	tunnel  bool
	running bool
	// peered is true when this bastion reaches the machine's network through
	// peering rather than by sitting in it.
	peered bool
}

// usable reports whether this bastion can actually carry a session from a
// terminal. Both halves are required and they are separate settings: a
// Standard SKU with tunnelling switched off is as unusable as a Basic one.
func (g gateway) usable() bool {
	return g.running && g.tunnel && !strings.EqualFold(g.sku, "Basic")
}

// gatewaysByVNet indexes bastions by the network they serve.
func gatewaysByVNet(hosts []*armnetwork.BastionHost) map[string][]gateway {
	out := map[string][]gateway{}
	for _, h := range hosts {
		if h == nil || h.Properties == nil {
			continue
		}
		g := gateway{
			name:    deref(h.Name),
			group:   groupOf(deref(h.ID)),
			running: strings.EqualFold(derefState(h.Properties.ProvisioningState), "Succeeded"),
		}
		if h.SKU != nil && h.SKU.Name != nil {
			g.sku = string(*h.SKU.Name)
		}
		if h.Properties.EnableTunneling != nil {
			g.tunnel = *h.Properties.EnableTunneling
		}
		for _, ip := range h.Properties.IPConfigurations {
			if ip == nil || ip.Properties == nil || ip.Properties.Subnet == nil {
				continue
			}
			if v := vnetOf(deref(ip.Properties.Subnet.ID)); v != "" {
				g.vnet = v
				break
			}
		}
		if g.vnet == "" {
			continue
		}
		key := ""
		for _, ip := range h.Properties.IPConfigurations {
			if ip != nil && ip.Properties != nil && ip.Properties.Subnet != nil {
				key = vnetKey(deref(ip.Properties.Subnet.ID))
				break
			}
		}
		if key == "" {
			continue
		}
		out[key] = append(out[key], g)
	}
	for v := range out {
		gws := out[v]
		sortGateways(gws)
		out[v] = gws
	}
	return out
}

// sortGateways puts the bastions that can actually carry a session first, a
// bastion in the machine's own network ahead of one reaching it through
// peering, then orders by name - so which one gets used never depends on the
// order the API happened to answer in.
func sortGateways(gws []gateway) {
	sort.SliceStable(gws, func(i, j int) bool {
		if a, b := gws[i].usable(), gws[j].usable(); a != b {
			return a
		}
		if gws[i].peered != gws[j].peered {
			return !gws[i].peered
		}
		return gws[i].name < gws[j].name
	})
}

// reachThroughPeering extends each bastion's reach to the networks peered with
// its own.
//
// Azure Bastion connects to machines in peered networks, not only its own, and
// estates are commonly built that way: one bastion in a hub network, every
// other network peered to it. Looking only at the machine's own network made
// all fifty machines of the estate this was built against read "no gateway"
// while the hub bastion could reach every one - proved by opening a session
// through it before this was written.
//
// Only a peering in the Connected state counts. One that is Initiated or
// Disconnected carries no traffic, and a bastion that looks reachable but is
// not is the worst answer to give.
func reachThroughPeering(own map[string][]gateway, peers map[string][]string) map[string][]gateway {
	out := make(map[string][]gateway, len(own))
	for key, gws := range own {
		out[key] = append(out[key], gws...)
	}
	for bastionNet, gws := range own {
		for _, peer := range peers[bastionNet] {
			for _, g := range gws {
				g.peered = true
				out[peer] = append(out[peer], g)
			}
		}
	}
	for key := range out {
		sortGateways(out[key])
	}
	return out
}

// groupOf pulls the resource group out of a resource ID, because every
// bastion command needs it alongside the name.
var groupPattern = regexp.MustCompile(`(?i)/resourceGroups/([^/]+)`)

func groupOf(resourceID string) string {
	m := groupPattern.FindStringSubmatch(resourceID)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

// classify decides whether a machine can be reached, and says why not.
//
// The order matters. A stopped machine is stopped whatever the network looks
// like, so that is asked first. Then whether any bastion serves the network at
// all, and only then whether one of them can actually be used - because
// "there is no bastion here" and "the bastion here is the wrong kind" are
// different problems with different fixes, and collapsing them would send
// someone to build a second bastion when they needed to upgrade the first.
func classify(n *inventory.Node, gws []gateway, vnet string) {
	defer inventory.Summarize(n, inventory.DefaultRouteOrder)
	r := inventory.Route{Kind: inventory.RouteAzureBastion, Target: n.ID, Account: n.Account, Region: n.Region}
	defer func() { n.AddRoute(r) }()

	// A machine that is not running keeps its route, carrying its state, so
	// a cache older than its last start does not stop anyone trying.
	switch {
	case isStopped(n.State):
		n.Down, n.Status, n.Hint = true, inventory.StatusStopped, "start the machine"
	case isTerminated(n.State):
		n.Down, n.Status = true, inventory.StatusTerminated
	case n.State == "":
		n.Down, n.Status = true, inventory.StatusUnknown
	}
	if n.Down {
		r.Status, r.Hint = n.Status, n.Hint
		return
	}

	if len(gws) == 0 {
		r.Status = inventory.StatusNoGateway
		if vnet != "" {
			r.Hint = "no bastion in " + vnet + "; create one in that virtual network"
		} else {
			r.Hint = "no bastion reaches this machine's network"
		}
		return
	}
	for _, g := range gws {
		if g.usable() {
			r.Ready, r.Status = true, inventory.StatusOnline
			return
		}
	}

	// A bastion is there and cannot be used. Name which setting is wrong,
	// because the two are independent and the fix differs.
	g := gws[0]
	r.Status = StatusNoTunnel
	switch {
	case !g.running:
		r.Hint = "bastion " + g.name + " is not finished deploying"
	case strings.EqualFold(g.sku, "Basic"):
		r.Hint = "bastion " + g.name + " is the Basic SKU, which has no native client; " +
			"upgrade it to Standard and enable tunnelling"
	default:
		r.Hint = "bastion " + g.name + " has tunnelling disabled; run: " +
			"az network bastion update --name " + g.name + " --resource-group " + g.group +
			" --enable-tunneling true"
	}
}

// Azure reports power state as a status string rather than an enum, and the
// wording differs between the instance view and the summary APIs.
func isStopped(state string) bool {
	s := strings.ToLower(state)
	return strings.Contains(s, "deallocat") || strings.Contains(s, "stopp")
}

func isTerminated(state string) bool {
	s := strings.ToLower(state)
	return strings.Contains(s, "delet")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefState(s *armnetwork.ProvisioningState) string {
	if s == nil {
		return ""
	}
	return string(*s)
}

// tagsOf copies Azure's tags, which are pointers to strings.
func tagsOf(in map[string]*string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = deref(v)
	}
	return out
}
