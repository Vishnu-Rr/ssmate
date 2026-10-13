package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/ui"
	"github.com/spf13/cobra"
)

func newCloudsCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "clouds",
		Short: "Show each cloud: configured or not, its accounts, regions and machines",
		Long: "clouds summarises every cloud this build can reach: whether this machine\n" +
			"is configured for it, which accounts and regions ssmate has found\n" +
			"machines in, and how many of them can be reached right now.\n\n" +
			"It reads only local configuration and the cache, so it answers at once.\n" +
			"It does not prove that credentials still work - ssmate doctor does that.",
		Example: "  ssmate clouds\n" +
			"  ssmate clouds --json\n" +
			"  ssmate clouds --refresh     re-read every cloud first",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var nodes []inventory.Node
			var fetched time.Time
			if opts.refresh {
				all, err := loadInventory(cmd.Context(), opts)
				if err != nil {
					return err
				}
				nodes, fetched = all, time.Now()
			} else if path, err := inventory.CachePath(); err == nil {
				if c, err := inventory.LoadCache(path); err == nil {
					nodes, fetched = joined(c.Nodes), c.FetchedAt
				}
			}
			summaries := summariseClouds(cmd.Context(), nodes)
			if opts.json {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(summaries)
			}
			return writeClouds(os.Stdout, summaries, fetched, opts.theme())
		},
	}
}

// cloudSummary is one cloud, as far as local state can say.
type cloudSummary struct {
	Cloud      string   `json:"cloud"`
	Configured bool     `json:"configured"`
	Reason     string   `json:"reason,omitempty"`
	Accounts   []string `json:"accounts"`
	Regions    []string `json:"regions"`
	Machines   int      `json:"machines"`
	Reachable  int      `json:"reachable"`
}

// summariseClouds pairs each provider's local detection with what the cache
// holds for it.
func summariseClouds(ctx context.Context, nodes []inventory.Node) []cloudSummary {
	type acc struct {
		accounts, regions map[string]bool
		machines, reach   int
	}
	seen := map[string]*acc{}
	for _, n := range nodes {
		a := seen[n.Cloud]
		if a == nil {
			a = &acc{accounts: map[string]bool{}, regions: map[string]bool{}}
			seen[n.Cloud] = a
		}
		a.accounts[ui.ShortAccount(n.Account)] = true
		if n.Region != "" {
			a.regions[n.Region] = true
		}
		a.machines++
		if n.Reachable {
			a.reach++
		}
	}

	var out []cloudSummary
	for _, p := range provider.All() {
		s := cloudSummary{Cloud: p.Cloud(), Accounts: []string{}, Regions: []string{}}
		s.Configured, s.Reason = p.Detect(ctx)
		if a := seen[p.Cloud()]; a != nil {
			s.Accounts, s.Regions = sortedKeys(a.accounts), sortedKeys(a.regions)
			s.Machines, s.Reachable = a.machines, a.reach
		}
		out = append(out, s)
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// writeClouds prints the summary.
func writeClouds(out io.Writer, clouds []cloudSummary, fetched time.Time, t ui.Theme) error {
	w := tabwriter.NewWriter(colourWriter(out), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, t.Heading("CLOUD\tCONFIGURED\tACCOUNTS\tREGIONS\tMACHINES\tREACHABLE"))
	for _, c := range clouds {
		configured := t.Online("yes")
		if !c.Configured {
			configured = t.Dim("no")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			t.Heading(c.Cloud), configured,
			orDash(t, listSummary(c.Accounts, 3)),
			orDash(t, countOf(c.Regions)),
			orDash(t, count(c.Machines)),
			orDash(t, count(c.Reachable)))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	// What to do next, which differs by what is missing. A table that ends at
	// the facts leaves the reader to work out the obvious next command.
	fmt.Fprintln(out)
	for _, c := range clouds {
		if !c.Configured && c.Reason != "" {
			fmt.Fprintf(out, "  %s %s\n", t.Heading(c.Cloud+":"), t.Dim(c.Reason))
		}
	}
	switch {
	case fetched.IsZero():
		fmt.Fprintln(out, t.Dim("  no machines cached yet; run ssmate ls --refresh"))
	default:
		fmt.Fprintln(out, t.Dim("  cache written "+ui.Ago(fetched, time.Now())+" ago "+
			"· ssmate doctor checks the credentials · ssmate ls --cloud <name> lists one"))
	}
	return nil
}

// listSummary shows the first few values and a count of the rest, so one
// estate with forty accounts does not push the other columns off the screen.
func listSummary(vs []string, max int) string {
	if len(vs) == 0 {
		return ""
	}
	if len(vs) <= max {
		return strings.Join(vs, ", ")
	}
	return strings.Join(vs[:max], ", ") + fmt.Sprintf(" +%d", len(vs)-max)
}

func countOf(vs []string) string {
	if len(vs) == 0 {
		return ""
	}
	return fmt.Sprint(len(vs))
}

func count(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprint(n)
}

func orDash(t ui.Theme, s string) string {
	if s == "" {
		return t.Dim("-")
	}
	return s
}
