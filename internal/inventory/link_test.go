package inventory

import (
	"reflect"
	"testing"
)

// home is a machine as its own cloud lists it, reachable only by its bastion.
func home(id, ip, host string) Node {
	n := Node{Cloud: CloudOCI, ID: id, Name: host, PrivateIP: ip, Hostname: host, Region: "uk-london-1"}
	n.AddRoute(Route{Kind: RouteOCIForward, Target: id, Status: StatusNoGateway, Hint: "build a bastion"})
	return n
}

// registered is the same machine as Session Manager lists it.
func registered(id, ip, host string, o Outside) Node {
	n := Node{Cloud: CloudAWS, ID: id, Name: host, PrivateIP: ip, Hostname: host,
		Account: "prod", Region: "eu-west-1", Outside: &o}
	n.AddRoute(Route{Kind: RouteSSM, Target: id, Account: "prod", Region: "eu-west-1", Ready: true, Status: StatusOnline})
	return n
}

func byName(nodes []Node) map[string]Node {
	out := map[string]Node{}
	for _, n := range nodes {
		out[n.ID] = n
	}
	return out
}

func TestLinkBySource(t *testing.T) {
	got := Link([]Node{
		home("ocid1.instance.a", "10.0.2.15", "build-02"),
		registered("mi-0aaa", "192.168.9.9", "other-name", Outside{SourceID: "OCID1.INSTANCE.A"}),
	})
	if len(got) != 1 {
		t.Fatalf("want one machine, got %d", len(got))
	}
	n := got[0]
	if len(n.Routes) != 2 || n.Routes[1].Kind != RouteSSM || n.Routes[1].Target != "mi-0aaa" || n.Routes[1].Link != LinkSource {
		t.Errorf("routes = %+v", n.Routes)
	}
	if !reflect.DeepEqual(n.Aliases, []string{"mi-0aaa"}) {
		t.Errorf("aliases = %v", n.Aliases)
	}
	Summarize(&n, DefaultRouteOrder)
	if !n.Reachable || n.Via != "ssm" || n.Cloud != CloudOCI {
		t.Errorf("summary: reachable %v via %q cloud %q", n.Reachable, n.Via, n.Cloud)
	}
}

func TestLinkByTag(t *testing.T) {
	got := byName(Link([]Node{
		home("ocid1.instance.a", "10.0.2.15", "build-02"),
		registered("mi-0aaa", "", "", Outside{Tag: "ocid1.instance.a"}),
	}))
	if r := got["ocid1.instance.a"].Routes; len(r) != 2 || r[1].Link != LinkTag {
		t.Errorf("routes = %+v", r)
	}
	if _, still := got["mi-0aaa"]; still {
		t.Error("the registration should fold into the machine")
	}
}

// Azure records a VM ID beside its resource ID, and an agent registered from
// inside the machine is more likely to know the VM ID.
func TestLinkByAlias(t *testing.T) {
	az := Node{Cloud: CloudAzure, ID: "/subscriptions/s/vm/db-prod", Aliases: []string{"0f1e2d3c-aaaa"}}
	got := Link([]Node{az, registered("mi-0bbb", "", "", Outside{SourceID: "0F1E2D3C-AAAA"})})
	if len(got) != 1 || len(got[0].Routes) != 1 || got[0].Routes[0].Target != "mi-0bbb" {
		t.Errorf("got %+v", got)
	}
}

func TestLinkByAddressAndHostname(t *testing.T) {
	got := Link([]Node{
		home("ocid1.instance.a", "10.0.2.15", "build-02"),
		// The agent reports a fully qualified name; the first label is the host.
		registered("mi-0aaa", "10.0.2.15", "Build-02.sub.vcn.oraclevcn.com", Outside{}),
	})
	if len(got) != 1 || got[0].Routes[1].Link != LinkAddress {
		t.Errorf("got %+v", got)
	}
}

// Private addresses repeat across networks, so an address alone, or an
// address and hostname shared by two machines, must never link.
func TestLinkRefusesWhatIsNotCertainEnough(t *testing.T) {
	cases := map[string][]Node{
		"address alone": {
			home("ocid1.instance.a", "10.0.2.15", "build-02"),
			registered("mi-0aaa", "10.0.2.15", "web-7", Outside{}),
		},
		"two machines share the pair": {
			home("ocid1.instance.a", "10.0.2.15", "build-02"),
			home("ocid1.instance.b", "10.0.2.15", "build-02"),
			registered("mi-0aaa", "10.0.2.15", "build-02", Outside{}),
		},
		"two registrations share the pair": {
			home("ocid1.instance.a", "10.0.2.15", "build-02"),
			registered("mi-0aaa", "10.0.2.15", "build-02", Outside{}),
			registered("mi-0bbb", "10.0.2.15", "build-02", Outside{}),
		},
		"no hostname anywhere": {
			home("ocid1.instance.a", "10.0.2.15", ""),
			registered("mi-0aaa", "10.0.2.15", "", Outside{}),
		},
		"the home is not listed": {
			registered("mi-0aaa", "10.0.2.15", "build-02", Outside{Tag: "ocid1.instance.gone"}),
		},
		"a registration naming itself": {
			registered("mi-0aaa", "", "", Outside{SourceID: "mi-0aaa"}),
		},
	}
	for name, in := range cases {
		got := Link(in)
		if len(got) != len(in) {
			t.Errorf("%s: linked %d records into %d", name, len(in), len(got))
		}
	}
}

// A tag beats an address that points somewhere else.
func TestLinkPrefersTheStrongerEvidence(t *testing.T) {
	got := byName(Link([]Node{
		home("ocid1.instance.a", "10.0.2.15", "build-02"),
		home("ocid1.instance.b", "10.0.9.9", "build-03"),
		registered("mi-0aaa", "10.0.2.15", "build-02", Outside{Tag: "ocid1.instance.b"}),
	}))
	if len(got["ocid1.instance.b"].Routes) != 2 || len(got["ocid1.instance.a"].Routes) != 1 {
		t.Errorf("a: %+v\nb: %+v", got["ocid1.instance.a"].Routes, got["ocid1.instance.b"].Routes)
	}
}

// A stopped machine stays stopped, whatever its registration says.
func TestLinkedStoppedMachineIsStopped(t *testing.T) {
	h := home("ocid1.instance.a", "10.0.2.15", "build-02")
	h.Down, h.Status = true, StatusStopped
	got := Link([]Node{h, registered("mi-0aaa", "", "", Outside{Tag: "ocid1.instance.a"})})
	n := got[0]
	Summarize(&n, DefaultRouteOrder)
	if n.Reachable || n.Status != StatusStopped {
		t.Errorf("got reachable %v status %q", n.Reachable, n.Status)
	}
}

// Link runs on every load, so it must never change what it was given.
func TestLinkLeavesItsInputAlone(t *testing.T) {
	in := []Node{
		home("ocid1.instance.a", "10.0.2.15", "build-02"),
		registered("mi-0aaa", "", "", Outside{Tag: "ocid1.instance.a"}),
	}
	before := []Node{in[0].clone(), in[1].clone()}
	Link(in)
	Link(in)
	if !reflect.DeepEqual(in[0].Routes, before[0].Routes) || in[0].Aliases != nil || len(in) != 2 {
		t.Errorf("input changed: %+v", in)
	}
}
