package aws

import (
	"errors"
	"strings"
	"testing"
)

func TestSetupHintNamesConcreteCommands(t *testing.T) {
	h := SetupHint()
	for _, want := range []string{"aws configure sso", "aws configure", "ssmate again"} {
		if !strings.Contains(h, want) {
			t.Errorf("setup hint is missing %q:\n%s", want, h)
		}
	}
	// It must not be a wall of prose ending in a link. The people who see
	// this message are the ones who have never configured the AWS CLI.
	if strings.Count(h, "\n") < 3 {
		t.Error("setup hint should be a short numbered list, not one line")
	}
}

func TestSSOLoginCommandCarriesTheProfile(t *testing.T) {
	if got := ssoLogin("prod"); got != "aws sso login --profile prod" {
		t.Errorf("ssoLogin(prod) = %q", got)
	}
	if got := ssoLogin(""); got != "aws sso login" {
		t.Errorf("ssoLogin(empty) = %q", got)
	}
}

func TestCredentialProblemDistinguishesExpiryFromAbsence(t *testing.T) {
	cases := []struct{ err, want string }{
		{"token has expired and refresh failed", "have expired"},
		{"the SSO session associated with this profile has expired", "expired"},
		{"no valid providers in chain", "not usable"},
	}
	for _, c := range cases {
		got := credentialProblem("prod", errors.New(c.err))
		if !strings.Contains(got, c.want) {
			t.Errorf("credentialProblem(%q) = %q, want it to mention %q", c.err, got, c.want)
		}
		if !strings.Contains(got, "prod") {
			t.Errorf("credentialProblem should name the profile: %q", got)
		}
	}
}

func TestAWSCLIInstallIsACommandNotALink(t *testing.T) {
	got := awsCLIInstall()
	if strings.HasPrefix(strings.TrimSpace(got), "http") {
		t.Errorf("install hint is a bare link, not a command: %q", got)
	}
}
