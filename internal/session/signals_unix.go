//go:build !windows

package session

import (
	"os"
	"os/signal"
	"syscall"
)

// IgnoreTerminalSignals stops this process reacting to signals meant for the
// remote shell, so Ctrl-C reaches the remote process instead of killing the
// tunnel out from under it. The returned function restores normal handling.
//
// This lives in a build-tagged file because SIGQUIT and SIGTSTP do not exist
// on Windows. A runtime check on GOOS is not enough: the symbols must resolve
// at compile time, so naming them in shared code breaks the Windows build.
func IgnoreTerminalSignals() func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGQUIT, syscall.SIGTSTP)
	go func() {
		for range ch {
			// Swallow: the plugin forwards these to the remote process.
		}
	}()
	return func() { signal.Stop(ch); close(ch) }
}
