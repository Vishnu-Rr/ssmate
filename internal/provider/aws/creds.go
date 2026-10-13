// Package aws reaches machines through Amazon's Systems Manager.
//
// Nothing above internal/provider may import this package. Everything that is
// specific to Amazon lives here: the credential chain, the two-call inventory
// join, the session plugin, and the checks that only make sense for Systems
// Manager.
package aws

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Vishnu-Rr/ssmate/internal/awsconf"
	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/session"
	"github.com/aws/aws-sdk-go-v2/aws"
)

// Preflight verifies that what a command needs is actually present.
//
// It prints NOTHING when everything works. A tool that reports its own good
// health on every invocation teaches people to skim past its output, so the
// one time it has something to say, they miss it. Silence is the success
// signal; the only output is a problem and its fix.
//
// Nothing here is a nag that can be disabled, because nothing here fires
// unless the command genuinely cannot proceed.

// Credentials resolves credentials for one profile and region, or explains
// how to get some.
func Credentials(ctx context.Context, profile, region string) (aws.Config, error) {
	if !awsConfigured() {
		return aws.Config{}, exitcode.New(exitcode.Creds, "no AWS credentials found", SetupHint(), nil)
	}

	cfg, err := awsconf.Load(ctx, profile, region)
	if err != nil {
		return aws.Config{}, err
	}

	// Resolving is not the same as being valid. An expired IAM Identity
	// Center token loads fine and fails on first use, so force it here where
	// we can name the fix instead of surfacing an opaque API error later.
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		return aws.Config{}, exitcode.New(
			exitcode.Creds,
			credentialProblem(profile, err),
			loginCommand(profile),
			err,
		)
	}
	return cfg, nil
}

// Plugin checks for AWS's session-manager-plugin, which is the only
// hard dependency beyond credentials. Sessions are handed to it.
func Plugin() (*session.PluginInfo, error) {
	info, err := session.FindPlugin()
	if err != nil {
		// FindPlugin already distinguishes "absent" from "wrong CPU" and
		// carries the right install command for this platform.
		return nil, err
	}
	return info, nil
}

// awsConfigured reports whether there is any credential source at all.
func awsConfigured() bool {
	for _, k := range []string{
		"AWS_ACCESS_KEY_ID", "AWS_PROFILE", "AWS_ROLE_ARN",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_WEB_IDENTITY_TOKEN_FILE",
	} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	cfgPath, credPath := awsconf.DefaultPaths()
	for _, p := range []string{cfgPath, credPath} {
		if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
			return true
		}
	}
	// An EC2 instance can have a role with no files and no variables.
	if _, err := os.Stat(filepath.FromSlash("/sys/hypervisor/uuid")); err == nil {
		return true
	}
	return false
}

// credentialProblem turns an SDK error into one sentence a person can act on.
func credentialProblem(profile string, err error) string {
	who := "credentials"
	if profile != "" {
		who = "credentials for profile " + profile
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "expired") || strings.Contains(msg, "refresh failed"):
		return who + " have expired"
	case strings.Contains(msg, "sso session") || strings.Contains(msg, "sso"):
		return who + " need a fresh single sign-on session"
	case strings.Contains(msg, "no ec2 imds") || strings.Contains(msg, "failed to refresh"):
		return who + " could not be resolved"
	default:
		return who + " are not usable"
	}
}

// loginCommand is the command that fixes an expired or missing session.
func loginCommand(profile string) string {
	if !hasAWSCLI() {
		return "Install the AWS CLI to refresh your session:\n" +
			"    " + awsCLIInstall() + "\n" +
			"  then run:\n" +
			"    " + ssoLogin(profile)
	}
	return ssoLogin(profile)
}

func ssoLogin(profile string) string {
	if profile == "" {
		return "aws sso login"
	}
	return "aws sso login --profile " + profile
}

// setupHint is the first-run message for someone with no AWS setup at all.
// It assumes no prior knowledge, because the people this tool is for are
// precisely the ones who have never configured the AWS CLI.
func SetupHint() string {
	var b strings.Builder
	b.WriteString("ssmate uses the same credentials as the AWS CLI, so set those up once.\n")
	if !hasAWSCLI() {
		b.WriteString("\n  1. Install the AWS CLI:\n       " + awsCLIInstall() + "\n")
		b.WriteString("\n  2. Sign in. Most companies use single sign-on:\n")
	} else {
		b.WriteString("\n  Sign in. Most companies use single sign-on:\n")
	}
	b.WriteString("       aws configure sso\n")
	b.WriteString("\n     If you were given an access key instead:\n")
	b.WriteString("       aws configure\n")
	// If the plugin is missing too, say so now. Sending someone away to
	// install one thing, only to stop them again on the next run for a
	// second thing, is a bad first impression that costs nothing to avoid.
	if _, err := session.FindPlugin(); err != nil {
		b.WriteString("\n\n  You will also need AWS's session manager plugin, which ssmate\n")
		b.WriteString("  connects through. Install it at the same time:\n")
		b.WriteString("       " + session.InstallHint(runtime.GOOS, runtime.GOARCH) + "\n")
	}
	b.WriteString("\n  Then run ssmate again. It will pick the credentials up automatically.")
	return b.String()
}

func hasAWSCLI() bool {
	_, err := exec.LookPath("aws")
	return err == nil
}

func awsCLIInstall() string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install awscli"
	case "linux":
		return "sudo snap install aws-cli --classic    # or see https://aws.amazon.com/cli/"
	case "windows":
		return "winget install Amazon.AWSCLI"
	default:
		return "see https://aws.amazon.com/cli/"
	}
}
