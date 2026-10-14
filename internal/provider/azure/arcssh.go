package azure

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"golang.org/x/term"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
)

// A shell on an Arc machine is ssmate's own ssh, configured by az.
//
// az ssh arc starts ssh and, like az network bastion ssh, keeps its exit
// status to itself: the ssh extension waits for ssh only to tidy up its
// credentials and to record success for its telemetry, then returns 0. So
// ssmate asks az for the configuration instead (az ssh config, which writes
// a Host entry whose ProxyCommand is Microsoft's relay proxy, plus the key
// and certificate for Microsoft sign-in) and runs ssh against it, keeping
// the status and checking the host key.

// arcResourceType is the only Arc resource ssmate lists.
const arcResourceType = "Microsoft.HybridCompute/machines"

// arcSSH logs in to the Arc machine behind r and runs command, or a shell
// when there is none.
func arcSSH(ctx context.Context, n inventory.Node, r inventory.Route, o provider.SessionOptions) error {
	group, name := groupOf(r.Target), nameOf(r.Target)
	if group == "" || name == "" {
		return exitcode.New(exitcode.Err,
			fmt.Sprintf("%s has no Arc resource to connect to", n.DisplayName()), "ssmate ls --refresh", nil)
	}
	auth, err := arcAuth(o)
	if err != nil {
		return err
	}
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

	// The configuration, the relay credentials it points at, and for
	// Microsoft sign-in the key pair and certificate, all live here and go
	// when the session does.
	dir, err := os.MkdirTemp("", "ssmate-arc-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	config := filepath.Join(dir, "config")
	env := append(os.Environ(), "TMPDIR="+dir, "TMP="+dir, "TEMP="+dir)

	if err := runAzConfig(ctx, azBin, env, arcConfigArgs(group, name, r.Account, config, dir, auth, o)); err != nil {
		return err
	}
	host, err := firstHost(config)
	if err != nil {
		return err
	}

	alias := arcHostKeyAlias(r.Target)
	args := append([]string{"-F", config}, sshTunnelOptionsNoPort(alias, knownHosts)...)
	switch {
	case auth == "aad" || o.Key != "":
		args = append(args, "-o", "IdentitiesOnly=yes")
	case auth == "password":
		args = append(args, "-o", "PreferredAuthentications=keyboard-interactive,password")
	}
	args = append(args, host)
	args = append(args, o.Command...)
	user := o.User
	return runSSH(ctx, sshBin, args, func(code int) error {
		return loginFailure(code, "Azure Arc", auth, user, n.DisplayName(), alias, knownHosts)
	})
}

// arcAuth reads --auth, --user and --key for an Arc machine: Microsoft
// sign-in unless a local account is named, and a local account with a key
// or a password.
func arcAuth(o provider.SessionOptions) (string, error) {
	auth := strings.ToLower(o.Auth)
	switch auth {
	case "aad", "entra":
		return "aad", nil
	case "", "password", "key", "ssh-key":
	default:
		return "", exitcode.New(exitcode.Err,
			fmt.Sprintf("%q is not a way to log in", o.Auth), "--auth takes password, key or aad", nil)
	}
	if o.User == "" {
		if auth == "" {
			return "aad", nil
		}
		return "", exitcode.New(exitcode.Err,
			fmt.Sprintf("--auth %s on an Arc machine needs the local account to log in as", auth),
			"pass --user, or leave --auth out to sign in with your Microsoft account", nil)
	}
	if auth == "key" || auth == "ssh-key" {
		if o.Key == "" {
			return "", exitcode.New(exitcode.Err,
				"--auth key needs the key to log in with", "pass it: --key ~/.ssh/id_ed25519", nil)
		}
		return "key", nil
	}
	if o.Key != "" {
		return "key", nil
	}
	return "password", nil
}

// arcConfigArgs is the az ssh config call for one session.
func arcConfigArgs(group, name, subscription, config, dir, auth string, o provider.SessionOptions) []string {
	args := []string{"ssh", "config",
		"--resource-type", arcResourceType,
		"--resource-group", group, "--name", name,
		"--file", config, "--overwrite"}
	if subscription != "" {
		args = append(args, "--subscription", subscription)
	}
	if auth == "aad" {
		// Where az generates the key pair and has Microsoft sign it.
		return append(args, "--keys-destination-folder", filepath.Join(dir, "keys"))
	}
	args = append(args, "--local-user", o.User)
	if o.Key != "" {
		args = append(args, "--private-key-file", o.Key)
	}
	return args
}

// runAzConfig runs az ssh config.
//
// It may ask one question: whether to let the machine's Arc endpoint accept
// SSH on port 22, the first time anyone connects. That is a change to the
// machine's configuration, so it is the user's to answer, never ssmate's.
// With a terminal, az gets it and asks; without one there is nobody to ask,
// and az's own error says what is missing.
func runAzConfig(ctx context.Context, azBin string, env, args []string) error {
	cmd := exec.CommandContext(ctx, azBin, args...)
	cmd.Env = env
	var out bytes.Buffer
	if term.IsTerminal(int(os.Stdin.Fd())) {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
	} else {
		cmd.Stdout, cmd.Stderr = &out, &out
	}
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if code := ee.ExitCode(); code == 2 || code == 3 {
			return arcExitError(code)
		}
	}
	msg := "az could not prepare the session through Azure Arc"
	if last := lastLine(out.String()); last != "" {
		msg += ": " + last
	}
	return exitcode.New(exitcode.NotConnected, msg,
		"check the machine's Arc agent is connected: ssmate doctor", err)
}

// firstHost reads the Host name az wrote, so ssmate never has to know how az
// names it.
func firstHost(config string) (string, error) {
	f, err := os.Open(config)
	if err != nil {
		return "", fmt.Errorf("reading the configuration az wrote: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && strings.EqualFold(fields[0], "Host") {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("the configuration az wrote names no host")
}

// sshTunnelOptionsNoPort is sshTunnelOptions for a connection whose port the
// configuration already sets.
func sshTunnelOptionsNoPort(alias, knownHosts string) []string {
	return sshTunnelOptions("", alias, knownHosts)[2:]
}

// arcHostKeyAlias names an Arc machine in known_hosts by its resource ID.
func arcHostKeyAlias(resourceID string) string {
	return "azure-arc-" + aliasUnsafe.ReplaceAllString(strings.ToLower(resourceID), "-")
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
