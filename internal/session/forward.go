package session

import (
	"fmt"
	"strconv"
	"strings"
)

// Forward describes one port-forwarding request.
//
// Two shapes exist, and they use different Session Manager documents:
//
//	8080:80              forward a local port to a port ON the instance
//	5432:db.internal:5432 forward through the instance to another host
//
// The second is how you reach a database that only the instance can see.
type Forward struct {
	LocalPort  int
	RemoteHost string // empty means the instance itself
	RemotePort int
}

// ParseForward reads the "local:remote" and "local:host:remote" forms.
//
// IPv6 literals are rejected rather than mis-parsed: they contain colons, so
// splitting on ':' would silently produce nonsense. Better to refuse clearly.
func ParseForward(spec string) (Forward, error) {
	if strings.Count(spec, "[") > 0 || strings.Count(spec, "]") > 0 {
		return Forward{}, fmt.Errorf("IPv6 literals are not supported in %q; use a hostname", spec)
	}
	parts := strings.Split(spec, ":")

	port := func(s, what string) (int, error) {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return 0, fmt.Errorf("%s port %q is not a number", what, s)
		}
		if n < 1 || n > 65535 {
			return 0, fmt.Errorf("%s port %d is out of range 1-65535", what, n)
		}
		return n, nil
	}

	switch len(parts) {
	case 2:
		local, err := port(parts[0], "local")
		if err != nil {
			return Forward{}, err
		}
		remote, err := port(parts[1], "remote")
		if err != nil {
			return Forward{}, err
		}
		return Forward{LocalPort: local, RemotePort: remote}, nil

	case 3:
		local, err := port(parts[0], "local")
		if err != nil {
			return Forward{}, err
		}
		host := strings.TrimSpace(parts[1])
		if host == "" {
			return Forward{}, fmt.Errorf("no remote host in %q", spec)
		}
		remote, err := port(parts[2], "remote")
		if err != nil {
			return Forward{}, err
		}
		return Forward{LocalPort: local, RemoteHost: host, RemotePort: remote}, nil

	default:
		return Forward{}, fmt.Errorf(
			"cannot read %q; expected local:remote, or local:host:remote to reach through the instance", spec)
	}
}

// String renders the forward the way it was typed.
func (f Forward) String() string {
	if f.RemoteHost == "" {
		return fmt.Sprintf("%d:%d", f.LocalPort, f.RemotePort)
	}
	return fmt.Sprintf("%d:%s:%d", f.LocalPort, f.RemoteHost, f.RemotePort)
}

// Describe explains in words what the tunnel does, for the line printed
// before a session that then occupies the terminal indefinitely.
func (f Forward) Describe(target string) string {
	if f.RemoteHost == "" {
		return fmt.Sprintf("localhost:%d -> %s:%d", f.LocalPort, target, f.RemotePort)
	}
	return fmt.Sprintf("localhost:%d -> %s:%d (via %s)", f.LocalPort, f.RemoteHost, f.RemotePort, target)
}

// ForwardRequest builds the session request for a forward.
//
// Note the agent-version requirement differs between the two documents:
// forwarding to a remote host needs SSM Agent 3.1.1374.0 or newer on the
// instance, against 2.3.672.0 for a local forward. An older agent fails in a
// way that does not name the cause. We do not check it up front.
func ForwardRequest(target string, f Forward) Request {
	if f.RemoteHost == "" {
		return Request{
			Target:       target,
			DocumentName: "AWS-StartPortForwardingSession",
			Parameters: map[string][]string{
				"portNumber":      {strconv.Itoa(f.RemotePort)},
				"localPortNumber": {strconv.Itoa(f.LocalPort)},
			},
		}
	}
	return Request{
		Target:       target,
		DocumentName: "AWS-StartPortForwardingSessionToRemoteHost",
		Parameters: map[string][]string{
			"host":            {f.RemoteHost},
			"portNumber":      {strconv.Itoa(f.RemotePort)},
			"localPortNumber": {strconv.Itoa(f.LocalPort)},
		},
	}
}
