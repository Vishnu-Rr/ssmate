# How to build ssmate v0.1

The plan at `docs/superpowers/plans/2026-09-18-ssmate-v0.1.md` contains
complete, compiled-and-tested code for every step. Building it is therefore
mostly transcription and running tests, not design. That is why a mid-tier
model is a reasonable choice here, and it is also why the main risk is a
model deciding to improve on the plan rather than follow it.

Tasks 1 and 2 are already done and committed.

## Model choice

| Tasks | Model | Why |
|-------|-------|-----|
| 3, 4, 5, 6 | Sonnet | Pure data transformation, complete code given, tests are the gate |
| 7 | Sonnet, escalate if the race test fails twice | Concurrency. The code is given, but a wrong "fix" here is subtle |
| 8, 9, 10 | Sonnet | Complete code; the argv contract is exact and asserted by tests |
| 11, 12 | Sonnet | Straightforward once the framework is in |
| 13 | Sonnet, escalate on a second compile failure | Bubble Tea v2 stabilised in Feb 2026, so training data is thin and skewed to v1. The plan carries the literal v2 signatures |
| 14 | Sonnet | Wiring, but it is the first end-to-end run so review it properly |
| 15, 16 | Sonnet | Polish and packaging |

Escalating means switching that one task to a stronger model, not restarting.

## Opening prompt for the build session

Start a new session with the working directory set to this repository, and
paste this:

---

Read `docs/superpowers/plans/2026-09-18-ssmate-v0.1.md` and implement it
starting at Task 3. Tasks 1 and 2 are already committed.

Rules for this build:

1. Implement exactly what the plan specifies. The code in it has been
   compiled and tested. If you think something is wrong, stop and say so
   rather than silently changing it.
2. Follow the TDD order in each task: write the test, run it and watch it
   fail, write the implementation, run it and watch it pass, commit. Do not
   skip the failing run; it is what proves the test is real.
3. One commit per task, using the message given in the task.
4. If a test fails twice for the same reason, stop and report rather than
   trying a third variation.
5. Do not add dependencies beyond those already in `go.mod`.
6. Do not add features that are not in the plan. Later phases are
   deliberately out of scope.

Work through Task 3 to Task 6, then stop and summarise.

---

## Checkpoints

Stop and review after each batch. Each one ends somewhere runnable.

| After | What to check |
|-------|---------------|
| Task 6 | `go test ./... -race` green. Cache file written to the right place with mode 600 |
| Task 8 | The race test in Task 7 passes repeatedly, not once. Run it ten times |
| Task 10 | No session token appears in `ps` output during a real connect |
| Task 12 | `go run ./cmd/ssmate doctor` reports something sensible about your plugin |
| Task 14 | First real end-to-end connect against a live instance |
| Task 16 | `goreleaser check` and a snapshot build |

## The one check worth doing by hand

After Task 10, with a real session open, run `ps aux | grep session-manager`
from another terminal. The session token must not be visible. The plan puts
it in an environment variable specifically to prevent that, and a test
asserts it, but it is worth confirming once against the real binary.

## Running the tests

```bash
go build ./... && go vet ./... && go test ./... -race && gofmt -l .
```

`gofmt -l .` printing nothing is part of passing.
