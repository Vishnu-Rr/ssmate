package oci

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/session"
	"github.com/oracle/oci-go-sdk/v65/bastion"
	"github.com/oracle/oci-go-sdk/v65/common"
	"golang.org/x/crypto/ssh"
)

// sessionTTL is how long a bastion session lives.
//
// OCI will not extend a session and will not shorten one: whatever is asked
// for at creation is what it gets, and the slot is held until it expires. A
// bastion allows twenty at once, so a long default quietly exhausts a shared
// resource. Three hours is the service maximum; thirty minutes is the
// minimum. An hour covers ordinary work and frees the slot the same morning.
const sessionTTL = 3600

// activeTimeout bounds the wait for a session to come up. A healthy one is
// ready in well under a minute.
const activeTimeout = 90 * time.Second

// managedTimeout bounds the wait for a managed SSH session, which was measured
// at three to five minutes to come up: the plugin on the machine has to fetch
// and install the session's key before the session reports ready.
const managedTimeout = 8 * time.Minute

// sessionKind is one of the bastion's session types: what it is asked to
// reach, and how long it is given to come up.
type sessionKind struct {
	target func(n inventory.Node, targetIP string, targetPort int) bastion.CreateSessionTargetResourceDetails
	wait   time.Duration
	// note is said before a wait long enough to look like a hang.
	note string
}

// portForwarding tunnels to an address and port, and asks nothing of the
// machine; the login is up to a key the machine already trusts.
var portForwarding = sessionKind{
	target: func(_ inventory.Node, ip string, port int) bastion.CreateSessionTargetResourceDetails {
		return bastion.CreatePortForwardingSessionTargetResourceDetails{
			TargetResourcePrivateIpAddress: &ip,
			TargetResourcePort:             common.Int(port),
		}
	},
	wait: activeTimeout,
}

// managedSSH has the Bastion plugin on the machine accept the session's key
// for one account, so the throwaway key logs in as well as opening the door.
func managedSSH(user string) sessionKind {
	return sessionKind{
		target: func(n inventory.Node, ip string, port int) bastion.CreateSessionTargetResourceDetails {
			return bastion.CreateManagedSshSessionTargetResourceDetails{
				TargetResourceId:                      &n.ID,
				TargetResourceOperatingSystemUserName: &user,
				TargetResourcePrivateIpAddress:        &ip,
				TargetResourcePort:                    common.Int(port),
			}
		},
		wait: managedTimeout,
		note: "Opening a managed SSH session. OCI places a one-time key on the machine, which takes a few minutes.",
	}
}

// keypair is an ephemeral SSH identity, valid only for one session.
//
// The private key has to exist as a file because ssh reads it from one. It is
// created inside a directory only this user can enter, removed when the
// session ends, and never reused: the bastion authorises this key for this
// session and nothing else.
type keypair struct {
	dir         string
	privatePath string
	authorized  string
}

func newKeypair() (*keypair, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generating a session key: %w", err)
	}
	pub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "ssmate-oci-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "id_rsa")
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	// 0600 because ssh refuses a key any other account could read.
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return &keypair{
		dir:         dir,
		privatePath: path,
		authorized:  authorizedLine(pub),
	}, nil
}

// authorizedLine renders the public key as an authorized_keys line.
//
// The trailing comment is what ssh-keygen writes and is kept for the same
// reason: it labels the key wherever it is inspected. The bastion does not
// require it - that was measured both ways with the timing below controlled,
// because a first reading of the evidence suggested it did.
func authorizedLine(pub ssh.PublicKey) string {
	return strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(pub)), "\n") + " ssmate\n"
}

func (k *keypair) remove() {
	if k != nil && k.dir != "" {
		os.RemoveAll(k.dir)
	}
}

// bastionSSHOptions are the flags every connection through a bastion needs.
//
// IdentitiesOnly is not optional and was found the hard way: without it ssh
// offers the user's own ~/.ssh keys and any agent identities before the one
// the bastion actually authorised, the bastion rejects each in turn, and the
// connection can fail with "Permission denied (publickey)" while holding the
// correct key all along.
//
// accept-new rather than no: the bastion's hostname is per region and stable,
// so a first connection has nothing to compare against and would otherwise
// stop to ask - but a host key that later changes still has to be an error,
// which is exactly what "no" would throw away.
func bastionSSHOptions(k *keypair) []string {
	return []string{
		"-i", k.privatePath,
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=30",
	}
}

// bastionHost is where ssh connects to reach a session.
func bastionHost(region string) string {
	return "host.bastion." + region + ".oci.oraclecloud.com"
}

// Shell opens an interactive session.
//
// It is a port-forwarding session with ssh tunnelled through it, rather than
// the Managed SSH session type, for two reasons. Managed SSH takes three to
// five minutes against port forwarding's under a minute, and it requires an
// agent whose reported state turns out not to predict whether it works: a
// machine Oracle documents as unsupported connected fine, and one it documents
// as supported hung indefinitely. Port forwarding asks nothing of the machine.
//
// The consequence is that the machine authenticates the user, not us. The
// ephemeral key opens the bastion and nothing more; reaching a login prompt
// needs the user's own SSH identity, from their agent or ~/.ssh. ssmate never
// sees, stores or forwards a credential.
func (p Provider) Shell(ctx context.Context, n inventory.Node, o provider.SessionOptions) error {
	user, err := loginUserFor(n, o)
	if err != nil {
		return err
	}
	if n.PrivateIP == "" {
		return exitcode.New(exitcode.Err,
			fmt.Sprintf("%s has no private address to connect to", n.DisplayName()), "", nil)
	}
	if o.Print {
		return printShell(n, user, o.Key, o.Command)
	}
	return p.withSession(ctx, n, n.PrivateIP, 22, o, portForwarding, func(k *keypair, sessionID, allow string) error {
		return runSSH(ctx, allow, shellArgs(k, sessionID, n.Region, user, n.PrivateIP, o.Key, o.Command))
	})
}

// shellArgs builds the ssh that logs in to a machine through a bastion.
//
// There are two hops and they need different identities, which is the whole
// difficulty. The inner hop, run as the ProxyCommand, reaches the bastion and
// must offer only the throwaway key the session was opened with; offered
// anything else first, the bastion refuses it. The outer hop logs in to the
// machine itself, which has never seen that throwaway key and must be offered
// the user's own - from --key, or from their agent and ~/.ssh.
//
// An earlier version gave both hops the bastion's options. The bastion
// accepted the connection and every login to a machine then failed, because
// the only key offered to the machine was one it could not know.
func shellArgs(k *keypair, sessionID, region, user, ip, userKey string, command []string) []string {
	proxy := "ssh " + strings.Join(append(bastionSSHOptions(k),
		"-W", "%h:%p", "-p", "22", sessionID+"@"+bastionHost(region)), " ")

	args := []string{
		"-o", "ProxyCommand=" + proxy,
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=30",
	}
	if userKey != "" {
		// A key named explicitly is the only one worth offering, for the same
		// reason the bastion hop is strict: a server stops listening after a
		// handful of refused keys.
		args = append(args, "-i", userKey, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, user+"@"+ip)
	// ssh runs whatever follows the destination instead of a shell, and brings
	// its exit status home, so a script can branch on it.
	return append(args, command...)
}

// Forward opens a tunnel and holds it open.
func (p Provider) Forward(ctx context.Context, n inventory.Node, f session.Forward, o provider.SessionOptions) error {
	if n.PrivateIP == "" {
		return exitcode.New(exitcode.Err,
			fmt.Sprintf("%s has no private address to forward to", n.DisplayName()), "", nil)
	}
	if o.Print {
		return printForward(n, f, o.Bind)
	}
	host := f.RemoteHost
	if host == "" {
		host = n.PrivateIP
	}
	bind := o.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	return session.HoldOpen(ctx, func() error {
		return p.withSession(ctx, n, host, f.RemotePort, o, portForwarding, func(k *keypair, sessionID, allow string) error {
			args := append(bastionSSHOptions(k),
				"-N",
				"-o", "ExitOnForwardFailure=yes",
				"-L", fmt.Sprintf("%s:%d:%s:%d", bind, f.LocalPort, host, f.RemotePort),
				"-p", "22",
				sessionID+"@"+bastionHost(n.Region),
			)
			return runSSH(ctx, allow, args)
		})
	}, o.OnReconnect)
}

// withSession creates a bastion session, waits for it, runs use, and deletes
// the session however that ends.
//
// The delete matters more here than the equivalent would on Amazon. A bastion
// permits twenty concurrent sessions and will not end one early, so a leaked
// session holds a slot on a shared resource for the whole of its TTL.
// failureKind says which side of a bastion a connection failed on.
type failureKind int

const (
	// refusedClient: the bastion would not take a connection from this
	// machine. Almost always its client allowlist.
	refusedClient failureKind = iota
	// unreachedTarget: the bastion took the connection but could not reach
	// the machine behind it. Almost always the egress rules on the bastion's
	// own subnet.
	unreachedTarget
)

// bastionFailure is one bastion's reason for not carrying a connection.
//
// The two kinds have different fixes on different resources, and ssh reports
// both as a bare disconnect, so the distinction is made where it can be: a
// failed rehearsal is the bastion refusing us, and a real session failing
// after a rehearsal succeeded is the bastion failing to reach the machine.
type bastionFailure struct {
	bastion string
	allow   string
	target  string
	kind    failureKind
	detail  string
}

func (f *bastionFailure) Error() string { return f.reason() }

// reason says why, in terms of the resource that has to change.
func (f *bastionFailure) reason() string {
	switch f.kind {
	case unreachedTarget:
		return fmt.Sprintf("%s accepted the connection but cannot reach %s; its subnet's egress rules do not allow it",
			f.bastion, f.target)
	default:
		if f.allow != "" && f.allow != "0.0.0.0/0" {
			return fmt.Sprintf("%s refused this machine; it admits only %s", f.bastion, f.allow)
		}
		return fmt.Sprintf("%s refused the connection (%s)", f.bastion, f.detail)
	}
}

// ExitCode lets exitcode.From classify it without every caller wrapping it.
func (f *bastionFailure) ExitCode() exitcode.Code { return exitcode.NotConnected }

func (p Provider) withSession(
	ctx context.Context,
	n inventory.Node,
	targetIP string,
	targetPort int,
	o provider.SessionOptions,
	kind sessionKind,
	use func(k *keypair, sessionID, allow string) error,
) error {
	candidates := bastionCandidates(n)
	if len(candidates) == 0 {
		return exitcode.New(exitcode.NotConnected,
			fmt.Sprintf("no bastion reaches %s", n.DisplayName()),
			"create a bastion bound to that machine's VCN", nil)
	}

	cp, err := credentials(n.Account)
	if err != nil {
		return err
	}
	c, err := dial(cp, n.Region)
	if err != nil {
		return err
	}

	var failures []string
	for i, bastionID := range candidates {
		err := openThrough(ctx, c, n, bastionID, bastionAllow(n, i), targetIP, targetPort, o, kind, use)
		// Only a refusal by this particular bastion is worth trying the next
		// one for. A user who closed the session, or a machine that refused
		// the login, would get the same answer from every door.
		if err == nil || !worthAnotherBastion(err) {
			return err
		}
		var bf *bastionFailure
		if errors.As(err, &bf) {
			bf.bastion, bf.allow = bastionName(n, i), bastionAllow(n, i)
			bf.target = fmt.Sprintf("%s:%d", targetIP, targetPort)
			failures = append(failures, bf.reason())
		} else {
			failures = append(failures, bastionName(n, i)+": "+err.Error())
		}
		if i+1 < len(candidates) {
			fmt.Fprintf(os.Stderr, "%s could not carry the connection, trying the next bastion\n",
				bastionName(n, i))
		}
	}
	// Every door refused. Say why for each, because the fix is on whichever
	// one the user decides to change - and "connection closed by remote
	// host", which is all ssh says, points at none of them.
	hint := strings.Join(failures, "\n  ") + "\n  " +
		"either add this machine's address to a bastion's allowlist, or let a bastion's subnet reach the target"
	return exitcode.New(exitcode.NotConnected,
		fmt.Sprintf("no bastion could reach %s from this machine", n.DisplayName()), hint, nil)
}

// bastionCandidates lists the bastions worth trying, best guess first.
func bastionCandidates(n inventory.Node) []string {
	if all, ok := n.Detailed("bastions"); ok && all != "" {
		return strings.Split(all, ",")
	}
	if one, ok := n.Detailed("bastion"); ok && one != "" {
		return []string{one}
	}
	return nil
}

// bastionAllow reports the nth candidate's client allowlist.
//
// Each bastion has its own, and quoting the wrong one sends the user to fix a
// rule that was never the problem. Semicolons separate bastions because the
// blocks within one are comma separated already.
func bastionAllow(n inventory.Node, i int) string {
	if all, ok := n.Detailed("bastion_allows"); ok {
		if parts := strings.Split(all, ";"); i < len(parts) {
			return parts[i]
		}
	}
	if i == 0 {
		a, _ := n.Detailed("bastion_allow")
		return a
	}
	return ""
}

// bastionName names the nth candidate for a progress line.
func bastionName(n inventory.Node, i int) string {
	if all, ok := n.Detailed("bastion_names"); ok {
		if parts := strings.Split(all, ","); i < len(parts) {
			return parts[i]
		}
	}
	name, _ := n.Detailed("bastion_name")
	return name
}

// worthAnotherBastion reports whether a different door might succeed where
// this one did not.
//
// The two failures that a second bastion can fix are the ones this estate
// showed: a bastion that refuses your address through its client allowlist,
// and one that is admitted but whose own subnet cannot reach the target. Both
// surface as ssh failing to connect. A login the machine itself refused, or a
// session the user closed, will go the same way through every bastion.
func worthAnotherBastion(err error) bool {
	if errors.Is(err, session.ErrClosedByUser) {
		return false
	}
	var bf *bastionFailure
	if errors.As(err, &bf) {
		return true
	}
	return exitcode.From(err) == exitcode.NotConnected
}

// openThrough runs one attempt against one bastion.
func openThrough(
	ctx context.Context,
	c *clients,
	n inventory.Node,
	bastionID, allow, targetIP string,
	targetPort int,
	o provider.SessionOptions,
	kind sessionKind,
	use func(k *keypair, sessionID, allow string) error,
) error {
	k, err := newKeypair()
	if err != nil {
		return err
	}
	defer k.remove()

	name := "ssmate"
	if o.Reason != "" {
		name = "ssmate: " + o.Reason
	}
	resp, err := c.bastion.CreateSession(ctx, bastion.CreateSessionRequest{
		CreateSessionDetails: bastion.CreateSessionDetails{
			BastionId:             &bastionID,
			DisplayName:           &name,
			KeyType:               bastion.CreateSessionDetailsKeyTypePub,
			KeyDetails:            &bastion.PublicKeyDetails{PublicKeyContent: &k.authorized},
			TargetResourceDetails: kind.target(n, targetIP, targetPort),
			SessionTtlInSeconds:   common.Int(sessionTTL),
		},
	})
	if err != nil {
		return sessionCreateError(n, err)
	}
	sessionID := deref(resp.Session.Id)

	// Deleting runs on every path out, including a signal, because the slot
	// is not ours to hold.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = c.bastion.DeleteSession(cleanup, bastion.DeleteSessionRequest{SessionId: &sessionID})
	}()

	if kind.note != "" {
		fmt.Fprintln(os.Stderr, kind.note)
	}
	if err := waitActive(ctx, c.bastion, sessionID, kind.wait); err != nil {
		return err
	}
	if err := waitKeyAccepted(ctx, k, sessionID, n.Region, targetIP, targetPort); err != nil {
		return err
	}
	return use(k, sessionID, allow)
}

// waitActive blocks until the session is usable.
//
// ACTIVE is necessary but not sufficient: a session reports ACTIVE while
// carrying no traffic at all, because the bastion cannot know whether its own
// subnet is permitted to reach the target until something tries. That failure
// surfaces later, as an ssh timeout.
func waitActive(ctx context.Context, api bastionAPI, sessionID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := api.GetSession(ctx, bastion.GetSessionRequest{SessionId: &sessionID})
		if err != nil {
			return fmt.Errorf("checking the bastion session: %w", err)
		}
		switch resp.Session.LifecycleState {
		case bastion.SessionLifecycleStateActive:
			return nil
		case bastion.SessionLifecycleStateFailed:
			return exitcode.New(exitcode.NotConnected,
				"the bastion refused the session",
				deref(resp.Session.LifecycleDetails), nil)
		}
		if time.Now().After(deadline) {
			return exitcode.New(exitcode.NotConnected,
				fmt.Sprintf("the bastion session was still %s after %s",
					resp.Session.LifecycleState, timeout),
				"", nil)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// keyPropagation bounds the wait for a session's key to reach the bastion.
//
// Measured at around fifteen seconds on a healthy session. The bound is twice
// that rather than more, because the same wait is also spent on a bastion that
// is never going to accept us - one whose client allowlist excludes this
// machine refuses identically, and only falling through to the next one fixes
// that.
const keyPropagation = 30 * time.Second

// waitKeyAccepted blocks until the bastion will actually authenticate.
//
// A session reports ACTIVE before its public key has reached the host that
// checks it, and an ssh in that window is refused with "Permission denied
// (publickey)" - which reads as a wrong or unauthorised key and is neither.
// This is not documented anywhere and is the single most misleading thing
// about the service: everything looks correct and nothing works.
//
// So the connection is rehearsed before the user sees it. A non-interactive
// ssh proves authentication and nothing else; handing the terminal over only
// after it succeeds turns a confusing refusal into a few seconds of waiting.
func waitKeyAccepted(ctx context.Context, k *keypair, sessionID, region, targetIP string, targetPort int) error {
	bin, err := exec.LookPath("ssh")
	if err != nil {
		return exitcode.New(exitcode.Prereq, "ssh is not installed", sshInstallHint(), err)
	}
	deadline := time.Now().Add(keyPropagation)
	var last error
	for attempt := 1; ; attempt++ {
		port, err := freePort()
		if err != nil {
			return err
		}
		probe, cancel := context.WithTimeout(ctx, 20*time.Second)
		args := append(bastionSSHOptions(k),
			"-N", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes",
			"-L", fmt.Sprintf("127.0.0.1:%d:%s:%d", port, targetIP, targetPort),
			"-p", "22", sessionID+"@"+bastionHost(region),
		)
		last = runProbe(exec.CommandContext(probe, bin, args...))
		cancel()
		if last == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Only the not-yet-propagated refusal is worth waiting out. Anything
		// else is a real failure and waiting will not change it.
		if !errors.Is(last, errKeyNotYetAccepted) {
			// Anything but a key that has not propagated means this bastion
			// will not take a connection from here - most often its client
			// allowlist, which closes the connection before any key is asked
			// for. That is this bastion's refusal, not the machine's, so it
			// must let the next bastion be tried.
			return &bastionFailure{kind: refusedClient, detail: last.Error()}
		}
		if time.Now().After(deadline) {
			return &bastionFailure{kind: refusedClient, detail: "it would not accept the session key"}
		}
		if attempt == 2 {
			fmt.Fprintln(os.Stderr, "waiting for the bastion to accept the session key...")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// errKeyNotYetAccepted marks the refusal that waiting can fix.
var errKeyNotYetAccepted = errors.New("the bastion has not accepted the session key yet")

// freePort asks the kernel for a port nothing is using, so a rehearsal never
// collides with one the user asked for.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// runProbe starts a rehearsal and reports whether it authenticated.
//
// Authentication is proved by the process still running after a moment: an
// ssh that was refused exits at once, and one that was accepted sits holding
// the forward open. It is then stopped, having proved the only thing it was
// for. ssh's own words decide what a failure was, because a key that has not
// propagated and one the bastion will never accept are the same message and
// only the first is worth waiting out - the caller falls through to the next
// bastion for the second.
func runProbe(cmd *exec.Cmd) error {
	var stderr strings.Builder
	cmd.Stdin, cmd.Stdout = nil, nil
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		if strings.Contains(stderr.String(), "Permission denied") {
			return errKeyNotYetAccepted
		}
		if msg := strings.TrimSpace(firstMeaningfulLine(stderr.String())); msg != "" {
			return errors.New(msg)
		}
		return errKeyNotYetAccepted
	case <-time.After(4 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return nil
	}
}

// firstMeaningfulLine skips ssh's advisory banners to find what went wrong.
func firstMeaningfulLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "**") || strings.HasPrefix(line, "Warning:") {
			continue
		}
		return line
	}
	return ""
}

// runSSH hands the terminal to ssh and reports how it ended.
//
// Signals are ignored here for the same reason they are on Amazon: Ctrl-C is
// meant for whatever is running on the far side, not for the tool holding the
// connection open.
func runSSH(ctx context.Context, allow string, args []string) error {
	bin, err := exec.LookPath("ssh")
	if err != nil {
		return exitcode.New(exitcode.Prereq, "ssh is not installed",
			sshInstallHint(), err)
	}
	stop := session.IgnoreTerminalSignals()
	defer stop()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	if err == nil {
		// ssh exited cleanly, which means the user ended it. Rebuilding the
		// tunnel now would be the tool refusing to let go.
		return session.ErrClosedByUser
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return sshExitError(allow, ee.ExitCode())
	}
	return err
}

// sshExitError explains ssh's exit status.
//
// 255 is ssh's own "could not connect", and after a session has reported
// ACTIVE it almost always means the bastion's subnet cannot reach the target -
// an egress rule, not a broken machine. Anything else came from the remote
// command and belongs to the user.
func sshExitError(allow string, code int) error {
	if code == 255 {
		// ssh's own "could not connect". By this point the rehearsal has
		// already proved the bastion takes our key, so what failed is the
		// bastion reaching the machine behind it.
		return &bastionFailure{kind: unreachedTarget, allow: allow}
	}
	// Any other status came from the command on the machine, and belongs to
	// the user: a script branching on it needs it passed through unchanged.
	return exitcode.New(exitcode.Code(code), fmt.Sprintf("ssh exited %d", code), "", nil)
}

func sshInstallHint() string {
	return "install OpenSSH: macOS ships it, Debian and Ubuntu use apt install openssh-client, " +
		"Windows has it under Settings > Optional features"
}

// loginUserFor decides which account to log in as.
//
// OCI's API carries no login name, so the order is: what the user said, then
// what the machine's image implies, then an honest failure. Guessing wrong
// produces a permission denied that looks like a key problem, which is a
// worse outcome than being asked.
func loginUserFor(n inventory.Node, o provider.SessionOptions) (string, error) {
	if o.User != "" {
		return o.User, nil
	}
	if u, ok := n.Detailed("login_user"); ok && u != "" {
		return u, nil
	}
	osName, _ := n.Detailed("os")
	msg := fmt.Sprintf("cannot tell which user to log in as on %s", n.DisplayName())
	if osName != "" {
		msg += fmt.Sprintf(" (image reports %q)", osName)
	}
	return "", exitcode.New(exitcode.Err, msg,
		"pass --user, for example --user opc or --user ubuntu", nil)
}

// sessionCreateError explains the two refusals worth naming.
func sessionCreateError(n inventory.Node, err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "LimitExceeded"):
		name, _ := n.Detailed("bastion_name")
		return exitcode.New(exitcode.NotConnected,
			fmt.Sprintf("bastion %s already has its maximum of twenty sessions", name),
			"wait for one to expire, or delete an idle session: oci bastion session list --bastion-id <id>", err)
	case strings.Contains(msg, "NotAuthorizedOrNotFound"):
		return exitcode.New(exitcode.Denied,
			"not allowed to create a session on that bastion",
			"the policy needs manage bastion-session in the bastion's compartment", err)
	}
	return fmt.Errorf("creating a bastion session: %w", err)
}

// printShell and printForward show what would happen.
//
// There is no single vendor command that does this, so both halves are shown:
// the session the tool would create, and the ssh it would then run. Printing
// only one would be a command that does not work on its own.
func printShell(n inventory.Node, user, userKey string, command []string) error {
	bastionID, _ := n.Detailed("bastion")
	fmt.Println(createCommand(bastionID, n.PrivateIP, 22))
	identity := ""
	if userKey != "" {
		identity = " -i " + userKey
	}
	line := fmt.Sprintf("ssh -o ProxyCommand=\"ssh -i <session-key> -W %%h:%%p -p 22 <session-ocid>@%s\"%s %s@%s",
		bastionHost(n.Region), identity, user, n.PrivateIP)
	// What is printed has to be what would run. A printed command that omits
	// the part the user asked about is worse than not offering print at all.
	if len(command) > 0 {
		line += " " + strings.Join(command, " ")
	}
	fmt.Println(line)
	return nil
}

func printForward(n inventory.Node, f session.Forward, bind string) error {
	host := f.RemoteHost
	if host == "" {
		host = n.PrivateIP
	}
	if bind == "" {
		bind = "127.0.0.1"
	}
	bastionID, _ := n.Detailed("bastion")
	fmt.Println(createCommand(bastionID, host, f.RemotePort))
	fmt.Printf("ssh -i <key> -N -L %s:%d:%s:%d -p 22 <session-ocid>@%s\n",
		bind, f.LocalPort, host, f.RemotePort, bastionHost(n.Region))
	return nil
}

func createCommand(bastionID, ip string, port int) string {
	return "oci bastion session create-port-forwarding" +
		" --bastion-id " + bastionID +
		" --target-private-ip " + ip +
		" --target-port " + strconv.Itoa(port) +
		" --ssh-public-key-file <key>.pub" +
		" --session-ttl " + strconv.Itoa(sessionTTL)
}
