package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
)

// scope is everything the flags narrow a command to.
//
// One scope serves every command - ls, the machine list, connect's name
// resolution and shell completion - so a flag means the same thing wherever
// it is given. The alternative, each command interpreting its own flags, is
// how --region came to scope a refresh while being silently ignored by ls on
// the cached path: ls --region eu-west-2 listed every region there was.
type scope struct {
	clouds    []string
	regions   []string
	accounts  []string
	statuses  []string
	tags      []tagMatch
	reachable bool
	via       string
}

// tagMatch is one --tag: a key that must be present, and optionally the value
// it must have.
type tagMatch struct {
	key, value string
	hasValue   bool
}

// scopeFrom reads the flags, refusing values that could only ever match
// nothing. An unknown cloud name would otherwise produce an empty list, which
// reads as "you have no machines" rather than "you mistyped".
func scopeFrom(opts *options) (scope, error) {
	s := scope{
		clouds:    splitList(opts.cloud),
		regions:   splitList(opts.region),
		accounts:  splitList(opts.profile),
		statuses:  splitList(opts.status),
		reachable: opts.reachable,
		via:       strings.ToLower(strings.TrimSpace(opts.via)),
	}
	if err := checkVia(s.via); err != nil {
		return scope{}, err
	}
	known := map[string]bool{}
	for _, p := range provider.All() {
		known[p.Cloud()] = true
	}
	for _, c := range s.clouds {
		if !known[c] {
			names := make([]string, 0, len(known))
			for k := range known {
				names = append(names, k)
			}
			sort.Strings(names)
			return scope{}, exitcode.New(exitcode.Err,
				fmt.Sprintf("there is no cloud called %q", c),
				"clouds this build can reach: "+strings.Join(names, ", "), nil)
		}
	}
	for _, raw := range opts.tags {
		t, err := parseTag(raw)
		if err != nil {
			return scope{}, err
		}
		s.tags = append(s.tags, t)
	}
	return s, nil
}

// splitList reads a comma-separated flag. Values are lower-cased because
// every field they are compared with is compared case-insensitively.
func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, strings.ToLower(p))
		}
	}
	return out
}

func parseTag(raw string) (tagMatch, error) {
	key, value, hasValue := strings.Cut(raw, "=")
	key = strings.TrimSpace(key)
	if key == "" {
		return tagMatch{}, exitcode.New(exitcode.Err,
			fmt.Sprintf("%q is not a tag filter", raw),
			"use --tag KEY to require a tag, or --tag KEY=VALUE to require its value", nil)
	}
	return tagMatch{key: key, value: strings.TrimSpace(value), hasValue: hasValue}, nil
}

// empty reports whether the scope narrows nothing.
func (s scope) empty() bool {
	return len(s.clouds) == 0 && len(s.regions) == 0 && len(s.accounts) == 0 &&
		len(s.statuses) == 0 && len(s.tags) == 0 && !s.reachable && s.via == ""
}

// keep reports whether one machine is inside the scope.
func (s scope) keep(n inventory.Node) bool {
	if len(s.clouds) > 0 && !contains(s.clouds, n.Cloud) {
		return false
	}
	if len(s.regions) > 0 && !contains(s.regions, strings.ToLower(n.Region)) {
		return false
	}
	if len(s.accounts) > 0 && !contains(s.accounts, accountName(n.Account)) {
		return false
	}
	if len(s.statuses) > 0 && !contains(s.statuses, strings.ToLower(n.Status)) {
		return false
	}
	if s.reachable && !n.Reachable {
		return false
	}
	if s.via != "" && !n.HasRoute(s.via) {
		return false
	}
	for _, t := range s.tags {
		if !t.matches(n) {
			return false
		}
	}
	return true
}

// apply narrows a list of machines to the scope.
func (s scope) apply(nodes []inventory.Node) []inventory.Node {
	if s.empty() {
		return nodes
	}
	out := make([]inventory.Node, 0, len(nodes))
	for _, n := range nodes {
		if s.keep(n) {
			out = append(out, n)
		}
	}
	return out
}

// matches compares tags without regard to case, because tag conventions are
// inconsistent - Environment, environment and ENV all occur on one estate -
// and a filter that missed on capitalisation would read as "no such machine".
func (t tagMatch) matches(n inventory.Node) bool {
	for k, v := range n.Tags {
		if !strings.EqualFold(k, t.key) {
			continue
		}
		return !t.hasValue || strings.EqualFold(v, t.value)
	}
	return false
}

// accountName is the name an account is known by for filtering. The ambient
// default has no name of its own on AWS, and the list shows it as "default",
// so that is what a user types to mean it.
func accountName(account string) string {
	if account == "" {
		return "default"
	}
	return strings.ToLower(account)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// sweep is what a refresh re-reads, derived from the scope.
//
// A refresh can only ask a cloud for one region or one account at a time, so
// a scope naming several of either sweeps them all and lets the result filter
// do the narrowing - slower, but never wrong.
type sweep struct {
	clouds      map[string]bool
	region      string
	account     string
	allAccounts bool
}

func (s scope) sweep(usable []provider.Provider, allProfiles bool) sweep {
	w := sweep{clouds: map[string]bool{}, allAccounts: allProfiles}
	for _, p := range usable {
		if len(s.clouds) == 0 || contains(s.clouds, p.Cloud()) {
			w.clouds[p.Cloud()] = true
		}
	}
	if len(s.regions) == 1 {
		w.region = s.regions[0]
	}
	switch {
	case len(s.accounts) == 1:
		w.account = s.accounts[0]
	case len(s.accounts) > 1:
		w.allAccounts = true
	}
	return w
}

// covers reports whether a cached machine is one this sweep re-read.
//
// A machine the sweep covers is replaced by what the sweep found, including by
// nothing if it has gone. One the sweep did not cover is kept. Getting this
// wrong in the other direction is how a refresh of one region used to empty
// the cache of every other region until the next full refresh.
func (w sweep) covers(n inventory.Node) bool {
	if !w.clouds[n.Cloud] {
		return false
	}
	if w.region != "" && !strings.EqualFold(n.Region, w.region) {
		return false
	}
	if w.account != "" && !w.allAccounts && accountName(n.Account) != w.account {
		return false
	}
	return true
}

// merge lays a sweep's result over the cache.
//
// Only what the sweep covers is taken from it, and only what it covers is
// replaced. Both halves matter, because a provider may return more than it was
// asked for: Azure lists a whole subscription whatever region is named. Taking
// every fresh machine while keeping the cached copies outside the region is how
// an earlier version of this doubled every Azure machine on a refresh of one
// AWS region.
//
// A machine never appears twice however the halves overlap. The fresh copy
// wins, since it is the newer of the two.
func (w sweep) merge(cached, fresh []inventory.Node) []inventory.Node {
	type key struct{ cloud, id string }
	out := make([]inventory.Node, 0, len(cached)+len(fresh))
	have := make(map[key]bool, len(cached)+len(fresh))
	for _, n := range fresh {
		k := key{n.Cloud, n.ID}
		if w.covers(n) && !have[k] {
			out = append(out, n)
			have[k] = true
		}
	}
	for _, n := range cached {
		k := key{n.Cloud, n.ID}
		if !w.covers(n) && !have[k] {
			out = append(out, n)
			have[k] = true
		}
	}
	return out
}
