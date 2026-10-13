package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// Request is everything needed to open one session.
type Request struct {
	Target       string
	Region       string
	Profile      string
	DocumentName string
	Parameters   map[string][]string
	Reason       string
}

// StartSessionAPI is the one SSM call we need, narrow so it can be faked.
type StartSessionAPI interface {
	StartSession(context.Context, *ssm.StartSessionInput, ...func(*ssm.Options)) (*ssm.StartSessionOutput, error)
}

// ShellRequest builds a request for an interactive shell.
//
// By default it asks for a bash login shell rather than accepting Session
// Manager's default of plain sh. The default gives no prompt, no history and
// no tab completion, which is the single most-upvoted complaint against the
// SSM agent and a fix no other wrapper ships. Pass raw to opt out.
func ShellRequest(target string, raw bool) Request {
	if raw {
		return Request{Target: target}
	}
	return Request{
		Target:       target,
		DocumentName: "AWS-StartInteractiveCommand",
		Parameters:   map[string][]string{"command": {"bash -l"}},
	}
}

// CommandRequest builds a request that runs one command instead of a shell.
//
// It reuses the document the interactive shell already uses, so a command gets
// a terminal and behaves the way someone typing it would expect - sudo can
// prompt, and a program that checks for a tty finds one.
//
// What it cannot do is bring the remote exit status home. Session Manager does
// not carry one, so a caller that needs to branch on success has to read the
// output. Callers must say so rather than reporting a zero that means nothing.
func CommandRequest(target string, command []string) Request {
	return Request{
		Target:       target,
		DocumentName: "AWS-StartInteractiveCommand",
		Parameters:   map[string][]string{"command": {strings.Join(command, " ")}},
	}
}

// BuildPluginArgs assembles the argv and environment for the plugin.
//
// The contract comes from the AWS CLI's own implementation:
//
//	session-manager-plugin <response|ENV_NAME> <region> StartSession \
//	                       <profile> <parameters-json> <endpoint>
//
// On plugins new enough to support it, argv[0] is the NAME of an environment
// variable holding the response, not the response itself. That keeps the
// session token out of the process table, where any other user on the machine
// could read it with ps. We always prefer that path.
func BuildPluginArgs(plugin *PluginInfo, resp *ssm.StartSessionOutput, req Request, endpoint string) ([]string, []string, error) {
	respJSON, err := json.Marshal(map[string]string{
		"SessionId":  aws.ToString(resp.SessionId),
		"TokenValue": aws.ToString(resp.TokenValue),
		"StreamUrl":  aws.ToString(resp.StreamUrl),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("encode session response: %w", err)
	}

	params := map[string]any{"Target": req.Target}
	if req.DocumentName != "" {
		params["DocumentName"] = req.DocumentName
	}
	if len(req.Parameters) > 0 {
		params["Parameters"] = req.Parameters
	}
	paramJSON, err := json.Marshal(params)
	if err != nil {
		return nil, nil, fmt.Errorf("encode session parameters: %w", err)
	}

	first := string(respJSON)
	var env []string
	if plugin.SupportsEnvToken {
		first = EnvTokenVar
		env = append(env, EnvTokenVar+"="+string(respJSON))
	}

	return []string{
		first,
		req.Region,
		"StartSession",
		req.Profile,
		string(paramJSON),
		endpoint,
	}, env, nil
}

// Run opens a session and replaces this process's terminal with the plugin's.
//
// Signals that belong to the remote shell are ignored locally while the
// plugin runs, so Ctrl-C reaches the remote process rather than killing the
// tunnel out from under it. The AWS CLI does the same.
func Run(ctx context.Context, api StartSessionAPI, plugin *PluginInfo, req Request, endpoint string) error {
	in := &ssm.StartSessionInput{Target: aws.String(req.Target)}
	if req.DocumentName != "" {
		in.DocumentName = aws.String(req.DocumentName)
	}
	if len(req.Parameters) > 0 {
		in.Parameters = req.Parameters
	}
	if req.Reason != "" {
		in.Reason = aws.String(req.Reason)
	}

	resp, err := api.StartSession(ctx, in)
	if err != nil {
		return classifyStartError(err, req.Target)
	}

	args, extraEnv, err := BuildPluginArgs(plugin, resp, req, endpoint)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, plugin.Path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), extraEnv...)

	stop := IgnoreTerminalSignals()
	defer stop()

	if err := cmd.Run(); err != nil {
		return exitcode.New(exitcode.Err, "session ended abnormally", "", err)
	}
	return nil
}

// classifyStartError turns an AWS error into one carrying the right exit code
// and a remedy the user can act on.
func classifyStartError(err error, target string) error {
	msg := err.Error()
	switch {
	case containsAny(msg, "TargetNotConnected"):
		return exitcode.New(
			exitcode.NotConnected,
			fmt.Sprintf("%s is not reachable through SSM", target),
			"ssmate doctor "+target,
			err,
		)
	case containsAny(msg, "AccessDenied", "not authorized"):
		return exitcode.New(
			exitcode.Denied,
			fmt.Sprintf("not allowed to start a session on %s", target),
			"ssmate doctor "+target,
			err,
		)
	case containsAny(msg, "ExpiredToken", "expired"):
		return exitcode.New(
			exitcode.Creds,
			"credentials have expired",
			"aws sso login",
			err,
		)
	default:
		return exitcode.New(exitcode.Err, "could not start a session on "+target, "", err)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) && contains(s, sub) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
