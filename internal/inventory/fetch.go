package inventory

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// SSMAPI is the part of Systems Manager the fetch reads.
type SSMAPI interface {
	ssm.DescribeInstanceInformationAPIClient
	ListTagsForResource(ctx context.Context, in *ssm.ListTagsForResourceInput, opts ...func(*ssm.Options)) (*ssm.ListTagsForResourceOutput, error)
}

// FetchRegion lists every node visible in one profile and region.
//
// It reads both sources and joins them on instance ID, because neither is
// complete on its own. DescribeInstanceInformation omits stopped and
// terminated instances entirely, so without the EC2 side a stopped box and a
// box that never registered with SSM are indistinguishable — and telling
// those two apart is most of what makes a useful diagnostic. Conversely,
// hybrid and non-EC2 nodes appear only on the SSM side.
func FetchRegion(
	ctx context.Context,
	sc SSMAPI,
	ec ec2.DescribeInstancesAPIClient,
	profile, region string,
) ([]Node, error) {
	byID := map[string]*Node{}
	var outside []*Node

	ssmPager := ssm.NewDescribeInstanceInformationPaginator(sc, &ssm.DescribeInstanceInformationInput{
		MaxResults: aws.Int32(50),
	})
	for ssmPager.HasMorePages() {
		page, err := ssmPager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describe managed nodes in %s: %w", region, err)
		}
		for _, info := range page.InstanceInformationList {
			id := aws.ToString(info.InstanceId)
			if id == "" {
				continue
			}
			ping := string(info.PingStatus)
			n := &Node{
				Cloud:     CloudAWS,
				ID:        id,
				Account:   profile,
				Region:    region,
				PrivateIP: aws.ToString(info.IPAddress),
				Name:      aws.ToString(info.ComputerName),
				Agent: Agent{
					Present:  true,
					Online:   ping == "Online",
					Version:  aws.ToString(info.AgentVersion),
					Latest:   aws.ToBool(info.IsLatestVersion),
					LastSeen: aws.ToTime(info.LastPingDateTime),
				},
				Detail: map[string]string{
					"ping":          ping,
					"platform_type": string(info.PlatformType),
					"platform_name": aws.ToString(info.PlatformName),
				},
			}
			// A managed instance is a machine outside EC2 that registered
			// through a hybrid activation: it lives somewhere else, and this
			// record is a way into it.
			n.OS = aws.ToString(info.PlatformName)
			if info.ResourceType == ssmtypes.ResourceTypeManagedInstance {
				n.Hostname = aws.ToString(info.ComputerName)
				n.Outside = &Outside{}
				if src := aws.ToString(info.SourceId); src != id && info.SourceType != ssmtypes.SourceTypeAwsSsmManagedinstance {
					n.Outside.SourceID = src
				}
				n.Detail["resource_type"] = string(info.ResourceType)
				outside = append(outside, n)
			}
			byID[id] = n
		}
	}

	ec2Pager := ec2.NewDescribeInstancesPaginator(ec, &ec2.DescribeInstancesInput{})
	for ec2Pager.HasMorePages() {
		page, err := ec2Pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("describe instances in %s: %w", region, err)
		}
		for _, res := range page.Reservations {
			for _, inst := range res.Instances {
				id := aws.ToString(inst.InstanceId)
				if id == "" {
					continue
				}
				n, ok := byID[id]
				if !ok {
					n = &Node{Cloud: CloudAWS, ID: id, Account: profile, Region: region}
					byID[id] = n
				}
				if inst.State != nil {
					n.State = string(inst.State.Name)
				}
				switch inst.Architecture {
				case "x86_64":
					n.Arch = "amd64"
				case "arm64":
					n.Arch = "arm64"
				}
				if ip := aws.ToString(inst.PrivateIpAddress); ip != "" {
					n.PrivateIP = ip
				}
				n.PublicIP = aws.ToString(inst.PublicIpAddress)
				if n.Detail == nil {
					n.Detail = map[string]string{}
				}
				n.Detail["vpc"] = aws.ToString(inst.VpcId)
				n.Detail["subnet"] = aws.ToString(inst.SubnetId)
				if inst.IamInstanceProfile != nil {
					n.Detail["has_role"] = "true"
				}
				if len(inst.Tags) > 0 {
					n.Tags = make(map[string]string, len(inst.Tags))
					for _, tag := range inst.Tags {
						key := aws.ToString(tag.Key)
						val := aws.ToString(tag.Value)
						n.Tags[key] = val
						if key == "Name" && val != "" {
							n.Name = val
						}
					}
				}
			}
		}
	}

	// Tags are not in the listing. They are read only for registered
	// machines, which are few, because the ssmate:source tag on them is the
	// reliable way to say where they live. A failure costs that hint and
	// nothing else.
	for _, n := range outside {
		tags, err := sc.ListTagsForResource(ctx, &ssm.ListTagsForResourceInput{
			ResourceType: ssmtypes.ResourceTypeForTaggingManagedInstance,
			ResourceId:   aws.String(n.ID),
		})
		if err != nil {
			continue
		}
		for _, t := range tags.TagList {
			key, val := aws.ToString(t.Key), aws.ToString(t.Value)
			if n.Tags == nil {
				n.Tags = map[string]string{}
			}
			n.Tags[key] = val
			switch {
			case key == SourceTag:
				n.Outside.Tag = strings.TrimSpace(val)
			case key == "Name" && val != "":
				n.Name = val
			}
		}
	}

	out := make([]Node, 0, len(byID))
	for _, n := range byID {
		classify(n)
		out = append(out, *n)
	}
	// Sorted so output is stable across runs and diffable in tests.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// classify records Amazon's reachability verdict on a node.
//
// This is the only place the Amazon rule is written down. A node is reachable
// when Systems Manager holds a live heartbeat for it. Everything else is a
// diagnosable condition rather than an error, and the three that matter are
// distinguishable only because the fetch joins both sources: a stopped machine
// and one that never registered look identical from Systems Manager alone, and
// they need opposite fixes.
//
// The verdict is recorded on the Session Manager route, and Summarize turns it
// into the node's own. A machine that is not running is described by its own
// state instead, since no route is the problem there. Its route is still
// recorded, carrying that state, so a cache that is older than the machine's
// last start does not stop anyone trying.
func classify(n *Node) {
	r := Route{Kind: RouteSSM, Target: n.ID, Account: n.Account, Region: n.Region}
	switch {
	case n.Agent.Present && n.Agent.Online:
		r.Ready, r.Status = true, StatusOnline
	case n.Agent.Present:
		r.Status = StatusAgentLost
		r.Hint = "the agent stopped reporting; restart it on the machine"
	default:
		switch n.State {
		case "stopped", "stopping":
			n.Down, n.Status, n.Hint = true, StatusStopped, "start the machine"
		case "terminated", "shutting-down":
			n.Down, n.Status = true, StatusTerminated
		case "":
			n.Down, n.Status = true, StatusUnknown
		default:
			r.Status = StatusUnmanaged
			r.Hint = "running, but not registered with Systems Manager"
		}
	}
	if n.Down {
		r.Status, r.Hint = n.Status, n.Hint
	}
	n.AddRoute(r)
	Summarize(n, DefaultRouteOrder)
}
