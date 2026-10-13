package azure

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"github.com/Vishnu-Rr/ssmate/internal/doctor"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

// Checks returns Azure's diagnosis.
//
// Local questions first, so a missing CLI or extension is never reported as a
// cloud problem - which matters more here than elsewhere, because the CLI is
// not a convenience on this cloud, it is the data plane.
func (p Provider) Checks(ctx context.Context, n *inventory.Node, query string) doctor.Registry {
	account := ""
	if n != nil {
		account = n.Account
	}
	// An Arc record is a machine outside Azure, reached by az ssh arc rather
	// than by a bastion, so it needs the other extension and none of the
	// bastion's checks.
	if n != nil && n.Outside != nil {
		return doctor.Registry{
			cliCheck(),
			extensionCheck("ssh"),
			signInCheck(account),
			arcAgentCheck(*n),
		}
	}
	reg := doctor.Registry{
		cliCheck(),
		extensionCheck("bastion"),
		signInCheck(account),
	}
	if query != "" {
		reg = append(reg, targetChecks(n, query)...)
		if n != nil && !strings.EqualFold(n.OS, "Windows") {
			reg = append(reg, vmLoginCheck(*n))
		}
	}
	return reg
}

// vmLoginCheck says how the VM itself will be logged in to. With Microsoft's
// Entra login extension you sign in as yourself and nothing secret exists;
// without it the VM asks for its password, and the fix is three commands an
// administrator runs once, printed here rather than run.
func vmLoginCheck(n inventory.Node) doctor.Check {
	name := "sign-in on the machine"
	return doctor.Check{
		Name: name,
		Run: func(context.Context) doctor.Result {
			if v, _ := n.Detailed("login"); v == entraLogin {
				return doctor.Result{Name: name, Severity: doctor.Pass,
					Detail: "Microsoft sign-in (Entra ID); you need Virtual Machine User Login on it, " +
						"or Virtual Machine Administrator Login for sudo"}
			}
			group, _ := n.Detailed("resource_group")
			vm := nameOf(n.ID)
			scope := n.ID
			if group != "" {
				scope = strings.SplitN(n.ID, "/providers/", 2)[0]
			}
			return doctor.Result{
				Name: name, Severity: doctor.Warn,
				Detail: "the VM will ask for a password, or take your key with --key; Microsoft sign-in is not enabled on it",
				Remedy: "to sign in as yourself with no password, enable Microsoft sign-in once:\n" +
					"    az vm identity assign --resource-group " + group + " --name " + vm + "\n" +
					"    az vm extension set --publisher Microsoft.Azure.ActiveDirectory --name AADSSHLoginForLinux \\\n" +
					"        --resource-group " + group + " --vm-name " + vm + "\n" +
					"    az role assignment create --role \"Virtual Machine User Login\" --assignee <you@example.com> \\\n" +
					"        --scope " + scope,
			}
		},
	}
}

// arcAgentCheck reports whether the machine's Arc agent is connected, which is
// the one thing about an Arc session that can be known before trying it.
func arcAgentCheck(n inventory.Node) doctor.Check {
	name := "arc agent connected"
	return doctor.Check{
		Name: name,
		Run: func(context.Context) doctor.Result {
			if n.State == "Connected" {
				return doctor.Result{Name: name, Severity: doctor.Pass,
					Detail: fmt.Sprintf("%s in %s", n.ID, n.Where())}
			}
			state := strings.ToLower(n.State)
			if state == "" {
				state = "unknown"
			}
			return doctor.Result{
				Name: name, Severity: doctor.Fail,
				Detail: "the agent is " + state,
				Remedy: "on the machine, azcmagent show says why it is not connected",
			}
		},
	}
}

// cliCheck looks for the binary that actually moves the bytes.
func cliCheck() doctor.Check {
	name := "azure cli"
	return doctor.Check{
		Name: name,
		Run: func(context.Context) doctor.Result {
			path, err := exec.LookPath("az")
			if err != nil {
				return doctor.Result{
					Name: name, Severity: doctor.Fail, Detail: "not found on PATH",
					Remedy: "install it: https://learn.microsoft.com/cli/azure/install-azure-cli",
				}
			}
			return doctor.Result{Name: name, Severity: doctor.Pass, Detail: path}
		},
	}
}

// extensionCheck looks for the az extension a session needs: bastion for
// Azure's own machines, ssh for Arc.
//
// This is not an optional add-on here. Without it the connect commands do not
// exist, and the CLI's own error for a missing command reads like a bad
// argument rather than a missing install.
func extensionCheck(ext string) doctor.Check {
	name := ext + " extension"
	return doctor.Check{
		Name: name,
		Run: func(ctx context.Context) doctor.Result {
			bin, err := exec.LookPath("az")
			if err != nil {
				return doctor.Result{Name: name, Severity: doctor.Warn, Detail: "no az to ask"}
			}
			out, err := exec.CommandContext(ctx, bin, "extension", "show",
				"--name", ext, "--query", "version", "-o", "tsv").Output()
			version := strings.TrimSpace(string(out))
			if err != nil || version == "" {
				return doctor.Result{
					Name: name, Severity: doctor.Fail, Detail: "not installed",
					Remedy: "az extension add --name " + ext,
				}
			}
			return doctor.Result{Name: name, Severity: doctor.Pass, Detail: version}
		},
	}
}

// signInCheck makes the one call that proves the sign-in works.
func signInCheck(account string) doctor.Check {
	name := "azure sign-in"
	return doctor.Check{
		Name: name,
		Run: func(ctx context.Context) doctor.Result {
			if !Configured() {
				return doctor.Result{
					Name: name, Severity: doctor.Fail,
					Detail: "no Azure sign-in found", Remedy: "az login",
				}
			}
			sub, err := resolve(account)
			if err != nil {
				return doctor.Result{Name: name, Severity: doctor.Fail, Detail: err.Error()}
			}
			cred, err := credential()
			if err != nil {
				return doctor.Result{Name: name, Severity: doctor.Fail,
					Detail: credentialProblem(err).Error(), Remedy: "az login"}
			}
			c, err := armsubscriptions.NewClient(cred, nil)
			if err != nil {
				return doctor.Result{Name: name, Severity: doctor.Fail, Detail: err.Error()}
			}
			if _, err := c.Get(ctx, sub.ID, nil); err != nil {
				return doctor.Result{
					Name: name, Severity: doctor.Fail,
					Detail: firstLine(credentialProblem(err).Error()), Remedy: "az login",
				}
			}
			return doctor.Result{
				Name: name, Severity: doctor.Pass,
				Detail: fmt.Sprintf("subscription %s", sub.Name),
			}
		},
	}
}

// targetChecks diagnose one machine.
//
// The one that earns its place is the bastion SKU. Everything about a Basic
// bastion looks healthy - it is running, it is in the right network, it works
// in a browser - and it still cannot carry a terminal session. Without this
// check that failure arrives as a bare error from az, after the user has
// already gone looking for the resource ID.
func targetChecks(n *inventory.Node, query string) doctor.Registry {
	return doctor.Registry{
		{
			Name: "target exists",
			Run: func(context.Context) doctor.Result {
				if n == nil {
					return doctor.Result{
						Name: "target exists", Severity: doctor.Fail,
						Detail: fmt.Sprintf("nothing matches %q", query),
						Remedy: "ssmate ls --refresh",
					}
				}
				return doctor.Result{
					Name: "target exists", Severity: doctor.Pass,
					Detail: fmt.Sprintf("%s in %s", n.DisplayName(), n.Where()),
				}
			},
		},
		{
			Name: "machine running",
			Run: func(context.Context) doctor.Result {
				name := "machine running"
				if n == nil {
					return doctor.Result{Name: name, Severity: doctor.Warn, Detail: "no target to check"}
				}
				switch {
				case n.State == "":
					return doctor.Result{Name: name, Severity: doctor.Warn, Detail: "unknown power state"}
				case isStopped(n.State):
					group, _ := n.Detailed("resource_group")
					return doctor.Result{
						Name: name, Severity: doctor.Fail, Detail: n.State,
						Remedy: fmt.Sprintf("az vm start --name %s --resource-group %s", n.Name, group),
					}
				}
				return doctor.Result{Name: name, Severity: doctor.Pass, Detail: n.State}
			},
		},
		{
			Name: "bastion serves this network",
			Run: func(context.Context) doctor.Result {
				name := "bastion serves this network"
				if n == nil {
					return doctor.Result{Name: name, Severity: doctor.Warn, Detail: "no target to check"}
				}
				b, _ := n.Detailed("bastion")
				if b == "" {
					vnet, _ := n.Detailed("vnet")
					detail := "no bastion in this machine's virtual network"
					if vnet != "" {
						detail = "no bastion in " + vnet
					}
					return doctor.Result{
						Name: name, Severity: doctor.Fail, Detail: detail,
						Remedy: "create one in that network, Standard SKU with tunnelling enabled",
					}
				}
				if via, ok := n.Detailed("bastion_via"); ok && via != "" {
					// Worth saying: this machine depends on a peering, and if
					// the peering goes the machine goes with it.
					return doctor.Result{Name: name, Severity: doctor.Pass,
						Detail: fmt.Sprintf("%s, through peering with %s", b, via)}
				}
				return doctor.Result{Name: name, Severity: doctor.Pass, Detail: b}
			},
		},
		{
			Name: "bastion can carry a terminal session",
			Run: func(context.Context) doctor.Result {
				name := "bastion can carry a terminal session"
				if n == nil {
					return doctor.Result{Name: name, Severity: doctor.Warn, Detail: "no target to check"}
				}
				b, _ := n.Detailed("bastion")
				if b == "" {
					return doctor.Result{Name: name, Severity: doctor.Warn, Detail: "no bastion to check"}
				}
				sku, _ := n.Detailed("bastion_sku")
				group, _ := n.Detailed("bastion_group")
				tunnel, _ := n.Detailed("bastion_tunnel")
				if strings.EqualFold(sku, "Basic") || tunnel != "true" {
					return doctor.Result{
						Name: name, Severity: doctor.Fail,
						Detail: fmt.Sprintf("%s is %s with tunnelling %s", b, sku, tunnelWord(tunnel)),
						Remedy: skuRemedy(b, group, sku),
					}
				}
				return doctor.Result{
					Name: name, Severity: doctor.Pass,
					Detail: fmt.Sprintf("%s is %s with tunnelling on", b, sku),
				}
			},
		},
	}
}

func tunnelWord(v string) string {
	if v == "true" {
		return "on"
	}
	return "off"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
