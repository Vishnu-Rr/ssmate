package oci

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/doctor"
	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/session"
	"github.com/oracle/oci-go-sdk/v65/bastion"
	"github.com/oracle/oci-go-sdk/v65/computeinstanceagent"
	"github.com/oracle/oci-go-sdk/v65/core"
)

func running(withBastion bool) inventory.Node {
	n := inventory.Node{ID: "ocid1.instance.a", Name: "build-02", Account: "DEFAULT", Region: "uk-london-1",
		PrivateIP: "10.0.2.15", State: "RUNNING", Detail: map[string]string{"bastion_plugin": "enabled", "login_user": "opc"}}
	if withBastion {
		n.Detail["bastion"] = "ocid1.bastion.x"
	}
	classify(&n, withBastion, "app-vcn")
	return n
}

func TestClassifyManaged(t *testing.T) {
	cases := []struct {
		name, status string
		bastion      bool
		ready        bool
		want         string
	}{
		{"running plugin", "RUNNING", true, true, inventory.StatusOnline},
		{"stopped plugin", "STOPPED", true, false, "plugin stopped"},
		{"plugin state unreadable", "", true, false, inventory.StatusUnknown},
		{"no bastion", "RUNNING", false, false, inventory.StatusNoGateway},
	}
	for _, c := range cases {
		n := running(c.bastion)
		classifyManaged(&n, c.status)
		var r inventory.Route
		for _, x := range n.Routes {
			if x.Kind == inventory.RouteOCIManaged {
				r = x
			}
		}
		if r.Kind == "" || r.Ready != c.ready || r.Status != c.want {
			t.Errorf("%s: route %+v", c.name, r)
		}
	}
	off := inventory.Node{ID: "x", State: "STOPPED", Detail: map[string]string{}}
	classify(&off, true, "")
	classifyManaged(&off, "RUNNING")
	if off.HasRoute(inventory.RouteOCIManaged) {
		t.Error("a stopped machine gets no managed route")
	}
}

// Port forwarding stays the default; managed SSH is there for --via bastion.
func TestManagedRanksAfterPortForwarding(t *testing.T) {
	n := running(true)
	classifyManaged(&n, "RUNNING")
	if n.Via != "bastion-fwd" {
		t.Errorf("default via %q, want bastion-fwd", n.Via)
	}
	best, _ := n.BestRoute(inventory.DefaultRouteOrder)
	if best.Kind != inventory.RouteOCIForward {
		t.Errorf("best %s", best.Kind)
	}
	if !n.HasRoute("bastion") {
		t.Error("--via bastion should find the managed route")
	}
}

func TestBastionPluginWanted(t *testing.T) {
	yes, no := true, false
	plugin := func(state core.InstanceAgentPluginConfigDetailsDesiredStateEnum) []core.InstanceAgentPluginConfigDetails {
		return []core.InstanceAgentPluginConfigDetails{{Name: strPtr("Custom Logs Monitoring"), DesiredState: core.InstanceAgentPluginConfigDetailsDesiredStateEnabled},
			{Name: strPtr("Bastion"), DesiredState: state}}
	}
	cases := map[string]struct {
		cfg  *core.InstanceAgentConfig
		want bool
	}{
		"no agent config":     {nil, false},
		"plugin enabled":      {&core.InstanceAgentConfig{PluginsConfig: plugin(core.InstanceAgentPluginConfigDetailsDesiredStateEnabled)}, true},
		"plugin disabled":     {&core.InstanceAgentConfig{PluginsConfig: plugin(core.InstanceAgentPluginConfigDetailsDesiredStateDisabled)}, false},
		"plugin not listed":   {&core.InstanceAgentConfig{}, false},
		"all plugins off":     {&core.InstanceAgentConfig{AreAllPluginsDisabled: &yes, PluginsConfig: plugin(core.InstanceAgentPluginConfigDetailsDesiredStateEnabled)}, false},
		"all plugins not off": {&core.InstanceAgentConfig{AreAllPluginsDisabled: &no, PluginsConfig: plugin(core.InstanceAgentPluginConfigDetailsDesiredStateEnabled)}, true},
	}
	for name, c := range cases {
		if got := bastionPluginWanted(core.Instance{AgentConfig: c.cfg}); got != c.want {
			t.Errorf("%s: got %v", name, got)
		}
	}
}

type fakePlugins struct{ asked []string }

func (f *fakePlugins) GetInstanceAgentPlugin(_ context.Context, r computeinstanceagent.GetInstanceAgentPluginRequest) (computeinstanceagent.GetInstanceAgentPluginResponse, error) {
	f.asked = append(f.asked, *r.InstanceagentId)
	if *r.PluginName != "Bastion" {
		return computeinstanceagent.GetInstanceAgentPluginResponse{}, errors.New("wrong plugin")
	}
	return computeinstanceagent.GetInstanceAgentPluginResponse{
		InstanceAgentPlugin: computeinstanceagent.InstanceAgentPlugin{Status: computeinstanceagent.InstanceAgentPluginStatusRunning},
	}, nil
}

// The plugin is asked about only where someone switched it on.
func TestManagedRoutesAsksOnlyWhereEnabled(t *testing.T) {
	enabled := running(true)
	plain := running(true)
	plain.ID = "ocid1.instance.b"
	delete(plain.Detail, "bastion_plugin")
	nodes := []inventory.Node{enabled, plain}
	f := &fakePlugins{}
	managedRoutes(context.Background(), f, nodes, 2)
	if len(f.asked) != 1 || f.asked[0] != "ocid1.instance.a" {
		t.Errorf("asked about %v", f.asked)
	}
	if !nodes[0].HasRoute(inventory.RouteOCIManaged) || nodes[1].HasRoute(inventory.RouteOCIManaged) {
		t.Error("managed route on the wrong machine")
	}
}

func TestSessionKindsAskForTheRightTarget(t *testing.T) {
	n := inventory.Node{ID: "ocid1.instance.a"}
	pf, ok := portForwarding.target(n, "10.0.2.15", 22).(bastion.CreatePortForwardingSessionTargetResourceDetails)
	if !ok || *pf.TargetResourcePrivateIpAddress != "10.0.2.15" || *pf.TargetResourcePort != 22 {
		t.Errorf("port forwarding: %+v", pf)
	}
	m, ok := managedSSH("opc").target(n, "10.0.2.15", 22).(bastion.CreateManagedSshSessionTargetResourceDetails)
	if !ok || *m.TargetResourceId != "ocid1.instance.a" || *m.TargetResourceOperatingSystemUserName != "opc" ||
		*m.TargetResourcePrivateIpAddress != "10.0.2.15" || *m.TargetResourcePort != 22 {
		t.Errorf("managed: %+v", m)
	}
	if managedSSH("opc").wait <= portForwarding.wait || managedSSH("opc").note == "" {
		t.Error("a managed session needs the longer wait and a word about it")
	}
}

func TestManagedPrint(t *testing.T) {
	n := running(true)
	shell := captureOut(t, func() error {
		return ManagedHandler{}.Shell(context.Background(), n, inventory.Route{}, provider.SessionOptions{Print: true, Command: []string{"uptime"}})
	})
	for _, want := range []string{
		"oci bastion session create-managed-ssh --bastion-id ocid1.bastion.x --target-resource-id ocid1.instance.a --target-os-username opc",
		`ssh -i <session-key> -o ProxyCommand="ssh -i <session-key> -W %h:%p -p 22 <session-ocid>@host.bastion.uk-london-1.oci.oraclecloud.com" opc@10.0.2.15 uptime`,
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("shell print missing %q:\n%s", want, shell)
		}
	}
	fwd := captureOut(t, func() error {
		return ManagedHandler{}.Forward(context.Background(), n, inventory.Route{}, session.Forward{LocalPort: 8080, RemotePort: 80}, provider.SessionOptions{Print: true})
	})
	if !strings.Contains(fwd, "-N -L 127.0.0.1:8080:localhost:80 opc@10.0.2.15") {
		t.Errorf("forward print:\n%s", fwd)
	}
}

// A port-forwarding failure points at managed SSH only when the machine has it.
func TestWithManagedHint(t *testing.T) {
	fail := func() error {
		return exitcode.New(exitcode.NotConnected, "no bastion could reach build-02", "first", nil)
	}
	with := running(true)
	classifyManaged(&with, "RUNNING")
	var e *exitcode.Error
	if err := withManagedHint(with, fail()); !errors.As(err, &e) || !strings.Contains(e.Hint, "--via bastion") {
		t.Errorf("with a managed route: %v", err)
	}
	if err := withManagedHint(running(true), fail()); errors.As(err, &e) && strings.Contains(e.Hint, "--via bastion") {
		t.Error("hint offered without a managed route")
	}
	if err := withManagedHint(with, nil); err != nil {
		t.Error("nil must stay nil")
	}
}

func captureOut(t *testing.T, f func() error) string {
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

// doctor passes Oracle's Arm caveat on as a caution, not a verdict.
func TestManagedCheck(t *testing.T) {
	arm := running(true)
	arm.Detail["shape"], arm.Detail["os"] = "VM.Standard.A1.Flex", "Canonical Ubuntu"
	classifyManaged(&arm, "RUNNING")
	if r := managedCheck(arm).Run(context.Background()); r.Severity != doctor.Warn || !strings.Contains(r.Detail, "Oracle documents") {
		t.Errorf("arm ubuntu: %+v", r)
	}
	ol := running(true)
	ol.Detail["shape"], ol.Detail["os"] = "VM.Standard.A1.Flex", "Oracle Linux"
	classifyManaged(&ol, "RUNNING")
	if r := managedCheck(ol).Run(context.Background()); r.Severity != doctor.Pass {
		t.Errorf("arm oracle linux: %+v", r)
	}
	stopped := running(true)
	classifyManaged(&stopped, "STOPPED")
	if r := managedCheck(stopped).Run(context.Background()); r.Severity != doctor.Warn || r.Detail != "plugin stopped" {
		t.Errorf("stopped plugin: %+v", r)
	}
}
