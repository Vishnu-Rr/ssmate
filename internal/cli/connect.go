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
	"github.com/Vishnu-Rr/ssmate/internal/session"
	"github.com/Vishnu-Rr/ssmate/internal/ui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newConnectCmd(opts *options) *cobra.Command {
	var raw bool
	cmd := &cobra.Command{
		Use: "connect [target] [command...]",
		// ssh is what people type first, from az, gcloud and plain ssh. Without
		// the alias the root reads it as a machine name and fuzzy-matches it
		// against every name with an s, an s and an h in order.
		Aliases: []string{"ssh"},
		Short:   "Open a shell on a machine, or run one command on it",
		Long: "Target may be an identifier, a name, a private or public address, a tag\n" +
			"value or a bookmark. With no target, ssmate opens the machine list.\n\n" +
			"Anything after the target runs instead of a shell:\n\n" +
			"  ssmate connect web-1 uptime\n" +
			"  ssmate connect web-1 -- tail -n 50 /var/log/syslog\n\n" +
			"Use -- before a command that starts with a dash, or its flags would\n" +
			"be read as ssmate's own.\n\n" +
			"Through Azure Bastion, Azure Arc and OCI Bastion the command's exit\n" +
			"status comes back, so a script can branch on it. AWS Session Manager\n" +
			"does not carry one, so there ssmate reports only whether the session\n" +
			"itself succeeded.",
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
	return shellResult(h.Shell(ctx, node, route, provider.SessionOptions{
		Raw: raw, Print: opts.print, Reason: opts.reason, User: opts.user,
		Auth: opts.auth, Key: key, Command: command,
	}))
}

// shellResult turns a shell's ending into the command's result.
//
// Providers that exec ssh or az report a clean exit as
// session.ErrClosedByUser, so a held-open tunnel knows the user ended it and
// does not rebuild it. For a shell that same ending - the user typed exit, or
// the command run in its place succeeded - is simply success, and must not
// come back as an error and a non-zero exit.
func shellResult(err error) error {
	if errors.Is(err, session.ErrClosedByUser) {
		return nil
	}
	return err
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
	// A loose match - only the query's letters, in order, with gaps - is a
	// guess. However few there are, it is offered, never taken.
	loose := len(matches) > 0 && matches[0].Fuzzy

	switch {
	case len(matches) == 1 && !loose:
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
		if loose {
			var near []string
			for i, m := range matches {
				if i == 5 {
					near = append(near, fmt.Sprintf("and %d more", len(matches)-5))
					break
				}
				near = append(near, m.Node.DisplayName())
			}
			return inventory.Node{}, exitcode.New(
				exitcode.NotFound,
				fmt.Sprintf("nothing named %q; closest: %s", target, strings.Join(near, ", ")),
				"pass the name in full, or run interactively to pick from the list",
				nil,
			)
		}
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
			need := cloudsToSweep(c, cloudsInScope(ctx, sc, opts), time.Now())
			if len(need) == 0 {
				return sc.apply(joined(c.Nodes)), nil
			}
			return sc.apply(joined(sweepMissing(ctx, opts, c, need, path))), nil
		}
	}
	all, err := refreshInventory(ctx, opts, sc, path)
	if err != nil {
		return nil, err
	}
	return sc.apply(joined(all)), nil
}

// sweepMaxAge is how old a cloud's part of the cache may get before ssmate
// reads it again by itself. Within it the list opens at once; past it, the
// machines and their states are old enough to mislead.
const sweepMaxAge = 24 * time.Hour

// retryFailedAfter is how long a cloud whose automatic read failed is left
// alone, so one broken sign-in does not slow every command.
const retryFailedAfter = time.Hour

// cloudsToSweep picks, from the clouds signed in here and in scope, those the
// cache has never read in full or read too long ago, leaving out any whose
// automatic read failed recently.
func cloudsToSweep(c *inventory.Cache, clouds []string, now time.Time) []string {
	var out []string
	for _, cl := range clouds {
		if t, ok := c.Failed[cl]; ok && now.Sub(t) < retryFailedAfter {
			continue
		}
		if t, ok := c.SweptAt(cl); ok && now.Sub(t) < sweepMaxAge {
			continue
		}
		out = append(out, cl)
	}
	return out
}

// cloudsInScope lists the clouds signed in here that this command asked for.
// Signing in is decided from local configuration alone, so this costs no
// network call.
func cloudsInScope(ctx context.Context, sc scope, opts *options) []string {
	usable, _ := provider.Available(ctx)
	w := sc.sweep(usable, opts.allProfiles)
	out := make([]string, 0, len(w.clouds))
	for cl := range w.clouds {
		out = append(out, cl)
	}
	sort.Strings(out)
	return out
}

// sweepMissing reads the clouds in need in full and returns the cache with
// them merged in. If the read fails, the cache is still the best answer
// there is, so it is returned as it was, with the failure said.
func sweepMissing(ctx context.Context, opts *options, c *inventory.Cache, need []string, path string) []inventory.Node {
	t := opts.theme()
	why := "not in the list yet"
	if _, ok := c.SweptAt(need[0]); ok {
		why = "last read over a day ago"
	}
	fmt.Fprintln(os.Stderr, t.Dim(fmt.Sprintf("Reading %s (%s)...", strings.Join(need, ", "), why)))

	// The whole of each cloud, whatever this command narrows to: a partial
	// read could not be recorded as the cloud's.
	full := *opts
	full.cloud, full.region, full.profile = strings.Join(need, ","), "", ""
	fsc, err := scopeFrom(&full)
	if err == nil {
		var all []inventory.Node
		if all, err = refreshInventory(ctx, &full, fsc, path); err == nil {
			return all
		}
	}
	fmt.Fprintf(os.Stderr, "  could not read %s: %v\n", strings.Join(need, ", "), err)
	return c.Nodes
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
		mu     sync.Mutex
		all    []inventory.Node
		errs   []error
		wg     sync.WaitGroup
		read   = map[string]bool{}
		failed = map[string]bool{}
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
				failed[pv.Cloud()] = true
				return
			}
			read[pv.Cloud()] = true
			all = append(all, nodes...)
		}()
	}
	wg.Wait()

	// One cloud failing must not empty the answer: the machines the others
	// can see are still worth having.
	if len(all) == 0 && len(errs) > 0 {
		recordFailures(cachePath, failed)
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
	var cached *inventory.Cache
	if c, err := inventory.LoadCache(cachePath); err == nil {
		cached = c
	}
	merged, swept, fails := settle(w, cached, all, read, failed, time.Now().UTC())
	if err := inventory.SaveCacheSwept(cachePath, merged, swept, fails); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not write cache: %v\n", err)
	}
	return merged, nil
}

// settle folds one refresh into the cache: the machines it re-read replace
// what was cached for them, and its record of which clouds were read in full
// and which failed is updated.
//
// A cloud whose read failed was not re-read, so its cached machines stay.
// Treating it as covered would empty it from the list over one transient
// error, until the next good refresh. And only a read of a whole cloud, not
// one narrowed to a region or an account, stands for the cloud.
func settle(w sweep, cached *inventory.Cache, fresh []inventory.Node, read, failed map[string]bool, now time.Time) ([]inventory.Node, map[string]time.Time, map[string]time.Time) {
	clouds := make(map[string]bool, len(w.clouds))
	for cl, ok := range w.clouds {
		clouds[cl] = ok && !failed[cl]
	}
	w.clouds = clouds

	merged := fresh
	swept, fails := map[string]time.Time{}, map[string]time.Time{}
	if cached != nil {
		merged = w.merge(cached.Nodes, fresh)
		swept = cached.SweptClouds()
		for k, v := range cached.Failed {
			fails[k] = v
		}
	}
	whole := w.region == "" && w.account == ""
	for cl := range read {
		delete(fails, cl)
		if whole {
			swept[cl] = now
		}
	}
	for cl := range failed {
		fails[cl] = now
	}
	return merged, swept, fails
}

// recordFailures notes, in an existing cache, clouds whose read just failed,
// so they are not retried automatically on the next command.
func recordFailures(cachePath string, failed map[string]bool) {
	c, err := inventory.LoadCache(cachePath)
	if err != nil {
		return
	}
	fails := map[string]time.Time{}
	for k, v := range c.Failed {
		fails[k] = v
	}
	now := time.Now().UTC()
	for cl := range failed {
		fails[cl] = now
	}
	_ = inventory.SaveCacheSwept(cachePath, c.Nodes, c.SweptClouds(), fails)
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
