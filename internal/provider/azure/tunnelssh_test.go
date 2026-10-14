package azure

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/adrg/xdg"
	"golang.org/x/crypto/ssh"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/session"
)

// signInFixture writes what az ssh cert would: a key pair and a certificate
// for it, signed by a throwaway CA, naming principal as the account.
func signInFixture(t *testing.T, principal string) string {
	t.Helper()
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := ssh.NewSignerFromKey(caKey)
	if err != nil {
		t.Fatal(err)
	}
	cert := &ssh.Certificate{Key: pub, CertType: ssh.UserCert, ValidPrincipals: []string{principal},
		ValidBefore: ssh.CertTimeInfinity}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("id_rsa", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	write("id_rsa.pub", ssh.MarshalAuthorizedKey(pub))
	write("cert", ssh.MarshalAuthorizedKey(cert))
	return dir
}

// fakeTools puts an az and an ssh on PATH that behave like the real ones for
// these tests, and record what they were asked to do in the returned
// directory. sshExit is what ssh exits with; tunnelFails makes the tunnel
// refuse before it is ready.
func fakeTools(t *testing.T, fixture, sshExit string, tunnelFails bool) (rec string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tools are shell scripts")
	}
	bin, rec := t.TempDir(), t.TempDir()
	fail := ""
	if tunnelFails {
		fail = "echo 'ERROR: the bastion refused the tunnel' >&2; exit 1\n"
	}
	az := "#!/bin/sh\n" +
		"case \"$1 $2\" in\n" +
		"\"network bastion\")\n" +
		"  echo \"$@\" > \"" + rec + "/tunnel\"\n" + fail +
		"  echo 'Opening tunnel on port: 1' >&2\n" +
		"  echo 'Tunnel is ready, connect on port 1' >&2\n" +
		// Not exec: like Homebrew's launcher, the process holding the
		// tunnel is a child of the one ssmate started.
		"  sleep 600 ;;\n" +
		"\"ssh cert\")\n" +
		"  d=$(dirname \"$4\")\n" +
		"  cp \"" + fixture + "/id_rsa\" \"" + fixture + "/id_rsa.pub\" \"$d/\" && cp \"" + fixture + "/cert\" \"$4\"\n" +
		"  echo \"$d\" > \"" + rec + "/certdir\" ;;\n" +
		"*) exit 9 ;;\n" +
		"esac\n"
	sshScript := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + rec + "/ssh\"\nexit " + sshExit + "\n"
	for name, body := range map[string]string{"az": az, "ssh": sshScript} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// The session's own temporary directory goes here too, so a test that
	// dies part-way leaves nothing in the real one.
	t.Setenv("TMPDIR", t.TempDir())
	xdg.Reload()
	t.Cleanup(xdg.Reload)
	return rec
}

func bastionNode() inventory.Node {
	return inventory.Node{ID: vmID, Name: "db-prod", Account: "contoso",
		Aliases: []string{"0A1B2C3D-0000-4000-8000-000000000001"},
		Detail:  map[string]string{"login_user": "azureuser"}}
}

var testGateway = gateway{name: "bas", group: "rg-net"}

func readArgs(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s was never run: %v", filepath.Base(path), err)
	}
	return string(b)
}

func TestBastionSSHSignsInWithTheCertificate(t *testing.T) {
	rec := fakeTools(t, signInFixture(t, "you@example.com"), "0", false)

	started := time.Now()
	err := bastionSSH(context.Background(), testGateway, bastionNode(), "aad", "", "", nil)
	if !errors.Is(err, session.ErrClosedByUser) {
		t.Fatalf("a clean ssh exit = %v, want ErrClosedByUser", err)
	}
	// The tunnel's child must be stopped with it; left running, it holds the
	// output pipe and the session never returns.
	if took := time.Since(started); took > 8*time.Second {
		t.Errorf("the session took %s to end after ssh exited; the tunnel was not stopped", took)
	}

	tunnel := readArgs(t, filepath.Join(rec, "tunnel"))
	for _, want := range []string{"--name bas", "--resource-group rg-net", "--target-resource-id " + vmID, "--resource-port 22"} {
		if !strings.Contains(tunnel, want) {
			t.Errorf("tunnel lacks %q: %s", want, tunnel)
		}
	}
	args := readArgs(t, filepath.Join(rec, "ssh"))
	for _, want := range []string{
		"you@example.com@127.0.0.1",
		"HostKeyAlias=azure-vm-0a1b2c3d-0000-4000-8000-000000000001",
		"StrictHostKeyChecking=accept-new",
		"UserKnownHostsFile=" + filepath.Join(xdg.ConfigHome, "ssmate", "known_hosts"),
		"CertificateFile=",
		"IdentitiesOnly=yes",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("ssh lacks %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "StrictHostKeyChecking=no") {
		t.Error("host key checking must stay on")
	}
	certDir := strings.TrimSpace(readArgs(t, filepath.Join(rec, "certdir")))
	if _, err := os.Stat(certDir); !os.IsNotExist(err) {
		t.Errorf("%s survived the session; the key and certificate in it were left behind", certDir)
	}
}

func TestBastionSSHPassesTheExitCodeBack(t *testing.T) {
	fakeTools(t, signInFixture(t, "you@example.com"), "3", false)

	err := bastionSSH(context.Background(), testGateway, bastionNode(), "aad", "", "", []string{"false"})
	var e *exitcode.Error
	if !errors.As(err, &e) || e.Code != 3 {
		t.Fatalf("a command exiting 3 = %v, want exit code 3", err)
	}
}

func TestBastionSSHReportsARefusedLogin(t *testing.T) {
	fakeTools(t, signInFixture(t, "you@example.com"), "255", false)

	err := bastionSSH(context.Background(), testGateway, bastionNode(), "aad", "", "", nil)
	var e *exitcode.Error
	if !errors.As(err, &e) || e.Code != exitcode.NotConnected {
		t.Fatalf("ssh exiting 255 = %v, want NotConnected", err)
	}
	if !strings.Contains(e.Hint, "Virtual Machine User Login") {
		t.Errorf("hint does not name the role that admits you: %s", e.Hint)
	}
}

func TestBastionSSHStopsWhenTheTunnelFails(t *testing.T) {
	rec := fakeTools(t, signInFixture(t, "you@example.com"), "0", true)

	err := bastionSSH(context.Background(), testGateway, bastionNode(), "password", "azureuser", "", nil)
	if err == nil || !strings.Contains(err.Error(), "refused the tunnel") {
		t.Fatalf("a failed tunnel = %v, want an error carrying az's reason", err)
	}
	if _, err := os.Stat(filepath.Join(rec, "ssh")); err == nil {
		t.Error("ssh ran although the tunnel never opened")
	}
}

func TestBastionSSHAsksForAPasswordOnly(t *testing.T) {
	rec := fakeTools(t, signInFixture(t, "you@example.com"), "0", false)

	_ = bastionSSH(context.Background(), testGateway, bastionNode(), "password", "azureuser", "", nil)
	args := readArgs(t, filepath.Join(rec, "ssh"))
	if !strings.Contains(args, "PreferredAuthentications=keyboard-interactive,password") ||
		!strings.Contains(args, "azureuser@127.0.0.1") {
		t.Errorf("password login: %s", args)
	}
	if _, err := os.Stat(filepath.Join(rec, "certdir")); err == nil {
		t.Error("a password login asked Microsoft for a certificate")
	}
}

func TestCertPrincipal(t *testing.T) {
	dir := signInFixture(t, "someone@example.org")
	got, err := certPrincipal(filepath.Join(dir, "cert"))
	if err != nil || got != "someone@example.org" {
		t.Fatalf("certPrincipal = %q, %v", got, err)
	}
	if _, err := certPrincipal(filepath.Join(dir, "id_rsa.pub")); err == nil {
		t.Error("a plain public key is not a certificate")
	}
}

func TestPasswordOffIsNotOfferedAsTheFallback(t *testing.T) {
	vm := vmWith()
	vm.Properties.OSProfile.LinuxConfiguration = &armcompute.LinuxConfiguration{DisablePasswordAuthentication: to.Ptr(true)}
	n := buildNode(vm, Subscription{}, map[string]string{}, map[string]nic{}, map[string][]gateway{})
	if v, _ := n.Detailed("password_login"); v != "off" {
		t.Fatalf("password_login = %q, want off", v)
	}

	_, _, err := resolveLogin(n, provider.SessionOptions{})
	if err == nil || !strings.Contains(err.Error(), "password login turned off") {
		t.Fatalf("falling back to a password on a VM without one = %v, want a refusal", err)
	}
	if auth, _, err := resolveLogin(n, provider.SessionOptions{Auth: "password"}); err != nil || auth != "password" {
		t.Errorf("asked for a password explicitly = %q, %v; it should be tried", auth, err)
	}
	if auth, _, err := resolveLogin(n, provider.SessionOptions{Key: "/k"}); err != nil || auth != "key" {
		t.Errorf("with --key = %q, %v", auth, err)
	}

	open := buildNode(vmWith(), Subscription{}, map[string]string{}, map[string]nic{}, map[string][]gateway{})
	if auth, _, err := resolveLogin(open, provider.SessionOptions{}); err != nil || auth != "" {
		t.Errorf("a VM that allows passwords = %q, %v; the password fallback should stand", auth, err)
	}
}
