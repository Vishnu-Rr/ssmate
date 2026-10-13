// Package provider is the seam between the tool and the clouds it reaches
// machines in.
//
// Everything above this package works in the vocabulary of inventory.Node and
// never names a cloud. Everything below it may use one cloud's own concepts
// freely. That division is what lets a single command, a single picker and a
// single diagnosis serve services as different as a tokenised relay, a metered
// gateway and one you provision yourself.
package provider

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/Vishnu-Rr/ssmate/internal/doctor"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/session"
)

// Options shape an inventory sweep.
//
// Caching lives above this package rather than inside each provider, because
// one cache holds every cloud's machines and the freshness decision belongs to
// whoever is about to show them. A provider always performs a live fetch.
type Options struct {
	// Account restricts the sweep to one profile, subscription or tenancy.
	//
	// Empty is not "no account" - it means whichever one the cloud's own
	// tooling would pick with no flags, and each provider resolves that
	// itself: for AWS the unset profile, for Azure whatever `az account show`
	// reports, for OCI the profile literally named DEFAULT. A provider must
	// never treat empty as an error or substitute a name of its own, because
	// the user who passed no --account is asking for exactly that default.
	Account string

	// AllAccounts sweeps every account the local credentials expose, which is
	// the whole point on estates split across many of them.
	AllAccounts bool

	// Region restricts the sweep to one region. Empty means every region the
	// account has enabled.
	Region string

	// Concurrency bounds simultaneous per-region calls. Zero picks a default.
	Concurrency int

	// Progress and Warn report to the user mid-sweep. Both may be nil, and a
	// provider must work when they are.
	Progress func(string)
	Warn     func(string)
}

func (o Options) progress(format string, a ...any) {
	if o.Progress != nil {
		o.Progress(fmt.Sprintf(format, a...))
	}
}

func (o Options) warn(format string, a ...any) {
	if o.Warn != nil {
		o.Warn(fmt.Sprintf(format, a...))
	}
}

// Report lets a provider emit progress without exporting the helpers.
func (o Options) Report(format string, a ...any) { o.progress(format, a...) }

// Skipped lets a provider report something it could not reach, without
// failing the whole sweep. An account with one unreachable region must still
// return the other twenty.
func (o Options) Skipped(format string, a ...any) { o.warn(format, a...) }

// SessionOptions shape one connection.
type SessionOptions struct {
	// Raw asks for the cloud's default shell rather than a login shell.
	Raw bool

	// Command runs one command instead of opening a shell.
	//
	// Empty opens a shell. Whether the remote exit status survives the trip
	// home differs by cloud and a provider must not pretend otherwise: ssh
	// returns it, while Session Manager does not carry it at all. A caller
	// that needs to branch on success has to check the output, not the code,
	// on clouds where it cannot be known.
	Command []string

	// Print shows the equivalent vendor command and opens nothing.
	Print bool

	// Reason is recorded wherever the cloud has somewhere to record it.
	Reason string

	// Bind is the local address a forwarded port listens on. Empty means
	// loopback.
	//
	// Amazon's plugin always binds loopback and offers no way to change it,
	// so on AWS this is only ever checked, never applied. A provider running
	// ssh -L can honour it, and must, or --bind 0.0.0.0 reports success
	// against a port that is not actually reachable from anywhere.
	Bind string

	// Auth is how to log in, on the clouds that ask: "password", "key" or
	// "aad". Empty is the cloud's own default. Amazon never logs in at all,
	// and OCI always uses SSH keys, so today only Azure reads it.
	Auth string

	// Key is a private key to log in to the machine with. It is the user's own
	// key, for the machine - never the throwaway key a bastion session is
	// opened with, which the machine has never seen.
	Key string

	// User is the account to log in as on the machine.
	//
	// Empty means the provider guesses. Amazon never needs this - the agent
	// runs as its own user and no login happens. Every cloud that reaches a
	// machine over SSH does need it, and none of them can be asked: OCI's API
	// does not carry a login name, so the provider infers one from the image
	// (opc for Oracle Linux, ubuntu for Canonical), and Azure wants an
	// explicit --local-user. A guess from an image is a guess, so a user who
	// knows better must be able to say so.
	User string

	// OnReconnect is called when a tunnel drops and is rebuilt. Every cloud
	// here ends a healthy tunnel on its own schedule, so a reconnect is
	// normal and the user should be told rather than left wondering. May be
	// nil. Drawing stays above this package; the provider only reports.
	OnReconnect func(session.ReconnectEvent)
}

// Provider reaches machines in one cloud.
//
// A provider is responsible for the facts only it can know: whether the local
// machine is signed in, what exists, whether a given machine can be reached
// right now and why not, and how to open a session. It is not responsible for
// caching, ranking, resolving a name to a machine, or drawing anything.
type Provider interface {
	// Cloud is the identifier this provider registers under, and the value it
	// stamps on every node it returns.
	Cloud() string

	// Detect reports whether this cloud is usable here. When it is not, the
	// string is a single line a human can act on, not a stack trace: the tool
	// stays silent about clouds you do not use, so this text is only ever seen
	// by someone who asked.
	Detect(ctx context.Context) (bool, string)

	// Inventory lists every machine this provider can see, each with the
	// routes into it that this provider knows about and their verdicts.
	Inventory(ctx context.Context, o Options) ([]inventory.Node, error)

	// Checks returns this cloud's diagnosis.
	//
	// query is what the user typed, empty when they asked for a general
	// check-up. A non-empty query with a nil node means the target could not
	// be resolved, which is itself a finding worth reporting rather than an
	// error to swallow.
	Checks(ctx context.Context, n *inventory.Node, query string) doctor.Registry
}

// Handler opens sessions along one kind of route.
//
// Finding machines and getting into them are separate jobs, and they do not
// always belong to the same cloud: a machine found in one cloud may be reached
// through another cloud's session service. So a provider finds machines and
// a handler, registered under a route kind, opens the sessions. Each handler
// connects to the route's Target, Account and Region. It may read the node's
// own Detail only when the route belongs to the node's own cloud, since only
// then were those facts written by the same package.
type Handler interface {
	// Kind is the route kind this handler serves.
	Kind() string

	// Shell opens an interactive session, or runs SessionOptions.Command.
	Shell(ctx context.Context, n inventory.Node, r inventory.Route, o SessionOptions) error

	// Forward opens a tunnel.
	Forward(ctx context.Context, n inventory.Node, r inventory.Route, f session.Forward, o SessionOptions) error
}

// Prober is a handler that can try a route without opening a terminal on the
// machine: enough of a session to prove the way in carries traffic, and then
// nothing. It is what doctor --probe runs, for routes whose readiness can only
// be judged, not known, before something tries them.
type Prober interface {
	Probe(ctx context.Context, n inventory.Node, r inventory.Route, o SessionOptions) error
}

var (
	mu         sync.RWMutex
	registered = map[string]Provider{}
	handlers   = map[string]Handler{}
)

// RegisterHandler adds a route handler. Like providers, handlers register from
// their own package's init.
func RegisterHandler(h Handler) {
	mu.Lock()
	defer mu.Unlock()
	handlers[h.Kind()] = h
}

// HandlerFor returns the handler for a route kind.
func HandlerFor(kind string) (Handler, bool) {
	mu.RLock()
	defer mu.RUnlock()
	h, ok := handlers[kind]
	return h, ok
}

// Register adds a provider. Providers register from their own package's init,
// so a build can include or exclude a cloud by import alone.
func Register(p Provider) {
	mu.Lock()
	defer mu.Unlock()
	registered[p.Cloud()] = p
}

// All returns every registered provider, ordered so output is stable.
func All() []Provider {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Provider, 0, len(registered))
	for _, p := range registered {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cloud() < out[j].Cloud() })
	return out
}

// For returns one provider by cloud identifier.
func For(cloud string) (Provider, bool) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := registered[cloud]
	return p, ok
}

// Unavailable is a cloud that is registered but not usable here, with the
// reason.
type Unavailable struct {
	Cloud  string
	Reason string
}

// Available splits the registered providers into those usable on this machine
// and those not, with a reason for each exclusion.
//
// Detection runs concurrently because one cloud's credential check must not be
// delayed by another's, and a signed-out cloud can be slow to say so.
func Available(ctx context.Context) ([]Provider, []Unavailable) {
	all := All()
	type verdict struct {
		p      Provider
		ok     bool
		reason string
	}
	results := make([]verdict, len(all))
	var wg sync.WaitGroup
	for i, p := range all {
		wg.Add(1)
		go func(i int, p Provider) {
			defer wg.Done()
			ok, reason := p.Detect(ctx)
			results[i] = verdict{p, ok, reason}
		}(i, p)
	}
	wg.Wait()

	var usable []Provider
	var absent []Unavailable
	for _, v := range results {
		if v.ok {
			usable = append(usable, v.p)
			continue
		}
		absent = append(absent, Unavailable{Cloud: v.p.Cloud(), Reason: v.reason})
	}
	return usable, absent
}
