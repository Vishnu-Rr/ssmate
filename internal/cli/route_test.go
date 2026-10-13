package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

// told is everything an error tells the user: the message and the hint.
func told(err error) string {
	var e *exitcode.Error
	if errors.As(err, &e) {
		return e.Msg + "\n" + e.Hint
	}
	return err.Error()
}

func twoWays(ssmReady, fwdReady bool) inventory.Node {
	n := inventory.Node{Cloud: inventory.CloudOCI, ID: "ocid1.instance.x", Name: "build-02"}
	n.AddRoute(inventory.Route{Kind: inventory.RouteOCIForward, Target: n.ID, Ready: fwdReady})
	n.AddRoute(inventory.Route{Kind: inventory.RouteSSM, Target: "mi-0abc", Ready: ssmReady})
	return n
}

// The better way in wins when both are ready, whatever order they were found.
func TestPickRoutePrefersTheBetterReadyRoute(t *testing.T) {
	r, h, err := pickRoute(twoWays(true, true), "", inventory.DefaultRouteOrder)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != inventory.RouteSSM || r.Target != "mi-0abc" || h.Kind() != inventory.RouteSSM {
		t.Errorf("picked %s to %s, want ssm to mi-0abc", r.Kind, r.Target)
	}
}

// A ready route beats a better one that is not ready.
func TestPickRouteSkipsARouteThatIsNotReady(t *testing.T) {
	r, _, err := pickRoute(twoWays(false, true), "", inventory.DefaultRouteOrder)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != inventory.RouteOCIForward {
		t.Errorf("picked %s, want the ready oci-forward", r.Kind)
	}
}

// With nothing ready the best route is still tried: the verdict may come from
// a cache older than the machine's last start.
func TestPickRouteStillTriesWhenNothingIsReady(t *testing.T) {
	r, _, err := pickRoute(twoWays(false, false), "", inventory.DefaultRouteOrder)
	if err != nil {
		t.Fatalf("want an attempt, not a refusal: %v", err)
	}
	if r.Kind != inventory.RouteSSM {
		t.Errorf("picked %s, want ssm, the best in order", r.Kind)
	}
}

// --via narrows the choice, by kind or by short label.
func TestPickRouteHonoursVia(t *testing.T) {
	for _, via := range []string{"bastion-fwd", "oci-forward", "BASTION-FWD"} {
		r, _, err := pickRoute(twoWays(true, true), via, inventory.DefaultRouteOrder)
		if err != nil {
			t.Fatalf("--via %s: %v", via, err)
		}
		if r.Kind != inventory.RouteOCIForward {
			t.Errorf("--via %s picked %s", via, r.Kind)
		}
	}
}

// Asking for a way in the machine does not have names the ones it does have.
func TestPickRouteExplainsAMissingVia(t *testing.T) {
	_, _, err := pickRoute(twoWays(true, true), "arc", inventory.DefaultRouteOrder)
	if err == nil {
		t.Fatal("want an error: build-02 has no arc route")
	}
	msg := told(err)
	for _, want := range []string{"build-02", "arc", "ssm", "bastion-fwd"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q: %s", want, msg)
		}
	}
}

// A typo is reported as a typo, listing the names that do work.
func TestPickRouteRejectsAnUnknownVia(t *testing.T) {
	_, _, err := pickRoute(twoWays(true, true), "ssn", inventory.DefaultRouteOrder)
	if err == nil || !strings.Contains(told(err), "not a way in") || !strings.Contains(told(err), "bastion-fwd") {
		t.Errorf("want 'not a way in' naming the valid ones, got %v", err)
	}
}

// A machine with no route at all says so and points at doctor.
func TestPickRouteWithNoRoutes(t *testing.T) {
	_, _, err := pickRoute(inventory.Node{Name: "legacy-4"}, "", inventory.DefaultRouteOrder)
	if err == nil || !strings.Contains(err.Error(), "legacy-4 has no way in") {
		t.Errorf("got %v", err)
	}
}

// A route kind this build has no handler for names the machine and the kind,
// rather than failing somewhere deep in a session.
func TestPickRouteNamesAKindWithNoHandler(t *testing.T) {
	n := inventory.Node{Name: "db-1"}
	n.AddRoute(inventory.Route{Kind: "teleport", Ready: true})
	_, _, err := pickRoute(n, "", inventory.DefaultRouteOrder)
	if err == nil || !strings.Contains(err.Error(), "db-1") || !strings.Contains(err.Error(), "teleport") {
		t.Errorf("got %v", err)
	}
}

// SSMATE_ROUTES changes which ready route wins.
func TestRouteOrderFromEnvironment(t *testing.T) {
	t.Setenv(routesEnv, "bastion-fwd")
	r, _, err := pickRoute(twoWays(true, true), "", routeOrder())
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != inventory.RouteOCIForward {
		t.Errorf("SSMATE_ROUTES=bastion-fwd picked %s", r.Kind)
	}
}

// --via also narrows what is listed and resolved.
func TestScopeVia(t *testing.T) {
	a := twoWays(true, true)
	b := inventory.Node{Name: "db-prod"}
	b.AddRoute(inventory.Route{Kind: inventory.RouteAzureBastion, Ready: true})
	sc, err := scopeFrom(&options{via: "ssm"})
	if err != nil {
		t.Fatal(err)
	}
	got := sc.apply([]inventory.Node{a, b})
	if len(got) != 1 || got[0].Name != "build-02" {
		t.Errorf("--via ssm kept %v", got)
	}
	if _, err := scopeFrom(&options{via: "nope"}); err == nil {
		t.Error("--via nope should be refused")
	}
}

// An OCI machine registered with Session Manager is one row, still in OCI,
// reached through Session Manager at the registration's ID - and narrowing
// to its cloud keeps the way in another cloud gave it.
func TestJoinedMachineIsReachedThroughItsRegistration(t *testing.T) {
	oci := inventory.Node{Cloud: inventory.CloudOCI, ID: "ocid1.instance.a", Name: "build-02",
		Region: "uk-london-1", PrivateIP: "10.0.2.15", Hostname: "build-02"}
	oci.AddRoute(inventory.Route{Kind: inventory.RouteOCIForward, Target: oci.ID, Status: inventory.StatusNoGateway})
	reg := inventory.Node{Cloud: inventory.CloudAWS, ID: "mi-0abc", Name: "build-02", Account: "prod",
		Region: "eu-west-1", Outside: &inventory.Outside{Tag: "ocid1.instance.a"}}
	reg.AddRoute(inventory.Route{Kind: inventory.RouteSSM, Target: "mi-0abc", Account: "prod",
		Region: "eu-west-1", Ready: true, Status: inventory.StatusOnline})

	nodes := joined([]inventory.Node{oci, reg})
	if len(nodes) != 1 {
		t.Fatalf("want one machine, got %d", len(nodes))
	}
	n := nodes[0]
	if n.Cloud != inventory.CloudOCI || !n.Reachable || n.Via != "ssm" {
		t.Errorf("cloud %q reachable %v via %q", n.Cloud, n.Reachable, n.Via)
	}
	r, h, err := pickRoute(n, "", routeOrder())
	if err != nil || h.Kind() != inventory.RouteSSM || r.Target != "mi-0abc" || r.Account != "prod" || r.Region != "eu-west-1" {
		t.Errorf("picked %+v (%v)", r, err)
	}
	for _, o := range []options{{cloud: "oci"}, {via: "ssm"}, {cloud: "oci", via: "bastion-fwd"}} {
		sc, err := scopeFrom(&o)
		if err != nil {
			t.Fatal(err)
		}
		if got := sc.apply(nodes); len(got) != 1 {
			t.Errorf("%+v dropped the machine", o)
		}
	}
	if sc, _ := scopeFrom(&options{cloud: "aws"}); len(sc.apply(nodes)) != 0 {
		t.Error("--cloud aws should not list an OCI machine")
	}
}
