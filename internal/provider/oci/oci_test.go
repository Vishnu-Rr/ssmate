package oci

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
	"github.com/Vishnu-Rr/ssmate/internal/session"
)

// The two credential kinds fail identically on the wire and need opposite
// remedies, so the profile's kind has to be read from the file rather than
// guessed from the error.
func TestProfilesDistinguishSessionTokensFromKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(`[DEFAULT]
user=ocid1.user.oc1..a
fingerprint=aa:bb
key_file=/k.pem
tenancy=ocid1.tenancy.oc1..a
region=uk-london-1

# a comment, and a blank line follow

[work]
fingerprint = cc:dd
key_file = /k2.pem
region = us-ashburn-1
security_token_file = /tmp/token
`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Profiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d profiles, want 2: %+v", len(got), got)
	}
	if got[0].Name != "DEFAULT" || got[0].Token || got[0].Region != "uk-london-1" {
		t.Errorf("DEFAULT parsed wrong: %+v", got[0])
	}
	if got[1].Name != "work" || !got[1].Token || got[1].Region != "us-ashburn-1" {
		t.Errorf("work parsed wrong: %+v", got[1])
	}
}

// Reachability on OCI is a network fact, not a health report: a machine can be
// perfectly healthy and unreachable because nobody built a door.
func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      string
		hasGateway bool
		wantStatus string
		wantReach  bool
		wantHint   bool
	}{
		{"running behind a bastion", "RUNNING", true, inventory.StatusOnline, true, false},
		{"running with no bastion", "RUNNING", false, inventory.StatusNoGateway, false, true},
		{"stopped", "STOPPED", false, inventory.StatusStopped, false, true},
		{"stopped even with a bastion", "STOPPED", true, inventory.StatusStopped, false, true},
		{"terminated", "TERMINATED", false, inventory.StatusTerminated, false, false},
		{"unknown", "", false, inventory.StatusUnknown, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := inventory.Node{State: tc.state}
			classify(&n, tc.hasGateway, "app-vcn")
			if n.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", n.Status, tc.wantStatus)
			}
			if n.Reachable != tc.wantReach {
				t.Errorf("reachable = %v, want %v", n.Reachable, tc.wantReach)
			}
			if (n.Hint != "") != tc.wantHint {
				t.Errorf("hint = %q, wanted one: %v", n.Hint, tc.wantHint)
			}
		})
	}
}

// The no-bastion hint has to name the network, because the fix is made there
// and every other identifier on the machine is an OCID.
func TestNoGatewayHintNamesTheVCN(t *testing.T) {
	n := inventory.Node{State: "RUNNING"}
	classify(&n, false, "app-vcn")
	if got := n.Hint; got == "" || !contains(got, "app-vcn") {
		t.Errorf("hint should name the VCN, got %q", got)
	}
	m := inventory.Node{State: "RUNNING"}
	classify(&m, false, "")
	if m.Hint == "" {
		t.Error("an unnamed VCN still needs a hint")
	}
}

func TestLoginUser(t *testing.T) {
	for _, tc := range []struct{ image, want string }{
		{"Canonical Ubuntu", "ubuntu"},
		{"canonical ubuntu", "ubuntu"},
		{"Oracle Linux", "opc"},
		{"CentOS", "opc"},
		{"Windows", ""},
		{"", ""},
	} {
		if got := LoginUser(tc.image); got != tc.want {
			t.Errorf("LoginUser(%q) = %q, want %q", tc.image, got, tc.want)
		}
	}
}

// Users search tags without caring which of OCI's two tag systems a tag came
// from, so both arrive in one space.
func TestTagsOfFlattensBothSystems(t *testing.T) {
	got := tagsOf(
		map[string]string{"Name": "web-1"},
		map[string]map[string]any{"Operations": {"Environment": "prod", "CostCentre": 42}},
	)
	for k, want := range map[string]string{
		"Name":                   "web-1",
		"Operations.Environment": "prod",
		"Operations.CostCentre":  "42",
	} {
		if got[k] != want {
			t.Errorf("tags[%q] = %q, want %q", k, got[k], want)
		}
	}
}

// A VCN may hold several bastions and they are not interchangeable, so the
// connection has to be able to walk the list.
func TestBastionCandidates(t *testing.T) {
	multi := inventory.Node{Detail: map[string]string{
		"bastion":  "a",
		"bastions": "a,b,c",
	}}
	if got := bastionCandidates(multi); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("got %v, want all three in order", got)
	}
	single := inventory.Node{Detail: map[string]string{"bastion": "only"}}
	if got := bastionCandidates(single); len(got) != 1 || got[0] != "only" {
		t.Errorf("got %v, want the one", got)
	}
	if got := bastionCandidates(inventory.Node{}); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

// Trying a second bastion is only worth it for the failures a different door
// could fix. Reopening after the user left, or after the machine refused the
// login, would just repeat itself.
func TestWorthAnotherBastion(t *testing.T) {
	if !worthAnotherBastion(exitcode.New(exitcode.NotConnected, "no route", "", nil)) {
		t.Error("a connection failure should try the next bastion")
	}
	if worthAnotherBastion(session.ErrClosedByUser) {
		t.Error("the user closing it must not try another bastion")
	}
	if worthAnotherBastion(exitcode.New(exitcode.Denied, "not authorized", "", nil)) {
		t.Error("a permissions refusal is the same at every bastion")
	}
	if worthAnotherBastion(exitcode.New(exitcode.Code(1), "ssh exited 1", "", nil)) {
		t.Error("a remote command's own exit status is not a bastion problem")
	}
}

// The allowlist check is the difference between a correct diagnosis and
// blaming the key, so the containment test has to be right.
func TestCIDRsContain(t *testing.T) {
	blocks := []string{"10.20.0.0/23", "10.0.0.0/8"}
	for _, ip := range []string{"10.20.0.1", "10.20.1.255", "10.1.2.3"} {
		if !cidrsContain(blocks, ip) {
			t.Errorf("%s should be inside %v", ip, blocks)
		}
	}
	for _, ip := range []string{"192.168.4.1", "203.0.113.7", "not-an-ip", ""} {
		if cidrsContain(blocks, ip) {
			t.Errorf("%s should be outside %v", ip, blocks)
		}
	}
	if cidrsContain([]string{"garbage"}, "10.0.0.1") {
		t.Error("an unparseable block must not match")
	}
}

// A bastion still being built is not a door yet, and the order must not depend
// on which compartment answered first.
func TestActiveGatewaysAreFilteredAndOrdered(t *testing.T) {
	got := activeGateways([]bastionSummary{
		{id: "3", name: "zeta", state: "ACTIVE"},
		{id: "1", name: "pending", state: "CREATING"},
		{id: "2", name: "alpha", state: "ACTIVE"},
	})
	if len(got) != 2 {
		t.Fatalf("got %d, want the two active ones", len(got))
	}
	if got[0].name != "alpha" || got[1].name != "zeta" {
		t.Errorf("got %v, want alphabetical", []string{got[0].name, got[1].name})
	}
}

// Guessing the login account wrong produces a refusal that reads like a key
// problem, so an unknown account has to be an honest error naming the flag.
func TestLoginUserForPrefersTheUserThenTheImage(t *testing.T) {
	n := inventory.Node{Detail: map[string]string{"login_user": "ubuntu"}}
	if got, _ := loginUserFor(n, providerOptionsWithUser("opc")); got != "opc" {
		t.Errorf("an explicit --user must win, got %q", got)
	}
	if got, _ := loginUserFor(n, providerOptionsWithUser("")); got != "ubuntu" {
		t.Errorf("should fall back to the image, got %q", got)
	}
	unknown := inventory.Node{Name: "box", Detail: map[string]string{"os": "Windows"}}
	_, err := loginUserFor(unknown, providerOptionsWithUser(""))
	if err == nil {
		t.Fatal("an unknown account must be an error, not a guess")
	}
	if !contains(err.Error(), "box") {
		t.Errorf("the error should name the machine, got %q", err)
	}
}

func providerOptionsWithUser(u string) provider.SessionOptions {
	return provider.SessionOptions{User: u}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// A bastion open to every address will always authenticate, so trying it first
// means a failure is about reaching the machine rather than about us.
func TestActiveGatewaysTryOpenBastionsFirst(t *testing.T) {
	got := activeGateways([]bastionSummary{
		{name: "alpha", state: "ACTIVE", allow: []string{"10.20.0.0/23"}},
		{name: "zeta", state: "ACTIVE", allow: []string{"0.0.0.0/0"}},
		{name: "beta", state: "ACTIVE", allow: []string{"10.0.0.0/8"}},
	})
	if got[0].name != "zeta" {
		t.Errorf("first is %q, want the one open to any address", got[0].name)
	}
	// The restricted ones keep a stable order behind it.
	if got[1].name != "alpha" || got[2].name != "beta" {
		t.Errorf("rest = %v, want alphabetical", []string{got[1].name, got[2].name})
	}
}

func TestAdmitsAnyone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		allow []string
		want  bool
	}{
		{"unset is the service default, which is open", nil, true},
		{"explicitly open", []string{"0.0.0.0/0"}, true},
		{"open among others", []string{"10.0.0.0/8", "0.0.0.0/0"}, true},
		{"restricted", []string{"10.20.0.0/23"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := admitsAnyone(bastionSummary{allow: tc.allow}); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// Quoting the wrong bastion's allowlist sends the user to fix a rule that was
// never the problem.
func TestBastionAllowIsPerBastion(t *testing.T) {
	n := inventory.Node{Detail: map[string]string{
		"bastions":       "a,b",
		"bastion_allows": "0.0.0.0/0;10.20.0.0/23",
		"bastion_allow":  "0.0.0.0/0",
	}}
	if got := bastionAllow(n, 0); got != "0.0.0.0/0" {
		t.Errorf("first = %q", got)
	}
	if got := bastionAllow(n, 1); got != "10.20.0.0/23" {
		t.Errorf("second = %q", got)
	}
	single := inventory.Node{Detail: map[string]string{"bastion_allow": "10.0.0.0/8"}}
	if got := bastionAllow(single, 0); got != "10.0.0.0/8" {
		t.Errorf("single = %q", got)
	}
}

// ssh's advisory banners must not be mistaken for the failure.
func TestFirstMeaningfulLineSkipsBanners(t *testing.T) {
	out := "** WARNING: connection is not using a post-quantum key exchange algorithm.\n" +
		"** This session may be vulnerable.\n" +
		"kex_exchange_identification: read: Connection reset by peer\n"
	if got := firstMeaningfulLine(out); got != "kex_exchange_identification: read: Connection reset by peer" {
		t.Errorf("got %q", got)
	}
	if got := firstMeaningfulLine("** only a banner\n"); got != "" {
		t.Errorf("banners alone should yield nothing, got %q", got)
	}
}

// The rehearsal needs a port nothing else holds, or it fails for a reason
// that has nothing to do with the bastion.
func TestFreePortIsUsable(t *testing.T) {
	p, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if p <= 0 || p > 65535 {
		t.Fatalf("got %d, want a real port", p)
	}
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		t.Fatalf("port %d was not actually free: %v", p, err)
	}
	l.Close()
}

// The outer hop logs in to the machine, which has never seen the throwaway key
// a bastion session is opened with. Offering only that key - as an earlier
// version did, by giving both hops the bastion's options - fails every login.
func TestShellOffersTheMachineTheUsersIdentityNotTheSessionKey(t *testing.T) {
	k := &keypair{privatePath: "/tmp/session-key"}
	args := shellArgs(k, "ocid1.bastionsession.x", "us-ashburn-1", "ubuntu", "10.0.0.5", "", nil)

	outer := outerHop(args)
	for _, a := range outer {
		if a == "/tmp/session-key" {
			t.Fatalf("the machine was offered the bastion's session key: %v", outer)
		}
		if a == "IdentitiesOnly=yes" {
			t.Fatalf("with no --key the outer hop must let ssh use the agent and ~/.ssh: %v", outer)
		}
	}
	// The inner hop, inside ProxyCommand, is the one that must be strict.
	proxy := proxyCommand(args)
	if !strings.Contains(proxy, "/tmp/session-key") || !strings.Contains(proxy, "IdentitiesOnly=yes") {
		t.Errorf("the bastion hop must offer only the session key: %q", proxy)
	}
	if args[len(args)-1] != "ubuntu@10.0.0.5" {
		t.Errorf("destination = %q", args[len(args)-1])
	}
}

// A key named with --key is the one to offer the machine, and the only one.
func TestShellUsesAnExplicitKeyForTheMachine(t *testing.T) {
	k := &keypair{privatePath: "/tmp/session-key"}
	args := shellArgs(k, "sid", "us-ashburn-1", "opc", "10.0.0.5", "/home/me/.ssh/prod", []string{"uptime"})
	outer := outerHop(args)
	joined := strings.Join(outer, " ")
	if !strings.Contains(joined, "-i /home/me/.ssh/prod") || !strings.Contains(joined, "IdentitiesOnly=yes") {
		t.Errorf("the explicit key should be the only one offered: %v", outer)
	}
	if args[len(args)-1] != "uptime" {
		t.Errorf("a command should follow the destination, got %v", args)
	}
}

// outerHop is the argument list with the ProxyCommand value removed, which
// is everything the outer ssh itself is told.
func outerHop(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "ProxyCommand=") {
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func proxyCommand(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "ProxyCommand=") {
			return a
		}
	}
	return ""
}

// A bastion refusing the connection is that bastion's failure, not the
// machine's, so the next bastion must be tried. It used to come back as a plain
// error, which stopped the fall-through - and whether the other bastion was
// ever tried depended on which one happened to be first.
func TestBastionFailuresLetTheNextBastionBeTried(t *testing.T) {
	for _, err := range []error{
		&bastionFailure{kind: refusedClient, detail: "Connection closed by remote host"},
		&bastionFailure{kind: unreachedTarget},
		fmt.Errorf("wrapped: %w", &bastionFailure{kind: refusedClient}),
	} {
		if !worthAnotherBastion(err) {
			t.Errorf("%v should let the next bastion be tried", err)
		}
	}
}

// ssh reports both kinds of failure as a bare disconnect. The message has to
// say which resource to change, because the two fixes are on different ones.
func TestBastionFailureNamesTheResourceToChange(t *testing.T) {
	refused := (&bastionFailure{bastion: "app", kind: refusedClient, allow: "10.20.0.0/23"}).reason()
	if !strings.Contains(refused, "admits only 10.20.0.0/23") {
		t.Errorf("an allowlist refusal should quote the allowlist: %q", refused)
	}
	unreached := (&bastionFailure{bastion: "ph", kind: unreachedTarget, target: "10.0.0.5:22"}).reason()
	if !strings.Contains(unreached, "10.0.0.5:22") || !strings.Contains(unreached, "egress") {
		t.Errorf("an unreached target should name it and point at egress: %q", unreached)
	}
	// An open allowlist is not the cause and must not be blamed.
	open := (&bastionFailure{bastion: "x", kind: refusedClient, allow: "0.0.0.0/0", detail: "boom"}).reason()
	if strings.Contains(open, "admits only") {
		t.Errorf("a bastion open to every address should not blame its allowlist: %q", open)
	}
}

// ssh's 255 is its own "could not connect", which after a successful rehearsal
// means the bastion could not reach the machine. Any other status came from
// the remote command and must pass through for a script to branch on.
func TestSSHExitStatuses(t *testing.T) {
	var bf *bastionFailure
	if err := sshExitError("", 255); !errors.As(err, &bf) || bf.kind != unreachedTarget {
		t.Errorf("255 should be an unreached target, got %v", err)
	}
	if code := exitcode.From(sshExitError("", 3)); code != exitcode.Code(3) {
		t.Errorf("a remote exit status should pass through, got %v", code)
	}
}

// The verdict lands on the port-forwarding route, and the node's own summary
// is derived from it.
func TestClassifyRecordsTheForwardingRoute(t *testing.T) {
	n := inventory.Node{ID: "ocid1.instance.x", Account: "DEFAULT", Region: "us-ashburn-1", State: "RUNNING"}
	classify(&n, true, "app-vcn")
	if len(n.Routes) != 1 {
		t.Fatalf("routes = %+v", n.Routes)
	}
	r := n.Routes[0]
	if r.Kind != inventory.RouteOCIForward || !r.Ready || r.Target != n.ID || r.Region != "us-ashburn-1" {
		t.Errorf("route = %+v", r)
	}
	if !n.Reachable || n.Via != "bastion-fwd" {
		t.Errorf("summary: reachable %v via %q", n.Reachable, n.Via)
	}

	off := inventory.Node{ID: "ocid1.instance.y", State: "STOPPED"}
	classify(&off, true, "app-vcn")
	if !off.Down || off.Reachable || off.Status != inventory.StatusStopped {
		t.Errorf("stopped: down %v reachable %v status %q", off.Down, off.Reachable, off.Status)
	}
}
