// Command ssmate reaches private machines in AWS, Azure and OCI by name,
// without a jump host or an open port.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/Vishnu-Rr/ssmate/internal/cli"
	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
)

// version is set at build time by the linker via goreleaser's
// -X main.version=... ldflag. A `go install` build sets none of that, so it
// falls back to the VCS revision the Go toolchain embeds automatically.
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := cli.Execute(ctx, resolveVersion())
	if err == nil {
		return
	}

	// Print the message and, when we have one, the command that fixes it.
	var e *exitcode.Error
	if ok := asExitError(err, &e); ok {
		fmt.Fprintf(os.Stderr, "ssmate: %s\n", e.Error())
		if e.Hint != "" {
			fmt.Fprintf(os.Stderr, "\n  %s\n", e.Hint)
		}
	} else {
		fmt.Fprintf(os.Stderr, "ssmate: %v\n", err)
	}
	os.Exit(int(exitcode.From(err)))
}

// resolveVersion returns the linker-set version, or the VCS revision embedded
// by `go install` when no linker flag was passed.
func resolveVersion() string {
	if version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	// `go install ...@v0.1.2` builds from the module proxy, which carries no
	// VCS details, but the module's own version is recorded.
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return strings.TrimPrefix(v, "v")
	}
	var revision string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if revision == "" {
		return version
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if dirty {
		revision += "-dirty"
	}
	return revision
}

func asExitError(err error, target **exitcode.Error) bool {
	for err != nil {
		if e, ok := err.(*exitcode.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
