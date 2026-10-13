package azure

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/hybridcompute/armhybridcompute/v3"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/session"
)

// Provider reaches machines through Azure Bastion.
type Provider struct{}

// Compile-time proof that this satisfies the seam.
var _ provider.Provider = Provider{}

// Handler opens sessions through Azure Bastion's native client. Its routes always belong to
// the node's own cloud, so it reads the node's own Detail as before.
type Handler struct{ p Provider }

var _ provider.Handler = Handler{}

func init() {
	provider.Register(Provider{})
	provider.RegisterHandler(Handler{})
}

// Kind is the route kind this handler serves.
func (Handler) Kind() string { return inventory.RouteAzureBastion }

// Shell opens an interactive session, or runs one command.
func (h Handler) Shell(ctx context.Context, n inventory.Node, _ inventory.Route, o provider.SessionOptions) error {
	return h.p.Shell(ctx, n, o)
}

// Forward opens a tunnel.
func (h Handler) Forward(ctx context.Context, n inventory.Node, _ inventory.Route, f session.Forward, o provider.SessionOptions) error {
	return h.p.Forward(ctx, n, f, o)
}

// New returns the Azure provider. Registration happens in init, so importing
// this package is enough; New exists for tests that want one directly.
func New() Provider { return Provider{} }

func (Provider) Cloud() string { return inventory.CloudAzure }

// Detect reports whether Azure is usable here, from local state only.
func (Provider) Detect(context.Context) (bool, string) {
	if Configured() {
		return true, ""
	}
	return false, "not signed in to Azure; run az login"
}

// Inventory sweeps every selected subscription.
func (p Provider) Inventory(ctx context.Context, o provider.Options) ([]inventory.Node, error) {
	subs, err := selected(o)
	if err != nil {
		return nil, err
	}
	cred, err := credential()
	if err != nil {
		return nil, credentialProblem(err)
	}

	var (
		mu    sync.Mutex
		nodes []inventory.Node
		fatal []string
		wg    sync.WaitGroup
	)
	for _, s := range subs {
		sub := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			o.Report("azure %s", sub.Name)
			got, err := sweep(ctx, cred, sub, o)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fatal = append(fatal, fmt.Sprintf("%s: %v", sub.Name, credentialProblem(err)))
				return
			}
			nodes = append(nodes, got...)
		}()
	}
	wg.Wait()

	// One unusable subscription must not empty the sweep: an estate with
	// several routinely has one the sign-in cannot see.
	if len(nodes) == 0 && len(fatal) > 0 {
		return nil, fmt.Errorf("no Azure subscription could be read: %s", strings.Join(fatal, "; "))
	}
	for _, f := range fatal {
		o.Skipped("azure %s", f)
	}
	return nodes, nil
}

// selected resolves which subscriptions this sweep covers.
func selected(o provider.Options) ([]Subscription, error) {
	if o.AllAccounts {
		subs, err := Subscriptions()
		if err != nil {
			return nil, err
		}
		if len(subs) == 0 {
			return nil, fmt.Errorf("no enabled subscriptions; run: az login")
		}
		return subs, nil
	}
	s, err := resolve(o.Account)
	if err != nil {
		return nil, err
	}
	return []Subscription{s}, nil
}

// sweep lists every machine in one subscription.
//
// Four listings, each covering the whole subscription rather than one resource
// group at a time, because the join needs all of them and the cost here is the
// round trip. The vendor CLI's own `az vm list -d` does this per machine and
// takes over two minutes on the estate this was built against.
func sweep(ctx context.Context, cred azcore.TokenCredential, sub Subscription, o provider.Options) ([]inventory.Node, error) {
	vmClient, err := armcompute.NewVirtualMachinesClient(sub.ID, cred, nil)
	if err != nil {
		return nil, err
	}
	nicClient, err := armnetwork.NewInterfacesClient(sub.ID, cred, nil)
	if err != nil {
		return nil, err
	}
	bastionClient, err := armnetwork.NewBastionHostsClient(sub.ID, cred, nil)
	if err != nil {
		return nil, err
	}
	vnetClient, err := armnetwork.NewVirtualNetworksClient(sub.ID, cred, nil)
	if err != nil {
		return nil, err
	}
	arcClient, err := armhybridcompute.NewMachinesClient(sub.ID, cred, nil)
	if err != nil {
		return nil, err
	}

	var (
		machines []*armcompute.VirtualMachine
		states   map[string]string
		nics     map[string]nic
		byVNet   map[string][]gateway
		peers    map[string][]string
		arc      []*armhybridcompute.Machine
		errs     []error
		mu       sync.Mutex
		wg       sync.WaitGroup
	)
	record := func(err error) {
		if err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}
	}

	wg.Add(6)
	go func() { defer wg.Done(); var e error; arc, e = listArcMachines(ctx, arcClient); record(e) }()
	go func() { defer wg.Done(); var e error; machines, e = listVMs(ctx, vmClient, false); record(e) }()
	go func() { defer wg.Done(); var e error; states, e = listPowerStates(ctx, vmClient); record(e) }()
	go func() { defer wg.Done(); var e error; nics, e = listNICs(ctx, nicClient); record(e) }()
	go func() { defer wg.Done(); var e error; byVNet, e = listBastions(ctx, bastionClient); record(e) }()
	go func() { defer wg.Done(); var e error; peers, e = listPeerings(ctx, vnetClient); record(e) }()
	wg.Wait()
	byVNet = reachThroughPeering(byVNet, peers)

	// The machine list is the only one a sweep cannot do without. Missing
	// bastions or power states degrade the answer; missing machines mean
	// there is no answer.
	if len(machines) == 0 && len(arc) == 0 && len(errs) > 0 {
		return nil, errs[0]
	}
	for _, e := range errs {
		o.Skipped("azure %s: %v", sub.Name, e)
	}

	out := make([]inventory.Node, 0, len(machines)+len(arc))
	for _, vm := range machines {
		out = append(out, buildNode(vm, sub, states, nics, byVNet))
	}
	// Arc machines live elsewhere; their records exist to carry Arc as a
	// way in, and are linked to the machines' own records on load.
	for _, m := range arc {
		if m != nil {
			out = append(out, buildArcNode(m, sub))
		}
	}
	return out, nil
}

// buildNode turns one virtual machine into a node the rest of the tool treats
// exactly like any other machine.
func buildNode(
	vm *armcompute.VirtualMachine,
	sub Subscription,
	states map[string]string,
	nics map[string]nic,
	byVNet map[string][]gateway,
) inventory.Node {
	id := deref(vm.ID)
	n := inventory.Node{
		Cloud:   inventory.CloudAzure,
		ID:      id,
		Name:    deref(vm.Name),
		Account: sub.Name,
		Region:  deref(vm.Location),
		State:   states[strings.ToLower(id)],
		Tags:    tagsOf(vm.Tags),
		Detail:  map[string]string{},
	}
	if g := groupOf(id); g != "" {
		n.Detail["resource_group"] = g
	}
	if vm.Properties != nil && vm.Properties.HardwareProfile != nil && vm.Properties.HardwareProfile.VMSize != nil {
		n.Detail["size"] = string(*vm.Properties.HardwareProfile.VMSize)
	}
	// The VM ID is the identifier a machine's own metadata service reports,
	// so it is what an agent registered from inside the machine is most
	// likely to have recorded.
	if vm.Properties != nil {
		if id := deref(vm.Properties.VMID); id != "" {
			n.Aliases = append(n.Aliases, id)
		}
		if vm.Properties.OSProfile != nil {
			n.Hostname = deref(vm.Properties.OSProfile.ComputerName)
		}
	}

	var vnet, netKey string
	if vm.Properties != nil && vm.Properties.NetworkProfile != nil {
		for _, ref := range vm.Properties.NetworkProfile.NetworkInterfaces {
			if ref == nil {
				continue
			}
			got, ok := nics[strings.ToLower(deref(ref.ID))]
			if !ok {
				continue
			}
			// A machine may carry several interfaces; the primary one is the
			// address a bastion session is pointed at.
			if n.PrivateIP == "" || got.primary {
				n.PrivateIP = got.privateIP
				n.PublicIP = got.publicIP
				vnet = vnetOf(got.subnetID)
				netKey = vnetKey(got.subnetID)
			}
			if got.primary {
				break
			}
		}
	}
	if vnet != "" {
		n.Detail["vnet"] = vnet
	}
	if u := adminUser(vm); u != "" {
		n.Detail["login_user"] = u
	}

	gws := byVNet[netKey]
	if len(gws) > 0 {
		n.Detail["bastion"] = gws[0].name
		n.Detail["bastion_group"] = gws[0].group
		n.Detail["bastion_sku"] = gws[0].sku
		n.Detail["bastion_tunnel"] = fmt.Sprint(gws[0].tunnel)
		if gws[0].peered {
			// Said because it is not obvious: a bastion in another network is
			// carrying this, and if that peering is removed this machine
			// becomes unreachable with nothing about the machine changed.
			n.Detail["bastion_via"] = gws[0].vnet
		}
	}
	classify(&n, gws, vnet)
	return n
}

// adminUser reports the account Azure created the machine with.
//
// Unlike every other cloud here, Azure records this: the machine was created
// with an administrator username and the API still carries it. So the guessing
// the Oracle provider has to do is unnecessary, and --user is only for the
// cases where someone has since made another account.
func adminUser(vm *armcompute.VirtualMachine) string {
	if vm.Properties == nil || vm.Properties.OSProfile == nil {
		return ""
	}
	return deref(vm.Properties.OSProfile.AdminUsername)
}

// listVMs pages through every machine in the subscription.
func listVMs(ctx context.Context, c *armcompute.VirtualMachinesClient, statusOnly bool) ([]*armcompute.VirtualMachine, error) {
	opts := &armcompute.VirtualMachinesClientListAllOptions{}
	if statusOnly {
		opts.StatusOnly = to.Ptr("true")
	}
	var out []*armcompute.VirtualMachine
	pager := c.NewListAllPager(opts)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return out, err
		}
		out = append(out, page.Value...)
	}
	return out, nil
}

// listPowerStates asks for every machine's running state in one pass.
//
// statusOnly is a separate listing rather than an expansion of the first
// because Azure only allows the instance view to be expanded alongside a
// filter. Two whole-subscription calls are still far cheaper than one per
// machine.
func listPowerStates(ctx context.Context, c *armcompute.VirtualMachinesClient) (map[string]string, error) {
	vms, err := listVMs(ctx, c, true)
	out := make(map[string]string, len(vms))
	for _, vm := range vms {
		if vm == nil || vm.Properties == nil || vm.Properties.InstanceView == nil {
			continue
		}
		for _, st := range vm.Properties.InstanceView.Statuses {
			if st == nil || st.Code == nil {
				continue
			}
			if code := *st.Code; strings.HasPrefix(code, "PowerState/") {
				out[strings.ToLower(deref(vm.ID))] = strings.TrimPrefix(code, "PowerState/")
			}
		}
	}
	return out, err
}

// nic is the part of a network interface the join needs.
type nic struct {
	privateIP string
	publicIP  string
	subnetID  string
	primary   bool
}

// listNICs pages through every interface in the subscription at once, rather
// than fetching one per machine.
func listNICs(ctx context.Context, c *armnetwork.InterfacesClient) (map[string]nic, error) {
	out := map[string]nic{}
	pager := c.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return out, err
		}
		for _, in := range page.Value {
			if in == nil || in.Properties == nil {
				continue
			}
			got := nic{}
			if in.Properties.Primary != nil {
				got.primary = *in.Properties.Primary
			}
			for _, cfg := range in.Properties.IPConfigurations {
				if cfg == nil || cfg.Properties == nil {
					continue
				}
				got.privateIP = deref(cfg.Properties.PrivateIPAddress)
				if cfg.Properties.Subnet != nil {
					got.subnetID = deref(cfg.Properties.Subnet.ID)
				}
				if pip := cfg.Properties.PublicIPAddress; pip != nil && pip.Properties != nil {
					got.publicIP = deref(pip.Properties.IPAddress)
				}
				break
			}
			out[strings.ToLower(deref(in.ID))] = got
		}
	}
	return out, nil
}

// listPeerings maps every network to the networks it is peered with.
//
// Each network carries its own peerings, so one listing of networks answers
// the whole question with no call per network. Only Connected peerings are
// kept: one that is Initiated or Disconnected carries no traffic.
func listPeerings(ctx context.Context, c *armnetwork.VirtualNetworksClient) (map[string][]string, error) {
	out := map[string][]string{}
	pager := c.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return out, err
		}
		for _, v := range page.Value {
			if v == nil || v.Properties == nil {
				continue
			}
			from := vnetKey(deref(v.ID))
			if from == "" {
				continue
			}
			for _, pr := range v.Properties.VirtualNetworkPeerings {
				if pr == nil || pr.Properties == nil || pr.Properties.RemoteVirtualNetwork == nil {
					continue
				}
				if pr.Properties.PeeringState == nil ||
					*pr.Properties.PeeringState != armnetwork.VirtualNetworkPeeringStateConnected {
					continue
				}
				if to := vnetKey(deref(pr.Properties.RemoteVirtualNetwork.ID)); to != "" {
					out[from] = append(out[from], to)
				}
			}
		}
	}
	return out, nil
}

// listBastions pages through every bastion and indexes them by network.
func listBastions(ctx context.Context, c *armnetwork.BastionHostsClient) (map[string][]gateway, error) {
	var hosts []*armnetwork.BastionHost
	pager := c.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return gatewaysByVNet(hosts), err
		}
		hosts = append(hosts, page.Value...)
	}
	return gatewaysByVNet(hosts), nil
}
