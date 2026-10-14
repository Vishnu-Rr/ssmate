package azure

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adrg/xdg"
	"golang.org/x/crypto/ssh"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/session"
)

// A shell through Azure Bastion is ssmate's own ssh, over a tunnel az holds
// open.
//
// az network bastion ssh would do it in one command, but it runs ssh and
// throws the exit status away (the bastion extension's ssh_bastion_host calls
// subprocess.call and ignores what it returns), so every session comes back
// as 0: a refused login, a command that failed, all of it. It also turns host
// key checking off, with StrictHostKeyChecking=no and an empty known_hosts.
// Holding only the tunnel with az, and running ssh ourselves, keeps both.

// tunnelReadyTimeout bounds how long az may take to say the tunnel is up.
// Measured, it is a few seconds; the slack is for a slow token refresh.
const tunnelReadyTimeout = 90 * time.Second

// certFileName is where az ssh cert writes the certificate it has Microsoft
// sign; the key pair it generates lands beside it as id_rsa and id_rsa.pub.
const certFileName = "id_rsa.pub-aadcert.pub"

// bastionSSH logs in to n through the bastion gw and runs command, or a shell
// when there is none. auth is aad, key or password.
func bastionSSH(ctx context.Context, gw gateway, n inventory.Node, auth, user, key string, command []string) error {
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		return exitcode.New(exitcode.Prereq, "ssh is not installed",
			"install OpenSSH: macOS ships it, Debian and Ubuntu use apt install openssh-client, "+
				"Windows has it under Settings > Optional features", err)
	}
	azBin, err := exec.LookPath("az")
	if err != nil {
		return exitcode.New(exitcode.Prereq, "the Azure CLI is not installed",
			"install it: https://learn.microsoft.com/cli/azure/install-azure-cli", err)
	}
	knownHosts, err := xdg.ConfigFile(filepath.Join("ssmate", "known_hosts"))
	if err != nil {
		return err
	}

	// Everything az writes for this session - the key pair and certificate
	// for Microsoft sign-in, anything it puts in its temporary directory -
	// goes here, and goes when the session does.
	dir, err := os.MkdirTemp("", "ssmate-az-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	env := append(os.Environ(), "TMPDIR="+dir, "TMP="+dir, "TEMP="+dir)

	var identity []string
	switch auth {
	case "aad":
		certPath := filepath.Join(dir, certFileName)
		if err := runQuiet(ctx, azBin, env, "ssh", "cert", "--file", certPath); err != nil {
			return exitcode.New(exitcode.Prereq,
				"could not get a sign-in certificate from Microsoft: "+err.Error(),
				"it needs the az ssh extension (az extension add --name ssh) and a current az login", nil)
		}
		// Entra signs the certificate for one principal, the account you
		// arrive as on the machine; az's own bastion command reads it the
		// same way.
		if user, err = certPrincipal(certPath); err != nil {
			return err
		}
		identity = []string{"-i", filepath.Join(dir, "id_rsa"), "-o", "CertificateFile=" + certPath, "-o", "IdentitiesOnly=yes"}
	case "key":
		identity = []string{"-i", key, "-o", "IdentitiesOnly=yes"}
	default:
		// A password was asked for, so ask for one: without this, ssh offers
		// every key in the agent first, and a server that stops listening
		// after a handful of refusals never gets to the password prompt.
		identity = []string{"-o", "PreferredAuthentications=keyboard-interactive,password"}
	}

	port, err := freePort()
	if err != nil {
		return err
	}
	stop, err := openTunnel(ctx, azBin, env, gw, n, port)
	if err != nil {
		return err
	}
	defer stop()

	args := append(sshTunnelOptions(port, hostKeyAlias(n), knownHosts), identity...)
	args = append(args, user+"@127.0.0.1")
	// ssh runs whatever follows the destination instead of a shell, and
	// brings its exit status home.
	args = append(args, command...)
	return runSSH(ctx, sshBin, args, auth, user, n, knownHosts)
}

// sshTunnelOptions are the flags for ssh through a local tunnel.
//
// The tunnel is always 127.0.0.1, so ssh's usual record of host keys by
// address would mix every machine reached this way into one entry. HostKeyAlias
// records each machine under its own name instead, in a known_hosts of
// ssmate's own, and accept-new trusts a machine the first time and refuses it
// if its key later changes - which is what az turns off.
func sshTunnelOptions(port, alias, knownHosts string) []string {
	return []string{
		"-p", port,
		"-o", "HostKeyAlias=" + alias,
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=30",
		// Quiets "Permanently added ... to the list of known hosts" on a
		// first connection; a refused login or a changed host key is an
		// error and still shows.
		"-o", "LogLevel=ERROR",
	}
}

// hostKeyAlias names a machine in known_hosts: by the VM ID Azure gives it
// where known, which survives a rename, and by its resource ID otherwise.
func hostKeyAlias(n inventory.Node) string {
	for _, a := range n.Aliases {
		if vmIDPattern.MatchString(a) {
			return "azure-vm-" + strings.ToLower(a)
		}
	}
	return "azure-" + aliasUnsafe.ReplaceAllString(strings.ToLower(n.ID), "-")
}

var (
	vmIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	aliasUnsafe = regexp.MustCompile(`[^a-z0-9.-]+`)
)

// certPrincipal reads the account an OpenSSH certificate lets you log in as.
func certPrincipal(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading the sign-in certificate: %w", err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(raw)
	if err != nil {
		return "", fmt.Errorf("reading the sign-in certificate: %w", err)
	}
	cert, ok := pub.(*ssh.Certificate)
	if !ok || len(cert.ValidPrincipals) == 0 {
		return "", errors.New("the sign-in certificate names no account to log in as")
	}
	return cert.ValidPrincipals[0], nil
}

// freePort asks the system for a local port nothing is using.
func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port), nil
}

// openTunnel starts az's bastion tunnel to port 22 on the machine and waits
// until az says it is ready. stop closes it.
//
// Readiness is read from what az prints rather than by connecting to the port
// to see: each connection to the tunnel is a real connection to the
// machine's sshd, and a probe would be one more half-open login in its log.
func openTunnel(ctx context.Context, azBin string, env []string, gw gateway, n inventory.Node, port string) (stop func(), err error) {
	tctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(tctx, azBin,
		"network", "bastion", "tunnel",
		"--name", gw.name,
		"--resource-group", gw.group,
		"--target-resource-id", n.ID,
		"--resource-port", "22",
		"--port", port,
		"--subscription", n.Account)
	cmd.Env = env
	stopsWholeTree(cmd)
	// A writer, not StderrPipe: exec drains it itself and Wait waits for that,
	// so nothing az says on the way out is lost to a pipe Wait has closed.
	out := &tunnelOutput{ready: make(chan struct{})}
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	stop = func() {
		cancel()
		<-exited
	}
	explain := func(what string) error {
		msg := "the bastion tunnel " + what
		if last := out.last(); last != "" {
			msg += ": " + last
		}
		return exitcode.New(exitcode.NotConnected, msg,
			"check the bastion allows this machine and that the target is running: ssmate doctor "+n.DisplayName(), nil)
	}

	select {
	case <-out.ready:
		return stop, nil
	case <-exited:
		cancel()
		return nil, explain("closed before it was ready")
	case <-time.After(tunnelReadyTimeout):
		stop()
		return nil, explain(fmt.Sprintf("was not ready after %s", tunnelReadyTimeout))
	case <-ctx.Done():
		stop()
		return nil, ctx.Err()
	}
}

// tunnelOutput watches what az prints for the line saying the tunnel is up,
// and keeps the last thing it said for an error.
type tunnelOutput struct {
	mu        sync.Mutex
	partial   string
	lastLine  string
	ready     chan struct{}
	signalled bool
}

func (t *tunnelOutput) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.partial += string(p)
	for {
		i := strings.IndexByte(t.partial, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(t.partial[:i])
		t.partial = t.partial[i+1:]
		if line != "" {
			t.lastLine = line
		}
		if !t.signalled && strings.Contains(line, "Tunnel is ready") {
			t.signalled = true
			close(t.ready)
		}
	}
	return len(p), nil
}

func (t *tunnelOutput) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p := strings.TrimSpace(t.partial); p != "" {
		return p
	}
	return t.lastLine
}

// runQuiet runs an az command for its effect, keeping its output for the
// error when it fails.
func runQuiet(ctx context.Context, bin string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		return fmt.Errorf("%s", strings.TrimSpace(lines[len(lines)-1]))
	}
	return nil
}

// runSSH hands the terminal to ssh and turns its exit status into ssmate's.
func runSSH(ctx context.Context, bin string, args []string, auth, user string, n inventory.Node, knownHosts string) error {
	stopSignals := session.IgnoreTerminalSignals()
	defer stopSignals()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if err == nil {
		// ssh exited cleanly: the user ended the shell, or the command
		// succeeded.
		return session.ErrClosedByUser
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return err
	}
	return sshExitError(ee.ExitCode(), auth, user, n, knownHosts)
}

// sshExitError explains ssh's exit status.
//
// 255 is ssh's own failure. The tunnel was already up, so the bastion let us
// through: what failed is the login, or the machine's host key, or sshd on
// the machine. Anything else is the remote command's own status, passed
// through unchanged for a script to branch on.
func sshExitError(code int, auth, user string, n inventory.Node, knownHosts string) error {
	if code != 255 {
		return exitcode.New(exitcode.Code(code), fmt.Sprintf("the command exited %d", code), "", nil)
	}
	var hint string
	switch auth {
	case "aad":
		hint = "you need the Virtual Machine User Login role on it, or Virtual Machine Administrator Login for sudo"
	case "key":
		hint = "check that this key is the one on the machine for " + user
	default:
		hint = "check the password, and that the machine allows password login"
	}
	hint += fmt.Sprintf("; if ssh reported a changed host key and the machine was rebuilt: ssh-keygen -R %s -f %q",
		hostKeyAlias(n), knownHosts)
	return exitcode.New(exitcode.NotConnected,
		fmt.Sprintf("could not log in to %s through the bastion", n.DisplayName()), hint, nil)
}
