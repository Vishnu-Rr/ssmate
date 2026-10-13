package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/Vishnu-Rr/ssmate/internal/doctor"
	"github.com/Vishnu-Rr/ssmate/internal/inventory"
)

// outboundGuide tells someone how to give a machine a way in that exposes
// nothing, when it has no way in that works.
//
// A blocked bastion can be fixed by opening it, and doctor says how. But the
// fix that needs no door at all is an agent on the machine that connects
// out, and the steps for that depend on facts about the machine - its
// operating system, its processor, its identifier - that are already known
// here. So they are printed filled in, for the user to run. ssmate never runs
// them: they create credentials and install software, which is not a tool's
// decision to make.
func outboundGuide(n inventory.Node) (doctor.Check, bool) {
	if n.Down || n.Reachable || n.Cloud == inventory.CloudAWS || n.HasRoute(inventory.RouteSSM) {
		return doctor.Check{}, false
	}
	name := "a way in that exposes nothing"
	return doctor.Check{
		Name: name,
		Run: func(context.Context) doctor.Result {
			return doctor.Result{
				Name: name, Severity: doctor.Warn,
				Detail: "register the machine with AWS Session Manager" + arcAlternative(n) +
					"; its agent connects out, so no bastion, address list or key is needed",
				Remedy: ssmSteps(n) + arcSteps(n),
			}
		},
	}, true
}

func arcAlternative(n inventory.Node) string {
	if n.Cloud == inventory.CloudAzure {
		return "" // Arc does not take Azure's own virtual machines.
	}
	return " or Azure Arc"
}

// ssmSteps are the commands that register one machine with Session Manager
// and tie the registration to it, in the order they are run.
func ssmSteps(n inventory.Node) string {
	family, arch := agentBuild(n)
	return strings.Join([]string{
		"AWS Session Manager. Once per AWS account, a role for the agent:",
		"    aws iam create-role --role-name SSMHybridRole --assume-role-policy-document \\",
		`        '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ssm.amazonaws.com"},"Action":"sts:AssumeRole"}]}'`,
		"    aws iam attach-role-policy --role-name SSMHybridRole \\",
		"        --policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
		"  a registration for this one machine (the code it prints is a credential):",
		"    aws ssm create-activation --iam-role SSMHybridRole --registration-limit 1 \\",
		fmt.Sprintf("        --default-instance-name %s --region <aws-region>", n.DisplayName()),
		fmt.Sprintf("  on the machine (%s, %s):", orUnknown(n.OS, "its operating system"), orUnknown(arch, "its processor")),
		fmt.Sprintf("    curl -o /tmp/ssm-setup-cli https://amazon-ssm-<aws-region>.s3.<aws-region>.amazonaws.com/latest/%s_%s/ssm-setup-cli", family, arch),
		"    sudo chmod +x /tmp/ssm-setup-cli",
		"    sudo /tmp/ssm-setup-cli -register -activation-code <code> -activation-id <id> -region <aws-region>",
		"  then tie the registration to this machine, so ssmate shows them as one:",
		"    aws ssm add-tags-to-resource --resource-type ManagedInstance --resource-id <mi-id> \\",
		fmt.Sprintf("        --tags Key=%s,Value=%s --region <aws-region>", inventory.SourceTag, n.ID),
	}, "\n")
}

// arcSteps connect a machine outside Azure to Azure Arc, tagged with its own
// identifier so ssmate links the two.
func arcSteps(n inventory.Node) string {
	if n.Cloud == inventory.CloudAzure {
		return ""
	}
	return "\n" + strings.Join([]string{
		"or with Azure Arc: install the Connected Machine agent",
		"  (https://learn.microsoft.com/azure/azure-arc/servers/agent-overview), then on the machine:",
		"    sudo azcmagent connect --subscription-id <subscription> --resource-group <group> \\",
		fmt.Sprintf("        --location <azure-region> --use-device-code --tags \"%s=%s\"", inventory.SourceTag, n.ID),
	}, "\n")
}

// agentBuild picks the Session Manager agent build: the Debian packaging for
// Debian and Ubuntu, the generic Linux one otherwise, and the processor. What
// is not known is left as a choice for the user to make, not guessed.
func agentBuild(n inventory.Node) (family, arch string) {
	os := strings.ToLower(n.OS)
	switch {
	case strings.Contains(os, "ubuntu"), strings.Contains(os, "debian"):
		family = "debian"
	case os != "":
		family = "linux"
	default:
		family = "<debian|linux>"
	}
	arch = n.Arch
	if arch == "" {
		arch = "<amd64|arm64>"
	}
	return family, arch
}

func orUnknown(v, unknown string) string {
	if v == "" || strings.HasPrefix(v, "<") {
		return unknown + " not known"
	}
	return v
}
