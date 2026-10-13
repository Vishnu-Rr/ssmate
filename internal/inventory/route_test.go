package inventory

import (
	"reflect"
	"testing"
)

func TestParseRouteOrder(t *testing.T) {
	cases := []struct {
		spec    string
		want    []string
		unknown []string
	}{
		{"", DefaultRouteOrder, nil},
		{"arc", []string{RouteArc, RouteSSM, RouteAzureBastion, RouteOCIForward, RouteOCIManaged}, nil},
		// "bastion" is the label two kinds share, so it brings both forward.
		{"bastion, ssm", []string{RouteAzureBastion, RouteOCIManaged, RouteSSM, RouteArc, RouteOCIForward}, nil},
		{"SSM,ssm,oci-forward", []string{RouteSSM, RouteOCIForward, RouteArc, RouteAzureBastion, RouteOCIManaged}, nil},
		{"ssn,arc", []string{RouteArc, RouteSSM, RouteAzureBastion, RouteOCIForward, RouteOCIManaged}, []string{"ssn"}},
	}
	for _, c := range cases {
		got, unknown := ParseRouteOrder(c.spec)
		if !reflect.DeepEqual(got, c.want) || !reflect.DeepEqual(unknown, c.unknown) {
			t.Errorf("%q: got %v %v, want %v %v", c.spec, got, unknown, c.want, c.unknown)
		}
	}
}

func TestAddRouteReplacesTheSameRoute(t *testing.T) {
	var n Node
	n.AddRoute(Route{Kind: RouteSSM, Target: "mi-1", Status: "agent lost"})
	n.AddRoute(Route{Kind: RouteSSM, Target: "mi-1", Ready: true, Status: StatusOnline})
	n.AddRoute(Route{Kind: RouteSSM, Target: "mi-2"})
	if len(n.Routes) != 2 || !n.Routes[0].Ready {
		t.Errorf("got %+v", n.Routes)
	}
}

func TestSummarize(t *testing.T) {
	ready := Route{Kind: RouteOCIForward, Ready: true, Status: StatusOnline}
	lost := Route{Kind: RouteSSM, Status: StatusAgentLost, Hint: "restart the agent"}
	noGateway := Route{Kind: RouteOCIForward, Status: StatusNoGateway, Hint: "build a bastion"}

	t.Run("best ready route wins", func(t *testing.T) {
		n := Node{Routes: []Route{lost, ready}}
		Summarize(&n, DefaultRouteOrder)
		if !n.Reachable || n.Status != StatusOnline || n.Via != "bastion-fwd" || n.Hint != "" {
			t.Errorf("got %v %q %q %q", n.Reachable, n.Status, n.Via, n.Hint)
		}
	})
	t.Run("nothing ready shows the preferred route's blocker", func(t *testing.T) {
		n := Node{Routes: []Route{noGateway, lost}}
		Summarize(&n, DefaultRouteOrder)
		if n.Reachable || n.Status != StatusAgentLost || n.Hint != "restart the agent" || n.Via != "ssm" {
			t.Errorf("got %v %q %q %q", n.Reachable, n.Status, n.Hint, n.Via)
		}
	})
	t.Run("a stopped machine is described by its own state", func(t *testing.T) {
		n := Node{Down: true, Status: StatusStopped, Hint: "start the machine",
			Routes: []Route{{Kind: RouteSSM, Status: StatusStopped}}}
		Summarize(&n, DefaultRouteOrder)
		if n.Reachable || n.Status != StatusStopped || n.Via != "" {
			t.Errorf("got %v %q %q", n.Reachable, n.Status, n.Via)
		}
	})
	t.Run("no route at all", func(t *testing.T) {
		n := Node{Status: "stale", Reachable: true}
		Summarize(&n, DefaultRouteOrder)
		if n.Reachable || n.Status != StatusNoRoute || n.Via != "" {
			t.Errorf("got %v %q %q", n.Reachable, n.Status, n.Via)
		}
	})
	t.Run("summarising twice changes nothing", func(t *testing.T) {
		n := Node{Routes: []Route{lost, ready}}
		Summarize(&n, DefaultRouteOrder)
		first := n
		Summarize(&n, DefaultRouteOrder)
		if !reflect.DeepEqual(first, n) {
			t.Errorf("not idempotent: %+v then %+v", first, n)
		}
	})
}

func TestHasRouteAcceptsKindOrLabel(t *testing.T) {
	n := Node{Routes: []Route{{Kind: RouteOCIForward}}}
	for _, name := range []string{"oci-forward", "bastion-fwd", "Bastion-Fwd"} {
		if !n.HasRoute(name) {
			t.Errorf("HasRoute(%q) = false", name)
		}
	}
	if n.HasRoute("ssm") || n.HasRoute("bastion") {
		t.Error("matched a route the node does not have")
	}
}
