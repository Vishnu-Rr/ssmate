package cli

import (
	"sort"
	"strings"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/spf13/cobra"
)

// completeTargets offers machine names to the shell.
//
// It reads only the cache and never refreshes. A completion runs while someone
// is holding down Tab, so it has a fraction of a second before it feels
// broken, and a sweep takes the better part of a minute. An empty completion
// on a cold cache is the right answer; a shell that hangs is not.
func completeTargets(opts *options) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
		// Only the first argument names a machine. Offering machine names
		// where a port specification belongs is worse than offering nothing.
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return suggestTargets(cachedNodes(opts), bookmarkNames(), prefix),
			cobra.ShellCompDirectiveNoFileComp
	}
}

// cachedNodes reads what is already known, honouring --cloud so completion
// matches what the same command would act on.
func cachedNodes(opts *options) []inventory.Node {
	path, err := inventory.CachePath()
	if err != nil {
		return nil
	}
	c, err := inventory.LoadCache(path)
	if err != nil {
		return nil
	}
	// A completion must offer what the same command would act on, so it
	// honours the same scope. A bad flag value offers nothing rather than
	// failing: the error belongs to the command, not to the Tab key.
	sc, err := scopeFrom(opts)
	if err != nil {
		return nil
	}
	return sc.apply(joined(c.Nodes))
}

// bookmarkNames lists the shorter names someone has given machines. They are
// offered first because a bookmark exists precisely so it is what gets typed.
func bookmarkNames() []string {
	_, bm, err := openBookmarks()
	if err != nil || bm == nil {
		return nil
	}
	pairs := bm.Sorted()
	names := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		names = append(names, kv[0])
	}
	return names
}

// suggestTargets builds the completion list.
//
// Each entry carries a description after a tab, which zsh and fish show
// beside the name. On an estate where forty machines are called something
// like "worker", the cloud and region are what tell them apart, so a bare
// list of identical names would be no help at all.
func suggestTargets(nodes []inventory.Node, bookmarks []string, prefix string) []string {
	// Bookmarks and machines are ordered separately and then joined, so the
	// bookmarks stay at the top. Sorting the combined list is what put them
	// wherever the alphabet happened to place them.
	var named []string
	for _, b := range bookmarks {
		if strings.HasPrefix(b, prefix) {
			named = append(named, b+"\tbookmark")
		}
	}
	sort.Strings(named)

	var out []string

	// Names are not unique across clouds, regions or accounts. A name that
	// appears once completes to itself. One that appears more than once
	// completes to identifiers, because the bare name is ambiguous and the
	// tool would only refuse it later.
	//
	// The match still runs against the name in both cases. Someone typing
	// "worker" knows the name, not the identifier, so matching identifiers
	// against what they typed would offer nothing at all - which is how this
	// was wrong the first time.
	seen := map[string]int{}
	for _, n := range nodes {
		seen[n.DisplayName()]++
	}
	for _, n := range nodes {
		name := n.DisplayName()
		matches := strings.HasPrefix(name, prefix) || strings.HasPrefix(n.ID, prefix)
		if !matches {
			continue
		}
		if seen[name] == 1 {
			out = append(out, name+"\t"+describe(n))
			continue
		}
		out = append(out, n.ID+"\t"+name+" "+describe(n))
	}
	sort.Strings(out)
	return append(named, out...)
}

// describe is the short context a shell shows beside a completion.
func describe(n inventory.Node) string {
	parts := []string{n.Cloud, n.Region}
	if n.Status != "" {
		parts = append(parts, n.Status)
	}
	return strings.Join(parts, " ")
}
