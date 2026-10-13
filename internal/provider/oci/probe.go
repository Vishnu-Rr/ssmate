package oci

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
)

// The rules between a bastion and a machine can be judged from here, but a
// rule that names a service or another security group cannot, and neither can
// anything the rules do not show, such as a firewall on the machine itself. A
// probe settles it: it opens a port-forwarding session to the machine's SSH
// port and reads the greeting the machine's SSH server sends first. That
// proves the bastion admits this client and reaches the machine, and it logs
// in to nothing.

var (
	_ provider.Prober = Handler{}
	_ provider.Prober = ManagedHandler{}
)

// Probe tries the way in a port-forwarding session would take.
func (h Handler) Probe(ctx context.Context, n inventory.Node, _ inventory.Route, o provider.SessionOptions) error {
	return h.p.probe(ctx, n, o)
}

// Probe tries the bastions a managed session would go through. A managed
// session takes minutes to open; the path it needs is proved the quick way.
func (h ManagedHandler) Probe(ctx context.Context, n inventory.Node, _ inventory.Route, o provider.SessionOptions) error {
	return h.p.probe(ctx, n, o)
}

// probeWait bounds the whole attempt through one bastion once its session is
// up: ssh starting, the tunnel opening, and the greeting arriving.
const probeWait = 30 * time.Second

func (p Provider) probe(ctx context.Context, n inventory.Node, o provider.SessionOptions) error {
	if n.PrivateIP == "" {
		return exitcode.New(exitcode.Err,
			fmt.Sprintf("%s has no private address to probe", n.DisplayName()), "", nil)
	}
	return p.withSession(ctx, n, n.PrivateIP, sshPort, o, portForwarding, func(k *keypair, sessionID, allow string) error {
		bin, err := exec.LookPath("ssh")
		if err != nil {
			return exitcode.New(exitcode.Prereq, "ssh is not installed", sshInstallHint(), err)
		}
		port, err := freePort()
		if err != nil {
			return err
		}
		pctx, cancel := context.WithTimeout(ctx, probeWait)
		defer cancel()
		args := append(bastionSSHOptions(k),
			"-N", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes",
			"-L", fmt.Sprintf("127.0.0.1:%d:%s:%d", port, n.PrivateIP, sshPort),
			"-p", "22", sessionID+"@"+bastionHost(n.Region))
		cmd := exec.CommandContext(pctx, bin, args...)
		if err := cmd.Start(); err != nil {
			return err
		}
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

		greeting, err := readGreeting(pctx, port)
		if err != nil {
			return &bastionFailure{kind: unreachedTarget,
				detail: fmt.Sprintf("nothing answered on %s:%d through the bastion", n.PrivateIP, sshPort)}
		}
		if !strings.HasPrefix(greeting, "SSH-") {
			return exitcode.New(exitcode.NotConnected,
				fmt.Sprintf("something on %s:%d answered, but not an SSH server", n.PrivateIP, sshPort), "", nil)
		}
		return nil
	})
}

// readGreeting connects to the forwarded port and reads the first line. An
// SSH server speaks first, so the line arrives without sending anything.
// Connections that close at once are retried until the deadline: the tunnel
// may not be listening yet, and a bastion that cannot reach the machine
// accepts the local connection and then drops it.
func readGreeting(ctx context.Context, port int) (string, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	var last error
	for ctx.Err() == nil {
		conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			line, rerr := bufio.NewReader(conn).ReadString('\n')
			conn.Close()
			if rerr == nil || strings.HasPrefix(line, "SSH-") {
				return strings.TrimSpace(line), nil
			}
			last = rerr
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	if last == nil {
		last = ctx.Err()
	}
	return "", last
}
