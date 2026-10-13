// Package inventory gathers, joins and caches the set of machines a user can
// reach, in whichever clouds they are signed in to.
package inventory

import "time"

// Agent describes whatever software the cloud runs on the machine to make it
// reachable, where such a thing exists.
//
// Amazon's Systems Manager agent and Azure's Connected Machine agent both fit
// this shape and both report health the same way. Azure Bastion and Oracle's
// port forwarding involve no agent at all, so Present is false there and the
// remaining fields carry nothing. Absence of an agent is not a fault; it means
// reachability is decided by the network rather than by a heartbeat.
type Agent struct {
	Present  bool      `json:"present"`
	Online   bool      `json:"online,omitempty"`
	Version  string    `json:"version,omitempty"`
	Latest   bool      `json:"latest,omitempty"`
	LastSeen time.Time `json:"last_seen,omitempty"`
}

// Node is one machine, in any cloud.
//
// Every field above Detail is one the whole tool may read: the picker, the
// resolver, the listing and the diagnosis work from these and nothing else.
// That is what lets one command serve three clouds.
//
// Each way into the machine is a Route, and each route carries its own
// verdict, because the rule differs per way in: on Session Manager it is an
// agent heartbeat, on Azure Bastion a gateway's tier, on an OCI bastion a set
// of network facts. Reachable, Status, Hint and Via are then derived from the
// routes by Summarize, in one place, so the list, the listing and the
// diagnosis can never describe the same machine differently.
type Node struct {
	Cloud     string            `json:"cloud"`
	ID        string            `json:"id"`
	Name      string            `json:"name,omitempty"`
	Account   string            `json:"account"`
	Region    string            `json:"region"`
	PrivateIP string            `json:"private_ip,omitempty"`
	PublicIP  string            `json:"public_ip,omitempty"`
	State     string            `json:"state,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`

	// OS and Arch describe the machine where its cloud says: the operating
	// system's name, and amd64 or arm64. They decide which agent build a
	// machine needs, so the setup steps doctor prints can be exact.
	OS   string `json:"os,omitempty"`
	Arch string `json:"arch,omitempty"`

	// Hostname is the name the machine's own operating system goes by, where
	// the cloud reports it. Together with the address it is how a record from
	// one inventory is recognised as the same machine in another.
	Hostname string `json:"hostname,omitempty"`

	// Aliases are other identifiers the same machine is known by: Azure's VM
	// ID beside its resource ID, or the session service's ID for a machine
	// whose records were linked. A target typed as any of them resolves here.
	Aliases []string `json:"aliases,omitempty"`

	// Outside marks a record for a machine that does not live in the cloud
	// that listed it: a machine elsewhere, registered with that cloud's
	// session service. Such a record exists to contribute a way in, and Link
	// folds it into the machine's own record when it can find one.
	Outside *Outside `json:"outside,omitempty"`

	Agent Agent `json:"agent,omitzero"`

	// Routes are every way into the machine that was found.
	Routes []Route `json:"routes,omitempty"`

	// Down means the machine itself is not running: stopped, terminated, or
	// in a state its cloud does not count as up. Its provider then sets Status
	// and Hint to describe that state, and its routes are beside the point.
	Down bool `json:"down,omitempty"`

	// Reachable, Status, Hint and Via are the summary Summarize derives. Via
	// is the short name of the route a session would use, or empty when there
	// is none.
	Reachable bool   `json:"reachable"`
	Status    string `json:"status"`
	Hint      string `json:"hint,omitempty"`
	Via       string `json:"via,omitempty"`

	// Detail carries facts only this machine's own provider understands, and
	// nothing outside that provider may read it. Keeping them here rather than
	// as named fields is what stops one cloud's vocabulary leaking into the
	// shared types, which is the mistake that made this refactor necessary.
	Detail map[string]string `json:"detail,omitempty"`
}

// Outside says what is known about where a registered machine really lives.
type Outside struct {
	// SourceID is what the registering service recorded about the machine
	// it was registered from, when it recorded anything.
	SourceID string `json:"source_id,omitempty"`

	// Tag is the machine's identifier in its own cloud, as written by whoever
	// registered it in the ssmate:source tag.
	Tag string `json:"tag,omitempty"`
}

// SourceTag is the tag that names a registered machine's home: its
// identifier in its own cloud, such as an OCI instance OCID or an Azure VM's
// resource ID. It is the reliable way to link the two records.
const SourceTag = "ssmate:source"

// DisplayName is what a human should see: the name if there is one, and the
// cloud's own identifier otherwise.
func (n Node) DisplayName() string {
	if n.Name != "" {
		return n.Name
	}
	return n.ID
}

// Connectable reports whether a session can plausibly be started right now.
// It is the provider's prediction, not a guarantee; only an attempt settles it.
func (n Node) Connectable() bool { return n.Reachable }

// Where names the account and region a machine sits in. Every cloud has a
// default account that carries no name of its own, so the region stands alone
// rather than trailing an empty account and a separator.
func (n Node) Where() string {
	if n.Account == "" {
		return n.Region
	}
	return n.Account + "/" + n.Region
}

// Detailed returns one provider-private fact, and whether it was recorded.
func (n Node) Detailed(key string) (string, bool) {
	if n.Detail == nil {
		return "", false
	}
	v, ok := n.Detail[key]
	return v, ok
}

// Status values that more than one provider uses. A provider may record any
// short lowercase phrase; these exist so the common cases stay spelled the
// same way across clouds, because users grep this output.
const (
	StatusOnline     = "online"
	StatusStopped    = "stopped"
	StatusTerminated = "terminated"
	StatusAgentLost  = "agent lost"
	StatusUnmanaged  = "unmanaged"
	StatusUnknown    = "unknown"
	StatusNoGateway  = "no gateway"
	StatusNoRoute    = "no route"
	// StatusBlocked is a way in that exists but is known not to work from
	// here: every bastion refuses this client, or none can reach the machine.
	StatusBlocked = "blocked"
)

// Cloud identifiers. A provider registers under one of these.
const (
	CloudAWS   = "aws"
	CloudAzure = "azure"
	CloudOCI   = "oci"
)
