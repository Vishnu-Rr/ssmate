package inventory

import "testing"

func TestDisplayNamePrefersName(t *testing.T) {
	n := Node{ID: "i-0abc", Name: "web-prod-1"}
	if got := n.DisplayName(); got != "web-prod-1" {
		t.Fatalf("DisplayName() = %q, want web-prod-1", got)
	}
}

func TestDisplayNameFallsBackToID(t *testing.T) {
	n := Node{ID: "i-0abc"}
	if got := n.DisplayName(); got != "i-0abc" {
		t.Fatalf("DisplayName() = %q, want i-0abc", got)
	}
}

func TestClassifyMarksOnlyLiveAgentsReachable(t *testing.T) {
	cases := []struct {
		name string
		node Node
		want bool
	}{
		{"online and running", Node{Agent: Agent{Present: true, Online: true}, State: "running"}, true},
		{"agent lost", Node{Agent: Agent{Present: true}, State: "running"}, false},
		{"not managed", Node{State: "running"}, false},
		{"stopped", Node{State: "stopped"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := tc.node
			classify(&n)
			if got := n.Connectable(); got != tc.want {
				t.Fatalf("Connectable() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestClassifyNamesEachCondition(t *testing.T) {
	cases := []struct {
		node Node
		want string
	}{
		{Node{Agent: Agent{Present: true, Online: true}, State: "running"}, StatusOnline},
		{Node{Agent: Agent{Present: true}, State: "running"}, StatusAgentLost},
		{Node{State: "stopped"}, StatusStopped},
		{Node{State: "running"}, StatusUnmanaged},
		{Node{State: "terminated"}, StatusTerminated},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			n := tc.node
			classify(&n)
			if n.Status != tc.want {
				t.Fatalf("Status = %q, want %q", n.Status, tc.want)
			}
		})
	}
}

func TestClassifyDistinguishesStoppedFromUnmanaged(t *testing.T) {
	// This is the whole reason the EC2 join exists: the SSM inventory API
	// omits stopped nodes, so without EC2 data both look identical, and they
	// need opposite fixes.
	stopped := Node{State: "stopped"}
	unmanaged := Node{State: "running"}
	classify(&stopped)
	classify(&unmanaged)
	if stopped.Status == unmanaged.Status {
		t.Fatal("stopped and unmanaged must not report the same status")
	}
	if stopped.Hint == "" || unmanaged.Hint == "" {
		t.Fatal("both conditions must carry a hint, since each needs a different fix")
	}
}

func TestDetailStaysProviderPrivate(t *testing.T) {
	n := Node{Detail: map[string]string{"vpc": "vpc-1"}}
	if v, ok := n.Detailed("vpc"); !ok || v != "vpc-1" {
		t.Fatalf("Detailed(vpc) = %q,%v", v, ok)
	}
	if _, ok := n.Detailed("absent"); ok {
		t.Fatal("absent key must report missing")
	}
	var zero Node
	if _, ok := zero.Detailed("vpc"); ok {
		t.Fatal("a node with no detail must not panic or claim a value")
	}
}

// Every cloud has a default account that carries no name: an unset AWS profile,
// az's current subscription, OCI's DEFAULT. Rendering one as "/ap-southeast-2"
// makes users read a path where there is none.
func TestWhereOmitsTheUnnamedDefaultAccount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account string
		region  string
		want    string
	}{
		{"named account", "prod", "ap-southeast-2", "prod/ap-southeast-2"},
		{"default account", "", "ap-southeast-2", "ap-southeast-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := Node{Account: tc.account, Region: tc.region}
			if got := n.Where(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
