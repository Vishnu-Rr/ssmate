package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/ui"
	"github.com/spf13/cobra"
)

// Output formats for ls. Each is for a different reader: the table for a
// person, wide for a person who needs to copy an address, json for a program,
// and ids and names for a pipeline that wants one value per line.
var lsFormats = []string{"table", "wide", "json", "ids", "names"}

// Orders for ls, matching the machine list's so the two agree.
var lsOrders = []string{"name", "cloud", "region", "status", "recent"}

func newLsCmd(opts *options) *cobra.Command {
	var output, order string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List every machine ssmate can see",
		Long: "ls shows every machine across every account and region of every cloud\n" +
			"you are signed in to, with whether each can be reached right now.",
		Example: "  ssmate ls                               every machine, every cloud\n" +
			"  ssmate ls --cloud azure                 one cloud\n" +
			"  ssmate ls --region eu-west-2,us-west-2  two regions\n" +
			"  ssmate ls --status \"no gateway\"         machines with no way in\n" +
			"  ssmate ls --tag Environment=production  by tag\n" +
			"  ssmate ls --reachable --sort recent     what you can reach, most used first\n" +
			"  ssmate ls --via ssm                     machines reached through Session Manager\n" +
			"  ssmate ls -o wide                       with identifiers and addresses\n" +
			"  ssmate ls -o ids | xargs ...            one identifier per line\n" +
			"  ssmate ls --refresh --cloud oci         re-read one cloud, keep the rest",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.json {
				output = "json"
			}
			if !contains(lsFormats, output) {
				return exitcode.New(exitcode.Err, fmt.Sprintf("%q is not an output format", output),
					"--output takes "+strings.Join(lsFormats, ", "), nil)
			}
			if !contains(lsOrders, order) {
				return exitcode.New(exitcode.Err, fmt.Sprintf("%q is not an order", order),
					"--sort takes "+strings.Join(lsOrders, ", "), nil)
			}
			nodes, err := loadInventory(cmd.Context(), opts)
			if err != nil {
				return err
			}
			sortMachines(nodes, order, useScore())
			return writeMachines(os.Stdout, nodes, output, opts.theme())
		},
	}
	f := cmd.Flags()
	f.StringVarP(&output, "output", "o", "table", "`format`: "+strings.Join(lsFormats, ", "))
	f.StringVar(&order, "sort", "name", "`order`: "+strings.Join(lsOrders, ", "))
	_ = cmd.RegisterFlagCompletionFunc("output", fixedValues(lsFormats))
	_ = cmd.RegisterFlagCompletionFunc("sort", fixedValues(lsOrders))
	return cmd
}

func fixedValues(vs []string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return vs, cobra.ShellCompDirectiveNoFileComp
	}
}

// sortMachines orders the list. Every order falls back to name, then
// identifier, so the same command prints the same order every time - a list a
// script diffs must not reshuffle itself between identical runs.
func sortMachines(nodes []inventory.Node, by string, recent func(string) (float64, time.Time)) {
	score := func(id string) float64 {
		if recent == nil {
			return 0
		}
		s, _ := recent(id)
		return s
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		switch by {
		case "cloud":
			if a.Cloud != b.Cloud {
				return a.Cloud < b.Cloud
			}
		case "region":
			if a.Region != b.Region {
				return a.Region < b.Region
			}
		case "status":
			if a.Reachable != b.Reachable {
				return a.Reachable
			}
			if a.Status != b.Status {
				return a.Status < b.Status
			}
		case "recent":
			if sa, sb := score(a.ID), score(b.ID); sa != sb {
				return sa > sb
			}
		}
		if an, bn := a.DisplayName(), b.DisplayName(); an != bn {
			return an < bn
		}
		return a.ID < b.ID
	})
}

// writeMachines prints the list in the chosen format.
func writeMachines(out io.Writer, nodes []inventory.Node, format string, t ui.Theme) error {
	switch format {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(nodes)
	case "ids", "names":
		// One value per line and nothing else - no heading, no colour - so
		// the output can go straight into xargs or a while loop.
		for _, n := range nodes {
			v := n.ID
			if format == "names" {
				v = n.DisplayName()
			}
			fmt.Fprintln(out, v)
		}
		return nil
	}

	// The table leaves identifiers out by default. They differ in length by
	// an order of magnitude between clouds - an EC2 instance ID is 19
	// characters and an OCI OCID over a hundred - so including one sizes the
	// table to a string nobody reads. wide puts it back, last, where its
	// length does the least harm.
	w := tabwriter.NewWriter(colourWriter(out), 0, 0, 2, ' ', 0)
	head := "NAME\tCLOUD\tREGION\tACCOUNT\tVIA\tSTATUS"
	if format == "wide" {
		head += "\tADDRESS\tID"
	}
	fmt.Fprintln(w, t.Heading(head))
	for _, n := range nodes {
		via := n.Via
		if via == "" {
			via = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s",
			n.DisplayName(), t.Dim(n.Cloud), t.Dim(n.Region),
			t.Dim(ui.ShortAccount(n.Account)), t.Dim(via), t.StatusStyle(n.Status)(n.Status))
		if format == "wide" {
			addr := n.PrivateIP
			if addr == "" {
				addr = "-"
			}
			fmt.Fprintf(w, "\t%s\t%s", addr, t.Dim(n.ID))
		}
		fmt.Fprintln(w)
	}
	return w.Flush()
}
