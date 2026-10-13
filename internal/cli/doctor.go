package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Vishnu-Rr/ssmate/internal/doctor"
	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/resolve"
	"github.com/spf13/cobra"
)

func newDoctorCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor [target]",
		Short: "Diagnose why a connection is not working",
		Long: "doctor names the cause and prints the command that fixes it.\n\n" +
			"  ssmate doctor                check this machine's setup for every cloud\n" +
			"  ssmate doctor web-1          check everything between you and one machine\n" +
			"  ssmate doctor web-1 --json   the same findings, for a script\n" +
			"  ssmate doctor web-1 --probe  also try a real session through the way in\n\n" +
			"It checks your side first, so a missing binary is never reported as a\n" +
			"cloud permissions error, and it never stops at the first failure: three\n" +
			"problems should take one run to find, not three.\n\n" +
			"With a target it asks only the cloud that machine lives in. Every\n" +
			"remedy is a command you can copy, not a description of one.\n\n" +
			"Exits non-zero when anything failed, so a script can gate on it.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeTargets(opts),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 1 {
				query = args[0]
			}
			return runDoctor(cmd.Context(), opts, query, nil)
		},
	}
	cmd.Flags().BoolVar(&opts.probe, "probe", false,
		"also open a session through the way in, without logging in, to prove it carries traffic")
	return cmd
}

// runDoctorFor diagnoses a machine the caller already has.
//
// The machine list uses this: it knows exactly which machine is selected, so
// resolving a name back into one would repeat work it has already done, and
// would fail on the machines whose names are not unique.
func runDoctorFor(ctx context.Context, opts *options, node inventory.Node) error {
	return runDoctor(ctx, opts, node.DisplayName(), &node)
}

// runDoctor runs the diagnosis.
//
// known short-circuits resolution when the caller already has the machine.
func runDoctor(ctx context.Context, opts *options, query string, known *inventory.Node) error {
	usable, absent := provider.Available(ctx)
	if len(usable) == 0 {
		return noCloudsError(absent)
	}

	// Resolve the target once, then ask only the cloud it lives in. Asking
	// every cloud about a machine in one of them produces a page of
	// irrelevant failures.
	found := known
	var owner provider.Provider
	if found != nil {
		owner, _ = provider.For(found.Cloud)
	} else if query != "" {
		if nodes, err := loadInventory(ctx, opts); err == nil {
			if ms := resolve.Resolve(nodes, query); len(ms) == 1 {
				found = &ms[0].Node
				owner, _ = provider.For(found.Cloud)
			}
		}
	}

	reg := doctor.Registry{}
	if linked, rec, carrier := linkedRoute(found, opts); carrier != nil {
		// The machine would be reached through another cloud's session
		// service, so that is what to diagnose: its credentials, its tools
		// and its record of the machine. The machine's own cloud's bastion
		// is not on the path and its failures would only be noise.
		reg = append(reg, linkedCheck(*found, linked, rec))
		reg = append(reg, carrier.Checks(ctx, &rec, rec.ID)...)
	} else if owner != nil {
		reg = append(reg, owner.Checks(ctx, found, query)...)
	} else {
		for _, p := range usable {
			reg = append(reg, p.Checks(ctx, nil, query)...)
		}
	}

	if found != nil {
		if g, ok := outboundGuide(*found); ok {
			reg = append(reg, g)
		}
		if opts.probe {
			reg = append(reg, probeCheck(*found, opts))
		}
	}

	results := reg.RunAll(ctx)

	if opts.json {
		// A caller asking for JSON wants data, not a verdict, so this path
		// always exits zero.
		return doctor.RenderJSON(os.Stdout, results)
	}

	// Any failure sends the whole report to stderr, so a pipeline sees it,
	// and produces a non-zero exit code so a script can gate on it.
	out := os.Stdout
	code := doctor.ExitCode(results)
	if code != exitcode.OK {
		out = os.Stderr
	}
	if err := doctor.RenderThemed(colourWriter(out), results, opts.symbols(), opts.theme()); err != nil {
		return err
	}
	if code != exitcode.OK {
		return exitcode.New(code, "diagnostics found problems", "", nil)
	}
	return nil
}

// linkedRoute finds the route a session to this machine would use when that
// route came from another inventory, with that inventory's own record of the
// machine and the provider that carries it. A nil provider means the session
// would go the machine's own cloud's way.
func linkedRoute(n *inventory.Node, opts *options) (inventory.Route, inventory.Node, provider.Provider) {
	if n == nil {
		return inventory.Route{}, inventory.Node{}, nil
	}
	r, _, err := pickRoute(*n, opts.via, routeOrder())
	if err != nil || r.Link == "" {
		return inventory.Route{}, inventory.Node{}, nil
	}
	p, ok := provider.For(inventory.RouteCloud(r.Kind))
	if !ok {
		return inventory.Route{}, inventory.Node{}, nil
	}
	path, err := inventory.CachePath()
	if err != nil {
		return inventory.Route{}, inventory.Node{}, nil
	}
	c, err := inventory.LoadCache(path)
	if err != nil {
		return inventory.Route{}, inventory.Node{}, nil
	}
	for _, rec := range c.Nodes {
		if rec.ID == r.Target && rec.Cloud == p.Cloud() {
			inventory.Summarize(&rec, routeOrder())
			return r, rec, p
		}
	}
	return inventory.Route{}, inventory.Node{}, nil
}

// linkedCheck says which record the session would go through and how it was
// tied to the machine, because a link made on address and hostname is a
// judgement worth being able to see.
func linkedCheck(n inventory.Node, r inventory.Route, rec inventory.Node) doctor.Check {
	how := map[string]string{
		inventory.LinkSource:  "the registering service recorded this machine as its source",
		inventory.LinkTag:     "its " + inventory.SourceTag + " tag names this machine",
		inventory.LinkAddress: "matched by address and hostname, which is a judgement",
	}[r.Link]
	sev, remedy := doctor.Pass, ""
	if r.Link == inventory.LinkAddress {
		sev = doctor.Warn
		switch r.Kind {
		case inventory.RouteSSM:
			remedy = fmt.Sprintf("make the link certain by tagging the registration:\n"+
				"    aws ssm add-tags-to-resource --resource-type ManagedInstance --resource-id %s \\\n"+
				"        --tags Key=%s,Value=%s --region %s", rec.ID, inventory.SourceTag, n.ID, rec.Region)
			if rec.Account != "" {
				remedy += " --profile " + rec.Account
			}
		case inventory.RouteArc:
			remedy = fmt.Sprintf("make the link certain by tagging the Arc machine:\n"+
				"    az tag update --resource-id %s --operation Merge --tags %s=%s",
				rec.ID, inventory.SourceTag, n.ID)
		}
	}
	return doctor.Check{
		Name: "way in",
		Run: func(context.Context) doctor.Result {
			return doctor.Result{
				Name:     "way in",
				Severity: sev,
				Detail:   fmt.Sprintf("via %s, as %s in %s; %s", inventory.RouteLabel(r.Kind), rec.ID, rec.Where(), how),
				Remedy:   remedy,
			}
		},
	}
}

// probeCheck tries the way in a session would take, as far as proving it
// carries traffic and no further. The rules on the way are judged without it;
// this settles what they could not.
func probeCheck(n inventory.Node, opts *options) doctor.Check {
	name := "probe"
	return doctor.Check{
		Name: name,
		Run: func(ctx context.Context) doctor.Result {
			r, h, err := pickRoute(n, opts.via, routeOrder())
			if err != nil {
				return doctor.Result{Name: name, Severity: doctor.Fail, Detail: err.Error()}
			}
			label := "via " + inventory.RouteLabel(r.Kind)
			p, ok := h.(provider.Prober)
			if !ok {
				return doctor.Result{Name: name, Severity: doctor.Warn,
					Detail: label + ": probing is not built for this way in yet; ssmate connect tries it for real"}
			}
			if err := p.Probe(ctx, n, r, provider.SessionOptions{Reason: "doctor --probe"}); err != nil {
				detail := err.Error()
				var e *exitcode.Error
				remedy := ""
				if errors.As(err, &e) {
					remedy = e.Hint
				}
				return doctor.Result{Name: name, Severity: doctor.Fail, Detail: label + ": " + detail, Remedy: remedy}
			}
			return doctor.Result{Name: name, Severity: doctor.Pass,
				Detail: label + ": a session reached the machine's SSH server"}
		},
	}
}
