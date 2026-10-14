package azure

import (
	"context"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/Vishnu-Rr/ssmate/internal/doctor"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
)

const vmID = "/subscriptions/s/resourceGroups/rg-app/providers/Microsoft.Compute/virtualMachines/db-prod"

func vmWith(extensions ...string) *armcompute.VirtualMachine {
	vm := &armcompute.VirtualMachine{
		ID: to.Ptr(vmID), Name: to.Ptr("db-prod"), Location: to.Ptr("westeurope"),
		Properties: &armcompute.VirtualMachineProperties{
			StorageProfile: &armcompute.StorageProfile{OSDisk: &armcompute.OSDisk{OSType: to.Ptr(armcompute.OperatingSystemTypesLinux)}},
			OSProfile:      &armcompute.OSProfile{AdminUsername: to.Ptr("azureuser")},
		},
	}
	for _, e := range extensions {
		vm.Resources = append(vm.Resources, &armcompute.VirtualMachineExtension{ID: to.Ptr(vmID + "/extensions/" + e)})
	}
	return vm
}

func TestBuildNodeNoticesEntraLogin(t *testing.T) {
	n := buildNode(vmWith("enablevmaccess", "aadsshloginforlinux"), Subscription{Name: "contoso"}, map[string]string{}, map[string]nic{}, map[string][]gateway{})
	if v, _ := n.Detailed("login"); v != entraLogin {
		t.Errorf("login = %q", v)
	}
	if n.OS != "Linux" {
		t.Errorf("os = %q", n.OS)
	}
	plain := buildNode(vmWith("enablevmaccess"), Subscription{}, map[string]string{}, map[string]nic{}, map[string][]gateway{})
	if _, ok := plain.Detailed("login"); ok {
		t.Error("no extension, no Entra login")
	}
}

func TestResolveLogin(t *testing.T) {
	entra := inventory.Node{Name: "db-prod", Detail: map[string]string{"login": entraLogin, "login_user": "azureuser"}}
	plain := inventory.Node{Name: "db-prod", Detail: map[string]string{"login_user": "azureuser"}}
	nobody := inventory.Node{Name: "db-prod", Detail: map[string]string{}}
	cases := []struct {
		name       string
		n          inventory.Node
		o          provider.SessionOptions
		auth, user string
		err        bool
	}{
		{"entra by default", entra, provider.SessionOptions{}, "aad", "", false},
		{"a named user means that local account", entra, provider.SessionOptions{User: "ops"}, "", "ops", false},
		{"asked for a password", entra, provider.SessionOptions{Auth: "password"}, "password", "azureuser", false},
		{"a key means key login", entra, provider.SessionOptions{Key: "/k"}, "key", "azureuser", false},
		{"no extension falls back to the password", plain, provider.SessionOptions{}, "", "azureuser", false},
		{"aad can be asked for anywhere", plain, provider.SessionOptions{Auth: "AAD"}, "aad", "", false},
		{"no user known", nobody, provider.SessionOptions{}, "", "", true},
	}
	for _, c := range cases {
		auth, user, err := resolveLogin(c.n, c.o)
		if (err != nil) != c.err || auth != c.auth || user != c.user {
			t.Errorf("%s: got %q %q %v", c.name, auth, user, err)
		}
	}
}

func TestEntraShellPrint(t *testing.T) {
	n := inventory.Node{ID: vmID, Name: "db-prod", Account: "contoso", Detail: map[string]string{
		"login": entraLogin, "login_user": "azureuser",
		"bastion": "bas", "bastion_group": "rg-net", "bastion_sku": "Standard", "bastion_tunnel": "true"}}
	out := capture(t, func() error {
		return Handler{}.Shell(context.Background(), n, inventory.Route{}, provider.SessionOptions{Print: true})
	})
	for _, want := range []string{
		"az network bastion tunnel --name bas --resource-group rg-net",
		"az ssh cert --file",
		"CertificateFile=",
		"StrictHostKeyChecking=accept-new",
		"<account in the certificate>@127.0.0.1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("print lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "az az") || strings.Contains(out, "StrictHostKeyChecking=no") {
		t.Errorf("print: %s", out)
	}
}

func TestVMLoginCheck(t *testing.T) {
	entra := inventory.Node{ID: vmID, Detail: map[string]string{"login": entraLogin, "resource_group": "rg-app"}}
	if r := vmLoginCheck(entra).Run(context.Background()); r.Severity != doctor.Pass {
		t.Errorf("entra: %+v", r)
	}
	plain := inventory.Node{ID: vmID, Detail: map[string]string{"resource_group": "rg-app"}}
	r := vmLoginCheck(plain).Run(context.Background())
	for _, want := range []string{
		"az vm identity assign --resource-group rg-app --name db-prod",
		"--name AADSSHLoginForLinux",
		`--role "Virtual Machine User Login"`,
		"--scope /subscriptions/s/resourceGroups/rg-app",
	} {
		if r.Severity != doctor.Warn || !strings.Contains(r.Remedy, want) {
			t.Errorf("remedy missing %q:\n%s", want, r.Remedy)
		}
	}
}
