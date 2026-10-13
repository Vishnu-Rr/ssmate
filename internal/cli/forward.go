package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/session"
	"github.com/spf13/cobra"
)

func newForwardCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "forward <target> <local:remote | local:host:remote>",
		Short: "Tunnel a local port to a machine, or through it",
		Long: `Open a tunnel and hold it until you press Ctrl-C.

  ssmate forward web-1 8080:80
      localhost:8080 reaches port 80 on the machine

  ssmate forward web-1 5432:db.internal:5432
      localhost:5432 reaches the database, through the instance

The second form is how you reach a database that only the instance can see,
without giving the database a public address or keeping a bastion host
permanently open.`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeTargets(opts),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runForward(cmd, opts, args[0], args[1])
		},
	}
}

func runForward(cmd *cobra.Command, opts *options, target, spec string) error {
	fwd, err := session.ParseForward(spec)
	if err != nil {
		return exitcode.New(exitcode.Err, err.Error(),
			"ssmate forward <target> 8080:80\n  ssmate forward <target> 5432:db.internal:5432", nil)
	}

	ctx := cmd.Context()
	nodes, err := loadInventory(ctx, opts)
	if err != nil {
		return err
	}
	node, err := chooseNodeVia(ctx, nodes, target, opts)
	if err != nil {
		return err
	}
	return runForwardNode(ctx, opts, node, fwd)
}

// runForwardNode forwards a port from a machine already chosen.
//
// Split out so the machine list can forward directly from a selection,
// without making the user quit and retype an identifier.
func runForwardNode(ctx context.Context, opts *options, node inventory.Node, fwd session.Forward) error {
	route, h, err := pickRoute(node, opts.via, routeOrder())
	if err != nil {
		return err
	}

	if opts.print {
		return h.Forward(ctx, node, route, fwd, provider.SessionOptions{Print: true})
	}

	// Fail on a taken local port before opening a session, so the user gets
	// a clear message instead of one from deep inside the vendor binary.
	// Checked after --print, because printing binds nothing.
	if err := checkLocalPortFree(opts.bind, fwd.LocalPort); err != nil {
		return err
	}

	t := opts.theme()
	fmt.Fprintf(os.Stderr, "%s  %s\n", t.Heading(fwd.Describe(node.DisplayName())),
		t.Dim(fmt.Sprintf("%s in %s via %s", node.ID, node.Region, inventory.RouteLabel(route.Kind))))
	fmt.Fprintln(os.Stderr, t.Dim("Ctrl-C to close the tunnel."))

	// Ctrl-C must end the tunnel. Unlike a shell session there is no remote
	// process that wants the signal, so the context cancellation from main
	// is what stops the loop.
	key, err := keyFile(opts.key)
	if err != nil {
		return err
	}
	return h.Forward(ctx, node, route, fwd, provider.SessionOptions{
		Reason: opts.reason,
		User:   opts.user,
		Bind:   opts.bind,
		Auth:   opts.auth,
		Key:    key,
		OnReconnect: func(e session.ReconnectEvent) {
			// Every one of these services ends a healthy tunnel on its own
			// schedule. Say what happened so a reconnect does not look like
			// a fault.
			fmt.Fprintf(os.Stderr, "%s\n", t.Dim(fmt.Sprintf(
				"tunnel closed after %s, reopening in %s...",
				e.Lasted.Round(time.Second), e.Delay.Round(time.Second))))
		},
	})
}

// checkLocalPortFree reports a taken port as a clear error rather than
// letting the plugin fail with "address already in use" from inside a
// subprocess.
func checkLocalPortFree(bind string, port int) error {
	if bind == "" {
		bind = "127.0.0.1"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, fmt.Sprint(port)))
	if err != nil {
		return exitcode.New(
			exitcode.Err,
			fmt.Sprintf("local port %d is already in use", port),
			fmt.Sprintf("lsof -nP -iTCP:%d -sTCP:LISTEN     # see what has it\n  "+
				"ssmate forward ... %d:...                  # or pick another local port", port, port+1),
			nil,
		)
	}
	return ln.Close()
}
