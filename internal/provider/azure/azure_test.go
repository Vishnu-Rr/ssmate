package azure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

// Azure resource IDs are a path, so the network a machine sits in is already
// inside the subnet ID its interface carries. Getting this wrong silently
// detaches every machine from every bastion.
func TestVnetOfAndGroupOf(t *testing.T) {
	id := "/subscriptions/abc/resourceGroups/APP-RG/providers/Microsoft.Network/" +
		"virtualNetworks/vnet-app/subnets/AzureBastionSubnet"
	if got := vnetOf(id); got != "vnet-app" {
		t.Errorf("vnetOf = %q", got)
	}
	if got := groupOf(id); got != "APP-RG" {
		t.Errorf("groupOf = %q", got)
	}
	// Azure is inconsistent about the case of segment names in IDs.
	lower := strings.ReplaceAll(id, "virtualNetworks", "virtualnetworks")
	if got := vnetOf(lower); got != "vnet-app" {
		t.Errorf("lowercase segment: vnetOf = %q", got)
	}
	if vnetOf("/subscriptions/abc/resourceGroups/x") != "" {
		t.Error("an ID with no network must yield nothing, not a wrong answer")
	}
}

// The two settings are independent, and both are required. A Standard bastion
// with tunnelling off is as unusable as a Basic one, which is exactly the
// case Microsoft's own comparison table obscures.
func TestGatewayUsableNeedsBothSkuAndTunnel(t *testing.T) {
	for _, tc := range []struct {
		name string
		g    gateway
		want bool
	}{
		{"standard with tunnelling", gateway{sku: "Standard", tunnel: true, running: true}, true},
		{"premium with tunnelling", gateway{sku: "Premium", tunnel: true, running: true}, true},
		{"standard without tunnelling", gateway{sku: "Standard", tunnel: false, running: true}, false},
		{"basic with tunnelling somehow", gateway{sku: "Basic", tunnel: true, running: true}, false},
		{"basic", gateway{sku: "Basic", tunnel: false, running: true}, false},
		{"still deploying", gateway{sku: "Standard", tunnel: true, running: false}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.g.usable(); got != tc.want {
				t.Errorf("usable = %v, want %v", got, tc.want)
			}
		})
	}
}

// "No bastion here" and "the bastion here is the wrong kind" are different
// problems with different fixes. Collapsing them sends someone to build a
// second bastion when they needed to upgrade the first.
func TestClassifySeparatesMissingFromUnusable(t *testing.T) {
	running := func() inventory.Node { return inventory.Node{State: "running"} }

	n := running()
	classify(&n, []gateway{{name: "b", sku: "Standard", tunnel: true, running: true}}, "vnet-a")
	if !n.Reachable || n.Status != inventory.StatusOnline {
		t.Errorf("usable bastion: status %q reachable %v", n.Status, n.Reachable)
	}

	n = running()
	classify(&n, nil, "vnet-a")
	if n.Status != inventory.StatusNoGateway || !strings.Contains(n.Hint, "vnet-a") {
		t.Errorf("no bastion: status %q hint %q", n.Status, n.Hint)
	}

	n = running()
	classify(&n, []gateway{{name: "basic-b", group: "rg", sku: "Basic", running: true}}, "vnet-a")
	if n.Status != StatusNoTunnel || n.Reachable {
		t.Errorf("basic bastion: status %q reachable %v", n.Status, n.Reachable)
	}
	if !strings.Contains(n.Hint, "Basic") || !strings.Contains(n.Hint, "Standard") {
		t.Errorf("basic hint should name the SKU and the upgrade, got %q", n.Hint)
	}

	n = running()
	classify(&n, []gateway{{name: "std-b", group: "rg", sku: "Standard", running: true}}, "vnet-a")
	if n.Status != StatusNoTunnel {
		t.Errorf("tunnelling off: status %q", n.Status)
	}
	if !strings.Contains(n.Hint, "enable-tunneling") || strings.Contains(n.Hint, "--sku") {
		t.Errorf("tunnelling hint should offer only the tunnelling fix, got %q", n.Hint)
	}
}

// A stopped machine is stopped whatever the network looks like, and saying
// "no tunnel" about it would send someone to the wrong place entirely.
func TestClassifyAsksAboutTheMachineFirst(t *testing.T) {
	basic := []gateway{{name: "b", sku: "Basic", running: true}}
	for _, tc := range []struct{ state, want string }{
		{"deallocated", inventory.StatusStopped},
		{"VM deallocated", inventory.StatusStopped},
		{"stopped", inventory.StatusStopped},
		{"deleting", inventory.StatusTerminated},
		{"", inventory.StatusUnknown},
	} {
		n := inventory.Node{State: tc.state}
		classify(&n, basic, "vnet-a")
		if n.Status != tc.want {
			t.Errorf("state %q: status %q, want %q", tc.state, n.Status, tc.want)
		}
	}
}

// Which bastion gets used must not depend on the order the API answered in.
func TestGatewayOrderingPrefersUsable(t *testing.T) {
	out := map[string][]gateway{}
	_ = out
	gws := []gateway{
		{name: "zeta", sku: "Standard", tunnel: true, running: true},
		{name: "alpha", sku: "Basic", running: true},
	}
	// gatewaysByVNet sorts; replicate its comparison on a known slice.
	sortGateways(gws)
	if gws[0].name != "zeta" {
		t.Errorf("first is %q, want the usable one even though it sorts later by name", gws[0].name)
	}
}

// The remedy has to name the command that fixes the half that is wrong.
func TestSkuRemedy(t *testing.T) {
	basic := skuRemedy("b", "rg", "Basic")
	if !strings.Contains(basic, "--sku Standard") || !strings.Contains(basic, "--enable-tunneling true") {
		t.Errorf("basic remedy = %q", basic)
	}
	off := skuRemedy("b", "rg", "Standard")
	if strings.Contains(off, "--sku") || !strings.Contains(off, "--enable-tunneling true") {
		t.Errorf("tunnelling remedy = %q", off)
	}
}

// The az CLI writes this file with a byte order mark on some platforms, which
// json.Unmarshal rejects outright.
func TestSubscriptionsHandlesBOMAndSkipsDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AZURE_CONFIG_DIR", dir)
	body := "\ufeff" + `{"subscriptions":[
		{"id":"1","name":"Live","state":"Enabled","isDefault":true},
		{"id":"2","name":"Old","state":"Disabled","isDefault":false},
		{"id":"3","name":"Other","state":"Enabled","isDefault":false}]}`
	if err := os.WriteFile(filepath.Join(dir, "azureProfile.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Subscriptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want the two enabled ones: %+v", len(got), got)
	}
	d, err := defaultSubscription()
	if err != nil || d.Name != "Live" {
		t.Errorf("default = %+v err %v", d, err)
	}
	// A name is as good as an ID, because nobody remembers the ID.
	if s, err := resolve("Other"); err != nil || s.ID != "3" {
		t.Errorf("resolve by name = %+v err %v", s, err)
	}
	if s, err := resolve("1"); err != nil || s.Name != "Live" {
		t.Errorf("resolve by id = %+v err %v", s, err)
	}
	if _, err := resolve("Nope"); err == nil {
		t.Error("an unknown subscription must be an error")
	}
}

// Detection must stay a filesystem question, so it can run for a cloud the
// user has never touched.
func TestConfiguredIsLocalOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AZURE_CONFIG_DIR", dir)
	t.Setenv("AZURE_CLIENT_ID", "")
	t.Setenv("AZURE_TENANT_ID", "")
	if Configured() {
		t.Error("an empty config dir is not a sign-in")
	}
	if err := os.WriteFile(filepath.Join(dir, "azureProfile.json"), []byte(`{"subscriptions":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Configured() {
		t.Error("a profile written by az counts as configured")
	}
}

// The printed command must be one the user can paste and run.
func TestPrintableQuotesOnlyWhatNeedsIt(t *testing.T) {
	got := printable([]string{"network", "bastion", "ssh", "--name", "my bastion"})
	if got != `az network bastion ssh --name "my bastion"` {
		t.Errorf("got %q", got)
	}
}

// Password is the default because it works everywhere without preparation,
// and it is safe: az reads it from the terminal, so it never passes through
// ssmate or an argument list.
func TestLoginArgs(t *testing.T) {
	for _, tc := range []struct {
		auth, key string
		want      []string
		fails     bool
	}{
		{"", "", []string{"--auth-type", "password", "--username", "ubuntu"}, false},
		{"password", "", []string{"--auth-type", "password", "--username", "ubuntu"}, false},
		{"key", "/k", []string{"--auth-type", "ssh-key", "--username", "ubuntu", "--ssh-key", "/k"}, false},
		{"KEY", "/k", []string{"--auth-type", "ssh-key", "--username", "ubuntu", "--ssh-key", "/k"}, false},
		{"aad", "", []string{"--auth-type", "AAD"}, false},
		// Key auth with no key would hand az a login it cannot complete.
		{"key", "", nil, true},
		{"kerberos", "", nil, true},
	} {
		got, err := loginArgs(tc.auth, tc.key, "ubuntu")
		if tc.fails {
			if err == nil {
				t.Errorf("auth %q key %q should be refused", tc.auth, tc.key)
			}
			continue
		}
		if err != nil {
			t.Errorf("auth %q: %v", tc.auth, err)
			continue
		}
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("auth %q = %v, want %v", tc.auth, got, tc.want)
		}
	}
}

// A network's identity is its resource group and name together. Network names
// repeat across resource groups, so the name alone would join a machine to a
// bastion in an unrelated network.
func TestVnetKeyIncludesTheResourceGroup(t *testing.T) {
	a := "/subscriptions/s/resourceGroups/Team-A/providers/Microsoft.Network/virtualNetworks/vnet-default/subnets/x"
	b := "/subscriptions/s/resourceGroups/team-b/providers/Microsoft.Network/virtualNetworks/vnet-default/subnets/x"
	if vnetKey(a) == vnetKey(b) {
		t.Error("two networks with one name in different groups must not share a key")
	}
	if vnetKey(a) != "team-a/vnet-default" {
		t.Errorf("key = %q", vnetKey(a))
	}
	// A peering's remote ID points at the network itself, with no subnet.
	net := "/subscriptions/s/resourceGroups/TEAM-A/providers/Microsoft.Network/virtualNetworks/vnet-default"
	if vnetKey(net) != vnetKey(a) {
		t.Error("a network's own ID and a subnet beneath it must give the same key")
	}
}

// Azure Bastion reaches machines in peered networks. Looking only at the
// machine's own network made fifty machines read "no gateway" while a hub
// bastion could reach every one.
func TestBastionReachesThroughPeering(t *testing.T) {
	hub := gateway{name: "hub-bastion", vnet: "vnet-hub", sku: "Standard", tunnel: true, running: true}
	own := map[string][]gateway{"rg/vnet-hub": {hub}}
	peers := map[string][]string{"rg/vnet-hub": {"rg/vnet-spoke"}}

	got := reachThroughPeering(own, peers)
	spoke := got["rg/vnet-spoke"]
	if len(spoke) != 1 || spoke[0].name != "hub-bastion" || !spoke[0].peered {
		t.Fatalf("the spoke should be reachable through the hub bastion: %+v", spoke)
	}
	if hubGws := got["rg/vnet-hub"]; len(hubGws) != 1 || hubGws[0].peered {
		t.Errorf("the hub's own bastion is not peered: %+v", hubGws)
	}
	// A network with no bastion and no peering stays unreachable.
	if len(got["rg/vnet-island"]) != 0 {
		t.Error("an unpeered network gained a bastion")
	}
}

// A bastion in the machine's own network is preferred to one reaching it
// through peering, so taking a peering away never changes which bastion a
// directly served machine uses.
func TestOwnNetworkBastionIsPreferredToAPeeredOne(t *testing.T) {
	local := gateway{name: "z-local", sku: "Standard", tunnel: true, running: true}
	hub := gateway{name: "a-hub", sku: "Standard", tunnel: true, running: true}
	got := reachThroughPeering(
		map[string][]gateway{"rg/spoke": {local}, "rg/hub": {hub}},
		map[string][]string{"rg/hub": {"rg/spoke"}},
	)
	if first := got["rg/spoke"][0]; first.name != "z-local" {
		t.Errorf("the spoke's own bastion should come first, got %q", first.name)
	}
}

// A peered bastion that cannot carry a terminal session is still no use, and
// classify has to say so exactly as it would for a local one.
func TestPeeredBasicBastionIsStillNoTunnel(t *testing.T) {
	basic := gateway{name: "hub", group: "rg", sku: "Basic", running: true, peered: true}
	n := inventory.Node{State: "running"}
	classify(&n, []gateway{basic}, "vnet-spoke")
	if n.Status != StatusNoTunnel {
		t.Errorf("status = %q", n.Status)
	}
}

// The verdict lands on the Azure Bastion route, and the node's own summary is
// derived from it.
func TestClassifyRecordsTheBastionRoute(t *testing.T) {
	n := inventory.Node{ID: "/vm/db-prod", Account: "sub", Region: "westeurope", State: "VM running"}
	classify(&n, []gateway{{name: "bas", sku: "Standard", tunnel: true, running: true}}, "vnet-app")
	if len(n.Routes) != 1 {
		t.Fatalf("routes = %+v", n.Routes)
	}
	r := n.Routes[0]
	if r.Kind != inventory.RouteAzureBastion || !r.Ready || r.Target != n.ID || r.Account != "sub" || r.Region != "westeurope" {
		t.Errorf("route = %+v", r)
	}
	if !n.Reachable || n.Via != "bastion" {
		t.Errorf("summary: reachable %v via %q", n.Reachable, n.Via)
	}

	off := inventory.Node{ID: "/vm/x", State: "VM deallocated"}
	classify(&off, nil, "")
	if !off.Down || off.Status != inventory.StatusStopped || len(off.Routes) != 1 || off.Routes[0].Status != inventory.StatusStopped {
		t.Errorf("stopped: down %v status %q routes %+v", off.Down, off.Status, off.Routes)
	}
}

// The hostname and the VM ID are what a registration from inside the machine
// knows it by, so both are recorded.
func TestBuildNodeRecordsHostnameAndVMID(t *testing.T) {
	vm := &armcompute.VirtualMachine{
		ID:       to.Ptr("/subscriptions/s/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/db-prod"),
		Name:     to.Ptr("db-prod"),
		Location: to.Ptr("westeurope"),
		Properties: &armcompute.VirtualMachineProperties{
			VMID:      to.Ptr("0f1e2d3c-aaaa"),
			OSProfile: &armcompute.OSProfile{ComputerName: to.Ptr("dbprod01")},
		},
	}
	n := buildNode(vm, Subscription{Name: "contoso"}, map[string]string{}, map[string]nic{}, map[string][]gateway{})
	if n.Hostname != "dbprod01" || len(n.Aliases) != 1 || n.Aliases[0] != "0f1e2d3c-aaaa" {
		t.Errorf("hostname %q aliases %v", n.Hostname, n.Aliases)
	}
}
