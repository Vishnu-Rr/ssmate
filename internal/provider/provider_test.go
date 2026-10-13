package provider

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/doctor"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

// fake is a provider that records what it was asked and answers as told.
type fake struct {
	cloud   string
	ok      bool
	reason  string
	nodes   []inventory.Node
	err     error
	slow    chan struct{}
	queries []string
	mu      sync.Mutex
}

func (f *fake) Cloud() string { return f.cloud }

func (f *fake) Detect(context.Context) (bool, string) {
	if f.slow != nil {
		<-f.slow
	}
	return f.ok, f.reason
}

func (f *fake) Inventory(context.Context, Options) ([]inventory.Node, error) {
	return f.nodes, f.err
}

func (f *fake) Checks(_ context.Context, _ *inventory.Node, query string) doctor.Registry {
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.mu.Unlock()
	return nil
}

// withRegistry swaps the package registry for the duration of a test, so tests
// never see providers a real build registered from init.
func withRegistry(t *testing.T, ps ...Provider) {
	t.Helper()
	mu.Lock()
	saved := registered
	registered = map[string]Provider{}
	mu.Unlock()
	for _, p := range ps {
		Register(p)
	}
	t.Cleanup(func() {
		mu.Lock()
		registered = saved
		mu.Unlock()
	})
}

func TestAllIsOrderedSoOutputIsStable(t *testing.T) {
	withRegistry(t, &fake{cloud: "oci"}, &fake{cloud: "aws"}, &fake{cloud: "azure"})
	got := All()
	want := []string{"aws", "azure", "oci"}
	if len(got) != len(want) {
		t.Fatalf("got %d providers, want %d", len(got), len(want))
	}
	for i, p := range got {
		if p.Cloud() != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestAvailableSeparatesUsableFromAbsentWithAReason(t *testing.T) {
	withRegistry(t,
		&fake{cloud: "aws", ok: true},
		&fake{cloud: "azure", ok: false, reason: "run az login"},
	)
	usable, absent := Available(context.Background())
	if len(usable) != 1 || usable[0].Cloud() != "aws" {
		t.Fatalf("usable = %v, want just aws", usable)
	}
	if len(absent) != 1 || absent[0].Cloud != "azure" {
		t.Fatalf("absent = %+v, want azure", absent)
	}
	if absent[0].Reason != "run az login" {
		t.Errorf("an unusable cloud must carry a reason a person can act on, got %q", absent[0].Reason)
	}
}

func TestAvailableDetectsConcurrently(t *testing.T) {
	// One signed-out cloud can be slow to say so. If detection were serial,
	// a single slow provider would delay every other one, which on a machine
	// signed in to one cloud is the common case rather than the rare one.
	gate := make(chan struct{})
	slow := &fake{cloud: "azure", ok: false, reason: "slow", slow: gate}
	fast := &fake{cloud: "aws", ok: true}
	withRegistry(t, slow, fast)

	done := make(chan struct{})
	go func() {
		Available(context.Background())
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Available returned before the slow provider answered")
	default:
	}
	close(gate)
	<-done
}

func TestForFindsAProviderByCloud(t *testing.T) {
	withRegistry(t, &fake{cloud: "aws"})
	if _, ok := For("aws"); !ok {
		t.Fatal("registered cloud should be findable")
	}
	if _, ok := For("gcp"); ok {
		t.Fatal("an unregistered cloud must report missing rather than a zero provider")
	}
}

func TestOptionsReportersToleratNilHooks(t *testing.T) {
	// A provider must work when nobody is listening. Progress and Warn are
	// nil in any non-interactive caller.
	var o Options
	o.Report("sweeping %d", 3)
	o.Skipped("skipped %v", errors.New("boom"))
}

func TestOptionsReportersReachTheHooks(t *testing.T) {
	var progress, warned string
	o := Options{
		Progress: func(s string) { progress = s },
		Warn:     func(s string) { warned = s },
	}
	o.Report("sweeping %d region(s)", 6)
	o.Skipped("skipped: %v", errors.New("eu-west-1 unreachable"))
	if progress != "sweeping 6 region(s)" {
		t.Errorf("Report = %q", progress)
	}
	if warned != "skipped: eu-west-1 unreachable" {
		t.Errorf("Skipped = %q", warned)
	}
}
