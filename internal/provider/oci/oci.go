package oci

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/session"
	"github.com/oracle/oci-go-sdk/v65/bastion"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// Provider reaches machines through Oracle's Bastion service.
type Provider struct{}

// Compile-time proof that this satisfies the seam.
var _ provider.Provider = Provider{}

// Handler opens sessions through an OCI bastion port-forwarding session. Its routes always belong to
// the node's own cloud, so it reads the node's own Detail as before.
type Handler struct{ p Provider }

var _ provider.Handler = Handler{}

func init() {
	provider.Register(Provider{})
	provider.RegisterHandler(Handler{})
}

// Kind is the route kind this handler serves.
func (Handler) Kind() string { return inventory.RouteOCIForward }

// Shell opens an interactive session, or runs one command.
func (h Handler) Shell(ctx context.Context, n inventory.Node, _ inventory.Route, o provider.SessionOptions) error {
	return withManagedHint(n, h.p.Shell(ctx, n, o))
}

// withManagedHint adds the other way in to a port-forwarding failure, when
// the machine has one. Port forwarding needs a key the machine already
// trusts; a managed session needs none, and someone without such a key would
// otherwise never learn the option exists.
func withManagedHint(n inventory.Node, err error) error {
	var e *exitcode.Error
	if err == nil || !n.HasRoute(inventory.RouteOCIManaged) || !errors.As(err, &e) || e.Code != exitcode.NotConnected {
		return err
	}
	e.Hint = strings.TrimSpace(e.Hint + "\n  no key of yours on the machine? --via bastion opens a managed SSH session, " +
		"which needs none; it takes a few minutes to open")
	return e
}

// Forward opens a tunnel.
func (h Handler) Forward(ctx context.Context, n inventory.Node, _ inventory.Route, f session.Forward, o provider.SessionOptions) error {
	return h.p.Forward(ctx, n, f, o)
}

// New returns the Oracle provider. Registration happens in init, so importing
// this package is enough; New exists for tests that want one directly.
func New() Provider { return Provider{} }

func (Provider) Cloud() string { return inventory.CloudOCI }

// Detect reports whether Oracle is usable here.
//
// Like the Amazon check this is a local filesystem question only. Detection
// runs on every invocation, including for clouds the user has never touched,
// so it must not make a network call. Whether the credentials in that file
// still work is a question for the diagnosis, which can name the fix - and on
// OCI that fix is usually a session token that expired an hour ago.
func (Provider) Detect(context.Context) (bool, string) {
	if Configured() {
		return true, ""
	}
	return false, "no OCI config found; run oci setup config to create ~/.oci/config"
}

// Inventory sweeps every selected profile and region.
func (p Provider) Inventory(ctx context.Context, o provider.Options) ([]inventory.Node, error) {
	profiles, err := selectedProfiles(o)
	if err != nil {
		return nil, err
	}

	var (
		mu    sync.Mutex
		nodes []inventory.Node
		fatal []string
	)
	for _, prof := range profiles {
		got, err := p.sweepProfile(ctx, prof, o)
		mu.Lock()
		if err != nil {
			fatal = append(fatal, fmt.Sprintf("%s: %v", prof, err))
		}
		nodes = append(nodes, got...)
		mu.Unlock()
	}

	// A profile that cannot authenticate must not empty the whole sweep: one
	// expired session token is normal on an estate with several tenancies,
	// and the machines the other profiles can see are still worth showing.
	if len(nodes) == 0 && len(fatal) > 0 {
		return nil, fmt.Errorf("no OCI profile could be used: %s", joinLines(fatal))
	}
	for _, f := range fatal {
		o.Skipped("oci %s", f)
	}
	return nodes, nil
}

// selectedProfiles resolves which profiles this sweep covers.
func selectedProfiles(o provider.Options) ([]string, error) {
	if !o.AllAccounts {
		if o.Account != "" {
			return []string{o.Account}, nil
		}
		return []string{DefaultProfile}, nil
	}
	all, err := Profiles(ConfigPath())
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", ConfigPath(), err)
	}
	var names []string
	for _, p := range all {
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s declares no profiles", ConfigPath())
	}
	return names, nil
}

// sweepProfile lists every machine one profile can see, across every region
// its tenancy has turned on.
func (p Provider) sweepProfile(ctx context.Context, profile string, o provider.Options) ([]inventory.Node, error) {
	cp, err := credentials(profile)
	if err != nil {
		return nil, err
	}
	tenancy, err := cp.TenancyOCID()
	if err != nil {
		return nil, fmt.Errorf("no tenancy in profile %s: %w", profile, err)
	}

	home, err := dial(cp, regionOf(profile))
	if err != nil {
		return nil, err
	}

	// Compartments and region subscriptions are tenancy-wide, so they are
	// asked once rather than once per region.
	comps, err := compartments(ctx, home.identity, tenancy)
	if err != nil {
		return nil, credentialProblem(profile, err)
	}
	ids := compartmentIDs(tenancy, comps)
	names := compartmentNames(tenancy, comps)

	regions := []string{o.Region}
	if o.Region == "" {
		regions, err = subscribedRegions(ctx, home.identity, tenancy)
		if err != nil {
			return nil, credentialProblem(profile, err)
		}
	}

	var (
		mu    sync.Mutex
		nodes []inventory.Node
		wg    sync.WaitGroup
	)
	for _, r := range regions {
		region := r
		wg.Add(1)
		go func() {
			defer wg.Done()
			o.Report("oci %s %s", profile, region)
			got, err := sweepRegion(ctx, cp, profile, region, tenancy, ids, names, o)
			if err != nil {
				o.Skipped("oci %s/%s: %v", profile, region, err)
				return
			}
			mu.Lock()
			nodes = append(nodes, got...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return nodes, nil
}

// sweepRegion builds every node in one region.
func sweepRegion(
	ctx context.Context,
	cp common.ConfigurationProvider,
	profile, region, tenancy string,
	compartmentIDs []string,
	compartmentNames map[string]string,
	o provider.Options,
) ([]inventory.Node, error) {
	c, err := dial(cp, region)
	if err != nil {
		return nil, err
	}

	// The network picture first: which subnet sits in which VCN, and which
	// VCNs have a bastion. Reachability is decided entirely by these.
	subnets, subnetLists := subnetVCNs(ctx, c.network, compartmentIDs)
	vcnNames := vcnNames(ctx, c.network, compartmentIDs)
	byVCN := gateways(ctx, c.bastion, compartmentIDs)
	withAllowLists(ctx, c.bastion, byVCN)
	addr := addresses(ctx, c.network, attachments(ctx, c.compute, compartmentIDs), o.Concurrency)
	images := newImageCache(c.compute)

	// The rules that decide whether a bastion can reach a machine: a call per
	// compartment for the security lists, and one per security group that
	// is actually attached to a machine.
	groups := map[string]bool{}
	for _, a := range addr {
		for _, id := range a.nsgIDs {
			groups[id] = true
		}
	}
	w := network{
		subnetLists: subnetLists,
		lists:       securityLists(ctx, c.network, compartmentIDs),
		nsgs:        nsgRules(ctx, c.network, groups),
	}

	var nodes []inventory.Node
	for _, id := range compartmentIDs {
		cid := id
		var page *string
		for {
			resp, err := c.compute.ListInstances(ctx,
				core.ListInstancesRequest{CompartmentId: &cid, Page: page})
			if err != nil {
				break // a compartment we may not read is not a region failure
			}
			for _, in := range resp.Items {
				nodes = append(nodes, buildNode(ctx, in, profile, region,
					compartmentNames[cid], subnets, vcnNames, byVCN, addr, images, w))
			}
			if resp.OpcNextPage == nil {
				break
			}
			page = resp.OpcNextPage
		}
	}
	managedRoutes(ctx, c.plugins, nodes, o.Concurrency)
	return nodes, nil
}

// buildNode turns one Oracle instance into a node the rest of the tool can
// treat exactly like any other machine.
func buildNode(
	ctx context.Context,
	in core.Instance,
	profile, region, compartment string,
	subnets map[string]string,
	vcnNames map[string]string,
	byVCN map[string][]bastionSummary,
	addr map[string]addressing,
	images *imageCache,
	w network,
) inventory.Node {
	id := deref(in.Id)
	a := addr[id]
	vcn := subnets[a.subnetID]
	gws := byVCN[vcn]

	n := inventory.Node{
		Cloud:     inventory.CloudOCI,
		ID:        id,
		Name:      deref(in.DisplayName),
		Account:   profile,
		Region:    region,
		PrivateIP: a.privateIP,
		PublicIP:  a.publicIP,
		Hostname:  a.hostname,
		State:     string(in.LifecycleState),
		Tags:      tagsOf(in.FreeformTags, in.DefinedTags),
		Detail:    map[string]string{},
	}
	if compartment != "" {
		n.Detail["compartment"] = compartment
	}
	if a.subnetID != "" {
		n.Detail["subnet"] = a.subnetID
	}
	if vcn != "" {
		n.Detail["vcn"] = vcn
		if name := vcnNames[vcn]; name != "" {
			n.Detail["vcn_name"] = name
		}
	}
	if s := deref(in.Shape); s != "" {
		n.Detail["shape"] = s
		n.Arch = archOf(s)
	}
	// A VCN may hold several bastions and they are not interchangeable: one
	// may admit your address but be unable to reach the machine, while
	// another can reach it but not admit you. Each is judged against its
	// allowlist and the rules between it and the machine, best first, and a
	// connection walks the list in that order.
	doors := judge(w, activeGateways(gws), a)
	if len(doors) > 0 {
		usable := make([]bastionSummary, 0, len(doors))
		for _, d := range doors {
			usable = append(usable, d.g)
		}
		n.Detail["bastion"] = usable[0].id
		n.Detail["bastion_name"] = usable[0].name
		n.Detail["bastion_state"] = usable[0].state
		n.Detail["reach"], n.Detail["reach_why"] = reachOf(doors)
		var ids, names, allows []string
		for _, g := range usable {
			ids = append(ids, g.id)
			names = append(names, g.name)
			allows = append(allows, strings.Join(g.allow, ","))
		}
		if len(usable) > 1 {
			n.Detail["bastions"] = strings.Join(ids, ",")
			n.Detail["bastion_names"] = strings.Join(names, ",")
			// Semicolons here: the blocks within one allowlist are already
			// comma separated.
			n.Detail["bastion_allows"] = strings.Join(allows, ";")
		}
		if allows[0] != "" {
			n.Detail["bastion_allow"] = allows[0]
		}
	}
	// Only running machines are worth an image lookup: the login account
	// matters when connecting, and you cannot connect to a stopped box.
	if in.LifecycleState == core.InstanceLifecycleStateRunning {
		if os := images.osFor(ctx, imageID(in)); os != "" {
			n.Detail["os"] = os
			n.OS = os
			if u := LoginUser(os); u != "" {
				n.Detail["login_user"] = u
			}
		}
	}

	classify(&n, len(doors) > 0, vcnNames[vcn])
	blockIfNoDoor(&n)
	if !n.Down && bastionPluginWanted(in) {
		// Whether the plugin is actually running takes a call per machine,
		// so it is only asked where someone turned it on. The answer is
		// filled in by managedRoutes once the sweep has every machine.
		n.Detail["bastion_plugin"] = "enabled"
		n.Detail["compartment_id"] = deref(in.CompartmentId)
	}
	return n
}

// bastionPluginWanted reports whether the Bastion plugin is switched on for a
// machine. It is off unless someone enabled it, since Oracle ships it off.
func bastionPluginWanted(in core.Instance) bool {
	if in.AgentConfig == nil || (in.AgentConfig.AreAllPluginsDisabled != nil && *in.AgentConfig.AreAllPluginsDisabled) {
		return false
	}
	for _, pc := range in.AgentConfig.PluginsConfig {
		if deref(pc.Name) == "Bastion" {
			return pc.DesiredState == core.InstanceAgentPluginConfigDetailsDesiredStateEnabled
		}
	}
	return false
}

// activeGateways returns the bastions on this VCN that are actually up, in a
// stable order. A bastion being created or deleted is not a door yet, and an
// unsorted list would make which one gets tried first depend on the order
// compartments happened to answer in.
func activeGateways(gws []bastionSummary) []bastionSummary {
	var out []bastionSummary
	for _, g := range gws {
		if g.state == string(bastion.BastionLifecycleStateActive) {
			out = append(out, g)
		}
	}
	// Try the doors that will certainly let us in first.
	//
	// A bastion open to every address always authenticates, so any failure
	// through it is about reaching the machine - which is the useful answer.
	// One with a restricted allowlist may simply refuse this laptop, and
	// refuses identically to a key that has not propagated yet, so it cannot
	// be told apart without spending the whole propagation wait. Ordering
	// costs nothing and spends that wait only when it has to.
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := admitsAnyone(out[i]), admitsAnyone(out[j]); a != b {
			return a
		}
		return out[i].name < out[j].name
	})
	return out
}

// admitsAnyone reports whether a bastion's client allowlist excludes nobody.
// An unset list is the service's own default, which is also open.
func admitsAnyone(g bastionSummary) bool {
	if len(g.allow) == 0 {
		return true
	}
	for _, c := range g.allow {
		if strings.TrimSpace(c) == "0.0.0.0/0" {
			return true
		}
	}
	return false
}

// imageID digs the image out of an instance's source details, which the SDK
// models as an interface because a machine may boot from a volume instead.
func imageID(in core.Instance) string {
	if src, ok := in.SourceDetails.(core.InstanceSourceViaImageDetails); ok {
		return deref(src.ImageId)
	}
	return ""
}

// compartmentNames maps a compartment to something a human recognises, since
// every other identifier here is an OCID.
func compartmentNames(tenancy string, comps []identity.Compartment) map[string]string {
	out := map[string]string{tenancy: "root"}
	for _, c := range comps {
		if c.Id != nil && c.Name != nil {
			out[*c.Id] = *c.Name
		}
	}
	return out
}

func joinLines(s []string) string {
	out := ""
	for i, l := range s {
		if i > 0 {
			out += "; "
		}
		out += l
	}
	return out
}

// sortNodes gives a stable order, since regions finish in whatever order they
// finish in.
func sortNodes(nodes []inventory.Node) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Region != nodes[j].Region {
			return nodes[i].Region < nodes[j].Region
		}
		return nodes[i].DisplayName() < nodes[j].DisplayName()
	})
}

// archOf reads a shape's processor family. OCI's Arm shapes are the Ampere
// standard shapes, VM.Standard.A1.Flex and its successors; everything else
// on offer is x86. GPU shapes such as VM.GPU.A10 name the GPU, not the CPU,
// which is why the match is on the Standard family only.
func archOf(shape string) string {
	if armShape.MatchString(shape) {
		return "arm64"
	}
	return "amd64"
}

var armShape = regexp.MustCompile(`^(VM|BM)\.Standard\.A\d+\.`)
