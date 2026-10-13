package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/spf13/cobra"
)

func newInfoCmd(opts *options) *cobra.Command {
	var pretty bool
	cmd := &cobra.Command{
		Use:   "info <target>",
		Short: "Print everything known about one machine, as JSON",
		Long: "info resolves a name the same way connect does and prints the machine\n" +
			"as JSON, for scripting against.\n\n" +
			"  ssmate info web-1 | jq -r .private_ip\n" +
			"  ssmate info web-1 | jq -r .detail.bastion_name",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTargets(opts),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodes, err := loadInventory(cmd.Context(), opts)
			if err != nil {
				return err
			}
			// Resolve exactly as connect does, so a name that works there
			// works here. A tool whose two commands disagree about what a
			// name means is worse than one that cannot resolve names at all.
			node, err := chooseNode(nodes, args[0], opts)
			if err != nil {
				return err
			}
			return writeInfo(os.Stdout, node, pretty)
		},
	}
	cmd.Flags().BoolVar(&pretty, "pretty", false, "indent the JSON for reading rather than piping")
	return cmd
}

// writeInfo prints one machine as JSON.
//
// The shape is the node itself, identical to one element of ls --json, rather
// than a wrapper of its own. Two commands that describe the same thing
// differently is a tax on everyone who scripts against either.
func writeInfo(w *os.File, n inventory.Node, pretty bool) error {
	enc := json.NewEncoder(w)
	if pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(n); err != nil {
		return exitcode.New(exitcode.Err, fmt.Sprintf("writing JSON: %v", err), "", nil)
	}
	return nil
}
