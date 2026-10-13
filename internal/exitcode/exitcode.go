// Package exitcode defines ssmate's process exit codes and the error type
// that carries one. Exit codes are part of the CLI's public contract: they
// are documented in the README and scripts depend on them, so the constants
// below must never be renumbered.
package exitcode

import "errors"

// Code is a process exit status.
type Code int

const (
	// OK means the command succeeded.
	OK Code = 0
	// Err is any failure without a more specific code.
	Err Code = 1
	// Cancelled means the user interrupted or declined.
	Cancelled Code = 2
	// NotFound means the requested target matched no known node.
	NotFound Code = 3
	// Creds means credentials are missing, expired, or unusable.
	Creds Code = 4
	// Denied means AWS refused the call on permissions grounds.
	Denied Code = 5
	// NotConnected means the node exists but SSM cannot reach it.
	NotConnected Code = 6
	// Prereq means a required local dependency is missing.
	Prereq Code = 7
)

// Error is an error that knows which exit code it should produce, and
// optionally how the user can fix it. Hint should be a command the user can
// copy and run, not a description of one.
type Error struct {
	Code Code
	Msg  string
	Hint string
	Err  error
}

// New builds an Error. wrapped may be nil.
func New(code Code, msg, hint string, wrapped error) *Error {
	return &Error{Code: code, Msg: msg, Hint: hint, Err: wrapped}
}

func (e *Error) Error() string {
	if e.Err == nil {
		return e.Msg
	}
	return e.Msg + ": " + e.Err.Error()
}

// Unwrap lets errors.Is and errors.As see through to the cause.
func (e *Error) Unwrap() error { return e.Err }

// From extracts the exit code an error should produce. A nil error is OK;
// anything that is not an *Error is a general failure.
func From(err error) Code {
	if err == nil {
		return OK
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return Err
}
