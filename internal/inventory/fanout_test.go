package inventory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func targets(n int) []Target {
	out := make([]Target, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Target{Profile: "default", Region: fmt.Sprintf("r-%02d", i)})
	}
	return out
}

func TestFanOutCollectsFromEveryTarget(t *testing.T) {
	fetch := func(ctx context.Context, profile, region string) ([]Node, error) {
		return []Node{{ID: "i-" + region, Account: profile, Region: region}}, nil
	}
	nodes, errs := FanOut(context.Background(), targets(10), 4, fetch)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(nodes) != 10 {
		t.Fatalf("got %d nodes, want 10", len(nodes))
	}
}

func TestFanOutContinuesPastAFailingRegion(t *testing.T) {
	fetch := func(ctx context.Context, profile, region string) ([]Node, error) {
		if region == "r-03" {
			return nil, errors.New("access denied")
		}
		return []Node{{ID: "i-" + region}}, nil
	}
	nodes, errs := FanOut(context.Background(), targets(10), 4, fetch)
	if len(nodes) != 9 {
		t.Fatalf("got %d nodes, want 9", len(nodes))
	}
	if len(errs) != 1 {
		t.Fatalf("got %d errors, want 1", len(errs))
	}
}

func TestFanOutRespectsConcurrencyLimit(t *testing.T) {
	var live, peak int64
	var mu sync.Mutex
	fetch := func(ctx context.Context, profile, region string) ([]Node, error) {
		n := atomic.AddInt64(&live, 1)
		mu.Lock()
		if n > peak {
			peak = n
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt64(&live, -1)
		return nil, nil
	}
	if _, errs := FanOut(context.Background(), targets(20), 3, fetch); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	mu.Lock()
	defer mu.Unlock()
	if peak > 3 {
		t.Fatalf("peak concurrency %d exceeded limit of 3", peak)
	}
}

func TestFanOutStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var started int64
	fetch := func(ctx context.Context, profile, region string) ([]Node, error) {
		if atomic.AddInt64(&started, 1) == 2 {
			cancel()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
			return nil, nil
		}
	}
	FanOut(ctx, targets(50), 2, fetch)
	if n := atomic.LoadInt64(&started); n >= 50 {
		t.Fatalf("started %d fetches despite cancellation", n)
	}
}

func TestFanOutIsDeterministicallyOrdered(t *testing.T) {
	fetch := func(ctx context.Context, profile, region string) ([]Node, error) {
		// Sleep inversely to region order, so completion order differs
		// from target order and a naive append would be unstable.
		if region == "r-00" {
			time.Sleep(20 * time.Millisecond)
		}
		return []Node{{ID: "i-" + region, Region: region}}, nil
	}
	nodes, _ := FanOut(context.Background(), targets(5), 5, fetch)
	for i := 1; i < len(nodes); i++ {
		if nodes[i-1].ID > nodes[i].ID {
			t.Fatalf("results not sorted: %s before %s", nodes[i-1].ID, nodes[i].ID)
		}
	}
}

func TestFanOutHandlesEmptyTargets(t *testing.T) {
	nodes, errs := FanOut(context.Background(), nil, 4, nil)
	if len(nodes) != 0 || len(errs) != 0 {
		t.Fatalf("empty targets produced %d nodes and %d errors", len(nodes), len(errs))
	}
}
