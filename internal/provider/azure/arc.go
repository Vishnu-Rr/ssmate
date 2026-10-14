package azure

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/hybridcompute/armhybridcompute/v3"
	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/session"
	"github.com/adrg/xdg"
)

// Azure Arc reaches machines that are not in Azure at all.
//
// A server in another cloud or a rack runs Microsoft's Connected Machine
// agent, which connects out to Azure; `az ssh arc` then carries an SSH session
// back through that connection. Nothing on the machine listens to the network
// and it needs no public address, which makes Arc, like Session Manager, a way
// in that exposes nothing. Arc does not take Azure's own virtual machines, so
// every Arc record is a machine that lives somewhere else.
//
// The machine still wants a login, which Session Manager does not: Microsoft
// sign-in where the machine has the extension for it, or a local account.

// ArcHandler opens sessions through Azure Arc.
type ArcHandler struct{}

var _ provider.Handler = ArcHandler{}

func init() { provider.RegisterHandler(ArcHandler{}) }

// Kind is the route kind this handler serves.
func (ArcHandler) Kind() string { return inventory.RouteArc }

// Shell opens an interactive session, or runs one command.
func (ArcHandler) Shell(ctx context.Context, n inventory.Node, r inventory.Route, o provider.SessionOptions) error {
	// arcArgs is what a forward runs; here it checks --auth, --user and --key
	// the same way, so the two never disagree about what is allowed.
	if _, err := arcArgs(n, r, o); err != nil {
		return err
	}
	if o.Print {
		printArcSSH(n, r, o)
		return nil
	}
	// Any password prompt comes from ssh itself and is read from the terminal,
	// so it never passes through ssmate.
	return arcSSH(ctx, n, r, o)
}

// printArcSSH shows what an Arc session runs, for --print: the configuration
// az writes, then ssh against it, with the session's directory and the host
// az names shown as placeholders.
func printArcSSH(n inventory.Node, r inventory.Route, o provider.SessionOptions) {
	auth, err := arcAuth(o)
	if err != nil {
		fmt.Println(err)
		return
	}
	knownHosts := filepath.Join(xdg.ConfigHome, "ssmate", "known_hosts")
	fmt.Println(printable(arcConfigArgs(groupOf(r.Target), nameOf(r.Target), r.Account,
		"<dir>/config", "<dir>", auth, o)))
	ssh := append([]string{"-F", "<dir>/config"}, sshTunnelOptionsNoPort(arcHostKeyAlias(r.Target), knownHosts)...)
	ssh = append(ssh, "<host named in that file>")
	fmt.Println(printCommand("ssh", append(ssh, o.Command...)))
}

// Forward opens a tunnel through the machine and holds it open.
func (ArcHandler) Forward(ctx context.Context, n inventory.Node, r inventory.Route, f session.Forward, o provider.SessionOptions) error {
	args, err := arcArgs(n, r, o)
	if err != nil {
		return err
	}
	bind := o.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	host := f.RemoteHost
	if host == "" {
		host = "localhost"
	}
	spec := net.JoinHostPort(bind, strconv.Itoa(f.LocalPort)) + ":" + net.JoinHostPort(host, strconv.Itoa(f.RemotePort))
	args = append(args, "--", "-N", "-L", spec)
	if o.Print {
		fmt.Println(printable(args))
		return nil
	}
	return session.HoldOpen(ctx, func() error { return runAzAs(ctx, args, arcExitError) }, o.OnReconnect)
}

// arcArgs builds the az ssh arc command for one machine.
//
// Microsoft sign-in is the default, as it is for az itself: the user logs in
// as themselves and no shared credential exists. A local account is used when
// one is named with --user, with --key if given and a password prompt
// otherwise.
func arcArgs(n inventory.Node, r inventory.Route, o provider.SessionOptions) ([]string, error) {
	group, name := groupOf(r.Target), nameOf(r.Target)
	if group == "" || name == "" {
		return nil, exitcode.New(exitcode.Err,
			fmt.Sprintf("%s has no Arc resource to connect to", n.DisplayName()), "ssmate ls --refresh", nil)
	}
	args := []string{"ssh", "arc", "--resource-group", group, "--name", name}
	if r.Account != "" {
		args = append(args, "--subscription", r.Account)
	}
	switch strings.ToLower(o.Auth) {
	case "aad", "entra":
		return args, nil
	case "", "password", "key", "ssh-key":
	default:
		return nil, exitcode.New(exitcode.Err,
			fmt.Sprintf("%q is not a way to log in", o.Auth),
			"--auth takes password, key or aad", nil)
	}
	auth := strings.ToLower(o.Auth)
	if o.User == "" {
		if auth == "" {
			return args, nil
		}
		return nil, exitcode.New(exitcode.Err,
			fmt.Sprintf("--auth %s on an Arc machine needs the local account to log in as", auth),
			"pass --user, or leave --auth out to sign in with your Microsoft account", nil)
	}
	args = append(args, "--local-user", o.User)
	if o.Key != "" {
		args = append(args, "--private-key-file", o.Key)
	} else if auth == "key" || auth == "ssh-key" {
		return nil, exitcode.New(exitcode.Err,
			"--auth key needs the key to log in with", "pass it: --key ~/.ssh/id_ed25519", nil)
	}
	return args, nil
}

// arcExitError explains what az's exit status means for an Arc session.
//
// 2 is az's "no such command", which for this one means the ssh extension is
// missing; ssh's own 255 means the connection itself failed.
func arcExitError(code int) error {
	switch code {
	case 2, 3:
		return exitcode.New(exitcode.Prereq,
			"the az ssh command is unavailable",
			"install the extension: az extension add --name ssh", nil)
	case 255:
		return exitcode.New(exitcode.NotConnected,
			"the session through Azure Arc could not be opened",
			"check the machine's agent is connected and sshd is running on it, and that you hold "+
				"Virtual Machine User Login (or Local User Login) on it", nil)
	case 1:
		return exitcode.New(exitcode.NotConnected,
			"the Azure CLI could not open the session",
			"ssmate doctor <machine>   # checks the agent, the extension and your sign-in", nil)
	}
	return exitcode.New(exitcode.Code(code), fmt.Sprintf("az exited %d", code), "", nil)
}

// nameOf is the last segment of an Azure resource ID.
func nameOf(resourceID string) string {
	id := strings.TrimRight(resourceID, "/")
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		return id[i+1:]
	}
	return ""
}

// listArcMachines lists every Arc machine in a subscription.
//
// A subscription that has never used Arc has never registered the service
// that holds it, and asking returns an error. That is not a fault worth
// mentioning on every refresh, so it reads as no machines at all.
func listArcMachines(ctx context.Context, c *armhybridcompute.MachinesClient) ([]*armhybridcompute.Machine, error) {
	var out []*armhybridcompute.Machine
	pager := c.NewListBySubscriptionPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			if arcNotInUse(err) {
				return nil, nil
			}
			return out, fmt.Errorf("list Arc machines: %w", err)
		}
		out = append(out, page.Value...)
	}
	return out, nil
}

// arcNotInUse reports whether an error only says Arc was never set up here.
func arcNotInUse(err error) bool {
	var re *azcore.ResponseError
	if !errors.As(err, &re) {
		return false
	}
	switch re.ErrorCode {
	case "MissingSubscriptionRegistration", "SubscriptionNotRegistered", "NoRegisteredProviderFound":
		return true
	}
	return re.StatusCode == 404
}

// buildArcNode turns one Arc machine into a record of a machine that lives
// elsewhere, carrying Arc as its way in.
func buildArcNode(m *armhybridcompute.Machine, sub Subscription) inventory.Node {
	id := deref(m.ID)
	n := inventory.Node{
		Cloud:   inventory.CloudAzure,
		ID:      id,
		Name:    deref(m.Name),
		Account: sub.Name,
		Region:  deref(m.Location),
		Tags:    tagsOf(m.Tags),
		Detail:  map[string]string{"resource_type": "Microsoft.HybridCompute/machines"},
		Outside: &inventory.Outside{},
	}
	if g := groupOf(id); g != "" {
		n.Detail["resource_group"] = g
	}
	if t, ok := n.Tags[inventory.SourceTag]; ok {
		n.Outside.Tag = strings.TrimSpace(t)
	}
	status := ""
	if p := m.Properties; p != nil {
		if p.DisplayName != nil && *p.DisplayName != "" {
			n.Name = *p.DisplayName
		}
		if p.OSProfile != nil {
			n.Hostname = deref(p.OSProfile.ComputerName)
		}
		n.PrivateIP = firstIPv4(p.NetworkProfile)
		if p.Status != nil {
			status = string(*p.Status)
		}
		if p.CloudMetadata != nil && deref(p.CloudMetadata.Provider) != "" {
			n.Detail["runs_in"] = deref(p.CloudMetadata.Provider)
		}
		if os := deref(p.OSName); os != "" {
			n.Detail["os"] = os
			n.OS = os
		}
		if v := deref(p.AgentVersion); v != "" {
			n.Agent = inventory.Agent{Present: true, Online: status == string(armhybridcompute.StatusTypesConnected), Version: v}
			if p.LastStatusChange != nil {
				n.Agent.LastSeen = *p.LastStatusChange
			}
		}
	}
	n.State = status
	classifyArc(&n, status)
	return n
}

// classifyArc records whether Arc can carry a session to the machine.
//
// Only the agent's connection is known from here. Whether sshd runs on the
// machine, and whether the SSH port is allowed in its Arc configuration, show
// up only when a session is tried, and az offers to set the port then.
func classifyArc(n *inventory.Node, status string) {
	r := inventory.Route{Kind: inventory.RouteArc, Target: n.ID, Account: n.Account, Region: n.Region}
	switch status {
	case string(armhybridcompute.StatusTypesConnected):
		r.Ready, r.Status = true, inventory.StatusOnline
	case string(armhybridcompute.StatusTypesDisconnected):
		r.Status = inventory.StatusAgentLost
		r.Hint = "the Arc agent stopped reporting; check it on the machine: azcmagent show"
	case "":
		r.Status = inventory.StatusUnknown
	default:
		r.Status = strings.ToLower(status)
		r.Hint = "the Arc agent is " + strings.ToLower(status) + "; check it on the machine: azcmagent show"
	}
	n.AddRoute(r)
	inventory.Summarize(n, inventory.DefaultRouteOrder)
}

// firstIPv4 is the machine's first IPv4 address, which is what its own cloud
// records it by and so what a link by address compares.
func firstIPv4(p *armhybridcompute.NetworkProfile) string {
	if p == nil {
		return ""
	}
	for _, nic := range p.NetworkInterfaces {
		if nic == nil {
			continue
		}
		for _, a := range nic.IPAddresses {
			if a == nil {
				continue
			}
			ip := net.ParseIP(deref(a.Address))
			if ip != nil && ip.To4() != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				return ip.String()
			}
		}
	}
	return ""
}
