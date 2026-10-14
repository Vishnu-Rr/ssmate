//go:build !windows

package azure

import (
	"os/exec"
	"syscall"
	"time"
)

// stopsWholeTree makes cancelling cmd stop everything it started.
//
// az is a launcher: Homebrew's is a bash script that runs Python as a child
// rather than exec'ing it, so killing az's own process leaves the Python that
// holds the tunnel running, holding the port and the output pipe, and Wait
// never returns. Starting az in a process group of its own and signalling the
// group reaches the Python too. A group of its own also keeps a Ctrl-C typed
// at a password prompt from tearing the tunnel down under ssh.
func stopsWholeTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		pgid := -cmd.Process.Pid
		err := syscall.Kill(pgid, syscall.SIGTERM)
		// A tunnel that ignores SIGTERM still has to go.
		time.AfterFunc(3*time.Second, func() { _ = syscall.Kill(pgid, syscall.SIGKILL) })
		return err
	}
	// Should anything still hold the output pipe after that, stop waiting.
	cmd.WaitDelay = 5 * time.Second
}
