//go:build windows

package session

import (
	"os"
	"os/signal"
)

// IgnoreTerminalSignals stops this process reacting to signals meant for the
// remote shell. Windows has only Interrupt; SIGQUIT and SIGTSTP do not exist.
func IgnoreTerminalSignals() func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	go func() {
		for range ch {
			// Swallow: the plugin forwards these to the remote process.
		}
	}()
	return func() { signal.Stop(ch); close(ch) }
}
