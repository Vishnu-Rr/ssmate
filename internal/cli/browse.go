package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/session"
	"github.com/Vishnu-Rr/ssmate/internal/ui"
)

// runBrowse opens the machine list and carries out whatever was chosen there.
//
// Everything you want to do to a machine begins by finding it, so the list
// does not end at "which one" and send you back to the shell to retype an
// identifier you were just looking at. It hands back an action, and this
// dispatches it.
func runBrowse(ctx context.Context, opts *options) error {
	if opts.noInput || !stdinIsTerminal() {
		return exitcode.New(exitcode.Err,
			"no machine given and no terminal to choose one in",
			"name a machine, or run ssmate ls to see them", nil)
	}
	nodes, err := loadInventory(ctx, opts)
	if err != nil {
		return err
	}
	choice, err := browse(nodes, opts)
	if err != nil {
		return err
	}

	switch choice.Action {
	case ui.ActionForward:
		return forwardFromList(ctx, opts, choice)
	case ui.ActionDoctor:
		fmt.Fprintf(os.Stderr, "\nNext time:  ssmate doctor %s\n\n", choice.Node.ID)
		return runDoctorFor(ctx, opts, choice.Node)
	case ui.ActionBookmark:
		return bookmarkFromList(choice)
	default:
		fmt.Fprintf(os.Stderr, "\nNext time:  ssmate connect %s\n\n", choice.Node.ID)
		return runConnectNode(ctx, opts, choice.Node, false, nil)
	}
}

// forwardFromList asks for the port pair the list cannot know, then forwards.
//
// The list deliberately does not grow a form for this. A port pair is two
// numbers and a prompt is two lines, where a form inside the list would be a
// second focus model, its own validation and its own way to get stuck.
func forwardFromList(ctx context.Context, opts *options, choice ui.Choice) error {
	spec, err := askLine(fmt.Sprintf(
		"forward from %s — local:remote (for example 5432:5432): ", choice.Node.DisplayName()))
	if err != nil {
		return err
	}
	fwd, err := session.ParseForward(spec)
	if err != nil {
		return exitcode.New(exitcode.Err, err.Error(),
			"the form is local:remote, or local:host:remote to reach past the machine", nil)
	}
	fmt.Fprintf(os.Stderr, "\nNext time:  ssmate forward %s %s\n\n", choice.Node.ID, spec)
	return runForwardNode(ctx, opts, choice.Node, fwd)
}

// bookmarkFromList names a machine so it can be reached by a shorter word.
func bookmarkFromList(choice ui.Choice) error {
	name, err := askLine(fmt.Sprintf("name for %s: ", choice.Node.DisplayName()))
	if err != nil {
		return err
	}
	previous, existed, err := addBookmark(name, choice.Node.ID)
	if err != nil {
		return err
	}
	if existed && previous != choice.Node.ID {
		fmt.Fprintf(os.Stderr, "%s now points at %s (was %s)\n", name, choice.Node.ID, previous)
		return nil
	}
	fmt.Fprintf(os.Stderr, "bookmarked %s as %s\n", choice.Node.DisplayName(), name)
	return nil
}

// askLine reads one line from the terminal.
//
// It reads a whole line rather than one whitespace-delimited word, because a
// word is not what was asked for: a forward specification is one token today
// but a name need not be, and a reader that silently keeps the first word of
// an answer is worse than one that refuses it.
//
// A blank line, an end of input and a closed terminal are all the same thing
// here - the user declined - and all produce the cancelled code rather than a
// failure, so a script can tell "they said no" from "it broke".
var askLine = func(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	return readLineFrom(os.Stdin)
}

// readLineFrom is the testable half of askLine.
func readLineFrom(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
			return "", exitcode.New(exitcode.Err, "could not read the answer", "", err)
		}
		return "", exitcode.New(exitcode.Cancelled, "cancelled", "", nil)
	}
	line := strings.TrimSpace(sc.Text())
	if line == "" {
		return "", exitcode.New(exitcode.Cancelled, "cancelled", "", nil)
	}
	return line, nil
}
