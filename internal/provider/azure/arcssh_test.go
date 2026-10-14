package azure

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/adrg/xdg"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/session"
)

// fakeArcTools puts an az and an ssh on PATH. az ssh config writes a Host
// entry the way the ssh extension does, records its arguments and where it
// wrote, or fails when configFails; ssh records its arguments and exits with
// sshExit.
func fakeArcTools(t *testing.T, sshExit string, configFails bool) (rec string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tools are shell scripts")
	}
	bin, rec := t.TempDir(), t.TempDir()
	fail := ""
	if configFails {
		fail = "  echo 'ERROR: the machine is not connected to Azure Arc' >&2; exit 1\n"
	}
	az := "#!/bin/sh\n" +
		"[ \"$1 $2\" = \"ssh config\" ] || exit 9\n" +
		"printf '%s\\n' \"$@\" > \"" + rec + "/az\"\n" + fail +
		"file=''; prev=''\n" +
		"for a in \"$@\"; do [ \"$prev\" = --file ] && file=\"$a\"; prev=\"$a\"; done\n" +
		"printf '\\nHost rg-arc-build-02\\n\\tHostName build-02\\n\\tUser you@example.com\\n\\tProxyCommand \"/proxy\" -r \"%s.relay\"\\n' \"$file\" > \"$file\"\n" +
		"echo \"$(dirname \"$file\")\" > \"" + rec + "/dir\"\n"
	sshScript := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + rec + "/ssh\"\nexit " + sshExit + "\n"
	for name, body := range map[string]string{"az": az, "ssh": sshScript} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	xdg.Reload()
	t.Cleanup(xdg.Reload)
	return rec
}

var arcRoute = inventory.Route{Kind: inventory.RouteArc, Target: arcID, Account: "contoso"}

func TestArcSSHSignsInAndKeepsTheStatus(t *testing.T) {
	rec := fakeArcTools(t, "0", false)

	err := arcSSH(context.Background(), inventory.Node{Name: "build-02"}, arcRoute, provider.SessionOptions{})
	if !errors.Is(err, session.ErrClosedByUser) {
		t.Fatalf("a clean ssh exit = %v, want ErrClosedByUser", err)
	}
	az := readArgs(t, filepath.Join(rec, "az"))
	if !strings.Contains(az, "--keys-destination-folder") || strings.Contains(az, "--local-user") {
		t.Errorf("Microsoft sign-in should have az make the key and certificate:\n%s", az)
	}
	args := readArgs(t, filepath.Join(rec, "ssh"))
	for _, want := range []string{"-F\n", "HostKeyAlias=azure-arc-", "StrictHostKeyChecking=accept-new", "IdentitiesOnly=yes", "rg-arc-build-02"} {
		if !strings.Contains(args, want) {
			t.Errorf("ssh lacks %q:\n%s", want, args)
		}
	}
	dir := strings.TrimSpace(readArgs(t, filepath.Join(rec, "dir")))
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s survived the session; its relay credentials and keys were left behind", dir)
	}
}

func TestArcSSHPassesTheExitCodeBack(t *testing.T) {
	fakeArcTools(t, "5", false)

	err := arcSSH(context.Background(), inventory.Node{Name: "build-02"}, arcRoute,
		provider.SessionOptions{Command: []string{"false"}})
	var e *exitcode.Error
	if !errors.As(err, &e) || e.Code != 5 {
		t.Fatalf("a command exiting 5 = %v, want exit code 5", err)
	}
}

func TestArcSSHLocalAccountRefused(t *testing.T) {
	rec := fakeArcTools(t, "255", false)

	err := arcSSH(context.Background(), inventory.Node{Name: "build-02"}, arcRoute,
		provider.SessionOptions{User: "ubuntu", Key: "/keys/id_ed25519"})
	var e *exitcode.Error
	if !errors.As(err, &e) || e.Code != exitcode.NotConnected {
		t.Fatalf("ssh exiting 255 = %v, want NotConnected", err)
	}
	if !strings.Contains(e.Hint, "Virtual Machine Local User Login") {
		t.Errorf("hint does not name the role a local account needs: %s", e.Hint)
	}
	az := readArgs(t, filepath.Join(rec, "az"))
	if !strings.Contains(az, "--local-user\nubuntu") || !strings.Contains(az, "--private-key-file\n/keys/id_ed25519") {
		t.Errorf("az ssh config: %s", az)
	}
}

func TestArcSSHStopsWhenAzCannotConfigure(t *testing.T) {
	rec := fakeArcTools(t, "0", true)

	err := arcSSH(context.Background(), inventory.Node{Name: "build-02"}, arcRoute, provider.SessionOptions{})
	if err == nil || !strings.Contains(err.Error(), "not connected to Azure Arc") {
		t.Fatalf("a failed az ssh config = %v, want an error carrying az's reason", err)
	}
	if _, err := os.Stat(filepath.Join(rec, "ssh")); err == nil {
		t.Error("ssh ran although az never wrote a configuration")
	}
}

func TestArcAuth(t *testing.T) {
	for _, tc := range []struct {
		o       provider.SessionOptions
		want    string
		wantErr bool
	}{
		{provider.SessionOptions{}, "aad", false},
		{provider.SessionOptions{Auth: "entra"}, "aad", false},
		{provider.SessionOptions{User: "ubuntu"}, "password", false},
		{provider.SessionOptions{User: "ubuntu", Key: "/k"}, "key", false},
		{provider.SessionOptions{User: "ubuntu", Auth: "key"}, "", true},
		{provider.SessionOptions{Auth: "password"}, "", true},
		{provider.SessionOptions{Auth: "telnet"}, "", true},
	} {
		got, err := arcAuth(tc.o)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("arcAuth(%+v) = %q, %v", tc.o, got, err)
		}
	}
}
