package oci

import (
	"context"

	"github.com/oracle/oci-go-sdk/v65/bastion"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/computeinstanceagent"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// The SDK's clients are concrete structs carrying hundreds of methods. These
// interfaces name only what the sweep calls, so a test can supply a fake
// without implementing a cloud.

type identityAPI interface {
	ListCompartments(context.Context, identity.ListCompartmentsRequest) (identity.ListCompartmentsResponse, error)
	ListRegionSubscriptions(context.Context, identity.ListRegionSubscriptionsRequest) (identity.ListRegionSubscriptionsResponse, error)
}

type computeAPI interface {
	ListInstances(context.Context, core.ListInstancesRequest) (core.ListInstancesResponse, error)
	ListVnicAttachments(context.Context, core.ListVnicAttachmentsRequest) (core.ListVnicAttachmentsResponse, error)
	GetImage(context.Context, core.GetImageRequest) (core.GetImageResponse, error)
}

type networkAPI interface {
	ListSubnets(context.Context, core.ListSubnetsRequest) (core.ListSubnetsResponse, error)
	ListVcns(context.Context, core.ListVcnsRequest) (core.ListVcnsResponse, error)
	GetVnic(context.Context, core.GetVnicRequest) (core.GetVnicResponse, error)
	ListSecurityLists(context.Context, core.ListSecurityListsRequest) (core.ListSecurityListsResponse, error)
	ListNetworkSecurityGroupSecurityRules(context.Context, core.ListNetworkSecurityGroupSecurityRulesRequest) (core.ListNetworkSecurityGroupSecurityRulesResponse, error)
}

type bastionAPI interface {
	ListBastions(context.Context, bastion.ListBastionsRequest) (bastion.ListBastionsResponse, error)
	GetBastion(context.Context, bastion.GetBastionRequest) (bastion.GetBastionResponse, error)
	CreateSession(context.Context, bastion.CreateSessionRequest) (bastion.CreateSessionResponse, error)
	GetSession(context.Context, bastion.GetSessionRequest) (bastion.GetSessionResponse, error)
	DeleteSession(context.Context, bastion.DeleteSessionRequest) (bastion.DeleteSessionResponse, error)
	ListSessions(context.Context, bastion.ListSessionsRequest) (bastion.ListSessionsResponse, error)
}

type pluginAPI interface {
	GetInstanceAgentPlugin(context.Context, computeinstanceagent.GetInstanceAgentPluginRequest) (computeinstanceagent.GetInstanceAgentPluginResponse, error)
}

// clients is one region's worth of API access.
//
// They are built once per region and shared across every call in that region.
// Building one per call is what made the Amazon sweep slow: the cost is the
// TLS handshake, not the request.
type clients struct {
	identity identityAPI
	compute  computeAPI
	network  networkAPI
	bastion  bastionAPI
	plugins  pluginAPI
}

// dial builds the clients for one region.
func dial(cp common.ConfigurationProvider, region string) (*clients, error) {
	idc, err := identity.NewIdentityClientWithConfigurationProvider(cp)
	if err != nil {
		return nil, err
	}
	cc, err := core.NewComputeClientWithConfigurationProvider(cp)
	if err != nil {
		return nil, err
	}
	nc, err := core.NewVirtualNetworkClientWithConfigurationProvider(cp)
	if err != nil {
		return nil, err
	}
	bc, err := bastion.NewBastionClientWithConfigurationProvider(cp)
	if err != nil {
		return nil, err
	}
	pc, err := computeinstanceagent.NewPluginClientWithConfigurationProvider(cp)
	if err != nil {
		return nil, err
	}
	if region != "" {
		idc.SetRegion(region)
		cc.SetRegion(region)
		nc.SetRegion(region)
		bc.SetRegion(region)
		pc.SetRegion(region)
	}
	return &clients{identity: &idc, compute: &cc, network: &nc, bastion: &bc, plugins: &pc}, nil
}

// deref reads a pointer the SDK may leave nil. Every field on every OCI
// response is a pointer, so this is the difference between a sweep and a
// panic.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
