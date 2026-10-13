package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flagGroup is the annotation that files a flag under a heading in help.
const flagGroup = "ssmate_group"

// groups are the help headings, in the order they are shown, with the order
// following the order a person decides things: which machine, how to reach
// it, and what to get back.
var groups = []struct{ key, title string }{
	{"narrow", "Which machines"},
	{"connect", "How to connect"},
	{"output", "Output"},
}

// groupedFlags renders a flag set under headings.
//
// Twenty flags in one alphabetical block is a list you read top to bottom to
// find the one you want. Grouped by what they are for, you skip straight to
// the heading that matches your question. Flags with no group - a command's
// own - come first, under the command's name, because they are what that
// command is about.
func groupedFlags(fs *pflag.FlagSet, own string) string {
	byGroup := map[string]*pflag.FlagSet{}
	for _, g := range groups {
		byGroup[g.key] = pflag.NewFlagSet(g.key, pflag.ContinueOnError)
	}
	ungrouped := pflag.NewFlagSet("own", pflag.ContinueOnError)
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		if g, ok := f.Annotations[flagGroup]; ok && len(g) > 0 {
			if set, ok := byGroup[g[0]]; ok {
				set.AddFlag(f)
				return
			}
		}
		ungrouped.AddFlag(f)
	})

	var b strings.Builder
	write := func(title string, set *pflag.FlagSet) {
		if !set.HasFlags() {
			return
		}
		fmt.Fprintf(&b, "\n%s:\n%s", title, set.FlagUsagesWrapped(100))
	}
	write(own, ungrouped)
	for _, g := range groups {
		write(g.title, byGroup[g.key])
	}
	return b.String()
}

// usageTemplate is cobra's, with the flag blocks grouped.
const usageTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

Commands:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableFlags}}
{{groupedFlags .}}{{end}}{{if .HasAvailableSubCommands}}
Use "{{.CommandPath}} [command] --help" for more about a command.{{end}}
`

// installHelp wires the grouped help into every command.
func installHelp(root *cobra.Command) {
	cobra.AddTemplateFunc("groupedFlags", func(c *cobra.Command) string {
		own := "Flags"
		if c.HasParent() {
			own = strings.ToUpper(c.Name()[:1]) + c.Name()[1:] + " flags"
		}
		return groupedFlags(c.Flags(), own)
	})
	root.SetUsageTemplate(usageTemplate)
}

// registerValueCompletion completes flag values from what is actually cached.
//
// Completing --region with every region AWS has would offer thirty names for
// an estate that lives in six. Completing from the cache offers the six, which
// is the list the user is choosing from.
func registerValueCompletion(root *cobra.Command, opts *options) {
	fromCache := func(field func(inventory.Node) string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			seen := map[string]int{}
			for _, n := range cachedAll() {
				if v := field(n); v != "" {
					seen[v]++
				}
			}
			out := make([]string, 0, len(seen))
			for v, count := range seen {
				out = append(out, fmt.Sprintf("%s\t%d machines", v, count))
			}
			sort.Strings(out)
			return out, cobra.ShellCompDirectiveNoFileComp
		}
	}
	complete := map[string]func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective){
		"cloud":   fromCache(func(n inventory.Node) string { return n.Cloud }),
		"region":  fromCache(func(n inventory.Node) string { return n.Region }),
		"account": fromCache(func(n inventory.Node) string { return accountName(n.Account) }),
		"profile": fromCache(func(n inventory.Node) string { return accountName(n.Account) }),
		"status":  fromCache(func(n inventory.Node) string { return n.Status }),
		"tag": func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			seen := map[string]bool{}
			for _, n := range cachedAll() {
				for k := range n.Tags {
					seen[k] = true
				}
			}
			out := make([]string, 0, len(seen))
			for k := range seen {
				out = append(out, k)
			}
			sort.Strings(out)
			return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
		},
		"via": func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return []string{
				"ssm\tAWS Session Manager: the machine's agent connects out",
				"arc\tAzure Arc: the machine's agent connects out",
				"bastion\ta cloud bastion: Azure Bastion, or OCI with a one-time key",
				"bastion-fwd\tan OCI bastion forwarding to SSH, with your own key",
			}, cobra.ShellCompDirectiveNoFileComp
		},
		"auth": func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return []string{
				"password\tthe cloud prompts for it; ssmate never sees it",
				"key\tan SSH key, from --key or your agent",
				"aad\tyour Entra ID sign-in, where the machine allows it",
			}, cobra.ShellCompDirectiveNoFileComp
		},
	}
	for name, fn := range complete {
		_ = root.RegisterFlagCompletionFunc(name, fn)
	}
}

// cachedAll reads every cached machine, unfiltered, for completing values.
func cachedAll() []inventory.Node {
	path, err := inventory.CachePath()
	if err != nil {
		return nil
	}
	c, err := inventory.LoadCache(path)
	if err != nil {
		return nil
	}
	return joined(c.Nodes)
}
