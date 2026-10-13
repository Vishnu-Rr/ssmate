package inventory

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Target is one profile-and-region pair to sweep.
type Target struct {
	Profile string
	Region  string
}

// String names a target the way Node.Where does, so the default profile -
// which has no name - reports as a bare region rather than a leading slash.
func (t Target) String() string {
	if t.Profile == "" {
		return t.Region
	}
	return t.Profile + "/" + t.Region
}

// RegionFetcher fetches the nodes for one target. It exists so FanOut can be
// tested without AWS clients.
type RegionFetcher func(ctx context.Context, profile, region string) ([]Node, error)

// FanOut sweeps every target concurrently, up to concurrency at a time.
//
// It returns whatever it managed to collect alongside whatever failed, rather
// than failing the sweep on the first error. Accounts routinely have regions
// that are enabled but unreachable, or that the caller cannot read; losing
// the other twenty regions to one of those would make the tool useless in
// exactly the estates it is built for.
func FanOut(ctx context.Context, targets []Target, concurrency int, fetch RegionFetcher) ([]Node, []error) {
	if len(targets) == 0 {
		return nil, nil
	}
	if concurrency < 1 {
		concurrency = 1
	}

	var (
		mu    sync.Mutex
		nodes []Node
		errs  []error
		wg    sync.WaitGroup
	)
	sem := make(chan struct{}, concurrency)

	for _, tgt := range targets {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(tgt Target) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			got, err := fetch(ctx, tgt.Profile, tgt.Region)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", tgt, err))
				return
			}
			nodes = append(nodes, got...)
		}(tgt)
	}
	wg.Wait()

	// Goroutines finish in arbitrary order, so sort for a stable result.
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].ID != nodes[j].ID {
			return nodes[i].ID < nodes[j].ID
		}
		return nodes[i].Region < nodes[j].Region
	})
	return nodes, errs
}
