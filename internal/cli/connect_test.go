package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/provider"
)

// With no credentials anywhere, the user needs to know what every cloud
// wanted - not just the first one that failed. The exit code has to say
// credentials, because that is what a wrapper script branches on.
func TestNoCloudsErrorListsEveryCloud(t *testing.T) {
	err := noCloudsError([]provider.Unavailable{
		{Cloud: "aws", Reason: "no credentials; run aws sso login"},
		{Cloud: "oci", Reason: "no ~/.oci/config"},
	})
	var ex *exitcode.Error
	if !errors.As(err, &ex) {
		t.Fatalf("want an *exitcode.Error, got %T", err)
	}
	msg := ex.Error() + "\n" + ex.Hint
	for _, want := range []string{"aws", "aws sso login", "oci", "~/.oci/config"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message should mention %q, got %q", want, msg)
		}
	}
	if code := exitcode.From(err); code != exitcode.Creds {
		t.Errorf("got exit code %v, want %v (credentials)", code, exitcode.Creds)
	}
}
