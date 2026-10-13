package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

const fakeToken = "SUPER-SECRET-TOKEN-VALUE"

func fakeResponse() *ssm.StartSessionOutput {
	return &ssm.StartSessionOutput{
		SessionId:  aws.String("user-0123456789"),
		TokenValue: aws.String(fakeToken),
		StreamUrl:  aws.String("wss://ssmmessages.us-west-2.amazonaws.com/v1/data-channel/user-0123456789"),
	}
}

func req() Request {
	return Request{
		Target:       "i-0abc",
		Region:       "us-west-2",
		Profile:      "prod",
		DocumentName: "AWS-StartInteractiveCommand",
		Parameters:   map[string][]string{"command": {"bash -l"}},
	}
}

func TestBuildPluginArgsUsesDocumentedArgvOrder(t *testing.T) {
	p := &PluginInfo{Path: "/usr/local/bin/session-manager-plugin", Version: "1.2.835.0", SupportsEnvToken: true}
	args, _, err := BuildPluginArgs(p, fakeResponse(), req(), "https://ssm.us-west-2.amazonaws.com")
	if err != nil {
		t.Fatalf("BuildPluginArgs: %v", err)
	}
	if len(args) != 6 {
		t.Fatalf("got %d args, want 6: %v", len(args), args)
	}
	if args[1] != "us-west-2" {
		t.Errorf("args[1] = %q, want the region", args[1])
	}
	if args[2] != "StartSession" {
		t.Errorf("args[2] = %q, want StartSession", args[2])
	}
	if args[3] != "prod" {
		t.Errorf("args[3] = %q, want the profile", args[3])
	}
	if args[5] != "https://ssm.us-west-2.amazonaws.com" {
		t.Errorf("args[5] = %q, want the endpoint", args[5])
	}
}

func TestBuildPluginArgsKeepsTokenOutOfArgvOnModernPlugin(t *testing.T) {
	p := &PluginInfo{Path: "x", Version: "1.2.835.0", SupportsEnvToken: true}
	args, env, err := BuildPluginArgs(p, fakeResponse(), req(), "https://ssm.us-west-2.amazonaws.com")
	if err != nil {
		t.Fatalf("BuildPluginArgs: %v", err)
	}
	for i, a := range args {
		if strings.Contains(a, fakeToken) {
			t.Fatalf("session token leaked into argv[%d]: anyone running ps could read it", i)
		}
	}
	if args[0] != EnvTokenVar {
		t.Errorf("args[0] = %q, want the env var name %q", args[0], EnvTokenVar)
	}
	var found bool
	for _, kv := range env {
		if strings.HasPrefix(kv, EnvTokenVar+"=") {
			found = true
			if !strings.Contains(kv, fakeToken) {
				t.Error("env var does not carry the token")
			}
		}
	}
	if !found {
		t.Fatalf("%s not present in env", EnvTokenVar)
	}
}

func TestBuildPluginArgsFallsBackToArgvOnOldPlugin(t *testing.T) {
	p := &PluginInfo{Path: "x", Version: "1.2.400.0", SupportsEnvToken: false}
	args, env, err := BuildPluginArgs(p, fakeResponse(), req(), "https://ssm.us-west-2.amazonaws.com")
	if err != nil {
		t.Fatalf("BuildPluginArgs: %v", err)
	}
	if !strings.Contains(args[0], fakeToken) {
		t.Error("old plugin must receive the response JSON as argv[0]")
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, EnvTokenVar+"=") {
			t.Error("old plugin must not be given the env var")
		}
	}
}

func TestBuildPluginArgsEncodesParametersAsJSON(t *testing.T) {
	p := &PluginInfo{Path: "x", Version: "1.2.835.0", SupportsEnvToken: true}
	args, _, err := BuildPluginArgs(p, fakeResponse(), req(), "https://ssm.us-west-2.amazonaws.com")
	if err != nil {
		t.Fatalf("BuildPluginArgs: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(args[4]), &decoded); err != nil {
		t.Fatalf("args[4] is not valid JSON: %v", err)
	}
	if decoded["Target"] != "i-0abc" {
		t.Errorf("parameters JSON missing Target: %v", decoded)
	}
}

func TestShellRequestDefaultsToLoginBashOnLinux(t *testing.T) {
	// Session Manager's default shell is sh: no prompt, no history, no
	// completion. It is the most-upvoted complaint against the agent.
	r := ShellRequest("i-0abc", false)
	if r.DocumentName != "AWS-StartInteractiveCommand" {
		t.Fatalf("DocumentName = %q, want AWS-StartInteractiveCommand", r.DocumentName)
	}
	cmd, ok := r.Parameters["command"]
	if !ok || len(cmd) != 1 || !strings.Contains(cmd[0], "bash") {
		t.Fatalf("Parameters[command] = %v, want a bash login shell", cmd)
	}
}

func TestShellRequestRawUsesTheDefaultDocument(t *testing.T) {
	r := ShellRequest("i-0abc", true)
	if r.DocumentName != "" {
		t.Fatalf("DocumentName = %q, want empty so AWS picks the default", r.DocumentName)
	}
	if len(r.Parameters) != 0 {
		t.Fatalf("Parameters = %v, want none", r.Parameters)
	}
}

// A command runs through the same document as the interactive shell, so it
// gets a terminal: sudo can prompt and a program that checks for a tty finds
// one.
func TestCommandRequest(t *testing.T) {
	req := CommandRequest("i-123", []string{"tail", "-n", "50", "/var/log/syslog"})
	if req.DocumentName != "AWS-StartInteractiveCommand" {
		t.Errorf("document = %q", req.DocumentName)
	}
	got := req.Parameters["command"]
	if len(got) != 1 || got[0] != "tail -n 50 /var/log/syslog" {
		t.Errorf("command = %v", got)
	}
	if req.Target != "i-123" {
		t.Errorf("target = %q", req.Target)
	}
}

// An empty command must not silently become an empty shell request, since the
// caller only reaches this when a command was actually asked for.
func TestCommandRequestWithNoWordsIsStillACommandDocument(t *testing.T) {
	req := CommandRequest("i-123", nil)
	if req.DocumentName != "AWS-StartInteractiveCommand" {
		t.Errorf("document = %q", req.DocumentName)
	}
}
