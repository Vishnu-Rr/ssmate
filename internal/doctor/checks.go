package doctor

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
	"github.com/Vishnu-Rr/ssmate/internal/session"
)

// LocalChecks are the checks that need nothing but this machine.
//
// They run first, always. The AWS CLI checks a permissions API before it
// checks whether the plugin is installed, so a user missing the plugin is
// told they lack permission. Local first means we never misattribute a local
// problem to the cloud.
func LocalChecks() Registry {
	return Registry{PluginCheck(), CredentialsCheck(nil)}
}

// firstLine keeps a multi-line error readable on the detail line; the full
// guidance lives in the remedy beneath it.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// CredentialsResolver reports whether AWS credentials are usable, and why
// not when they are not. It is injected so the check is testable without
// touching a real credential chain.
type CredentialsResolver func(context.Context) (detail string, err error)

// CredentialsCheck verifies that AWS credentials resolve. Pass nil to use the
// real chain.
func CredentialsCheck(resolve CredentialsResolver) Check {
	return Check{
		Name: "aws credentials",
		Run: func(ctx context.Context) Result {
			const name = "aws credentials"
			if resolve == nil {
				return Result{Name: name, Severity: Warn,
					Detail: "not checked"}
			}
			detail, err := resolve(ctx)
			if err != nil {
				// Use the hint the error carries. Hardcoding a login command
				// here would tell someone who has never configured AWS to
				// refresh a session that does not exist.
				remedy := "aws sso login"
				var ec *exitcode.Error
				if errors.As(err, &ec) && strings.TrimSpace(ec.Hint) != "" {
					remedy = ec.Hint
				}
				return Result{
					Name: name, Severity: Fail,
					Detail: firstLine(err.Error()),
					Remedy: remedy,
				}
			}
			return Result{Name: name, Severity: Pass, Detail: detail}
		},
	}
}

// PluginCheck verifies session-manager-plugin is present, runnable on this
// CPU, and new enough for AWS to still support it.
func PluginCheck() Check {
	return Check{
		Name: "session manager plugin",
		Run: func(ctx context.Context) Result {
			info, err := session.FindPlugin()
			if err != nil {
				// FindPlugin attaches a situation-specific hint, such as the
				// wrong-CPU wording, which Error() does not carry. Prefer it,
				// and fall back to the plain platform hint.
				//
				// Note the arguments: passing empty strings here would fall
				// through InstallHint's switch to its default branch and
				// produce a bare documentation URL, which is exactly what
				// this tool refuses to do and what Task 9's own test forbids.
				remedy := session.InstallHint(runtime.GOOS, runtime.GOARCH)
				var ec *exitcode.Error
				if errors.As(err, &ec) && strings.TrimSpace(ec.Hint) != "" {
					remedy = ec.Hint
				}
				return Result{
					Name:     "session manager plugin",
					Severity: Fail,
					Detail:   err.Error(),
					Remedy:   remedy,
				}
			}
			if info.TooOld {
				return Result{
					Name:     "session manager plugin",
					Severity: Warn,
					Detail: fmt.Sprintf("version %s is below AWS's supported minimum of %s",
						info.Version, session.MinimumVersion),
					Remedy: session.InstallHint(runtime.GOOS, runtime.GOARCH),
				}
			}
			return Result{
				Name:     "session manager plugin",
				Severity: Pass,
				Detail:   info.Version,
			}
		},
	}
}

// TargetChecks diagnose one target from data already fetched.
//
// They take a Node rather than API clients so that every diagnosis is a pure
// function of inventory we already hold: no extra calls, no extra latency,
// and exhaustively testable. A nil node means the target was never found,
// which is itself the first diagnosis.
func TargetChecks(node *inventory.Node, query string) Registry {
	return Registry{
		existsCheck(node, query),
		runningCheck(node),
		managedCheck(node),
		agentOnlineCheck(node),
		agentVersionCheck(node),
	}
}

func existsCheck(node *inventory.Node, query string) Check {
	return Check{
		Name: "target exists",
		Run: func(context.Context) Result {
			if node == nil {
				return Result{
					Name:     "target exists",
					Severity: Fail,
					Detail:   fmt.Sprintf("nothing matching %q in any known profile or region", query),
					Remedy:   "ssmate ls --refresh   # rebuild the inventory cache",
				}
			}
			return Result{
				Name:     "target exists",
				Severity: Pass,
				Detail:   fmt.Sprintf("%s in %s", node.ID, node.Where()),
			}
		},
	}
}

func runningCheck(node *inventory.Node) Check {
	return Check{
		Name: "instance running",
		Run: func(context.Context) Result {
			const name = "instance running"
			if node == nil {
				return Result{Name: name, Severity: Warn, Detail: "no target to check"}
			}
			switch node.State {
			case "running":
				return Result{Name: name, Severity: Pass, Detail: "running"}
			case "":
				// Hybrid nodes have no EC2 state and that is normal.
				return Result{Name: name, Severity: Pass, Detail: "not an EC2 instance"}
			case "stopped", "stopping":
				return Result{
					Name: name, Severity: Fail,
					Detail: "instance is " + node.State,
					Remedy: fmt.Sprintf("aws ec2 start-instances --instance-ids %s --region %s",
						node.ID, node.Region),
				}
			default:
				return Result{
					Name: name, Severity: Fail,
					Detail: "instance is " + node.State,
					Remedy: "launch a replacement instance; this one cannot be started",
				}
			}
		},
	}
}

func managedCheck(node *inventory.Node) Check {
	return Check{
		Name: "managed by ssm",
		Run: func(context.Context) Result {
			const name = "managed by ssm"
			if node == nil {
				return Result{Name: name, Severity: Warn, Detail: "no target to check"}
			}
			if node.Agent.Present {
				return Result{Name: name, Severity: Pass, Detail: "registered with SSM"}
			}
			if node.State != "running" {
				// Being absent from SSM is expected when it is not running,
				// so do not pile a second failure onto the same cause.
				return Result{Name: name, Severity: Warn, Detail: "not registered, because it is not running"}
			}
			if _, hasRole := node.Detailed("has_role"); !hasRole {
				return Result{
					Name: name, Severity: Fail,
					Detail: "running but not registered with SSM, and it has no instance profile",
					Remedy: fmt.Sprintf(
						"attach a role with AmazonSSMManagedInstanceCore, or enable Default Host Management:\n"+
							"    aws ec2 associate-iam-instance-profile --instance-id %s --region %s \\\n"+
							"        --iam-instance-profile Name=<your-ssm-role>",
						node.ID, node.Region),
				}
			}
			return Result{
				Name: name, Severity: Fail,
				Detail: "running with a role attached, but never registered with SSM",
				Remedy: "check the agent is installed and running, and that the subnet can reach " +
					"ssm, ssmmessages and ec2messages on 443",
			}
		},
	}
}

func agentOnlineCheck(node *inventory.Node) Check {
	return Check{
		Name: "agent online",
		Run: func(context.Context) Result {
			const name = "agent online"
			if node == nil || !node.Agent.Present {
				return Result{Name: name, Severity: Warn, Detail: "not registered with SSM"}
			}
			if node.Agent.Online {
				return Result{Name: name, Severity: Pass, Detail: "online"}
			}
			ping, _ := node.Detailed("ping")
			if ping == "" {
				ping = "unknown"
			}
			detail := "agent status is " + ping
			if !node.Agent.LastSeen.IsZero() {
				detail += fmt.Sprintf(", last seen %s ago", time.Since(node.Agent.LastSeen).Round(time.Minute))
			}
			return Result{
				Name: name, Severity: Fail, Detail: detail,
				Remedy: "restart the agent on the box, or check the subnet still reaches " +
					"ssmmessages on 443",
			}
		},
	}
}

func agentVersionCheck(node *inventory.Node) Check {
	return Check{
		Name: "agent up to date",
		Run: func(context.Context) Result {
			const name = "agent up to date"
			if node == nil || !node.Agent.Present || node.Agent.Version == "" {
				return Result{Name: name, Severity: Warn, Detail: "agent version unknown"}
			}
			if node.Agent.Latest {
				return Result{Name: name, Severity: Pass, Detail: node.Agent.Version}
			}
			return Result{
				Name: name, Severity: Warn,
				Detail: node.Agent.Version + " is not the latest",
				Remedy: "aws ssm send-command --document-name AWS-UpdateSSMAgent " +
					"--instance-ids " + node.ID + " --region " + node.Region,
			}
		},
	}
}
