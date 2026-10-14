package azure

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/adrg/xdg"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/session"
)

// Azure is the one cloud here whose data plane this tool cannot speak.
//
// Reaching a machine through Bastion from a terminal means POSTing for a token
// and then carrying the session over a WebSocket to an undocumented endpoint.
// There is no Go SDK for it and no published protocol; the only implementation
// is the Python one inside the az CLI's bastion extension. Reimplementing an
// unpublished protocol against someone's production estate would be a poor
// trade, so the bytes are moved by the vendor's own binary, exactly as the
// Amazon path hands them to session-manager-plugin.
//
// What this provider adds over typing the az command yourself is the part az
// will not tell you: which machines exist, which of them a bastion can
// actually reach, and - the one that costs people an afternoon - that the
// bastion in front of a machine is the wrong SKU. az reports that as a bare
// failure at connect time, after you have found the resource ID by hand.

// Shell opens an interactive session.
func (p Provider) Shell(ctx context.Context, n inventory.Node, o provider.SessionOptions) error {
	gw, err := gatewayFor(n)
	if err != nil {
		return err
	}
	auth, user, err := resolveLogin(n, o)
	if err != nil {
		return err
	}
	if _, err := loginArgs(auth, o.Key, user); err != nil {
		return err
	}
	if auth == "aad" && !o.Print && len(o.Command) == 0 {
		fmt.Fprintln(os.Stderr, "Signing in with your Microsoft account (Entra ID).")
	}
	if o.Print {
		printBastionSSH(gw, n, auth, user, o.Key, o.Command)
		return nil
	}
	// With a password, ssh prompts for it and reads it from the terminal
	// itself. ssmate never sees it, never stores it and never passes it as
	// an argument, which is the only way to handle one safely.
	return bastionSSH(ctx, gw, n, normalAuth(auth), user, o.Key, o.Command)
}

// normalAuth folds the spellings --auth accepts into aad, key or password.
func normalAuth(auth string) string {
	switch strings.ToLower(auth) {
	case "aad", "entra":
		return "aad"
	case "key", "ssh-key":
		return "key"
	}
	return "password"
}

// printBastionSSH shows what a session runs, for --print: the tunnel az
// holds, then ssh through it, with the port and certificate it would pick
// at run time shown as placeholders.
func printBastionSSH(gw gateway, n inventory.Node, auth, user, key string, command []string) {
	auth = normalAuth(auth)
	knownHosts := filepath.Join(xdg.ConfigHome, "ssmate", "known_hosts")
	fmt.Println(printable([]string{"network", "bastion", "tunnel",
		"--name", gw.name, "--resource-group", gw.group, "--target-resource-id", n.ID,
		"--resource-port", "22", "--port", "<port>", "--subscription", n.Account}) + " &")
	ssh := sshTunnelOptions("<port>", hostKeyAlias(n), knownHosts)
	switch auth {
	case "aad":
		fmt.Println(printable([]string{"ssh", "cert", "--file", "<dir>/" + certFileName}))
		ssh = append(ssh, "-i", "<dir>/id_rsa", "-o", "CertificateFile=<dir>/"+certFileName, "-o", "IdentitiesOnly=yes")
		user = "<account in the certificate>"
	case "key":
		ssh = append(ssh, "-i", key, "-o", "IdentitiesOnly=yes")
	default:
		ssh = append(ssh, "-o", "PreferredAuthentications=keyboard-interactive,password")
	}
	ssh = append(ssh, user+"@127.0.0.1")
	fmt.Println(printCommand("ssh", append(ssh, command...)))
}

// resolveLogin decides how to log in on a VM, and as whom.
//
// What was asked for wins. Otherwise a key given with --key means key login,
// and a VM with Microsoft's Entra login extension is signed in to as yourself,
// which needs no password or key at all. Only then does it fall back to a
// password for the account the VM was created with. A --user with no --auth
// asks for that local account, so it never turns into an Entra sign-in.
func resolveLogin(n inventory.Node, o provider.SessionOptions) (auth, user string, err error) {
	auth = strings.ToLower(o.Auth)
	if auth == "" {
		login, _ := n.Detailed("login")
		switch {
		case o.Key != "":
			auth = "key"
		case o.User == "" && login == entraLogin:
			auth = "aad"
		}
	}
	if auth == "aad" || auth == "entra" {
		return "aad", "", nil
	}
	user = o.User
	if user == "" {
		user, _ = n.Detailed("login_user")
	}
	if user == "" {
		return "", "", exitcode.New(exitcode.Err,
			fmt.Sprintf("cannot tell which user to log in as on %s", n.DisplayName()),
			"pass --user", nil)
	}
	// Falling back to a password on a VM built without password login only
	// earns a prompt that can never succeed. Asked for explicitly, it is
	// tried anyway: sshd may have been changed since the VM was built.
	if auth == "" {
		if pw, _ := n.Detailed("password_login"); pw == "off" {
			return "", "", exitcode.New(exitcode.Err,
				fmt.Sprintf("%s was built with password login turned off, so a password will not work", n.DisplayName()),
				fmt.Sprintf("log in with its key: --key <file> (the key for %s), "+
					"or turn on Microsoft sign-in: ssmate doctor %s shows how", user, n.DisplayName()), nil)
		}
	}
	return auth, user, nil
}

// loginArgs turns --auth and --key into what the bastion command expects.
//
// Password is the default because it is the one that works on every machine
// without preparation, and it is safe here: az reads the password from the
// terminal itself, so it never passes through ssmate, an argument list or a
// log. A key is better where it is set up, and Entra ID better still where the
// machine has the extension for it - the user signs in as themselves and no
// shared credential exists at all.
func loginArgs(auth, key, user string) ([]string, error) {
	switch strings.ToLower(auth) {
	case "", "password":
		return []string{"--auth-type", "password", "--username", user}, nil
	case "key", "ssh-key":
		if key == "" {
			return nil, exitcode.New(exitcode.Err,
				"--auth key needs the key to log in with",
				"pass it: --key ~/.ssh/id_ed25519", nil)
		}
		return []string{"--auth-type", "ssh-key", "--username", user, "--ssh-key", key}, nil
	case "aad", "entra":
		// Entra ID signs the user in as themselves, so there is no local
		// account name to pass.
		return []string{"--auth-type", "AAD"}, nil
	default:
		return nil, exitcode.New(exitcode.Err,
			fmt.Sprintf("%q is not a way to log in", auth),
			"--auth takes password, key or aad", nil)
	}
}

// Forward opens a tunnel and holds it open.
func (p Provider) Forward(ctx context.Context, n inventory.Node, f session.Forward, o provider.SessionOptions) error {
	gw, err := gatewayFor(n)
	if err != nil {
		return err
	}
	args := []string{
		"network", "bastion", "tunnel",
		"--name", gw.name,
		"--resource-group", gw.group,
		"--target-resource-id", n.ID,
		"--resource-port", strconv.Itoa(f.RemotePort),
		"--port", strconv.Itoa(f.LocalPort),
		"--subscription", n.Account,
	}
	if o.Print {
		fmt.Println(printable(args))
		return nil
	}
	// A bastion tunnel to a host other than the machine itself is not
	// something the vendor command can express: it forwards one port on the
	// target and nothing else. Saying so beats opening a tunnel that quietly
	// goes somewhere else.
	if f.RemoteHost != "" && f.RemoteHost != n.PrivateIP {
		return exitcode.New(exitcode.Err,
			fmt.Sprintf("Azure Bastion can only forward a port on the machine itself, not to %s", f.RemoteHost),
			"forward a port on the machine, then reach the other host from there", nil)
	}
	return session.HoldOpen(ctx, func() error { return runAz(ctx, args) }, o.OnReconnect)
}

// gatewayFor finds the usable bastion recorded for a machine, and explains
// precisely which of the two independent settings is wrong when there is one
// but it cannot be used.
func gatewayFor(n inventory.Node) (gateway, error) {
	name, _ := n.Detailed("bastion")
	group, _ := n.Detailed("bastion_group")
	if name == "" || group == "" {
		vnet, _ := n.Detailed("vnet")
		hint := "create a bastion in that virtual network, Standard SKU with tunnelling enabled"
		if vnet != "" {
			hint = "create a bastion in " + vnet + ", Standard SKU with tunnelling enabled"
		}
		return gateway{}, exitcode.New(exitcode.NotConnected,
			fmt.Sprintf("no bastion reaches %s", n.DisplayName()), hint, nil)
	}
	sku, _ := n.Detailed("bastion_sku")
	tunnel, _ := n.Detailed("bastion_tunnel")
	if strings.EqualFold(sku, "Basic") || tunnel != "true" {
		return gateway{}, exitcode.New(exitcode.NotConnected,
			fmt.Sprintf("bastion %s cannot carry a terminal session", name), skuRemedy(name, group, sku), nil)
	}
	return gateway{name: name, group: group, sku: sku, tunnel: true}, nil
}

// skuRemedy names the command that fixes whichever half is wrong.
//
// The SKU and the tunnelling switch are separate settings, and Microsoft's own
// comparison table does not agree with what the tooling accepts, so guessing
// from the tier alone sends people to the wrong fix.
func skuRemedy(name, group, sku string) string {
	if strings.EqualFold(sku, "Basic") {
		return "the Basic SKU has no native client. Upgrade it: az network bastion update --name " +
			name + " --resource-group " + group + " --sku Standard --enable-tunneling true"
	}
	return "tunnelling is switched off. Enable it: az network bastion update --name " +
		name + " --resource-group " + group + " --enable-tunneling true"
}

// runAz hands the terminal to the vendor CLI for a bastion session.
func runAz(ctx context.Context, args []string) error { return runAzAs(ctx, args, azExitError) }

// runAzAs hands the terminal to the vendor CLI, and reads its exit status
// with explain, since what a status means depends on the command.
func runAzAs(ctx context.Context, args []string, explain func(int) error) error {
	return runAzEnv(ctx, args, explain, nil)
}

// runAzEnv is runAzAs with the child's environment given; nil inherits ours.
func runAzEnv(ctx context.Context, args []string, explain func(int) error, env []string) error {
	bin, err := exec.LookPath("az")
	if err != nil {
		return exitcode.New(exitcode.Prereq, "the Azure CLI is not installed",
			"install it: https://learn.microsoft.com/cli/azure/install-azure-cli", err)
	}
	stop := session.IgnoreTerminalSignals()
	defer stop()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env
	err = cmd.Run()
	if err == nil {
		// az exited cleanly, which means the user ended it.
		return session.ErrClosedByUser
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return explain(ee.ExitCode())
	}
	return err
}

// azExitError explains what the CLI's exit status means here.
//
// 3 is the CLI's "resource not found", which for these commands almost always
// means the bastion extension is absent rather than the machine: the command
// itself does not exist until it is installed.
func azExitError(code int) error {
	switch code {
	case 3:
		return exitcode.New(exitcode.Prereq,
			"the az bastion command is unavailable",
			"install the extension: az extension add --name bastion", nil)
	case 1:
		return exitcode.New(exitcode.NotConnected,
			"the Azure CLI could not open the session",
			"check the bastion allows this machine and that the target is running", nil)
	}
	return exitcode.New(exitcode.Code(code), fmt.Sprintf("az exited %d", code), "", nil)
}

// printable renders the command a user could run themselves, quoting only what
// needs it.
func printable(args []string) string { return printCommand("az", args) }

// printCommand renders bin and its arguments as a line a shell would accept.
func printCommand(bin string, args []string) string {
	var b strings.Builder
	b.WriteString(bin)
	for _, a := range args {
		b.WriteByte(' ')
		if strings.ContainsAny(a, " \t\"'") {
			b.WriteString(strconv.Quote(a))
			continue
		}
		b.WriteString(a)
	}
	return b.String()
}
