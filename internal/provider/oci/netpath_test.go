package oci

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

func allowTCP(egress bool, peer string, dst *portRange, stateless bool) rule {
	return rule{egress: egress, peer: peer, protocol: "6", dst: dst, stateless: stateless}
}

func TestAdmitsInternet(t *testing.T) {
	cases := map[string]struct {
		allow []string
		want  verdict
	}{
		"open to all":        {[]string{"0.0.0.0/0"}, open},
		"an office address":  {[]string{"203.0.113.0/24"}, open},
		"only private":       {[]string{"10.20.0.0/23", "192.168.0.0/16"}, closed},
		"private and public": {[]string{"10.20.0.0/23", "198.51.100.7/32"}, open},
		"carrier nat":        {[]string{"100.64.0.0/10"}, closed},
		"nothing declared":   {nil, unknown},
		"unreadable":         {[]string{"not-a-cidr"}, unknown},
		"wider than private": {[]string{"172.0.0.0/8"}, open},
	}
	for name, c := range cases {
		if got := admitsInternet(c.allow); got != c.want {
			t.Errorf("%s: got %v want %v", name, got, c.want)
		}
	}
}

func TestPathTo(t *testing.T) {
	const bastion, target = "10.0.4.53", "10.0.2.15"
	everything := []rule{{egress: true, peer: "0.0.0.0/0", protocol: "all"}, {peer: "0.0.0.0/0", protocol: "all"}}
	cases := []struct {
		name     string
		from, to []rule
		want     verdict
		why      string
	}{
		{"wide open", everything, everything, open, ""},
		{"bastion may only reach its own subnet",
			[]rule{allowTCP(true, "10.0.4.0/26", &portRange{22, 22}, false)}, everything, closed, "nothing out to 10.0.2.15"},
		{"egress allowed but not on port 22",
			[]rule{allowTCP(true, "0.0.0.0/0", &portRange{443, 443}, false)}, everything, closed, "nothing out"},
		{"machine admits nothing from the bastion",
			everything, []rule{allowTCP(false, "10.9.0.0/16", nil, false)}, closed, "admits nothing from the bastion"},
		{"stateless egress needs a rule for the replies",
			[]rule{allowTCP(true, "10.0.2.0/24", nil, true)}, everything, closed, "replies from 10.0.2.15"},
		{"stateless egress with replies allowed",
			[]rule{allowTCP(true, "10.0.2.0/24", nil, true), allowTCP(false, "10.0.2.0/24", nil, true)}, everything, open, ""},
		{"a rule naming a security group cannot be judged",
			everything, []rule{{protocol: "all"}}, unknown, ""},
		{"icmp only",
			[]rule{{egress: true, peer: "0.0.0.0/0", protocol: "1"}}, everything, closed, "nothing out"},
	}
	for _, c := range cases {
		got, why := pathTo(c.from, bastion, c.to, target)
		if got != c.want || (c.why != "" && !strings.Contains(why, c.why)) {
			t.Errorf("%s: got %v %q", c.name, got, why)
		}
	}
}

func TestJudgeOrdersDoorsAndExplains(t *testing.T) {
	everything := []rule{{egress: true, peer: "0.0.0.0/0", protocol: "all"}, {peer: "0.0.0.0/0", protocol: "all"}}
	w := network{
		subnetLists: map[string][]string{"app": {"sl-app"}, "edge": {"sl-edge"}, "dark": {"sl-missing"}},
		lists: map[string][]rule{
			"sl-app":  everything,
			"sl-edge": {allowTCP(true, "10.0.4.0/26", &portRange{22, 22}, false)},
		},
	}
	gws := []bastionSummary{
		{name: "edge", allow: []string{"0.0.0.0/0"}, subnet: "edge", endpoint: "10.0.4.53"},
		{name: "inside", allow: []string{"10.0.2.0/23"}, subnet: "app", endpoint: "10.0.2.123"},
		{name: "unread", allow: []string{"0.0.0.0/0"}, subnet: "dark", endpoint: "10.0.6.9"},
		{name: "good", allow: []string{"0.0.0.0/0"}, subnet: "app", endpoint: "10.0.2.124"},
	}
	doors := judge(w, gws, addressing{privateIP: "10.0.2.15", subnetID: "app"})
	var order []string
	for _, d := range doors {
		order = append(order, d.g.name)
	}
	if strings.Join(order, ",") != "good,unread,edge,inside" {
		t.Errorf("order = %v", order)
	}
	if v, _ := reachOf(doors); v != "open" {
		t.Errorf("reach = %q", v)
	}
	v, why := reachOf(doors[2:])
	if v != reachBlocked || !strings.Contains(why, "edge cannot reach the machine") ||
		!strings.Contains(why, "inside admits only 10.0.2.0/23") {
		t.Errorf("blocked: %q %q", v, why)
	}
	if v, _ := reachOf(doors[1:2]); v != "unverified" {
		t.Errorf("unread rules should be unverified, got %q", v)
	}
}

func TestMachineRulesIncludeSecurityGroups(t *testing.T) {
	w := network{
		subnetLists: map[string][]string{"analytics": {"sl"}},
		lists:       map[string][]rule{"sl": {{egress: true, peer: "0.0.0.0/0", protocol: "all"}}},
		nsgs:        map[string][]rule{"nsg-1": {allowTCP(false, "10.0.4.0/26", &portRange{22, 22}, false)}},
	}
	a := addressing{privateIP: "10.0.4.10", subnetID: "analytics", nsgIDs: []string{"nsg-1"}}
	if v, why := pathTo(w.subnetRules("analytics"), "10.0.4.53", w.machineRules(a), a.privateIP); v != open {
		t.Errorf("the group admits the bastion: got %v %q", v, why)
	}
	a.nsgIDs = append(a.nsgIDs, "nsg-unread")
	if got := len(w.machineRules(a)); got < 3 {
		t.Errorf("an unread group should add unevaluable rules, got %d rules", got)
	}
}

func TestBlockedRoutes(t *testing.T) {
	n := running(true)
	n.Detail["reach"], n.Detail["reach_why"] = reachBlocked, "edge cannot reach the machine: nope"
	blockIfNoDoor(&n)
	classifyManaged(&n, "RUNNING")
	if n.Reachable || n.Status != inventory.StatusBlocked || n.Hint != "edge cannot reach the machine: nope" {
		t.Errorf("summary: %v %q %q", n.Reachable, n.Status, n.Hint)
	}
	for _, r := range n.Routes {
		if r.Ready || r.Status != inventory.StatusBlocked {
			t.Errorf("route %s: %+v", r.Kind, r)
		}
	}
}

func TestArchOf(t *testing.T) {
	for shape, want := range map[string]string{
		"VM.Standard.A1.Flex": "arm64", "BM.Standard.A1.160": "arm64", "VM.Standard.A2.Flex": "arm64",
		"VM.Standard.E5.Flex": "amd64", "VM.GPU.A10.1": "amd64", "VM.Standard3.Flex": "amd64",
	} {
		if got := archOf(shape); got != want {
			t.Errorf("%s: %s", shape, got)
		}
	}
}

func TestReadGreeting(t *testing.T) {
	serve := func(reply string) int {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				fmt.Fprint(c, reply)
				c.Close()
			}
		}()
		return l.Addr().(*net.TCPAddr).Port
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if got, err := readGreeting(ctx, serve("SSH-2.0-OpenSSH_9.6\r\n")); err != nil || got != "SSH-2.0-OpenSSH_9.6" {
		t.Errorf("greeting: %q %v", got, err)
	}
	short, cancel2 := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel2()
	if _, err := readGreeting(short, serve("")); err == nil {
		t.Error("a tunnel that drops every connection should fail")
	}
}
