package session

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PrintCommand renders the aws CLI command equivalent to a request.
//
// This is the teaching mode. The tool's whole premise is that people do not
// know SSM, and showing them the underlying command turns every use into a
// lesson rather than a black box. It also gives them something to paste into
// a runbook or a ticket.
func PrintCommand(req Request) string {
	var b strings.Builder
	b.WriteString("aws ssm start-session")
	fmt.Fprintf(&b, " --target %s", req.Target)
	if req.Region != "" {
		fmt.Fprintf(&b, " --region %s", req.Region)
	}
	if req.Profile != "" {
		fmt.Fprintf(&b, " --profile %s", req.Profile)
	}
	if req.DocumentName != "" {
		fmt.Fprintf(&b, " --document-name %s", req.DocumentName)
	}
	if len(req.Parameters) > 0 {
		raw, err := json.Marshal(req.Parameters)
		if err == nil {
			fmt.Fprintf(&b, " --parameters '%s'", string(raw))
		}
	}
	if req.Reason != "" {
		fmt.Fprintf(&b, " --reason '%s'", req.Reason)
	}
	return b.String()
}
