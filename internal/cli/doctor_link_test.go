package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/doctor"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

// A link made on address and hostname is shown as a judgement, with the one
// command that makes it certain; a tagged link is simply reported.
func TestLinkedCheck(t *testing.T) {
	n := inventory.Node{ID: "ocid1.instance.a", Name: "build-02"}
	rec := inventory.Node{ID: "mi-0abc", Account: "prod", Region: "eu-west-1"}

	guessed := linkedCheck(n, inventory.Route{Kind: inventory.RouteSSM, Link: inventory.LinkAddress}, rec).Run(context.Background())
	if guessed.Severity != doctor.Warn ||
		!strings.Contains(guessed.Remedy, "add-tags-to-resource --resource-type ManagedInstance --resource-id mi-0abc") ||
		!strings.Contains(guessed.Remedy, "Key=ssmate:source,Value=ocid1.instance.a") ||
		!strings.Contains(guessed.Remedy, "--region eu-west-1 --profile prod") {
		t.Errorf("address link: %+v", guessed)
	}

	tagged := linkedCheck(n, inventory.Route{Kind: inventory.RouteSSM, Link: inventory.LinkTag}, rec).Run(context.Background())
	if tagged.Severity != doctor.Pass || tagged.Remedy != "" || !strings.Contains(tagged.Detail, "via ssm, as mi-0abc") {
		t.Errorf("tag link: %+v", tagged)
	}
}
