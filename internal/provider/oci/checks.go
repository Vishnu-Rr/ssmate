package oci

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/Vishnu-Rr/ssmate/internal/doctor"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/oracle/oci-go-sdk/v65/bastion"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

// Checks returns Oracle's diagnosis.
//
// The order is deliberate and mirrors the Amazon one: everything answerable
// without the network comes first, so a missing ssh or an absent config file
// is never reported as a cloud problem.
func (p Provider) Checks(ctx context.Context, n *inventory.Node, query string) doctor.Registry {
	profile := DefaultProfile
	if n != nil && n.Account != "" {
		profile = n.Account
	}
	reg := doctor.Registry{
		sshCheck(),
		configCheck(),
		credentialsCheck(profile),
	}
	if query != "" {
		reg = append(reg, targetChecks(n, query)...)
	}
	return reg
}

// sshCheck looks for the binary that actually moves the bytes. OCI has no
// plugin of its own; ssh is the whole data plane.
func sshCheck() doctor.Check {
	return doctor.Check{
		Name: "ssh client",
		Run: func(context.Context) doctor.Result {
			path, err := exec.LookPath("ssh")
			if err != nil {
				return doctor.Result{
					Name: "ssh client", Severity: doctor.Fail,
					Detail: "not found on PATH", Remedy: sshInstallHint(),
				}
			}
			return doctor.Result{Name: "ssh client", Severity: doctor.Pass, Detail: path}
		},
	}
}

func configCheck() doctor.Check {
	return doctor.Check{
		Name: "oci config",
		Run: func(context.Context) doctor.Result {
			path := ConfigPath()
			if !Configured() {
				return doctor.Result{
					Name: "oci config", Severity: doctor.Fail,
					Detail: "no config at " + path, Remedy: "oci setup config",
				}
			}
			profiles, err := Profiles(path)
			if err != nil {
				return doctor.Result{
					Name: "oci config", Severity: doctor.Fail,
					Detail: firstLine(err.Error()),
				}
			}
			var names []string
			for _, pr := range profiles {
				label := pr.Name
				if pr.Token {
					label += " (session)"
				}
				names = append(names, label)
			}
			return doctor.Result{
				Name: "oci config", Severity: doctor.Pass,
				Detail: fmt.Sprintf("%d profiles: %s", len(names), strings.Join(names, ", ")),
			}
		},
	}
}

// credentialsCheck makes the one call that proves the credentials work, and
// translates the refusal into the command that fixes it. A session token and
// an API key fail identically on the wire and need opposite remedies.
func credentialsCheck(profile string) doctor.Check {
	name := "oci credentials"
	return doctor.Check{
		Name: name,
		Run: func(ctx context.Context) doctor.Result {
			cp, err := credentials(profile)
			if err != nil {
				return doctor.Result{Name: name, Severity: doctor.Fail, Detail: firstLine(err.Error())}
			}
			tenancy, err := cp.TenancyOCID()
			if err != nil {
				return doctor.Result{
					Name: name, Severity: doctor.Fail,
					Detail: fmt.Sprintf("profile %s declares no tenancy", profile),
				}
			}
			c, err := identity.NewIdentityClientWithConfigurationProvider(cp)
			if err != nil {
				return doctor.Result{Name: name, Severity: doctor.Fail, Detail: firstLine(err.Error())}
			}
			resp, err := c.ListRegionSubscriptions(ctx,
				identity.ListRegionSubscriptionsRequest{TenancyId: &tenancy})
			if err != nil {
				problem := credentialProblem(profile, err)
				remedy := ""
				if msg := problem.Error(); strings.Contains(msg, "run: ") {
					_, remedy, _ = strings.Cut(msg, "run: ")
				}
				return doctor.Result{
					Name: name, Severity: doctor.Fail,
					Detail: firstLine(problem.Error()), Remedy: remedy,
				}
			}
			return doctor.Result{
				Name: name, Severity: doctor.Pass,
				Detail: fmt.Sprintf("profile %s, %d subscribed regions", profile, len(resp.Items)),
			}
		},
	}
}

// targetChecks diagnose one machine.
//
// They answer a different question from Amazon's. There is no agent to be
// online, so nothing here asks about the machine's health: the questions are
// whether a door exists into its network, whether that door is open, and
// whether there is room to walk through it.
func targetChecks(n *inventory.Node, query string) doctor.Registry {
	reg := baseTargetChecks(n, query)
	if n != nil {
		if _, judged := n.Detailed("reach"); judged {
			reg = append(reg, reachCheck(*n))
		}
		if _, on := n.Detailed("bastion_plugin"); on {
			reg = append(reg, managedCheck(*n))
		}
	}
	return reg
}

// managedCheck reports on the key-free way in, for a machine whose Bastion
// plugin someone switched on.
//
// Oracle documents managed SSH as unsupported on Arm machines unless they run
// Oracle Linux. That is passed on as a caution rather than a verdict, because
// what Oracle documents did not predict what worked when it was measured: a
// machine documented as unsupported connected, and one documented as
// supported did not.
func managedCheck(n inventory.Node) doctor.Check {
	name := "managed SSH (no key needed)"
	return doctor.Check{
		Name: name,
		Run: func(context.Context) doctor.Result {
			var r inventory.Route
			for _, x := range n.Routes {
				if x.Kind == inventory.RouteOCIManaged {
					r = x
				}
			}
			if !r.Ready {
				status := r.Status
				if status == "" {
					status = "not available"
				}
				return doctor.Result{Name: name, Severity: doctor.Warn, Detail: status, Remedy: r.Hint}
			}
			shape, _ := n.Detailed("shape")
			os, _ := n.Detailed("os")
			if strings.Contains(shape, ".A") && !strings.Contains(strings.ToLower(os), "oracle linux") {
				return doctor.Result{
					Name: name, Severity: doctor.Warn,
					Detail: "the Bastion plugin is running, but Oracle documents managed SSH as unsupported on " +
						shape + " unless it runs Oracle Linux; it may still work",
					Remedy: "ssmate connect " + n.DisplayName() + " --via bastion   # takes a few minutes to open",
				}
			}
			return doctor.Result{
				Name: name, Severity: doctor.Pass,
				Detail: "the Bastion plugin is running; ssmate connect " + n.DisplayName() + " --via bastion",
			}
		},
	}
}

// reachCheck reports whether any bastion can carry a connection to the
// machine, judged from each bastion's allowlist and the security rules
// between it and the machine. A verdict from the rules is not a session, so
// an unverified one points at --probe, which tries one.
func reachCheck(n inventory.Node) doctor.Check {
	name := "a bastion can reach the machine"
	return doctor.Check{
		Name: name,
		Run: func(context.Context) doctor.Result {
			reach, _ := n.Detailed("reach")
			bName, _ := n.Detailed("bastion_name")
			switch reach {
			case "open":
				return doctor.Result{Name: name, Severity: doctor.Pass,
					Detail: fmt.Sprintf("%s admits clients from the internet and its rules allow port 22 to %s", bName, n.PrivateIP)}
			case reachBlocked:
				why, _ := n.Detailed("reach_why")
				return doctor.Result{
					Name: name, Severity: doctor.Fail, Detail: why,
					Remedy: "open one bastion's way to the machine: its client allowlist, and the security rules\n" +
						"between its subnet and the machine on port 22. Or give the machine a way in that\n" +
						"exposes nothing, below.",
				}
			}
			return doctor.Result{
				Name: name, Severity: doctor.Warn,
				Detail: "some of the rules on the way name a service or a security group, which cannot be judged from here",
				Remedy: "ssmate doctor " + n.DisplayName() + " --probe   # tries a real session through the bastion",
			}
		},
	}
}

func baseTargetChecks(n *inventory.Node, query string) doctor.Registry {
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
					Detail: fmt.Sprintf("%s in %s", n.ID, n.Where()),
				}
			},
		},
		{
			Name: "instance running",
			Run: func(context.Context) doctor.Result {
				if n == nil {
					return doctor.Result{Name: "instance running", Severity: doctor.Warn, Detail: "no target to check"}
				}
				if n.State == "RUNNING" {
					return doctor.Result{Name: "instance running", Severity: doctor.Pass, Detail: "RUNNING"}
				}
				return doctor.Result{
					Name: "instance running", Severity: doctor.Fail,
					Detail: n.State, Remedy: "oci compute instance action --action START --instance-id " + n.ID,
				}
			},
		},
		{
			Name: "a bastion serves this network",
			Run: func(context.Context) doctor.Result {
				name := "a bastion serves this network"
				if n == nil {
					return doctor.Result{Name: name, Severity: doctor.Warn, Detail: "no target to check"}
				}
				bName, _ := n.Detailed("bastion_name")
				state, _ := n.Detailed("bastion_state")
				vcn, hasVCN := n.Detailed("vcn_name")
				if bName == "" {
					detail := "no bastion in this machine's VCN"
					if hasVCN {
						detail = "no bastion in " + vcn
					}
					return doctor.Result{
						Name: name, Severity: doctor.Fail, Detail: detail,
						Remedy: "create a bastion bound to that VCN, with a private endpoint in a subnet that can reach the target",
					}
				}
				if state != string(bastion.BastionLifecycleStateActive) {
					return doctor.Result{
						Name: name, Severity: doctor.Fail,
						Detail: fmt.Sprintf("bastion %s is %s", bName, state),
					}
				}
				return doctor.Result{Name: name, Severity: doctor.Pass, Detail: bName}
			},
		},
		{
			Name: "bastion admits this machine",
			Run: func(ctx context.Context) doctor.Result {
				name := "bastion admits this machine"
				if n == nil {
					return doctor.Result{Name: name, Severity: doctor.Warn, Detail: "no target to check"}
				}
				allow, ok := n.Detailed("bastion_allow")
				if !ok || allow == "" {
					return doctor.Result{
						Name: name, Severity: doctor.Warn,
						Detail: "the bastion declares no client allowlist",
					}
				}
				blocks := strings.Split(allow, ",")
				if slices.Contains(blocks, "0.0.0.0/0") {
					return doctor.Result{Name: name, Severity: doctor.Pass, Detail: "open to any address"}
				}
				ip, err := publicIP(ctx)
				if err != nil {
					return doctor.Result{
						Name: name, Severity: doctor.Warn,
						Detail: "allows " + allow + "; could not determine this machine's public address",
					}
				}
				if cidrsContain(blocks, ip) {
					return doctor.Result{
						Name: name, Severity: doctor.Pass,
						Detail: fmt.Sprintf("%s is within %s", ip, allow),
					}
				}
				return doctor.Result{
					Name: name, Severity: doctor.Fail,
					Detail: fmt.Sprintf("this machine is %s, the bastion admits only %s", ip, allow),
					Remedy: "add your address to the bastion's client allowlist; until then every " +
						"connection fails as \"Permission denied (publickey)\", which looks like a key problem",
				}
			},
		},
		{
			Name: "login user known",
			Run: func(context.Context) doctor.Result {
				name := "login user known"
				if n == nil {
					return doctor.Result{Name: name, Severity: doctor.Warn, Detail: "no target to check"}
				}
				if u, ok := n.Detailed("login_user"); ok && u != "" {
					os, _ := n.Detailed("os")
					return doctor.Result{
						Name: name, Severity: doctor.Pass,
						Detail: fmt.Sprintf("%s, from image %q", u, os),
					}
				}
				// Not a failure: the user can say. But they have to be told
				// they will need to, rather than finding out from a refused
				// login that looks like a key problem.
				return doctor.Result{
					Name: name, Severity: doctor.Warn,
					Detail: "the image does not imply a login account",
					Remedy: "ssmate connect " + n.ID + " --user <name>",
				}
			},
		},
	}
}

// firstLine keeps a multi-line SDK error readable on one line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// cidrsContain reports whether any block covers this address.
func cidrsContain(blocks []string, ip string) bool {
	addr := net.ParseIP(strings.TrimSpace(ip))
	if addr == nil {
		return false
	}
	for _, b := range blocks {
		_, network, err := net.ParseCIDR(strings.TrimSpace(b))
		if err != nil {
			continue
		}
		if network.Contains(addr) {
			return true
		}
	}
	return false
}

// publicIP asks what address this machine reaches the internet from.
//
// This is the one check that talks to something outside the cloud, and it only
// runs when the user asked for a diagnosis of a specific machine - never
// during a sweep. Nothing is sent: the response is the request's own source
// address.
func publicIP(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://checkip.amazonaws.com", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}
