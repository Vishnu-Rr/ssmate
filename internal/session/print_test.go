package session

import (
	"strings"
	"testing"
)

func TestPrintCommandRendersARunnableAWSCommand(t *testing.T) {
	got := PrintCommand(Request{
		Target:       "i-0abc",
		Region:       "us-west-2",
		Profile:      "prod",
		DocumentName: "AWS-StartInteractiveCommand",
		Parameters:   map[string][]string{"command": {"bash -l"}},
	})
	for _, want := range []string{
		"aws ssm start-session",
		"--target i-0abc",
		"--region us-west-2",
		"--profile prod",
		"--document-name AWS-StartInteractiveCommand",
		"--parameters",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PrintCommand() = %q, missing %q", got, want)
		}
	}
}

func TestPrintCommandOmitsEmptyFlags(t *testing.T) {
	got := PrintCommand(Request{Target: "i-0abc", Region: "us-west-2"})
	if strings.Contains(got, "--profile") {
		t.Errorf("empty profile should not produce a flag: %q", got)
	}
	if strings.Contains(got, "--document-name") {
		t.Errorf("empty document should not produce a flag: %q", got)
	}
}

func TestPrintCommandQuotesParametersSafely(t *testing.T) {
	got := PrintCommand(Request{
		Target:       "i-0abc",
		Region:       "us-west-2",
		DocumentName: "AWS-StartInteractiveCommand",
		Parameters:   map[string][]string{"command": {"bash -l"}},
	})
	if !strings.Contains(got, "'") {
		t.Errorf("parameters containing spaces must be quoted: %q", got)
	}
}
