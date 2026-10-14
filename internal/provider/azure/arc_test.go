package azure

import (
	"bytes"
	"context"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/hybridcompute/armhybridcompute/v3"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/session"
)

const arcID = "/subscriptions/s/resourceGroups/rg-arc/providers/Microsoft.HybridCompute/machines/build-02"

func arcMachine(status armhybridcompute.StatusTypes) *armhybridcompute.Machine {
	return &armhybridcompute.Machine{
		ID:       to.Ptr(arcID),
		Name:     to.Ptr("build-02"),
		Location: to.Ptr("uksouth"),
		Tags:     map[string]*string{inventory.SourceTag: to.Ptr(" ocid1.instance.a ")},
		Properties: &armhybridcompute.MachineProperties{
			Status:        to.Ptr(status),
			AgentVersion:  to.Ptr("1.50"),
			OSName:        to.Ptr("linux"),
			OSProfile:     &armhybridcompute.OSProfile{ComputerName: to.Ptr("build-02")},
			CloudMetadata: &armhybridcompute.CloudMetadata{Provider: to.Ptr("OCI")},
			NetworkProfile: &armhybridcompute.NetworkProfile{NetworkInterfaces: []*armhybridcompute.NetworkInterface{{
				IPAddresses: []*armhybridcompute.IPAddress{
					{Address: to.Ptr("fe80::1")},
					{Address: to.Ptr("127.0.0.1")},
					{Address: to.Ptr("10.0.2.15")},
				},
			}}},
		},
	}
}

func TestBuildArcNode(t *testing.T) {
	n := buildArcNode(arcMachine(armhybridcompute.StatusTypesConnected), Subscription{Name: "contoso"})
	if n.Outside == nil || n.Outside.Tag != "ocid1.instance.a" {
		t.Errorf("outside = %+v", n.Outside)
	}
	if n.Hostname != "build-02" || n.PrivateIP != "10.0.2.15" || n.Account != "contoso" || n.Detail["runs_in"] != "OCI" {
		t.Errorf("hostname %q ip %q account %q runs_in %q", n.Hostname, n.PrivateIP, n.Account, n.Detail["runs_in"])
	}
	if len(n.Routes) != 1 || n.Routes[0].Kind != inventory.RouteArc || !n.Routes[0].Ready || n.Routes[0].Target != arcID {
		t.Errorf("routes = %+v", n.Routes)
	}
	if !n.Reachable || n.Via != "arc" {
		t.Errorf("reachable %v via %q", n.Reachable, n.Via)
	}

	lost := buildArcNode(arcMachine(armhybridcompute.StatusTypesDisconnected), Subscription{Name: "contoso"})
	if lost.Reachable || lost.Status != inventory.StatusAgentLost || !strings.Contains(lost.Hint, "azcmagent") {
		t.Errorf("disconnected: reachable %v status %q hint %q", lost.Reachable, lost.Status, lost.Hint)
	}
	if expired := buildArcNode(arcMachine("Expired"), Subscription{}); expired.Status != "expired" || expired.Reachable {
		t.Errorf("expired: %q", expired.Status)
	}
}

// An Arc record links to the machine's own record like any registration.
func TestArcRecordLinksToItsHome(t *testing.T) {
	home := inventory.Node{Cloud: inventory.CloudOCI, ID: "ocid1.instance.a", Name: "build-02"}
	home.AddRoute(inventory.Route{Kind: inventory.RouteOCIForward, Target: home.ID, Status: inventory.StatusNoGateway})
	got := inventory.Link([]inventory.Node{home, buildArcNode(arcMachine(armhybridcompute.StatusTypesConnected), Subscription{Name: "contoso"})})
	if len(got) != 1 {
		t.Fatalf("want one machine, got %d", len(got))
	}
	n := got[0]
	inventory.Summarize(&n, inventory.DefaultRouteOrder)
	if !n.Reachable || n.Via != "arc" || n.Cloud != inventory.CloudOCI {
		t.Errorf("reachable %v via %q cloud %q", n.Reachable, n.Via, n.Cloud)
	}
}

func TestArcArgs(t *testing.T) {
	r := inventory.Route{Kind: inventory.RouteArc, Target: arcID, Account: "contoso"}
	base := []string{"ssh", "arc", "--resource-group", "rg-arc", "--name", "build-02", "--subscription", "contoso"}
	cases := []struct {
		name string
		o    provider.SessionOptions
		want []string
		err  string
	}{
		{"microsoft sign-in by default", provider.SessionOptions{}, base, ""},
		{"aad ignores --user", provider.SessionOptions{Auth: "aad", User: "ubuntu"}, base, ""},
		{"local user, password", provider.SessionOptions{User: "ubuntu"}, append(append([]string{}, base...), "--local-user", "ubuntu"), ""},
		{"local user, key", provider.SessionOptions{User: "ubuntu", Key: "/k"}, append(append([]string{}, base...), "--local-user", "ubuntu", "--private-key-file", "/k"), ""},
		{"key auth without a key", provider.SessionOptions{Auth: "key", User: "ubuntu"}, nil, "needs the key"},
		{"password auth without a user", provider.SessionOptions{Auth: "password"}, nil, "local account"},
		{"unknown auth", provider.SessionOptions{Auth: "kerberos"}, nil, "not a way to log in"},
	}
	for _, c := range cases {
		got, err := arcArgs(inventory.Node{Name: "build-02"}, r, c.o)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: want error %q, got %v", c.name, c.err, err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v %v\n want %v", c.name, got, err, c.want)
		}
	}
}

// --print shows exactly what would run: a command goes after --, and a
// forward is an ssh -L on the machine's side.
func TestArcPrint(t *testing.T) {
	r := inventory.Route{Kind: inventory.RouteArc, Target: arcID, Account: "contoso"}
	n := inventory.Node{Name: "build-02"}
	shell := capture(t, func() error {
		return ArcHandler{}.Shell(context.Background(), n, r, provider.SessionOptions{Print: true, User: "ubuntu", Command: []string{"uptime"}})
	})
	for _, want := range []string{
		"az ssh config --resource-type Microsoft.HybridCompute/machines --resource-group rg-arc --name build-02 --file <dir>/config --overwrite --subscription contoso --local-user ubuntu",
		"ssh -F <dir>/config",
		"StrictHostKeyChecking=accept-new",
		"<host named in that file>\" uptime",
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("shell lacks %q:\n%s", want, shell)
		}
	}
	fwd := capture(t, func() error {
		return ArcHandler{}.Forward(context.Background(), n, r, session.Forward{LocalPort: 5432, RemotePort: 5432, RemoteHost: "db.internal"}, provider.SessionOptions{Print: true})
	})
	if !strings.Contains(fwd, "-- -N -L 127.0.0.1:5432:db.internal:5432") {
		t.Errorf("forward: %s", fwd)
	}
}

func TestArcNotInUse(t *testing.T) {
	for code, want := range map[string]bool{
		"MissingSubscriptionRegistration": true,
		"AuthorizationFailed":             false,
	} {
		if got := arcNotInUse(&azcore.ResponseError{ErrorCode: code, StatusCode: 409}); got != want {
			t.Errorf("%s: got %v", code, got)
		}
	}
	if !arcNotInUse(&azcore.ResponseError{StatusCode: 404}) {
		t.Error("a 404 means no Arc here")
	}
}

// An Arc record is diagnosed as one: the ssh extension, not the bastion one.
func TestChecksForAnArcRecord(t *testing.T) {
	n := buildArcNode(arcMachine(armhybridcompute.StatusTypesConnected), Subscription{Name: "contoso"})
	var names []string
	for _, c := range (Provider{}).Checks(context.Background(), &n, n.ID) {
		names = append(names, c.Name)
	}
	want := []string{"azure cli", "ssh extension", "azure sign-in", "arc agent connected"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("checks = %v, want %v", names, want)
	}
}

func capture(t *testing.T, f func() error) string {
	t.Helper()
	r, w, _ := os.Pipe()
	saved := os.Stdout
	os.Stdout = w
	err := f()
	w.Close()
	os.Stdout = saved
	var b bytes.Buffer
	io.Copy(&b, r)
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
