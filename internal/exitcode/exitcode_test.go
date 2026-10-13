package exitcode

import (
	"errors"
	"testing"
)

func TestFromReturnsErrForPlainError(t *testing.T) {
	if got := From(errors.New("boom")); got != Err {
		t.Fatalf("From(plain) = %d, want %d", got, Err)
	}
}

func TestFromReturnsOKForNil(t *testing.T) {
	if got := From(nil); got != OK {
		t.Fatalf("From(nil) = %d, want %d", got, OK)
	}
}

func TestFromReadsCodeFromTypedError(t *testing.T) {
	err := New(NotConnected, "i-123 is not connected", "run ssmate doctor i-123", nil)
	if got := From(err); got != NotConnected {
		t.Fatalf("From(typed) = %d, want %d", got, NotConnected)
	}
}

func TestFromUnwrapsNestedTypedError(t *testing.T) {
	inner := New(Denied, "denied", "", nil)
	outer := errors.New("wrapped: " + inner.Error())
	_ = outer
	nested := New(Creds, "creds", "", inner)
	if got := From(nested); got != Creds {
		t.Fatalf("From(nested) = %d, want %d", got, Creds)
	}
}

func TestErrorIncludesMessageAndWrapped(t *testing.T) {
	inner := errors.New("inner cause")
	err := New(Denied, "access denied", "check your policy", inner)
	got := err.Error()
	if got != "access denied: inner cause" {
		t.Fatalf("Error() = %q, want %q", got, "access denied: inner cause")
	}
	if !errors.Is(err, inner) {
		t.Fatal("errors.Is did not find the wrapped error")
	}
}

func TestErrorWithoutWrappedIsJustMessage(t *testing.T) {
	err := New(NotFound, "no such target", "", nil)
	if got := err.Error(); got != "no such target" {
		t.Fatalf("Error() = %q, want %q", got, "no such target")
	}
}
