package doctor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

// Note: this file's tests cover both the pure target checks and PluginCheck,
// which touches the real filesystem and therefore skips when the plugin is
// present and healthy.

func runAll(t *testing.T, reg Registry) map[string]Result {
	t.Helper()
	out := map[string]Result{}
	for _, r := range reg.RunAll(context.Background()) {
		out[r.Name] = r
	}
	return out
}

func TestTargetChecksReportNotFoundForNilNode(t *testing.T) {
	got := runAll(t, TargetChecks(nil, "web-prod-9"))
	r, ok := got["target exists"]
	if !ok {
		t.Fatalf("no 'target exists' check ran; got %v", keys(got))
	}
	if r.Severity != Fail {
		t.Errorf("severity = %v, want Fail", r.Severity)
	}
	if !strings.Contains(r.Detail, "web-prod-9") {
		t.Errorf("detail %q should name the query", r.Detail)
	}
	if r.Remedy == "" {
		t.Error("a not-found target must suggest something, such as refreshing the cache")
	}
}

func TestTargetChecksFailStoppedInstanceWithStartCommand(t *testing.T) {
	node := &inventory.Node{ID: "i-0abc", Region: "us-west-2", Account: "prod", State: "stopped"}
	got := runAll(t, TargetChecks(node, "i-0abc"))
	r := got["instance running"]
	if r.Severity != Fail {
		t.Fatalf("stopped instance severity = %v, want Fail", r.Severity)
	}
	if !strings.Contains(r.Remedy, "start-instances") || !strings.Contains(r.Remedy, "i-0abc") {
		t.Errorf("remedy %q should be a runnable start-instances command naming the instance", r.Remedy)
	}
}

func TestTargetChecksDistinguishUnmanagedFromStopped(t *testing.T) {
	// The whole reason the EC2 join exists. A running box absent from SSM
	// has a completely different fix from a stopped one.
	running := &inventory.Node{ID: "i-0abc", State: "running"}
	got := runAll(t, TargetChecks(running, "i-0abc"))

	if got["instance running"].Severity != Pass {
		t.Error("a running instance should pass the running check")
	}
	r := got["managed by ssm"]
	if r.Severity != Fail {
		t.Fatalf("unmanaged severity = %v, want Fail", r.Severity)
	}
	if !strings.Contains(strings.ToLower(r.Detail+r.Remedy), "role") {
		t.Errorf("an unmanaged instance with no instance profile should mention the role: %q / %q", r.Detail, r.Remedy)
	}
}

func TestTargetChecksFlagLostAgent(t *testing.T) {
	node := &inventory.Node{
		ID: "i-0abc", State: "running",
		Agent:  inventory.Agent{Present: true, LastSeen: time.Now().Add(-72 * time.Hour)},
		Detail: map[string]string{"ping": "ConnectionLost"},
	}
	got := runAll(t, TargetChecks(node, "i-0abc"))
	if got["agent online"].Severity != Fail {
		t.Fatalf("ConnectionLost should fail the agent check")
	}
}

func TestTargetChecksPassCleanNode(t *testing.T) {
	node := &inventory.Node{
		ID: "i-0abc", State: "running",
		Agent: inventory.Agent{
			Present: true, Online: true, Version: "3.3.1", Latest: true,
			LastSeen: time.Now().Add(-time.Minute),
		},
		Detail: map[string]string{"has_role": "true", "ping": "Online"},
	}
	for name, r := range runAll(t, TargetChecks(node, "i-0abc")) {
		if r.Severity == Fail {
			t.Errorf("healthy node failed check %q: %s", name, r.Detail)
		}
	}
}

func TestTargetChecksWarnOnOutdatedAgent(t *testing.T) {
	node := &inventory.Node{
		ID: "i-0abc", State: "running",
		Agent: inventory.Agent{
			Present: true, Online: true, Version: "2.0.0", LastSeen: time.Now(),
		},
		Detail: map[string]string{"has_role": "true", "ping": "Online"},
	}
	got := runAll(t, TargetChecks(node, "i-0abc"))
	if got["agent up to date"].Severity != Warn {
		t.Fatalf("an outdated agent should warn, not fail: %v", got["agent up to date"].Severity)
	}
}

func TestEveryFailingCheckCarriesARemedy(t *testing.T) {
	// The design principle: every failure names its cause and prints a fix.
	// This test is the enforcement.
	nodes := []*inventory.Node{
		nil,
		{ID: "i-1", State: "stopped"},
		{ID: "i-2", State: "running"},
		{ID: "i-3", State: "running", Agent: inventory.Agent{Present: true}},
	}
	for _, n := range nodes {
		for name, r := range runAll(t, TargetChecks(n, "q")) {
			if r.Severity == Fail && strings.TrimSpace(r.Remedy) == "" {
				t.Errorf("check %q fails without offering a remedy", name)
			}
		}
	}
}

func TestPluginCheckRemedyIsACommandNotALink(t *testing.T) {
	// This test exists because the remedy here was once built by calling
	// InstallHint with empty platform strings, which silently produced a bare
	// documentation URL. It compiled, vetted and formatted cleanly, so only a
	// test could catch it.
	r := PluginCheck().Run(context.Background())
	if r.Severity == Pass {
		t.Skip("plugin is installed and current on this machine")
	}
	if strings.HasPrefix(strings.TrimSpace(r.Remedy), "http") {
		t.Errorf("remedy %q is a bare documentation link, not a runnable command", r.Remedy)
	}
}

func keys(m map[string]Result) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
