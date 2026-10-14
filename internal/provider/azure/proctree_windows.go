//go:build windows

package azure

import (
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// stopsWholeTree makes cancelling cmd stop everything it started.
//
// az on Windows is az.cmd, which runs Python as a child; killing the cmd
// process alone would leave the Python holding the tunnel. taskkill /T ends
// the whole tree.
func stopsWholeTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	cmd.Cancel = func() error {
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
	cmd.WaitDelay = 5 * time.Second
}
