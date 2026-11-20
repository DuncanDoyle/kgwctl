# Sub-phase 2.5 — Ctrl-C in `check --live`

Kind: stabilization
Base: d095f1b
Issues: none (owner try-out, 2026-11-20) | Plan steps: Task 2.3.2

## Problem / goal
The owner stopped a `check --live` run with Ctrl-C on purpose. The report printed, then stderr said `Error: interrupted` and the exit code was 2. The run looks like it crashed. The report footer said "stopped by interrupted".

Reproduction (kind cluster with one broken route):

```bash
kgwctl check --live    # press Ctrl-C after a few seconds
# stdout: full report, footer "OBSERVED DURING THIS RUN (0) · … stopped by interrupted"
# stderr: Error: interrupted
# exit 2
```

## Expected behaviour
The owner chose option A (2026-11-20): a deliberate Ctrl-C ends a live run like `--timeout` does. The report counts, and exit 2 keeps meaning "could not check".

| Input / action | Expected |
|---|---|
| `check --live`, first Ctrl-C after an install was found | Full report on stdout. stderr has `Stopped by Ctrl-C.` Exit 0 or 1 from the findings, by the same rules as a `--timeout` stop, with no `Error:` line. Design §3.4's other exit-2 conditions still apply and print their error, as after a timeout. |
| Same, with `-o json` or `-o yaml` | `stopReason: interrupted` (schema unchanged). stderr and exit as above. |
| `check --live`, Ctrl-C while the session stops after `--timeout` | `stopReason: interrupted`. Exit 0 or 1 from the findings. |
| `check --live`, Ctrl-C while the report is being written | One complete report. The run already ended, so its stop reason stays and no stop line prints. Exit 0 or 1 from the findings. |
| `check` or `check --live`, writing the report returns an error | `Error: <write error>`, exit 2 (review 2.5, finding 2). A closed pipe on Unix ends the process with SIGPIPE first, as before. |
| `check --live`, Ctrl-C before any install was found | No report, exit 2 (unchanged). |
| `check --live`, second Ctrl-C | Exits at once, no report (unchanged). |
| Plain `check`, Ctrl-C | Exit 2 (unchanged): the run did not finish its checks. |
| Text footer | `stopped by Ctrl-C` or `stopped at --timeout`. |

## Root cause (if known)
`runLive` returns `interrupted()`, an `ExitError{Code: 2}` with an error, whenever the stop reason is interrupted (`internal/app/live.go`). `Execute` prints any `ExitError` that carries an error as `Error: <msg>`. The text printer prints the raw stop reason in the footer (`internal/print/check.go`).

## Acceptance tests
At the command boundary in `internal/app/live_test.go`: `TestLiveInterrupt` and `TestLiveInterruptDuringShutdown` assert exit 1 (a broken route), the stderr line and no `Error:`. `TestLiveInterruptDuringOutput` asserts exit 1 and no `Error:`. `TestLiveWriteError` asserts exit 2 with the write error. A text-output case asserts `stopped by Ctrl-C`, and `TestLiveNoANSIOffTerminal` asserts `stopped at --timeout`. `TestLiveInterruptNoInstall` and `TestCheckInterrupted` keep exit 2.

## Scope
In: `internal/app/live.go`, `internal/print/check.go`, their tests; README `--live` section; design §3.2, §3.4 and the "revisit" line in §10; `docs/decisions.md` (D-020); roadmap notes from the `glooctl check` comparison.
Out: plain `check` Ctrl-C behaviour; the JSON schema; progress lines; `--exclude` and other deferred flags (D-015).

## Plan / design impact
Design §3.4: Ctrl-C during `--live` with a report no longer gives exit 2. D-020 records the change, the item the design marked "revisit if it annoys in use".

## Definition of done
- [ ] acceptance tests fail before, pass after (report both)
- [ ] full test suite passes
- [ ] cross-model review of `git diff d095f1b..HEAD` + triage (stabilization)
- [ ] phase-2.5-complete.md written; phase-status.md updated
