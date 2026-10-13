package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

func blockedOCI() inventory.Node {
	n := inventory.Node{Cloud: inventory.CloudOCI, ID: "ocid1.instance.a", Name: "build-02",
		OS: "Canonical Ubuntu", Arch: "arm64"}
	n.AddRoute(inventory.Route{Kind: inventory.RouteOCIForward, Status: inventory.StatusBlocked})
	inventory.Summarize(&n, inventory.DefaultRouteOrder)
	return n
}

func TestOutboundGuideFillsInTheMachine(t *testing.T) {
	g, ok := outboundGuide(blockedOCI())
	if !ok {
		t.Fatal("a blocked OCI machine should get the guide")
	}
	r := g.Run(context.Background())
	for _, want := range []string{
		"latest/debian_arm64/ssm-setup-cli",
		"--default-instance-name build-02",
		"Key=ssmate:source,Value=ocid1.instance.a",
		`--tags "ssmate:source=ocid1.instance.a"`,
		"Canonical Ubuntu, arm64",
	} {
		if !strings.Contains(r.Remedy, want) {
			t.Errorf("remedy missing %q:\n%s", want, r.Remedy)
		}
	}
}

func TestOutboundGuideLeavesUnknownsAsChoices(t *testing.T) {
	n := blockedOCI()
	n.OS, n.Arch = "", ""
	g, _ := outboundGuide(n)
	r := g.Run(context.Background())
	if !strings.Contains(r.Remedy, "<debian|linux>_<amd64|arm64>") || !strings.Contains(r.Remedy, "not known") {
		t.Errorf("unknowns should be choices, not guesses:\n%s", r.Remedy)
	}
}

func TestOutboundGuideOnlyWhereItHelps(t *testing.T) {
	reachable := blockedOCI()
	reachable.Reachable = true
	stopped := blockedOCI()
	stopped.Down = true
	aws := blockedOCI()
	aws.Cloud = inventory.CloudAWS
	for name, n := range map[string]inventory.Node{"reachable": reachable, "stopped": stopped, "ec2": aws} {
		if _, ok := outboundGuide(n); ok {
			t.Errorf("%s: no guide expected", name)
		}
	}
	az := blockedOCI()
	az.Cloud = inventory.CloudAzure
	g, ok := outboundGuide(az)
	if !ok || strings.Contains(g.Run(context.Background()).Remedy, "azcmagent") {
		t.Error("an Azure VM gets Session Manager but not Arc, which does not take Azure's own machines")
	}
}
