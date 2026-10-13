// Package cli defines ssmate's command tree.
package cli

import (
	"context"
	"io"
	"os"

	"github.com/Vishnu-Rr/ssmate/internal/ui"
	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"
	// Providers register themselves from their own package's init, so a build
	// includes a cloud by importing it and excludes one by not. This is the
	// only place that list lives.
	_ "github.com/Vishnu-Rr/ssmate/internal/provider/aws"
	_ "github.com/Vishnu-Rr/ssmate/internal/provider/azure"
	_ "github.com/Vishnu-Rr/ssmate/internal/provider/oci"
)

// options holds the global flags, shared by every subcommand.
type options struct {
	profile     string
	region      string
	allProfiles bool
	refresh     bool
	json        bool
	noInput     bool
	print       bool
	bind        string
	reason      string
	user        string
	cloud       string
	status      string
	tags        []string
	reachable   bool
	auth        string
	key         string
	via         string
	probe       bool
}

func (o *options) symbols() ui.Symbols {
	return ui.NewSymbols(ui.SupportsUnicode(os.Getenv))
}

// theme returns the colour scheme for this invocation. Colour is suppressed
// when NO_COLOR is set, when the terminal is dumb, and when stdout is not a
// terminal, so piping output never injects escape codes into it.
func (o *options) theme() ui.Theme {
	return ui.NewTheme(ui.ColorEnabled(os.Getenv, fileIsTerminal(os.Stdout)))
}

// colourWriter adapts coloured output to what the terminal can actually draw.
//
// The palette is written in full colour, and a terminal that only supports
// 256 colours, or 16, renders full-colour sequences as the wrong colour or as
// visible junk. The machine list gets this for free from its renderer; plain
// command output does not, so it goes through here. With colour off or output
// piped the writer strips colour entirely, which is the same promise NO_COLOR
// makes.
func colourWriter(w io.Writer) io.Writer {
	return colorprofile.NewWriter(w, os.Environ())
}

// NewRoot builds the command tree.
//
// With no arguments the root opens the picker. That is the k9s and lazygit
// shape: interactive by default, with verbs underneath for scripting.
func NewRoot(version string) *cobra.Command {
	opts := &options{}

	root := &cobra.Command{
		Use:   "ssmate [target]",
		Short: "Reach private machines in AWS, Azure and OCI by name",
		Long: "ssmate finds machines across every account and region in AWS, Azure and\n" +
			"OCI by name, address or tag, opens a shell or a tunnel to them without\n" +
			"a jump host or a public address, and explains precisely why when it\n" +
			"cannot.\n\n" +
			"You do not need to know the identifier, or which cloud it is in.",
		Version:           version,
		SilenceUsage:      true,
		SilenceErrors:     true,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: completeTargets(opts),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return runConnectRaw(cmd.Context(), opts, args[0], false, args[1:])
			}
			return runBrowse(cmd.Context(), opts)
		},
	}

	f := root.PersistentFlags()

	// Which machines. Every one of these narrows the same way in every
	// command, because they all go through one scope.
	f.StringVar(&opts.cloud, "cloud", "", "only these `clouds`: aws, azure, oci; comma-separate several")
	f.StringVar(&opts.region, "region", "", "only these `regions`; comma-separate several")
	f.StringVar(&opts.profile, "account", "", "only these `accounts`: a profile, subscription or tenancy; comma-separate several")
	f.StringVar(&opts.profile, "profile", "", "the same as --account, for `accounts` named the vendor's way")
	f.StringVar(&opts.status, "status", "", "only machines in these `states`, such as online or \"no gateway\"")
	f.StringArrayVar(&opts.tags, "tag", nil, "only machines with this tag: `KEY[=VALUE]`, repeatable")
	f.BoolVar(&opts.reachable, "reachable", false, "only machines that can be reached right now")
	f.BoolVar(&opts.allProfiles, "all-profiles", false, "search every account each cloud's configuration lists")

	// How to connect.
	f.StringVar(&opts.user, "user", "", "log in as this `user`; defaults to the account the machine was built with")
	f.StringVar(&opts.auth, "auth", "", "how to log in on Azure: `method` is password, key or aad; by default Microsoft sign-in where the VM allows it")
	f.StringVar(&opts.key, "key", "", "private key `file` to log in to the machine with, where the cloud uses SSH")
	f.StringVar(&opts.bind, "bind", "127.0.0.1", "`address` a forwarded port listens on; 0.0.0.0 lets containers reach it")
	f.StringVar(&opts.reason, "reason", "", "a `reason`, recorded wherever the cloud keeps an audit log")
	f.StringVar(&opts.via, "via", "", "go in this `way`: ssm, arc, bastion or bastion-fwd; lists only machines that have it")

	// What comes back.
	f.BoolVar(&opts.json, "json", false, "emit JSON instead of text")
	f.BoolVar(&opts.print, "print", false, "print the vendor command that would run, and run nothing")
	f.BoolVar(&opts.refresh, "refresh", false, "rebuild the inventory cache first")
	f.BoolVar(&opts.noInput, "no-input", false, "never prompt; fail instead")

	for group, names := range map[string][]string{
		"narrow":  {"cloud", "region", "account", "profile", "status", "tag", "reachable", "all-profiles"},
		"connect": {"via", "user", "auth", "key", "bind", "reason"},
		"output":  {"json", "print", "refresh", "no-input"},
	} {
		for _, n := range names {
			_ = f.SetAnnotation(n, flagGroup, []string{group})
		}
	}
	registerValueCompletion(root, opts)
	installHelp(root)

	root.AddCommand(newConnectCmd(opts), newForwardCmd(opts), newLsCmd(opts),
		newDoctorCmd(opts), newBookmarkCmd(opts), newInfoCmd(opts), newCloudsCmd(opts))
	return root
}

// Execute runs the CLI.
func Execute(ctx context.Context, version string) error {
	return NewRoot(version).ExecuteContext(ctx)
}
