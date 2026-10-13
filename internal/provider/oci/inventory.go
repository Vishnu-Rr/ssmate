package oci

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/oracle/oci-go-sdk/v65/bastion"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// compartments lists every compartment the credentials can see, plus the
// tenancy itself, which holds resources but is not returned as a compartment.
func compartments(ctx context.Context, c identityAPI, tenancy string) ([]identity.Compartment, error) {
	var out []identity.Compartment
	var page *string
	for {
		resp, err := c.ListCompartments(ctx, identity.ListCompartmentsRequest{
			CompartmentId:          &tenancy,
			CompartmentIdInSubtree: common.Bool(true),
			AccessLevel:            identity.ListCompartmentsAccessLevelAccessible,
			Page:                   page,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, resp.Items...)
		if resp.OpcNextPage == nil {
			return out, nil
		}
		page = resp.OpcNextPage
	}
}

// compartmentIDs reduces compartments to the ones worth querying. A deleted
// compartment answers every list call with an error, so including one turns a
// clean sweep into a page of noise.
func compartmentIDs(tenancy string, comps []identity.Compartment) []string {
	ids := []string{tenancy}
	for _, c := range comps {
		if c.LifecycleState == identity.CompartmentLifecycleStateActive && c.Id != nil {
			ids = append(ids, *c.Id)
		}
	}
	return ids
}

// subscribedRegions lists the regions a tenancy has turned on. Sweeping the
// rest would be one failed call per region for nothing.
func subscribedRegions(ctx context.Context, c identityAPI, tenancy string) ([]string, error) {
	resp, err := c.ListRegionSubscriptions(ctx,
		identity.ListRegionSubscriptionsRequest{TenancyId: &tenancy})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range resp.Items {
		if r.RegionName != nil {
			out = append(out, *r.RegionName)
		}
	}
	sort.Strings(out)
	return out, nil
}

// bastionSummary is the part of a bastion the sweep needs, kept as our own
// type so the SDK's shape does not spread through the provider.
type bastionSummary struct {
	id    string
	name  string
	state string
	vcn   string
	// allow is the set of client addresses this bastion will talk to at all.
	allow []string
	// subnet and endpoint are where the bastion's own traffic starts: its
	// private endpoint's address, in the subnet whose rules it obeys.
	subnet   string
	endpoint string
}

// gateways maps a VCN to the bastions that can reach into it.
//
// This is the whole of OCI reachability. A bastion binds to one VCN when it is
// created and cannot be moved, so whether a machine can be reached is decided
// by whether anyone built a door into its network - not by anything running on
// the machine. A VCN may hold more than one: the estate this was built against
// has two bastions on a single VCN, so this maps to a slice rather than
// assuming the one-per-network the documentation reads as implying.
func gateways(ctx context.Context, c bastionAPI, compartmentIDs []string) map[string][]bastionSummary {
	byVCN := map[string][]bastionSummary{}
	for _, id := range compartmentIDs {
		cid := id
		var page *string
		for {
			resp, err := c.ListBastions(ctx, bastion.ListBastionsRequest{
				CompartmentId: &cid, Page: page,
			})
			if err != nil {
				break // a compartment we may not read is not a sweep failure
			}
			for _, b := range resp.Items {
				if b.TargetVcnId == nil {
					continue
				}
				byVCN[*b.TargetVcnId] = append(byVCN[*b.TargetVcnId], bastionSummary{
					id:    deref(b.Id),
					name:  deref(b.Name),
					state: string(b.LifecycleState),
					vcn:   *b.TargetVcnId,
				})
			}
			if resp.OpcNextPage == nil {
				break
			}
			page = resp.OpcNextPage
		}
	}
	return byVCN
}

// withAllowLists fills in each bastion's client allowlist.
//
// A bastion refuses any address outside this list before authentication, which
// surfaces as "Permission denied (publickey)" and reads exactly like a key
// problem. It is not carried on the list summary, so it costs one call per
// bastion - of which an estate has a handful, capped at five per region.
func withAllowLists(ctx context.Context, c bastionAPI, byVCN map[string][]bastionSummary) {
	for vcn, gws := range byVCN {
		for i := range gws {
			resp, err := c.GetBastion(ctx, bastion.GetBastionRequest{BastionId: &gws[i].id})
			if err != nil {
				continue
			}
			gws[i].allow = resp.Bastion.ClientCidrBlockAllowList
			gws[i].subnet = deref(resp.Bastion.TargetSubnetId)
			gws[i].endpoint = deref(resp.Bastion.PrivateEndpointIpAddress)
		}
		byVCN[vcn] = gws
	}
}

// vcnNames maps a VCN to its display name, so a machine with no bastion can be
// told which network needs one rather than being handed an OCID.
func vcnNames(ctx context.Context, c networkAPI, compartmentIDs []string) map[string]string {
	out := map[string]string{}
	for _, id := range compartmentIDs {
		cid := id
		var page *string
		for {
			resp, err := c.ListVcns(ctx, core.ListVcnsRequest{CompartmentId: &cid, Page: page})
			if err != nil {
				break
			}
			for _, v := range resp.Items {
				if v.Id != nil {
					out[*v.Id] = deref(v.DisplayName)
				}
			}
			if resp.OpcNextPage == nil {
				break
			}
			page = resp.OpcNextPage
		}
	}
	return out
}

// subnetVCNs maps every subnet to the VCN holding it, which is the link
// between a machine and the bastions that can reach it, and to the security
// lists every interface in it obeys.
func subnetVCNs(ctx context.Context, c networkAPI, compartmentIDs []string) (map[string]string, map[string][]string) {
	out := map[string]string{}
	lists := map[string][]string{}
	for _, id := range compartmentIDs {
		cid := id
		var page *string
		for {
			resp, err := c.ListSubnets(ctx, core.ListSubnetsRequest{CompartmentId: &cid, Page: page})
			if err != nil {
				break
			}
			for _, s := range resp.Items {
				if s.Id != nil && s.VcnId != nil {
					out[*s.Id] = *s.VcnId
					lists[*s.Id] = s.SecurityListIds
				}
			}
			if resp.OpcNextPage == nil {
				break
			}
			page = resp.OpcNextPage
		}
	}
	return out, lists
}

// attachment is the part of a VNIC attachment the sweep needs.
type attachment struct {
	instanceID string
	vnicID     string
}

// attachments lists which VNIC belongs to which instance. It is a per
// compartment call rather than a per instance one, which keeps the count in
// single figures no matter how many machines there are.
func attachments(ctx context.Context, c computeAPI, compartmentIDs []string) []attachment {
	var out []attachment
	for _, id := range compartmentIDs {
		cid := id
		var page *string
		for {
			resp, err := c.ListVnicAttachments(ctx,
				core.ListVnicAttachmentsRequest{CompartmentId: &cid, Page: page})
			if err != nil {
				break
			}
			for _, a := range resp.Items {
				if a.LifecycleState != core.VnicAttachmentLifecycleStateAttached {
					continue
				}
				if a.VnicId == nil || a.InstanceId == nil {
					continue
				}
				out = append(out, attachment{instanceID: *a.InstanceId, vnicID: *a.VnicId})
			}
			if resp.OpcNextPage == nil {
				break
			}
			page = resp.OpcNextPage
		}
	}
	return out
}

// addressing is where one machine sits on the network.
type addressing struct {
	privateIP string
	publicIP  string
	subnetID  string
	// nsgIDs are the network security groups on the interface.
	nsgIDs []string
	// hostname is the VNIC's hostname label, which OCI also sets as the
	// machine's own hostname when it is created.
	hostname string
}

// addresses resolves every attachment to an address.
//
// This is the only per-machine call in the sweep, because the private IP lives
// on the VNIC and nowhere else. They run concurrently for the same reason the
// Amazon sweep does: the cost is the round trip.
func addresses(ctx context.Context, c networkAPI, atts []attachment, concurrency int) map[string]addressing {
	if concurrency <= 0 {
		concurrency = 16
	}
	out := map[string]addressing{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	for _, a := range atts {
		att := a
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			resp, err := c.GetVnic(ctx, core.GetVnicRequest{VnicId: &att.vnicID})
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			// A machine may carry several VNICs. Keep the primary, which is
			// the address a bastion session will actually be pointed at.
			if _, seen := out[att.instanceID]; seen && resp.IsPrimary != nil && !*resp.IsPrimary {
				return
			}
			out[att.instanceID] = addressing{
				privateIP: deref(resp.PrivateIp),
				publicIP:  deref(resp.PublicIp),
				subnetID:  deref(resp.SubnetId),
				hostname:  deref(resp.HostnameLabel),
				nsgIDs:    resp.NsgIds,
			}
		}()
	}
	wg.Wait()
	return out
}

// osFor names the operating system family of an image, which is the only
// evidence OCI offers about what a machine's login account is called.
//
// Images are shared across machines, so this caches: an estate of fifty boxes
// is usually three or four images.
type imageCache struct {
	mu   sync.Mutex
	seen map[string]string
	api  computeAPI
}

func newImageCache(api computeAPI) *imageCache {
	return &imageCache{seen: map[string]string{}, api: api}
}

func (c *imageCache) osFor(ctx context.Context, imageID string) string {
	if imageID == "" {
		return ""
	}
	c.mu.Lock()
	if v, ok := c.seen[imageID]; ok {
		c.mu.Unlock()
		return v
	}
	c.mu.Unlock()

	var name string
	if resp, err := c.api.GetImage(ctx, core.GetImageRequest{ImageId: &imageID}); err == nil {
		name = deref(resp.OperatingSystem)
	}
	c.mu.Lock()
	c.seen[imageID] = name
	c.mu.Unlock()
	return name
}

// LoginUser guesses the account name to log in as from an image's operating
// system.
//
// OCI's API carries no login name anywhere, so this is the best evidence
// available, and it is still a guess: an image can be rebuilt with any account
// the builder likes. Whatever this returns must be overridable, which is what
// --user is for. An empty return means we genuinely do not know, and the
// caller should say so rather than trying one and failing obscurely.
func LoginUser(operatingSystem string) string {
	switch {
	case strings.Contains(strings.ToLower(operatingSystem), "ubuntu"):
		return "ubuntu"
	case strings.Contains(strings.ToLower(operatingSystem), "oracle linux"),
		strings.Contains(strings.ToLower(operatingSystem), "centos"):
		return "opc"
	default:
		return ""
	}
}

// classify decides whether a machine can be reached, and says why not.
//
// This is the one place Oracle's rule is written down. It differs from
// Amazon's in kind, not degree: Amazon asks an agent on the machine whether it
// is alive, while Oracle asks the network whether a door exists. A machine can
// be perfectly healthy here and still unreachable, and the fix is to build a
// bastion rather than to touch the machine.
func classify(n *inventory.Node, hasGateway bool, vcnName string) {
	r := inventory.Route{Kind: inventory.RouteOCIForward, Target: n.ID, Account: n.Account, Region: n.Region}
	switch n.State {
	case string(core.InstanceLifecycleStateRunning):
		if hasGateway {
			r.Ready, r.Status = true, inventory.StatusOnline
		} else {
			r.Status = inventory.StatusNoGateway
			if vcnName != "" {
				r.Hint = fmt.Sprintf("no bastion in %s; create one bound to that VCN", vcnName)
			} else {
				r.Hint = "no bastion reaches this machine's network; create one bound to its VCN"
			}
		}
	case string(core.InstanceLifecycleStateStopped), string(core.InstanceLifecycleStateStopping):
		n.Down, n.Status, n.Hint = true, inventory.StatusStopped, "start the machine"
	case string(core.InstanceLifecycleStateTerminated), string(core.InstanceLifecycleStateTerminating):
		n.Down, n.Status = true, inventory.StatusTerminated
	case "":
		n.Down, n.Status = true, inventory.StatusUnknown
	default:
		n.Down, n.Status = true, strings.ToLower(n.State)
	}
	// A machine that is not running keeps its route, carrying its state, so
	// a cache older than its last start does not stop anyone trying.
	if n.Down {
		r.Status, r.Hint = n.Status, n.Hint
	}
	n.AddRoute(r)
	inventory.Summarize(n, inventory.DefaultRouteOrder)
}

// tagsOf flattens both tag systems into one map.
//
// OCI has freeform tags, which are a flat map, and defined tags, which are
// namespaced. Users search across both without caring which is which, so they
// arrive here as one namespace.key space.
func tagsOf(freeform map[string]string, defined map[string]map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range freeform {
		out[k] = v
	}
	for ns, kv := range defined {
		for k, v := range kv {
			if s, ok := v.(string); ok {
				out[ns+"."+k] = s
			} else if v != nil {
				out[ns+"."+k] = fmt.Sprint(v)
			}
		}
	}
	return out
}
