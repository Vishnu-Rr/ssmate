package oci

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/session"
	"github.com/oracle/oci-go-sdk/v65/computeinstanceagent"
)

// Managed SSH is OCI's way in that needs no key of yours.
//
// A managed SSH session asks the Oracle Cloud Agent's Bastion plugin on the
// machine to accept the session's one-time key, so the same throwaway key
// opens the bastion and logs in to the machine. Port forwarding, the other
// session type, carries the connection but leaves the login to a key the
// machine already trusts.
//
// It is offered only where the plugin is actually running, and ranked last:
// it was measured at three to five minutes to open, and Oracle's own list of
// where it works did not predict where it worked. Someone who has a key on
// the machine is better served by port forwarding; someone who has none can
// ask for this with --via bastion.

// managedRoutes adds a managed SSH route to every machine whose Bastion plugin
// is switched on, ready when the plugin reports running and a bastion serves
// the machine's network.
func managedRoutes(ctx context.Context, api pluginAPI, nodes []inventory.Node, concurrency int) {
	if api == nil {
		return
	}
	if concurrency <= 0 {
		concurrency = 8
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for i := range nodes {
		if v, _ := nodes[i].Detailed("bastion_plugin"); v != "enabled" {
			continue
		}
		n := &nodes[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			status := ""
			compartment, _ := n.Detailed("compartment_id")
			resp, err := api.GetInstanceAgentPlugin(ctx, computeinstanceagent.GetInstanceAgentPluginRequest{
				InstanceagentId: &n.ID,
				CompartmentId:   &compartment,
				PluginName:      strPtr("Bastion"),
			})
			if err == nil {
				status = string(resp.Status)
			}
			classifyManaged(n, status)
		}()
	}
	wg.Wait()
}

// classifyManaged records the managed SSH route for a machine whose Bastion
// plugin is switched on. status is what the plugin reports, empty when it
// could not be read.
func classifyManaged(n *inventory.Node, status string) {
	if n.Down {
		return
	}
	n.Detail["bastion_plugin"] = strings.ToLower(status)
	r := inventory.Route{Kind: inventory.RouteOCIManaged, Target: n.ID, Account: n.Account, Region: n.Region}
	_, hasBastion := n.Detailed("bastion")
	reach, _ := n.Detailed("reach")
	switch {
	case hasBastion && reach == reachBlocked:
		// A managed session goes through the same bastions, so the same
		// rules stop it.
		r.Status = inventory.StatusBlocked
		r.Hint, _ = n.Detailed("reach_why")
	case status == string(computeinstanceagent.InstanceAgentPluginStatusRunning) && hasBastion:
		r.Ready, r.Status = true, inventory.StatusOnline
	case !hasBastion:
		r.Status = inventory.StatusNoGateway
		r.Hint = "no bastion reaches this machine's network; create one bound to its VCN"
	case status == "":
		r.Status = inventory.StatusUnknown
		r.Hint = "could not read the Bastion plugin's state; that needs: read instance-agent-plugins"
	default:
		r.Status = "plugin " + strings.ToLower(status)
		r.Hint = "the Bastion plugin is switched on but " + strings.ToLower(status) +
			"; check the Oracle Cloud Agent on the machine"
	}
	n.AddRoute(r)
	inventory.Summarize(n, inventory.DefaultRouteOrder)
}

func strPtr(s string) *string { return &s }

// ManagedHandler opens managed SSH sessions.
type ManagedHandler struct{ p Provider }

var _ provider.Handler = ManagedHandler{}

func init() { provider.RegisterHandler(ManagedHandler{}) }

// Kind is the route kind this handler serves.
func (ManagedHandler) Kind() string { return inventory.RouteOCIManaged }

// Shell logs in with the session's one-time key on both hops: the bastion
// accepts it because the session was opened with it, and the machine accepts
// it because the plugin placed it there for this account.
func (h ManagedHandler) Shell(ctx context.Context, n inventory.Node, _ inventory.Route, o provider.SessionOptions) error {
	user, err := managedTarget(n, o)
	if err != nil {
		return err
	}
	if o.Print {
		printManaged(n, user, append([]string{user + "@" + n.PrivateIP}, o.Command...))
		return nil
	}
	return h.p.withSession(ctx, n, n.PrivateIP, 22, o, managedSSH(user), func(k *keypair, sessionID, allow string) error {
		return runSSH(ctx, allow, shellArgs(k, sessionID, n.Region, user, n.PrivateIP, k.privatePath, o.Command))
	})
}

// Forward tunnels a port through the login a managed session allows, since
// that session type carries SSH to the machine and nothing else. The far end
// is resolved from the machine, so the default is the machine itself.
func (h ManagedHandler) Forward(ctx context.Context, n inventory.Node, _ inventory.Route, f session.Forward, o provider.SessionOptions) error {
	user, err := managedTarget(n, o)
	if err != nil {
		return err
	}
	spec := forwardSpec(o.Bind, f)
	if o.Print {
		printManaged(n, user, []string{"-N", "-L", spec, user + "@" + n.PrivateIP})
		return nil
	}
	return session.HoldOpen(ctx, func() error {
		return h.p.withSession(ctx, n, n.PrivateIP, 22, o, managedSSH(user), func(k *keypair, sessionID, allow string) error {
			args := shellArgs(k, sessionID, n.Region, user, n.PrivateIP, k.privatePath, nil)
			dest := args[len(args)-1]
			args = append(args[:len(args)-1], "-N", "-o", "ExitOnForwardFailure=yes", "-L", spec, dest)
			return runSSH(ctx, allow, args)
		})
	}, o.OnReconnect)
}

// managedTarget checks what a managed session needs: an address, and the
// account the plugin is to admit the key for.
func managedTarget(n inventory.Node, o provider.SessionOptions) (string, error) {
	if n.PrivateIP == "" {
		return "", exitcode.New(exitcode.Err,
			fmt.Sprintf("%s has no private address to connect to", n.DisplayName()), "", nil)
	}
	return loginUserFor(n, o)
}

// forwardSpec is the -L argument for a forward through a login.
func forwardSpec(bind string, f session.Forward) string {
	if bind == "" {
		bind = "127.0.0.1"
	}
	host := f.RemoteHost
	if host == "" {
		host = "localhost"
	}
	return net.JoinHostPort(bind, strconv.Itoa(f.LocalPort)) + ":" + net.JoinHostPort(host, strconv.Itoa(f.RemotePort))
}

// printManaged shows the two commands a managed session amounts to. The same
// placeholder key appears on both hops because the same key is used on both.
func printManaged(n inventory.Node, user string, rest []string) {
	bastionID, _ := n.Detailed("bastion")
	fmt.Println("oci bastion session create-managed-ssh" +
		" --bastion-id " + bastionID +
		" --target-resource-id " + n.ID +
		" --target-os-username " + user +
		" --target-private-ip " + n.PrivateIP +
		" --target-port 22" +
		" --ssh-public-key-file <session-key>.pub" +
		" --session-ttl " + strconv.Itoa(sessionTTL))
	fmt.Printf("ssh -i <session-key> -o ProxyCommand=\"ssh -i <session-key> -W %%h:%%p -p 22 <session-ocid>@%s\" %s\n",
		bastionHost(n.Region), strings.Join(rest, " "))
}
