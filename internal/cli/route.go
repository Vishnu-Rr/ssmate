package cli

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
)

// routesEnv names the environment variable that sets a different default
// order of ways in, as a comma-separated list such as "arc,ssm".
const routesEnv = "SSMATE_ROUTES"

var warnOrderOnce sync.Once

// routeOrder is the order ways in are preferred, best first.
//
// A name the variable does not recognise is reported once rather than
// ignored, because a misspelled preference that silently does nothing is the
// kind of thing someone debugs for an afternoon.
func routeOrder() []string {
	order, unknown := inventory.ParseRouteOrder(os.Getenv(routesEnv))
	if len(unknown) > 0 {
		warnOrderOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "  %s: ignoring %s; known ways in: %s\n",
				routesEnv, strings.Join(unknown, ", "), strings.Join(routeNames(), ", "))
		})
	}
	return order
}

// routeNames is every name --via accepts, short labels first.
func routeNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, k := range inventory.RouteKinds() {
		add(inventory.RouteLabel(k))
	}
	for _, k := range inventory.RouteKinds() {
		add(k)
	}
	return out
}

// checkVia refuses a --via that names no way in at all, so a typo reads as a
// typo rather than as "no machines".
func checkVia(via string) error {
	if via == "" || inventory.KnownRoute(via) {
		return nil
	}
	return exitcode.New(exitcode.Err, fmt.Sprintf("%q is not a way in", via),
		"--via takes "+strings.Join(routeNames(), ", "), nil)
}

// pickRoute chooses how to reach a machine.
//
// The first ready route in the preferred order wins. When none is ready the
// first in order is still tried, because the verdict came from a cache that
// may be older than the machine's last start, and refusing on a stale guess
// is worse than letting the cloud say no. --via narrows the choice to routes
// answering to that name.
func pickRoute(n inventory.Node, via string, order []string) (inventory.Route, provider.Handler, error) {
	if err := checkVia(via); err != nil {
		return inventory.Route{}, nil, err
	}
	var candidates []inventory.Route
	for _, r := range n.OrderedRoutes(order) {
		if via == "" || inventory.RouteMatches(r.Kind, via) {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		if via != "" && len(n.Routes) > 0 {
			var have []string
			for _, r := range n.OrderedRoutes(order) {
				have = append(have, inventory.RouteLabel(r.Kind))
			}
			return inventory.Route{}, nil, exitcode.New(exitcode.NotConnected,
				fmt.Sprintf("%s cannot be reached via %s", n.DisplayName(), via),
				"it can be reached via: "+strings.Join(have, ", "), nil)
		}
		return inventory.Route{}, nil, exitcode.New(exitcode.NotConnected,
			fmt.Sprintf("%s has no way in", n.DisplayName()),
			"ssmate doctor "+n.DisplayName()+"   # what is missing, and how to add it", nil)
	}
	best := candidates[0]
	for _, r := range candidates {
		if r.Ready {
			best = r
			break
		}
	}
	h, ok := provider.HandlerFor(best.Kind)
	if !ok {
		return inventory.Route{}, nil, exitcode.New(exitcode.Err,
			fmt.Sprintf("%s is reached via %s, which this build cannot open", n.DisplayName(), best.Kind),
			"", nil)
	}
	return best, h, nil
}
