package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/resolve"
	"github.com/Vishnu-Rr/ssmate/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newConnectCmd(opts *options) *cobra.Command {
	var raw bool
	cmd := &cobra.Command{
		Use:   "connect [target] [command...]",
		Short: "Open a shell on a machine, or run one command on it",
		Long: "Target may be an identifier, a name, a private or public address, a tag\n" +
			"value or a bookmark. With no target, ssmate opens the machine list.\n\n" +
			"Anything after the target runs instead of a shell:\n\n" +
			"  ssmate connect web-1 uptime\n" +
			"  ssmate connect web-1 -- tail -n 50 /var/log/syslog\n\n" +
			"Use -- before a command that starts with a dash, or its flags would\n" +
			"be read as ssmate's own.\n\n" +
			"On clouds reached over SSH the command's exit status comes back, so a\n" +
			"script can branch on it. AWS Session Manager does not carry one, so\n" +
			"there ssmate reports only whether the session itself succeeded.",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeTargets(opts),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := ""
			var command []string
			if len(args) > 0 {
				target, command = args[0], args[1:]
			}
			return runConnectRaw(cmd.Context(), opts, target, raw, command)
		},
	}
	cmd.Flags().BoolVar(&raw, "raw", false, "use the default sh shell instead of a bash login shell")
	return cmd
}

func runConnectRaw(ctx context.Context, opts *options, target string, raw bool, command []string) error {
	nodes, err := loadInventory(ctx, opts)
	if err != nil {
		return err
	}

	node, err := chooseNodeVia(ctx, nodes, target, opts)
	if err != nil {
		return err
	}
	return runConnectNode(ctx, opts, node, raw, command)
}

// chooseNodeVia resolves a target, and when --via is what hid it, says so.
//
// --via narrows the machines a name resolves among, so a machine that exists
// but has no such way in would otherwise be reported as not existing at all.
// That sends the user to refresh a cache that was never the problem.
func chooseNodeVia(ctx context.Context, nodes []inventory.Node, target string, opts *options) (inventory.Node, error) {
	node, err := chooseNode(nodes, target, opts)
	var ec *exitcode.Error
	if err == nil || opts.via == "" || !errors.As(err, &ec) || ec.Code != exitcode.NotFound {
		return node, err
	}
	wider := *opts
	wider.via = ""
	all, lerr := loadInventory(ctx, &wider)
	if lerr != nil {
		return node, err
	}
	found, ferr := chooseNode(all, target, &wider)
	if ferr != nil {
		return node, err
	}
	if _, _, perr := pickRoute(found, opts.via, routeOrder()); perr != nil {
		return inventory.Node{}, perr
	}
	return found, nil
}

// keyFile resolves --key, and refuses one that is not there.
//
// It is checked here, before anything is opened, because the alternative is
// learning about a mistyped path from ssh after a bastion session has already
// been created and has to be torn down - and on OCI that session held one of
// twenty slots while it did.
func keyFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = home + path[1:]
		}
	}
	st, err := os.Stat(path)
	switch {
	case err != nil:
		return "", exitcode.New(exitcode.Prereq,
			fmt.Sprintf("no key at %s", path), "check the path given to --key", nil)
	case st.IsDir():
		return "", exitcode.New(exitcode.Prereq,
			fmt.Sprintf("%s is a directory, not a key", path), "", nil)
	}
	return path, nil
}

// runConnectNode opens a shell on a machine already chosen.
func runConnectNode(ctx context.Context, opts *options, node inventory.Node, raw bool, command []string) error {
	route, h, err := pickRoute(node, opts.via, routeOrder())
	if err != nil {
		return err
	}
	key, err := keyFile(opts.key)
	if err != nil {
		return err
	}

	recordUse(node.ID)

	// A command is not an occasion for a banner: its output is the point, and
	// a line of ours above it is the first thing a pipeline would have to
	// strip back out.
	if !opts.print && len(command) == 0 {
		fmt.Fprintf(os.Stderr, "Connecting to %s (%s) in %s via %s\n",
			node.DisplayName(), node.ID, node.Where(), inventory.RouteLabel(route.Kind))
	}
	return h.Shell(ctx, node, route, provider.SessionOptions{
		Raw: raw, Print: opts.print, Reason: opts.reason, User: opts.user,
		Auth: opts.auth, Key: key, Command: command,
	})
}

// stdinIsTerminal reports whether stdin is an interactive terminal.
func stdinIsTerminal() bool { return fileIsTerminal(os.Stdin) }

// fileIsTerminal is the testable half of stdinIsTerminal.
//
// It asks the operating system whether the descriptor is a terminal, the same
// question isatty answers. An earlier version tested for a character device
// instead, to avoid a dependency. That was wrong: /dev/null is a character
// device, so redirecting from it looked interactive and the picker was opened
// against something that could never answer it.
func fileIsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// chooseNode turns a query into exactly one node, prompting when the query is
// ambiguous and refusing rather than guessing when it cannot prompt.
func chooseNode(nodes []inventory.Node, target string, opts *options) (inventory.Node, error) {
	target = expandBookmark(target)
	matches := resolve.Resolve(nodes, target)

	switch {
	case len(matches) == 1:
		return matches[0].Node, nil

	case len(matches) == 0:
		hint := "ssmate ls --refresh   # rebuild the inventory cache"
		if near := resolve.Suggest(nodes, target, 3); len(near) > 0 {
			hint = "did you mean:  " + strings.Join(near, "   ") +
				"\n  ssmate ls             # show everything"
		}
		return inventory.Node{}, exitcode.New(
			exitcode.NotFound,
			fmt.Sprintf("nothing matching %q in any known profile or region", target),
			hint,
			nil,
		)
	}

	// More than one match. Never guess.
	// Refuse to prompt when there is nobody to answer. Opening a picker
	// against a pipe crashes the terminal library and, worse, used to exit
	// zero, so a script could not tell it had failed.
	if opts.noInput || !stdinIsTerminal() {
		var b []byte
		for _, m := range matches {
			b = append(b, fmt.Sprintf("\n  %s  %s  %s", m.Node.ID, m.Node.Region, m.Node.DisplayName())...)
		}
		return inventory.Node{}, exitcode.New(
			exitcode.NotFound,
			fmt.Sprintf("%q matches %d instances:%s", target, len(matches), string(b)),
			"re-run with a full instance ID, or run interactively to pick from a list",
			nil,
		)
	}

	candidates := make([]inventory.Node, 0, len(matches))
	for _, m := range matches {
		candidates = append(candidates, m.Node)
	}
	choice, err := browse(candidates, opts)
	if err != nil {
		return inventory.Node{}, err
	}
	// Turn this interactive use into a command they can reuse. granted does
	// the same, and it is the cheapest teaching moment in the tool.
	fmt.Fprintf(os.Stderr, "\nNext time:  ssmate connect %s\n\n", choice.Node.ID)
	return choice.Node, nil
}

// browse opens the machine list and returns what the user chose to do.
func browse(nodes []inventory.Node, opts *options) (ui.Choice, error) {
	choice, err := ui.Browse(nodes, opts.symbols(), opts.theme(), useScore())
	if errors.Is(err, ui.ErrCancelled) {
		return ui.Choice{}, exitcode.New(exitcode.Cancelled, "cancelled", "", nil)
	}
	return choice, err
}

// useScore exposes how often and how recently a machine was reached, so the
// list can put the ones you actually use at the top.
//
// Failures are swallowed for the same reason ranking has always swallowed
// them: ordering is a convenience, and a broken state file must never stop
// you reaching a machine.
func useScore() func(string) (float64, time.Time) {
	path, err := inventory.RecentsPath()
	if err != nil {
		return nil
	}
	r := inventory.LoadRecents(path)
	now := time.Now()
	return func(id string) (float64, time.Time) {
		if e, ok := r.Entries[id]; ok {
			return e.Score(now), e.Last
		}
		return 0, time.Time{}
	}
}

// loadInventory returns cached nodes, refreshing when asked or when there is
// no usable cache. One cache holds every cloud, so freshness is decided here
// rather than inside any provider.
func loadInventory(ctx context.Context, opts *options) ([]inventory.Node, error) {
	sc, err := scopeFrom(opts)
	if err != nil {
		return nil, err
	}
	path, err := inventory.CachePath()
	if err != nil {
		return nil, err
	}
	if !opts.refresh {
		if c, err := inventory.LoadCache(path); err == nil {
			return sc.apply(joined(c.Nodes)), nil
		}
	}
	all, err := refreshInventory(ctx, opts, sc, path)
	if err != nil {
		return nil, err
	}
	return sc.apply(joined(all)), nil
}

// joined turns the records each inventory keeps into the machines a person
// sees: records of the same machine linked into one, and each machine's
// verdict decided from its ways in.
//
// It runs on every load, cached or fresh. The cache keeps every inventory's
// own records unjoined, so a refresh of one cloud never loses what another
// contributed; and the verdict depends on the order of ways in, which is this
// invocation's preference rather than the one in force when the cache was
// written. Narrowing comes after, so a machine filtered by cloud still has the
// way in another cloud gave it.
func joined(records []inventory.Node) []inventory.Node {
	nodes := inventory.Link(records)
	inventory.SummarizeAll(nodes, routeOrder())
	return nodes
}

// refreshInventory sweeps every cloud the machine is signed in to.
//
// Clouds you do not use are silent. They are not errors, they are not warnings
// and they are not mentioned, because most people use one cloud and a tool
// that complains about the other two on every run is a tool people stop
// reading.
func refreshInventory(ctx context.Context, opts *options, sc scope, cachePath string) ([]inventory.Node, error) {
	usable, absent := provider.Available(ctx)
	if len(usable) == 0 {
		return nil, noCloudsError(absent)
	}
	w := sc.sweep(usable, opts.allProfiles)
	if len(w.clouds) == 0 {
		return nil, exitcode.New(exitcode.Err,
			"none of the clouds asked for is signed in here",
			"clouds signed in here: "+strings.Join(cloudNames(usable), ", "), nil)
	}

	t := opts.theme()
	po := provider.Options{
		Account:     w.account,
		AllAccounts: w.allAccounts,
		Region:      w.region,
		Progress:    func(msg string) { fmt.Fprintf(os.Stderr, "%s\n", t.Dim(msg)) },
		Warn:        func(msg string) { fmt.Fprintf(os.Stderr, "  %s\n", msg) },
	}

	// Sweep the clouds at once. They share nothing, and a serial sweep makes
	// the slowest cloud's latency the sum of all of them.
	var (
		mu   sync.Mutex
		all  []inventory.Node
		errs []error
		wg   sync.WaitGroup
	)
	for _, p := range usable {
		if !w.clouds[p.Cloud()] {
			continue
		}
		pv := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			nodes, err := pv.Inventory(ctx, po)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", pv.Cloud(), err))
				return
			}
			all = append(all, nodes...)
		}()
	}
	wg.Wait()

	// One cloud failing must not empty the answer: the machines the others
	// can see are still worth having.
	if len(all) == 0 && len(errs) > 0 {
		return nil, errs[0]
	}
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "  %s\n", e)
	}

	// What this sweep re-read replaces the cache; what it did not read is
	// kept. Every scoped refresh goes through here - by cloud, region or
	// account - because each of them is a partial view of the estate, and
	// saving a partial view as the whole of it is how a refresh of one region
	// used to empty the cache of all the others.
	merged := all
	if c, err := inventory.LoadCache(cachePath); err == nil {
		merged = w.merge(c.Nodes, all)
	}
	if err := inventory.SaveCache(cachePath, merged); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not write cache: %v\n", err)
	}
	return merged, nil
}

// cloudNames lists what this build can reach, for an error that names the
// alternatives rather than only the mistake.
func cloudNames(ps []provider.Provider) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Cloud())
	}
	sort.Strings(out)
	return out
}

// noCloudsError explains that nothing is signed in, naming every cloud the
// build knows about and what each one wants.
func noCloudsError(absent []provider.Unavailable) error {
	var b strings.Builder
	for _, u := range absent {
		fmt.Fprintf(&b, "  %s: %s\n", u.Cloud, u.Reason)
	}
	return exitcode.New(exitcode.Creds, "no cloud credentials found",
		strings.TrimRight(b.String(), "\n"), nil)
}

// regionTimeout bounds how long one region may take before it is skipped.
// Generous enough for a slow but working region, short enough that a dead one
// does not hold up the sweep.
const regionTimeout = 20 * time.Second

// recordUse notes that a machine was connected to, so the list can put the
// ones you actually use at the top.
//
// Best effort: ordering is a convenience, and a broken state file must never
// stop you reaching a machine.
func recordUse(instanceID string) {
	path, err := inventory.RecentsPath()
	if err != nil {
		return
	}
	r := inventory.LoadRecents(path)
	r.Record(instanceID, time.Now())
	_ = r.Save(path, time.Now())
}
