package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/spf13/cobra"
)

func newBookmarkCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "bookmark",
		Aliases: []string{"bm"},
		Short:   "Name a target so you can reach it without remembering what it is",
		Long: `Give a target a name you will remember.

  ssmate bookmark add db db-primary-1
  ssmate my-shortcut                     # now resolves to that instance

A bookmark can point at anything ssmate accepts: an identifier, a name,
an IP, or a tag value. It is checked before everything else, so a bookmark
always wins over a coincidentally similar hostname.`,
	}
	cmd.AddCommand(
		newBookmarkAddCmd(opts),
		newBookmarkListCmd(opts),
		newBookmarkRemoveCmd(opts),
	)
	return cmd
}

func newBookmarkAddCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "add <name> <target>",
		Short: "Create or replace a bookmark",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			previous, existed, err := addBookmark(args[0], args[1])
			if err != nil {
				return err
			}
			t := opts.theme()
			if existed && previous != args[1] {
				// Say what was overwritten. Silently replacing something the
				// user wrote is how people lose work they cannot get back.
				fmt.Fprintf(os.Stderr, "%s now points at %s %s\n",
					t.Match(args[0]), args[1], t.Dim("(was "+previous+")"))
				return nil
			}
			fmt.Fprintf(os.Stderr, "%s -> %s\n", t.Match(args[0]), args[1])
			return nil
		},
	}
}

func newBookmarkListCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Show every bookmark",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, bm, err := openBookmarks()
			if err != nil {
				return err
			}
			if len(bm.Names) == 0 {
				fmt.Fprintln(os.Stderr, opts.theme().Dim(
					"No bookmarks yet.\n  ssmate bookmark add db my-database-host"))
				return nil
			}
			t := opts.theme()
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
			fmt.Fprintln(w, t.Heading("NAME\tTARGET"))
			for _, kv := range bm.Sorted() {
				fmt.Fprintf(w, "%s\t%s\n", t.Match(kv[0]), kv[1])
			}
			return w.Flush()
		},
	}
}

func newBookmarkRemoveCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Delete a bookmark",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, bm, err := openBookmarks()
			if err != nil {
				return err
			}
			if !bm.Remove(args[0]) {
				return exitcode.New(exitcode.NotFound,
					fmt.Sprintf("no bookmark called %q", args[0]),
					"ssmate bookmark list", nil)
			}
			if err := bm.Save(path); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "removed %s\n", opts.theme().Dim(args[0]))
			return nil
		},
	}
}

func openBookmarks() (string, *inventory.Bookmarks, error) {
	path, err := inventory.BookmarksPath()
	if err != nil {
		return "", nil, err
	}
	bm, err := inventory.LoadBookmarks(path)
	if err != nil {
		return "", nil, exitcode.New(exitcode.Err, err.Error(),
			"fix or delete "+path, err)
	}
	return path, bm, nil
}

// addBookmark creates or replaces a bookmark, reporting what it displaced.
//
// It is a plain function rather than only a command body because the machine
// list names things too, and both routes must behave identically - including
// telling you what you just overwrote.
func addBookmark(name, target string) (previous string, existed bool, err error) {
	path, bm, err := openBookmarks()
	if err != nil {
		return "", false, err
	}
	previous, existed = bm.Lookup(name)
	if err := bm.Set(name, target); err != nil {
		return "", false, exitcode.New(exitcode.Err, err.Error(), "", nil)
	}
	if err := bm.Save(path); err != nil {
		return "", false, err
	}
	return previous, existed, nil
}

// expandBookmark turns an alias into its target. Bookmarks are checked before
// anything else, so one always beats a coincidentally similar hostname.
func expandBookmark(target string) string {
	path, err := inventory.BookmarksPath()
	if err != nil {
		return target
	}
	bm, err := inventory.LoadBookmarks(path)
	if err != nil {
		return target
	}
	if resolved, ok := bm.Lookup(target); ok {
		return resolved
	}
	return target
}
