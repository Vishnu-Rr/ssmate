package oci

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/oracle/oci-go-sdk/v65/core"
)

// Whether a bastion can carry a connection is two questions, and OCI answers
// neither until something tries. Does the bastion admit you? Its client
// allowlist decides, before any authentication. Can the bastion reach the
// machine? The security lists on the bastion's subnet decide whether the
// traffic may leave, and the security lists on the machine's subnet together
// with the network security groups on its interface decide whether it may
// arrive. A session reports ACTIVE through all of that, so without asking
// here a machine shows as reachable when no session to it could ever work.
//
// Both are answered from the rules themselves. The answer is "unknown" rather
// than "no" wherever a rule names something that cannot be evaluated from
// here, such as a service or another security group, so a machine is never
// called blocked on a guess.

// verdict is a three-way answer: a rule allows it, no rule could, or a rule
// that cannot be evaluated from here might.
type verdict int

const (
	closed verdict = iota
	unknown
	open
)

// rule is one allow rule, from a security list or a network security group,
// reduced to what reachability needs. OCI rules only ever allow.
type rule struct {
	egress bool
	// peer is the CIDR on the other end: the destination of an egress rule,
	// the source of an ingress one. Empty when the rule names something that
	// is not a CIDR, which makes it unevaluable.
	peer      string
	protocol  string
	dst, src  *portRange
	stateless bool
}

type portRange struct{ min, max int }

func (p *portRange) has(port int) bool { return p == nil || (port >= p.min && port <= p.max) }

// coversEphemeral reports whether a range admits the ports a client picks for
// its own end of a connection. Linux picks from 32768 to 60999.
func (p *portRange) coversEphemeral() bool { return p == nil || (p.min <= 32768 && p.max >= 60999) }

const sshPort = 22

// tcp reports whether a rule's protocol carries TCP.
func (r rule) tcp() bool { return r.protocol == "all" || r.protocol == "6" }

// match finds whether any rule in one direction allows TCP between this side
// and ip, with the given test on its ports. It returns the verdict and
// whether the allowing rule was stateless, which decides whether replies need
// a rule of their own.
func match(rules []rule, egress bool, ip string, ports func(rule) bool) (verdict, bool) {
	v := closed
	for _, r := range rules {
		if r.egress != egress || !r.tcp() || !ports(r) {
			continue
		}
		if r.peer == "" {
			v = unknown
			continue
		}
		if cidrContains(r.peer, ip) {
			return open, r.stateless
		}
	}
	return v, false
}

// pathTo decides whether a bastion whose private endpoint is bastionIP, on a
// subnet with rules from, can open TCP to port 22 on targetIP, whose subnet
// and interface carry rules to. why explains a closed path in terms of the
// rule that has to change.
func pathTo(from []rule, bastionIP string, to []rule, targetIP string) (verdict, string) {
	toSSH := func(r rule) bool { return r.dst.has(sshPort) }
	reply := func(r rule) bool { return r.src.has(sshPort) && r.dst.coversEphemeral() }
	fromSSH := func(r rule) bool { return r.src.has(sshPort) }

	out, outStateless := match(from, true, targetIP, toSSH)
	if out == closed {
		return closed, fmt.Sprintf("its subnet's security lists allow nothing out to %s on port 22", targetIP)
	}
	if outStateless {
		if back, _ := match(from, false, targetIP, reply); back == closed {
			return closed, fmt.Sprintf("replies from %s are not allowed back into its subnet", targetIP)
		} else if back == unknown {
			out = unknown
		}
	}
	in, inStateless := match(to, false, bastionIP, toSSH)
	if in == closed {
		return closed, fmt.Sprintf("%s admits nothing from the bastion (%s) on port 22", targetIP, bastionIP)
	}
	if inStateless {
		if back, _ := match(to, true, bastionIP, fromSSH); back == closed {
			return closed, fmt.Sprintf("%s's replies to the bastion are not allowed out", targetIP)
		} else if back == unknown {
			in = unknown
		}
	}
	if out == unknown || in == unknown {
		return unknown, ""
	}
	return open, ""
}

// admitsInternet reports whether a client allowlist admits any address on
// the internet. A list of private ranges only admits clients that reach OCI
// over a private network, which a laptop on the internet never is. An empty
// list is unknown: it may equally be a bastion we could not read.
func admitsInternet(allow []string) verdict {
	if len(allow) == 0 {
		return unknown
	}
	for _, c := range allow {
		_, block, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			return unknown
		}
		if !privateBlock(block) {
			return open
		}
	}
	return closed
}

var privateRanges = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// privateBlock reports whether every address in a block is private.
func privateBlock(b *net.IPNet) bool {
	ones, _ := b.Mask.Size()
	for _, p := range privateRanges {
		pOnes, _ := p.Mask.Size()
		if ones >= pOnes && p.Contains(b.IP) {
			return true
		}
	}
	return false
}

func cidrContains(cidr, ip string) bool {
	_, block, err := net.ParseCIDR(strings.TrimSpace(cidr))
	addr := net.ParseIP(ip)
	return err == nil && addr != nil && block.Contains(addr)
}

// portsOf reads a TCP options block. Only TCP matters for SSH.
func portsOf(t *core.TcpOptions) (dst, src *portRange) {
	if t == nil {
		return nil, nil
	}
	if t.DestinationPortRange != nil && t.DestinationPortRange.Min != nil && t.DestinationPortRange.Max != nil {
		dst = &portRange{*t.DestinationPortRange.Min, *t.DestinationPortRange.Max}
	}
	if t.SourcePortRange != nil && t.SourcePortRange.Min != nil && t.SourcePortRange.Max != nil {
		src = &portRange{*t.SourcePortRange.Min, *t.SourcePortRange.Max}
	}
	return dst, src
}

func ingressRule(r core.IngressSecurityRule) rule {
	dst, src := portsOf(r.TcpOptions)
	peer := deref(r.Source)
	if r.SourceType != "" && r.SourceType != core.IngressSecurityRuleSourceTypeCidrBlock {
		peer = ""
	}
	return rule{peer: peer, protocol: deref(r.Protocol), dst: dst, src: src, stateless: r.IsStateless != nil && *r.IsStateless}
}

func egressRule(r core.EgressSecurityRule) rule {
	dst, src := portsOf(r.TcpOptions)
	peer := deref(r.Destination)
	if r.DestinationType != "" && r.DestinationType != core.EgressSecurityRuleDestinationTypeCidrBlock {
		peer = ""
	}
	return rule{egress: true, peer: peer, protocol: deref(r.Protocol), dst: dst, src: src, stateless: r.IsStateless != nil && *r.IsStateless}
}

func nsgRule(r core.SecurityRule) rule {
	dst, src := portsOf(r.TcpOptions)
	out := rule{egress: r.Direction == core.SecurityRuleDirectionEgress, protocol: deref(r.Protocol),
		dst: dst, src: src, stateless: r.IsStateless != nil && *r.IsStateless}
	if out.egress {
		if r.DestinationType == "" || r.DestinationType == core.SecurityRuleDestinationTypeCidrBlock {
			out.peer = deref(r.Destination)
		}
	} else if r.SourceType == "" || r.SourceType == core.SecurityRuleSourceTypeCidrBlock {
		out.peer = deref(r.Source)
	}
	return out
}

// securityLists reads every security list in the given compartments. It is a
// call per compartment rather than per list, and returns each list's rules.
func securityLists(ctx context.Context, c networkAPI, compartmentIDs []string) map[string][]rule {
	out := map[string][]rule{}
	for _, id := range compartmentIDs {
		cid := id
		var page *string
		for {
			resp, err := c.ListSecurityLists(ctx, core.ListSecurityListsRequest{CompartmentId: &cid, Page: page})
			if err != nil {
				break
			}
			for _, sl := range resp.Items {
				var rules []rule
				for _, r := range sl.IngressSecurityRules {
					rules = append(rules, ingressRule(r))
				}
				for _, r := range sl.EgressSecurityRules {
					rules = append(rules, egressRule(r))
				}
				out[deref(sl.Id)] = rules
			}
			if resp.OpcNextPage == nil {
				break
			}
			page = resp.OpcNextPage
		}
	}
	return out
}

// nsgRules reads the rules of the given network security groups. A group that
// cannot be read comes back as one unevaluable rule, so the path through it
// is unknown rather than closed.
func nsgRules(ctx context.Context, c networkAPI, ids map[string]bool) map[string][]rule {
	out := map[string][]rule{}
	for id := range ids {
		nsg := id
		var rules []rule
		var page *string
		for {
			resp, err := c.ListNetworkSecurityGroupSecurityRules(ctx,
				core.ListNetworkSecurityGroupSecurityRulesRequest{NetworkSecurityGroupId: &nsg, Page: page})
			if err != nil {
				rules = append(rules, rule{protocol: "all"}, rule{egress: true, protocol: "all"})
				break
			}
			for _, r := range resp.Items {
				rules = append(rules, nsgRule(r))
			}
			if resp.OpcNextPage == nil {
				break
			}
			page = resp.OpcNextPage
		}
		out[nsg] = rules
	}
	return out
}

// unevaluable stands in for rules that could not be read, so a path through
// them is unknown rather than closed.
var unevaluable = []rule{{protocol: "all"}, {egress: true, protocol: "all"}}

// network is one region's rules, gathered once per sweep.
type network struct {
	subnetLists map[string][]string
	lists       map[string][]rule
	nsgs        map[string][]rule
}

// subnetRules are the rules every interface in a subnet is subject to.
func (w network) subnetRules(subnet string) []rule {
	ids, ok := w.subnetLists[subnet]
	if !ok {
		return unevaluable
	}
	var out []rule
	for _, id := range ids {
		r, ok := w.lists[id]
		if !ok {
			out = append(out, unevaluable...)
			continue
		}
		out = append(out, r...)
	}
	return out
}

// machineRules are the rules a machine's interface is subject to: its
// subnet's security lists and its own security groups. Traffic is allowed if
// any of them allows it.
func (w network) machineRules(a addressing) []rule {
	out := w.subnetRules(a.subnetID)
	for _, id := range a.nsgIDs {
		r, ok := w.nsgs[id]
		if !ok {
			out = append(out, unevaluable...)
			continue
		}
		out = append(out, r...)
	}
	return out
}

// door is one bastion's verdict for one machine.
type door struct {
	g      bastionSummary
	admits verdict
	path   verdict
	why    string
}

// usable reports whether the door might carry a connection from a laptop on
// the internet. Unknown counts as might.
func (d door) usable() bool { return d.admits != closed && d.path != closed }

// reason says why a door cannot be used, in terms of what has to change.
func (d door) reason() string {
	if d.admits == closed {
		return fmt.Sprintf("%s admits only %s, private addresses no client on the internet has",
			d.g.name, strings.Join(d.g.allow, ", "))
	}
	return fmt.Sprintf("%s cannot reach the machine: %s", d.g.name, d.why)
}

// judge decides every door into one machine, best first: those certain to
// reach it, then those that might, then those that cannot.
func judge(w network, gws []bastionSummary, a addressing) []door {
	target := w.machineRules(a)
	var sure, maybe, shut []door
	for _, g := range gws {
		d := door{g: g, admits: admitsInternet(g.allow), path: unknown}
		if g.endpoint != "" && g.subnet != "" && a.privateIP != "" {
			d.path, d.why = pathTo(w.subnetRules(g.subnet), g.endpoint, target, a.privateIP)
		}
		switch {
		case !d.usable():
			shut = append(shut, d)
		case d.admits == open && d.path == open:
			sure = append(sure, d)
		default:
			maybe = append(maybe, d)
		}
	}
	return append(append(sure, maybe...), shut...)
}

// reachOf sums up a machine's doors: "open" when one is certain to carry a
// connection, "unverified" when one might, "blocked" when none can, with the
// reason for each in the last case.
func reachOf(doors []door) (string, string) {
	if len(doors) == 0 {
		return "", ""
	}
	if d := doors[0]; d.usable() {
		if d.admits == open && d.path == open {
			return "open", ""
		}
		return "unverified", ""
	}
	why := make([]string, 0, len(doors))
	for _, d := range doors {
		why = append(why, d.reason())
	}
	return "blocked", strings.Join(why, "; ")
}

// blockIfNoDoor marks a running machine's bastion route blocked when every
// bastion into its network is known to be unable to carry a connection, so
// the list stops calling it online.
func blockIfNoDoor(n *inventory.Node) {
	if v, _ := n.Detailed("reach"); v != reachBlocked || n.Down {
		return
	}
	why, _ := n.Detailed("reach_why")
	for i, r := range n.Routes {
		if r.Kind == inventory.RouteOCIForward {
			n.Routes[i].Ready, n.Routes[i].Status, n.Routes[i].Hint = false, inventory.StatusBlocked, why
		}
	}
	inventory.Summarize(n, inventory.DefaultRouteOrder)
}

const reachBlocked = "blocked"
