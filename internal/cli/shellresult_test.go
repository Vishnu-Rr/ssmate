package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/session"
)

func TestShellResultTreatsACleanEndingAsSuccess(t *testing.T) {
	failed := errors.New("ssh could not connect")
	for _, tc := range []struct {
		name string
		in   error
		want error
	}{
		{"user typed exit, or the command succeeded", session.ErrClosedByUser, nil},
		{"the same ending, wrapped", fmt.Errorf("bastion: %w", session.ErrClosedByUser), nil},
		{"a real failure", failed, failed},
		{"nothing to report", nil, nil},
	} {
		if got := shellResult(tc.in); !errors.Is(got, tc.want) || (tc.want == nil && got != nil) {
			t.Errorf("%s: shellResult(%v) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}
