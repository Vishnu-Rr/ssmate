package session

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func withPolicy(t *testing.T, p ReconnectPolicy) {
	t.Helper()
	activePolicy = &p
	t.Cleanup(func() { activePolicy = nil })
}

func TestPermanentFailuresAreNotRetried(t *testing.T) {
	for _, msg := range []string{
		"AccessDeniedException: not authorized to perform ssm:StartSession",
		"InvalidDocument: the specified document does not exist",
		"ValidationException: bad parameter",
	} {
		if !permanent(errors.New(msg)) {
			t.Errorf("should be permanent: %s", msg)
		}
	}
}

func TestTransientFailuresAreRetried(t *testing.T) {
	for _, msg := range []string{
		"connection reset by peer",
		"TargetNotConnected",
		"websocket: close 1006",
		"",
	} {
		if permanent(errors.New(msg)) {
			t.Errorf("should NOT be permanent: %q", msg)
		}
	}
	if permanent(nil) {
		t.Error("nil is not a permanent failure")
	}
}

func TestPolicyDefaultsSurviveTheIdleTimeout(t *testing.T) {
	// AWS ends an idle session at twenty minutes and recycles roughly
	// hourly. Both must read as "worked", not as failures, or a healthy
	// long-lived tunnel would exhaust the retry budget and stop.
	p := DefaultReconnect
	if p.MinUptime >= 20*time.Minute {
		t.Fatalf("MinUptime %v would classify the idle timeout as a failure", p.MinUptime)
	}
	if p.MinUptime >= time.Hour {
		t.Fatalf("MinUptime %v would classify the hourly recycle as a failure", p.MinUptime)
	}
}

func TestGivesUpAfterRepeatedFastFailures(t *testing.T) {
	withPolicy(t, ReconnectPolicy{
		MinUptime: time.Hour, MaxConsecutive: 3,
		BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
	})
	calls := 0
	err := runForwardingWith(context.Background(), func() error {
		calls++
		return errors.New("boom")
	}, nil)
	if err == nil {
		t.Fatal("repeated fast failures should eventually return an error")
	}
	if calls != 3 {
		t.Fatalf("attempted %d times, want 3", calls)
	}
}

func TestALongLivedSessionResetsTheFailureCount(t *testing.T) {
	// A tunnel that runs for hours, dropping each hour, must never exhaust
	// its budget. This is the case the whole feature exists for.
	withPolicy(t, ReconnectPolicy{
		MinUptime: 10 * time.Millisecond, MaxConsecutive: 3,
		BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
	})
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	err := runForwardingWith(ctx, func() error {
		calls++
		if calls >= 8 {
			cancel()
			return nil
		}
		time.Sleep(12 * time.Millisecond) // "worked", then dropped
		return errors.New("recycled")
	}, nil)
	if err != nil {
		t.Fatalf("a healthy reconnecting tunnel should not error: %v", err)
	}
	if calls < 8 {
		t.Fatalf("gave up after %d reconnects; long-lived tunnels must keep going", calls)
	}
}

func TestStopsImmediatelyWhenCancelled(t *testing.T) {
	withPolicy(t, ReconnectPolicy{
		MinUptime: time.Hour, MaxConsecutive: 100,
		BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	if err := runForwardingWith(ctx, func() error { calls++; return nil }, nil); err != nil {
		t.Fatalf("cancellation is not an error: %v", err)
	}
	if calls > 1 {
		t.Fatalf("ran %d times after cancellation", calls)
	}
}

func TestNotifyReportsEachRebuild(t *testing.T) {
	withPolicy(t, ReconnectPolicy{
		MinUptime: time.Hour, MaxConsecutive: 3,
		BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
	})
	var events []ReconnectEvent
	runForwardingWith(context.Background(), func() error { return errors.New("x") },
		func(e ReconnectEvent) { events = append(events, e) })
	if len(events) == 0 {
		t.Fatal("the user must be told the tunnel is being rebuilt")
	}
	if events[0].Attempt != 1 {
		t.Errorf("first event attempt = %d, want 1", events[0].Attempt)
	}
}

// A tunnel the service recycled should come back; one the user closed must
// not. Without this the tool reopens a session the person just walked away
// from, and on OCI that also burns one of twenty bastion slots for hours.
func TestUserClosingEndsTheLoop(t *testing.T) {
	withPolicy(t, ReconnectPolicy{
		MinUptime: time.Hour, MaxConsecutive: 5,
		BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
	})
	calls := 0
	err := runForwardingWith(context.Background(), func() error {
		calls++
		return fmt.Errorf("ssh exited: %w", ErrClosedByUser)
	}, nil)
	if err != nil {
		t.Errorf("a deliberate close is not a failure, got %v", err)
	}
	if calls != 1 {
		t.Errorf("rebuilt %d times after the user closed it, want 1 call and no rebuild", calls)
	}
}

// The sentinel must survive wrapping, since a provider reports the exit
// status alongside it.
func TestClosedByUserIsMatchedThroughWrapping(t *testing.T) {
	if !errors.Is(fmt.Errorf("ssh: %w", ErrClosedByUser), ErrClosedByUser) {
		t.Error("errors.Is should see through a wrap")
	}
	if errors.Is(errors.New("connection reset"), ErrClosedByUser) {
		t.Error("an unrelated error must not match")
	}
}

// OCI's two unretryable answers, alongside the Amazon ones already covered.
func TestOCIPermanentFailures(t *testing.T) {
	for _, msg := range []string{
		"NotAuthorizedOrNotFound: resource does not exist or you are not authorized",
		"LimitExceeded: bastion has reached its maximum session count",
	} {
		if !permanent(errors.New(msg)) {
			t.Errorf("should be permanent: %s", msg)
		}
	}
}
