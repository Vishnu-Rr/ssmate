package session

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ReconnectPolicy bounds how hard we try to rebuild a dropped tunnel.
type ReconnectPolicy struct {
	// MinUptime is how long a session must last to count as "worked". A
	// session that dies faster than this is failing for a reason that
	// retrying will not fix, such as a revoked permission, so those failures
	// accumulate toward MaxConsecutive. One that lasted longer resets the
	// counter, because that is the service recycling a healthy tunnel.
	MinUptime time.Duration

	// MaxConsecutive is how many fast failures in a row before giving up.
	MaxConsecutive int

	// BaseDelay is the first backoff, doubling up to MaxDelay.
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

// DefaultReconnect is tuned for the two ways AWS ends a healthy tunnel: the
// twenty-minute idle timeout, and the roughly hourly WebSocket recycle. Both
// arrive long after MinUptime, so neither counts as a failure.
var DefaultReconnect = ReconnectPolicy{
	MinUptime:      30 * time.Second,
	MaxConsecutive: 5,
	BaseDelay:      time.Second,
	MaxDelay:       15 * time.Second,
}

// ErrClosedByUser ends the hold-open loop without reconnecting.
//
// The loop cannot tell the two endings apart on its own. A tunnel that stops
// because the service recycled it should be rebuilt; one that stops because
// the user pressed Ctrl-C or typed exit must stay closed, or the tool fights
// the person trying to leave. Amazon's plugin does not distinguish them, so
// the AWS path never returns this. A provider that execs ssh can: exit 0 is
// the user, anything else is a failure.
var ErrClosedByUser = errors.New("session closed by user")

// ReconnectEvent describes one rebuild, for the caller to report.
type ReconnectEvent struct {
	Attempt int
	Lasted  time.Duration
	Delay   time.Duration
	Err     error
}

// RunForwarding holds a tunnel open across the service ending it.
//
// AWS closes an idle session after twenty minutes and recycles the underlying
// connection roughly hourly. Both are normal, and both currently mean the
// tunnel simply stops, which is the single most-complained-about thing about
// SSM port forwarding. So when a session ends and the caller has not asked us
// to stop, we open another.
//
// There IS a gap: the plugin owns the local socket, so it closes while we
// rebuild. Closing that gap properly needs us to own the listener, which
// means speaking the protocol directly. A second of downtime an hour is a
// large improvement on a tunnel that dies and stays dead.
func RunForwarding(
	ctx context.Context,
	api StartSessionAPI,
	plugin *PluginInfo,
	req Request,
	endpoint string,
	notify func(ReconnectEvent),
) error {
	return runForwardingWith(ctx, func() error {
		return Run(ctx, api, plugin, req, endpoint)
	}, notify)
}

// HoldOpen keeps reopening whatever once builds until the user stops it.
//
// It is RunForwarding without Amazon: every cloud here ends a healthy tunnel
// on its own schedule - AWS on an idle timer, OCI on a session TTL it will
// not extend - so the rebuild logic is shared and only the building differs.
// once must return ErrClosedByUser when the user ended it.
func HoldOpen(ctx context.Context, once func() error, notify func(ReconnectEvent)) error {
	return runForwardingWith(ctx, once, notify)
}

// runForwardingWith is the retry loop, separated from AWS so it can be
// tested for the behaviour that matters: that a tunnel dropping every hour
// reconnects forever, while one failing instantly gives up.
func runForwardingWith(ctx context.Context, once func() error, notify func(ReconnectEvent)) error {
	p := policyOrDefault()
	delay := p.BaseDelay
	var consecutive int

	for attempt := 1; ; attempt++ {
		started := nowFunc()
		err := once()
		lasted := nowFunc().Sub(started)

		// The caller stopped us. Not a failure.
		if ctx.Err() != nil {
			return nil
		}

		// The user ended it deliberately. Rebuilding would be the tool
		// refusing to let go.
		if errors.Is(err, ErrClosedByUser) {
			return nil
		}

		// Some failures will never succeed on a retry, and hammering them
		// just delays an error the user needs to read.
		if permanent(err) {
			return err
		}

		if lasted >= p.MinUptime {
			consecutive = 0
			delay = p.BaseDelay
		} else {
			consecutive++
			if consecutive >= p.MaxConsecutive {
				return fmt.Errorf("tunnel failed %d times in a row, giving up: %w", consecutive, err)
			}
		}

		if notify != nil {
			notify(ReconnectEvent{Attempt: attempt, Lasted: lasted, Delay: delay, Err: err})
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}

		if delay *= 2; delay > p.MaxDelay {
			delay = p.MaxDelay
		}
	}
}

// permanent reports whether retrying is pointless.
func permanent(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, fatal := range []string{
		"AccessDenied", "not authorized", "UnauthorizedOperation",
		"InvalidDocument", "ValidationException",
		// OCI answers both "does not exist" and "you may not see it" with
		// one message, and refuses a 21st session on a bastion. Neither
		// changes in the seconds a retry would take.
		"NotAuthorizedOrNotFound", "LimitExceeded",
	} {
		if contains(msg, fatal) {
			return true
		}
	}
	return false
}

// nowFunc and policyOrDefault exist so tests can substitute both.
var (
	nowFunc         = time.Now
	activePolicy    *ReconnectPolicy
	policyOrDefault = func() ReconnectPolicy {
		if activePolicy != nil {
			return *activePolicy
		}
		return DefaultReconnect
	}
)
