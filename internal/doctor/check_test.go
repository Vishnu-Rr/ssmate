package doctor

import (
	"context"
	"testing"

	"github.com/Vishnu-Rr/ssmate/internal/exitcode"
)

func res(name string, sev Severity) Result {
	return Result{Name: name, Severity: sev, Detail: "detail for " + name}
}

func TestRunAllRunsEveryCheckInOrder(t *testing.T) {
	var order []string
	reg := Registry{
		{Name: "first", Run: func(context.Context) Result { order = append(order, "first"); return res("first", Pass) }},
		{Name: "second", Run: func(context.Context) Result { order = append(order, "second"); return res("second", Fail) }},
		{Name: "third", Run: func(context.Context) Result { order = append(order, "third"); return res("third", Warn) }},
	}
	got := reg.RunAll(context.Background())
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
	if order[0] != "first" || order[1] != "second" || order[2] != "third" {
		t.Fatalf("checks ran out of order: %v", order)
	}
}

func TestRunAllContinuesAfterAFailure(t *testing.T) {
	ran := false
	reg := Registry{
		{Name: "fails", Run: func(context.Context) Result { return res("fails", Fail) }},
		{Name: "later", Run: func(context.Context) Result { ran = true; return res("later", Pass) }},
	}
	reg.RunAll(context.Background())
	if !ran {
		t.Fatal("a failing check stopped the run; every check must report")
	}
}

func TestSummarizeCounts(t *testing.T) {
	results := []Result{res("a", Pass), res("b", Pass), res("c", Warn), res("d", Fail)}
	p, w, f := Summarize(results)
	if p != 2 || w != 1 || f != 1 {
		t.Fatalf("Summarize = (%d,%d,%d), want (2,1,1)", p, w, f)
	}
}

func TestExitCodeIsNonZeroOnFailure(t *testing.T) {
	// A diagnostic that cannot fail a pipeline is decoration.
	if got := ExitCode([]Result{res("a", Pass), res("b", Fail)}); got == exitcode.OK {
		t.Fatal("a failing check must produce a non-zero exit code")
	}
}

func TestExitCodeIsZeroWhenOnlyWarnings(t *testing.T) {
	if got := ExitCode([]Result{res("a", Pass), res("b", Warn)}); got != exitcode.OK {
		t.Fatalf("ExitCode(warnings only) = %d, want 0", got)
	}
}

func TestExitCodeIsZeroWhenAllPass(t *testing.T) {
	if got := ExitCode([]Result{res("a", Pass)}); got != exitcode.OK {
		t.Fatalf("ExitCode(all pass) = %d, want 0", got)
	}
}
