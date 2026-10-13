// Package doctor diagnoses why a session cannot be opened, from the client
// side outward.
package doctor

import (
	"context"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
)

// Severity is how bad a result is.
type Severity int

const (
	// Pass means the check found nothing wrong.
	Pass Severity = iota
	// Warn means something is suboptimal but will probably still work.
	Warn
	// Fail means this will stop a session from opening.
	Fail
)

// String gives the lowercase name used in JSON output.
func (s Severity) String() string {
	switch s {
	case Pass:
		return "pass"
	case Warn:
		return "warn"
	default:
		return "fail"
	}
}

// Result is one check's verdict. Remedy should be a command the user can copy
// and run, not a sentence describing one.
type Result struct {
	Name     string `json:"name"`
	Severity Severity
	Detail   string `json:"detail,omitempty"`
	Remedy   string `json:"remedy,omitempty"`
}

// Check is a named diagnostic.
type Check struct {
	Name string
	Run  func(context.Context) Result
}

// Registry is an ordered list of checks. Order matters: cheap local checks
// come first, so the common failures are reported without waiting on network
// calls, and so we never blame AWS for a missing local binary.
type Registry []Check

// RunAll runs every check and returns every result.
//
// It never stops early. A user with three problems should learn about all
// three in one run rather than discovering them one command at a time.
func (r Registry) RunAll(ctx context.Context) []Result {
	out := make([]Result, 0, len(r))
	for _, c := range r {
		out = append(out, c.Run(ctx))
	}
	return out
}

// Summarize counts results by severity.
func Summarize(results []Result) (passed, warned, failed int) {
	for _, r := range results {
		switch r.Severity {
		case Pass:
			passed++
		case Warn:
			warned++
		case Fail:
			failed++
		}
	}
	return passed, warned, failed
}

// ExitCode is non-zero when any check failed, so scripts can gate on it.
// Warnings alone do not fail the run.
func ExitCode(results []Result) exitcode.Code {
	if _, _, failed := Summarize(results); failed > 0 {
		return exitcode.Err
	}
	return exitcode.OK
}
