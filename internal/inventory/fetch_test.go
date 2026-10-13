package inventory

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

type fakeSSM struct {
	pages [][]ssmtypes.InstanceInformation
	tags  map[string]map[string]string
	calls int
}

func (f *fakeSSM) DescribeInstanceInformation(ctx context.Context, in *ssm.DescribeInstanceInformationInput, _ ...func(*ssm.Options)) (*ssm.DescribeInstanceInformationOutput, error) {
	idx := f.calls
	f.calls++
	if idx >= len(f.pages) {
		return &ssm.DescribeInstanceInformationOutput{}, nil
	}
	out := &ssm.DescribeInstanceInformationOutput{InstanceInformationList: f.pages[idx]}
	if idx < len(f.pages)-1 {
		out.NextToken = aws.String("more")
	}
	return out, nil
}

func (f *fakeSSM) ListTagsForResource(_ context.Context, in *ssm.ListTagsForResourceInput, _ ...func(*ssm.Options)) (*ssm.ListTagsForResourceOutput, error) {
	var list []ssmtypes.Tag
	for k, v := range f.tags[aws.ToString(in.ResourceId)] {
		list = append(list, ssmtypes.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return &ssm.ListTagsForResourceOutput{TagList: list}, nil
}

type fakeEC2 struct {
	instances []ec2types.Instance
}

func (f *fakeEC2) DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	return &ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{{Instances: f.instances}},
	}, nil
}

func TestFetchRegionJoinsBothSources(t *testing.T) {
	last := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	sc := &fakeSSM{pages: [][]ssmtypes.InstanceInformation{{
		{
			InstanceId:       aws.String("i-online"),
			PingStatus:       ssmtypes.PingStatusOnline,
			AgentVersion:     aws.String("3.3.1"),
			IsLatestVersion:  aws.Bool(true),
			PlatformType:     ssmtypes.PlatformTypeLinux,
			PlatformName:     aws.String("Amazon Linux"),
			LastPingDateTime: aws.Time(last),
			ResourceType:     ssmtypes.ResourceTypeEc2Instance,
		},
	}}}
	ec := &fakeEC2{instances: []ec2types.Instance{
		{
			InstanceId:         aws.String("i-online"),
			State:              &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
			PrivateIpAddress:   aws.String("10.0.0.5"),
			VpcId:              aws.String("vpc-1"),
			SubnetId:           aws.String("subnet-1"),
			IamInstanceProfile: &ec2types.IamInstanceProfile{Arn: aws.String("arn:aws:iam::1:instance-profile/x")},
			Tags:               []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("web-1")}},
		},
	}}

	got, err := FetchRegion(context.Background(), sc, ec, "default", "us-west-2")
	if err != nil {
		t.Fatalf("FetchRegion: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d nodes, want 1", len(got))
	}
	n := got[0]
	if n.ID != "i-online" || n.Name != "web-1" || !n.Agent.Present ||
		!n.Agent.Online || n.PrivateIP != "10.0.0.5" || n.State != "running" {
		t.Fatalf("join produced wrong node: %+v", n)
	}
	if n.Cloud != CloudAWS {
		t.Errorf("Cloud = %q, want %q", n.Cloud, CloudAWS)
	}
	if !n.Reachable || n.Status != StatusOnline {
		t.Errorf("a live agent must classify as reachable and online, got %v/%q", n.Reachable, n.Status)
	}
	if !n.Agent.Latest {
		t.Error("AgentLatest should be true")
	}
	if _, ok := n.Detailed("has_role"); !ok {
		t.Error("an attached instance profile should be recorded in detail")
	}
	if n.Account != "default" || n.Region != "us-west-2" {
		t.Errorf("provenance wrong: profile=%q region=%q", n.Account, n.Region)
	}
	if !n.Agent.LastSeen.Equal(last) {
		t.Errorf("LastPing = %v, want %v", n.Agent.LastSeen, last)
	}
}

func TestFetchRegionIncludesStoppedInstanceAbsentFromSSM(t *testing.T) {
	sc := &fakeSSM{}
	ec := &fakeEC2{instances: []ec2types.Instance{{
		InstanceId: aws.String("i-stopped"),
		State:      &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped},
	}}}

	got, err := FetchRegion(context.Background(), sc, ec, "default", "us-west-2")
	if err != nil {
		t.Fatalf("FetchRegion: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d nodes, want 1", len(got))
	}
	if got[0].Agent.Present {
		t.Error("stopped instance should not be marked managed")
	}
	if got[0].Status != "stopped" {
		t.Errorf("Status() = %q, want stopped", got[0].Status)
	}
}

func TestFetchRegionIncludesHybridNodeAbsentFromEC2(t *testing.T) {
	sc := &fakeSSM{pages: [][]ssmtypes.InstanceInformation{{
		{
			InstanceId:   aws.String("mi-hybrid"),
			PingStatus:   ssmtypes.PingStatusOnline,
			ResourceType: ssmtypes.ResourceTypeManagedInstance,
			ComputerName: aws.String("onprem-box"),
		},
	}}}
	ec := &fakeEC2{}

	got, err := FetchRegion(context.Background(), sc, ec, "default", "us-west-2")
	if err != nil {
		t.Fatalf("FetchRegion: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d nodes, want 1", len(got))
	}
	if !got[0].Agent.Present || got[0].ID != "mi-hybrid" {
		t.Fatalf("hybrid node wrong: %+v", got[0])
	}
}

func TestFetchRegionFollowsPagination(t *testing.T) {
	sc := &fakeSSM{pages: [][]ssmtypes.InstanceInformation{
		{{InstanceId: aws.String("i-1"), PingStatus: ssmtypes.PingStatusOnline}},
		{{InstanceId: aws.String("i-2"), PingStatus: ssmtypes.PingStatusOnline}},
	}}
	got, err := FetchRegion(context.Background(), sc, &fakeEC2{}, "p", "r")
	if err != nil {
		t.Fatalf("FetchRegion: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d nodes, want 2 (pagination not followed)", len(got))
	}
}

func TestFetchRegionResultIsDeterministic(t *testing.T) {
	ec := &fakeEC2{instances: []ec2types.Instance{
		{InstanceId: aws.String("i-b"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
		{InstanceId: aws.String("i-a"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
	}}
	got, err := FetchRegion(context.Background(), &fakeSSM{}, ec, "p", "r")
	if err != nil {
		t.Fatalf("FetchRegion: %v", err)
	}
	if got[0].ID != "i-a" || got[1].ID != "i-b" {
		t.Fatalf("result not sorted by instance id: %v, %v", got[0].ID, got[1].ID)
	}
}

// The verdict lands on the Session Manager route, aimed at the instance in its
// own profile and region.
func TestClassifyRecordsTheSessionManagerRoute(t *testing.T) {
	n := Node{ID: "i-0a1", Account: "prod", Region: "eu-west-1", Agent: Agent{Present: true, Online: true}}
	classify(&n)
	if len(n.Routes) != 1 {
		t.Fatalf("routes = %+v", n.Routes)
	}
	r := n.Routes[0]
	if r.Kind != RouteSSM || !r.Ready || r.Target != "i-0a1" || r.Account != "prod" || r.Region != "eu-west-1" {
		t.Errorf("route = %+v", r)
	}
	if !n.Reachable || n.Via != "ssm" {
		t.Errorf("summary: reachable %v via %q", n.Reachable, n.Via)
	}

	unmanaged := Node{ID: "i-0b2", State: "running"}
	classify(&unmanaged)
	if unmanaged.Reachable || unmanaged.Status != StatusUnmanaged || unmanaged.Down {
		t.Errorf("unmanaged: %+v", unmanaged)
	}
}

// A machine registered from outside EC2 is recorded as living elsewhere, with
// what Systems Manager knows about where.
func TestFetchRegionRecordsRegisteredMachines(t *testing.T) {
	sc := &fakeSSM{
		pages: [][]ssmtypes.InstanceInformation{{{
			InstanceId:   aws.String("mi-0abc"),
			PingStatus:   ssmtypes.PingStatusOnline,
			ResourceType: ssmtypes.ResourceTypeManagedInstance,
			ComputerName: aws.String("build-02.sub.vcn.oraclevcn.com"),
			IPAddress:    aws.String("10.0.2.15"),
			SourceId:     aws.String("/subscriptions/s/vm/db-prod"),
			SourceType:   ssmtypes.SourceType("Azure::Compute::VirtualMachine"),
		}, {
			InstanceId:   aws.String("mi-0def"),
			PingStatus:   ssmtypes.PingStatusConnectionLost,
			ResourceType: ssmtypes.ResourceTypeManagedInstance,
			SourceId:     aws.String("mi-0def"),
			SourceType:   ssmtypes.SourceTypeAwsSsmManagedinstance,
		}}},
		tags: map[string]map[string]string{
			"mi-0abc": {SourceTag: " ocid1.instance.a ", "Name": "build-02", "team": "ci"},
		},
	}
	got, err := FetchRegion(context.Background(), sc, &fakeEC2{}, "prod", "eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Node{}
	for _, n := range got {
		by[n.ID] = n
	}
	a := by["mi-0abc"]
	if a.Outside == nil || a.Outside.SourceID != "/subscriptions/s/vm/db-prod" || a.Outside.Tag != "ocid1.instance.a" {
		t.Errorf("outside = %+v", a.Outside)
	}
	if a.Hostname != "build-02.sub.vcn.oraclevcn.com" || a.Name != "build-02" || a.Tags["team"] != "ci" {
		t.Errorf("hostname %q name %q tags %v", a.Hostname, a.Name, a.Tags)
	}
	if len(a.Routes) != 1 || a.Routes[0].Target != "mi-0abc" || !a.Routes[0].Ready || a.Down {
		t.Errorf("routes %+v down %v", a.Routes, a.Down)
	}
	// A source that is the registration itself says nothing about a home.
	if b := by["mi-0def"]; b.Outside == nil || b.Outside.SourceID != "" || b.Reachable {
		t.Errorf("mi-0def: outside %+v reachable %v", b.Outside, b.Reachable)
	}
}
